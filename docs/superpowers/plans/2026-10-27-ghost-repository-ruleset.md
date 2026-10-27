# `ghost_repository_ruleset` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `ghost_repository_ruleset` resource covering every ruleset feature the Ghost native API supports,
shaped like the GitHub provider's `github_repository_ruleset`.

**Architecture:**
- **Client.** `internal/client/rulesets.go` wraps the native `/api/repositories/{owner}/{repo}/rulesets` CRUD,
  plus an org-teams list for the tests.
- **Provider.** The work splits into three files:
  - `ruleset_model.go`: plain-Go conversions between the Terraform model and the API (patterns, rules,
    actors), so they can be unit tested without the framework.
  - `ruleset_resource.go`: the schema, validators, CRUD and rename handling, copied in shape from
    `issue_label_resource.go`.
  - `ruleset_validators.go`: plan-time validation of bypass actors and lists.

**Tech Stack:** Go, Terraform Plugin Framework (protocol 6), terraform-plugin-testing, httptest.

**Spec:** `docs/superpowers/specs/2026-10-27-ghost-repository-ruleset-design.md` (read it first; every "API
facts" bullet is a requirement this plan relies on).

## Global Constraints

- **API.** Use the native API only for rulesets, with no v3 ruleset calls. Repository lookups may use v3
  `GET /repos/{o}/{r}`, as `ghost_issue_label` does.
- **Auth and secrets.**
  - Send the PAT as `Authorization: Bearer`. Never add cookie or session auth.
  - Never commit, print or log a token. The real token lives in `playground/terraform.tfvars`, which is
    gitignored.
- **Test repositories.** Name them `tf-acc-*` and always destroy them, with a destroy check. Probe repositories
  are deleted by hand and confirmed gone (404).
- **Rate limit.** 120 requests per minute, shared by v3 and native. Don't run acceptance suites back to back.
- **Docs.** Run `go generate ./...` after any schema or description change, and commit `docs/`. Never edit
  `docs/` by hand.
- **Commits.** Use Conventional Commits: `type(scope): summary` in lower case and the imperative mood, with no
  full stop. End each body with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- **Git.** Never use bare `git stash`. Leave untracked `playground/labels-demo/` alone.
- **Schema style.**
  - Use nested attributes, not blocks.
  - `conditions.ref_name.include/exclude` and `rules.required_status_checks.required_check[].context`, as in
    the spec.
  - `bypass_actors` is a set. `include`, `exclude` and `required_check` are lists.
  - `allowed_merge_methods` is a set.
- **Limits.**
  - `name` is 1–100 characters and patterns are 1–255.
  - `required_approving_review_count` is 0–10.
  - `allowed_merge_methods` must be a non-empty subset of `merge`, `squash`, `rebase`.
  - `target` is `branch` (the default) or `tag`.
  - `enforcement` is `active`, `evaluate` or `disabled`.
  - `bypass_mode` is `always` (the default) or `pull_request`.
  - `role` is `write`, `maintain` or `admin`.
- **Ids.** `id` is `owner/slug:ruleset_xxx`, split at the first `:`.

## Review Focus

1. **Import, then a config that uses short patterns.** Import stores `refs/heads/main` while the config says
   `main`. The first plan after import must be empty, not a spurious update. *Task 3 adds a plan modifier and
   Task 4's import step asserts an empty plan.*
2. **Ruleset deleted outside Terraform.** The next plan must propose a create, not fail. *Task 4 adds an
   acceptance step that deletes it through the client.*
3. **`team_id` not known until apply** (from another resource or a variable). Validation must skip it rather
   than error. *Task 3 unit-tests the validator with an unknown `team_id`.*
4. **Leaving out `rules` and `conditions`, or writing them out empty, gives an empty plan after apply.**
   *Task 4's minimal config omits both, and the full config's later step writes them empty.*
5. **`required_status_checks` with `required_check = []`.** It is valid, must round-trip, and must not be
   confused with the rule being off. *Task 2 unit test and Task 4 acceptance step.*

---

### Task 0: Stack on the rate-limit branch and run the outstanding probes

**Files:**
- Modify: `docs/superpowers/specs/2026-10-27-ghost-repository-ruleset-design.md` (add a "Probe results"
  section and resolve the items in "Left to the builder")

**Interfaces:**
- Produces:
  - the default `required_approving_review_count` (used as the schema default in Task 3)
  - whether contexts and patterns are deduped and reordered (Tasks 2 and 3)
  - what Ghost does with a tag ruleset (Task 3 validator, if one is needed)
  - the JSON shape of `GET /api/organizations/{org}/teams` (Task 1)

- [ ] **Step 1: Rebase onto PR #4.**
  - If PR #4 has merged, run `git fetch origin && git rebase origin/main`.
  - Otherwise run `git rebase origin/feat/client-rate-limiting`.
  - Then check: `go test ./...` passes and `grep -n maxRetries internal/client/client.go` finds a match.
- [ ] **Step 2: Probe with the existing helper.** Use
  `/private/tmp/claude-501/-Users-jonny-orca-workspaces-terraform-provider-ghost-grunt/36c83b0a-d9a7-4f4c-b34f-84c66f8b09fe/scratchpad/probe/g.py`
  (`call(method, path, body)` reads the token from the tfvars without printing it). Create a private repo
  `tf-acc-ruleset-probe2` and record each answer:
  1. A `pull_request` rule with `parameters: {}`: what is the GET's `required_approving_review_count`?
  2. A `required_check` list of `[b, a, a]`: is it kept, deduplicated or sorted?
  3. `include: ["x", "x"]`: is it kept or deduplicated?
  4. A `target: "tag"` ruleset with `pull_request`, `required_status_checks` and `required_linear_history`:
     accepted and kept, accepted and dropped, or rejected (status and message)?
  5. Rename the repo through v3 `PATCH`, then `GET` the ruleset using the **old** owner/slug: what status?
  6. `GET /api/organizations/pippin/teams`: what is the top-level shape (array, or an object key) and what are
     the field names?
- [ ] **Step 3: Delete the probe repo and confirm the 404.**
- [ ] **Step 4: Update the spec.** Write the results into a new "Probe results (2026-10-27)" section, and turn
  each resolved "Left to the builder" bullet into the decision it implies.
- [ ] **Step 5: Commit.** Message: `docs: record ruleset probe results`.

---

### Task 1: Client

**Files:**
- Create: `internal/client/rulesets.go`
- Create: `internal/client/rulesets_test.go`
- Modify: `internal/client/client.go` (add `IsConflict`)

**Interfaces:**
- Produces:

```go
type Ruleset struct {
    ID           string         `json:"id"`
    Name         string         `json:"name"`
    Target       string         `json:"target"`
    Enforcement  string         `json:"enforcement"`
    Include      []string       `json:"include"`
    Exclude      []string       `json:"exclude"`
    BypassActors *[]BypassActor `json:"bypassActors"` // nil when Ghost omits it (caller not admin)
    Rules        []Rule         `json:"rules"`
}
type RulesetRequest struct { // every field always sent: PUT is a full replace
    Name, Target, Enforcement string
    Include, Exclude          []string      // never nil; marshal as []
    BypassActors              []BypassActor // never nil
    Rules                     []Rule        // never nil
} // JSON keys: name, target, enforcement, include, exclude, bypassActors, rules
type BypassActor struct {
    ActorType  string `json:"actorType"`
    Role       string `json:"role,omitempty"`
    TeamID     string `json:"teamId,omitempty"`
    BypassMode string `json:"bypassMode"`
}
type Rule struct {
    Type       string          `json:"type"`
    Parameters json.RawMessage `json:"parameters,omitempty"`
}
type PullRequestParameters struct { // all keys always sent
    RequiredApprovingReviewCount   int64    `json:"required_approving_review_count"`
    DismissStaleReviewsOnPush      bool     `json:"dismiss_stale_reviews_on_push"`
    RequireCodeOwnerReview         bool     `json:"require_code_owner_review"`
    RequireLastPushApproval        bool     `json:"require_last_push_approval"`
    RequiredReviewThreadResolution bool     `json:"required_review_thread_resolution"`
    AllowedMergeMethods            []string `json:"allowed_merge_methods"`
}
type RequiredStatusChecksParameters struct {
    RequiredStatusChecks             []StatusCheck `json:"required_status_checks"` // never nil
    StrictRequiredStatusChecksPolicy bool          `json:"strict_required_status_checks_policy"`
    DoNotEnforceOnCreate             bool          `json:"do_not_enforce_on_create"`
}
type StatusCheck struct{ Context string `json:"context"` }
type Team struct { /* fields per Task 0 probe 6; at least ID, Slug, Name string */ }

func (c *Client) CreateRuleset(ctx context.Context, owner, repo string, req RulesetRequest) (*Ruleset, error)
func (c *Client) GetRuleset(ctx context.Context, owner, repo, id string) (*Ruleset, error)
func (c *Client) ListRulesets(ctx context.Context, owner, repo string) ([]Ruleset, error) // unwraps {"rulesets":[...]}
func (c *Client) ReplaceRuleset(ctx context.Context, owner, repo, id string, req RulesetRequest) (*Ruleset, error) // PUT
func (c *Client) DeleteRuleset(ctx context.Context, owner, repo, id string) error
func (c *Client) ListOrganizationTeams(ctx context.Context, org string) ([]Team, error)
func IsConflict(err error) bool // APIError with 409
```

Paths: `NativePath("repositories", owner, repo, "rulesets", id...)` and
`NativePath("organizations", org, "teams")`.

- [ ] **Step 1: Write the failing tests** in `rulesets_test.go`, using `newTestClient` and `readBody` as
  `labels_test.go` does:
  - `TestCreateRuleset`: POST to `/api/repositories/octo/r/rulesets`. The body has exactly the 7 keys, and
    `include`, `exclude`, `bypassActors` and `rules` are `[]` (not `null`) when empty. A 201 with a fixture
    containing a Team actor and a `pull_request` rule decodes, with `ID == "ruleset_abc"`.
  - `TestGetRulesetWithoutBypassActors`: the fixture has no `bypassActors` key, so `r.BypassActors == nil`.
    With `"bypassActors":[]`, it is non-nil and has length 0.
  - `TestListRulesets`: unwraps `{"rulesets":[…2 items…]}`.
  - `TestReplaceRuleset`: PUT `/api/repositories/octo/r/rulesets/ruleset_abc` sends all 7 keys.
  - `TestDeleteRuleset`: DELETE, then a 204 gives a nil error.
  - `TestIsConflict`: true for a 409 `APIError`, false for a 404 or a plain error.
  - `TestListOrganizationTeams`: GET `/api/organizations/pippin/teams`, using the shape Task 0 recorded.
  - `TestPullRequestParametersMarshal`: a zero value marshals all six keys. Callers always set
    `AllowedMergeMethods` to a non-empty list, so assert only that the six keys are present.
- [ ] **Step 2: Run them and see them fail.** `go test ./internal/client/ -run 'Ruleset|Conflict|Teams'`
  should fail because the types are undefined.
- [ ] **Step 3: Implement** `rulesets.go` and `IsConflict`. `CreateRuleset` and `ReplaceRuleset` swap nil
  slices for empty ones before sending.
- [ ] **Step 4: Run them and see them pass.** `go test ./internal/client/` should pass, and
  `golangci-lint run ./internal/client/` should report 0 issues.
- [ ] **Step 5: Commit.** Message: `feat(client): add ruleset and organization team endpoints`.

---

### Task 2: Model conversions (plain Go)

**Files:**
- Create: `internal/provider/ruleset_model.go`
- Create: `internal/provider/ruleset_model_test.go`

**Interfaces:**
- Consumes (Task 1): `client.Rule`, `client.BypassActor`, `client.PullRequestParameters`,
  `client.RequiredStatusChecksParameters`, `client.StatusCheck`.
- Produces:

```go
// Domain form of the `rules` attribute; nil pointers mean the rule is off.
type rulesetRules struct {
    Creation, Update, Deletion, NonFastForward, RequiredLinearHistory, RequiredSignatures bool
    PullRequest          *client.PullRequestParameters
    RequiredStatusChecks *client.RequiredStatusChecksParameters
}
func (r rulesetRules) empty() bool
func rulesToAPI(r rulesetRules) []client.Rule        // emits in Ghost's order; never nil
func rulesFromAPI(rules []client.Rule) (rulesetRules, error) // errors on an unknown rule type, naming it

func normalizeRefPattern(target, pattern string) string
func patternsFromRemote(target string, prior, remote []string) []string
func bypassActorKey(a client.BypassActor) string // "RepositoryRole:maintain", "Team:<id>", "OrganizationAdmin"
func rulesetID(repository, rulesetID string) string
func parseRulesetID(id string) (repository, rulesetID string, err error)
```

Rule order for `rulesToAPI`: `creation`, `update`, `deletion`, `non_fast_forward`, `required_linear_history`,
`required_signatures`, `pull_request`, `required_status_checks`. If Task 0 showed a different order, use
Ghost's.

`patternsFromRemote`: for each remote pattern, use the first not-yet-used prior pattern `p` with
`normalizeRefPattern(target, p) == remote`, otherwise the remote value. Return `[]string{}`, never nil.

- [ ] **Step 1: Write the failing table tests:**
  - `TestNormalizeRefPattern`:
    - `(branch, "main")` gives `refs/heads/main`, and `(tag, "v*")` gives `refs/tags/v*`.
    - `~DEFAULT_BRANCH`, `~ALL` and `refs/tags/v1` (on a branch target) are unchanged.
  - `TestPatternsFromRemote`:
    - The prior `["main","~ALL"]` with remote `["refs/heads/main","~ALL"]` gives `["main","~ALL"]`.
    - With an empty prior (import), the remote comes back unchanged.
    - After a target change, the prior `["main"]` with target `tag` and remote `["refs/tags/main"]` gives
      `["main"]`.
    - The prior `["a"]` with remote `["refs/heads/b"]` gives `["refs/heads/b"]`.
    - A nil remote gives `[]string{}`.
  - `TestRulesRoundTrip`:
    - An all-on value, with every parameter non-default, survives `rulesToAPI` then `rulesFromAPI`
      unchanged.
    - `rulesToAPI(rulesetRules{})` is an empty, non-nil slice.
    - A `RequiredStatusChecks` with an empty `RequiredStatusChecks` slice round-trips as non-nil with
      length 0. This is Review Focus 5.
  - `TestRulesFromAPIUnknownType`: `{"type":"merge_queue"}` returns an error mentioning `merge_queue`.
  - `TestRulesFromAPIIgnoresParamsOnFlags`: `{"type":"deletion","parameters":{}}` sets `Deletion` true.
  - `TestBypassActorKey`: covers all three types. Two actors that differ only in `BypassMode` share a key.
  - `TestParseRulesetID`:
    - `alice/repo:ruleset_x` gives `("alice/repo","ruleset_x")`.
    - `alice/repo:a:b` gives rulesetID `a:b`.
    - `alice/repo`, `:x` and `alice/repo:` are errors.
- [ ] **Step 2: Run them and see them fail.** `go test ./internal/provider/ -run 'RefPattern|PatternsFrom|Rules|BypassActorKey|ParseRulesetID'`
  should fail because the functions are undefined.
- [ ] **Step 3: Implement** `ruleset_model.go`.
- [ ] **Step 4: Run them and see them pass.**
- [ ] **Step 5: Commit.** Message: `feat(provider): add ruleset model conversions`.

---

### Task 3: Schema, validators and pattern plan modifier

**Files:**
- Create: `internal/provider/ruleset_resource.go` (Metadata, Schema, Configure, and stub CRUD that returns
  "not implemented" until Task 4)
- Create: `internal/provider/ruleset_validators.go`
- Create: `internal/provider/ruleset_resource_test.go` (unit and plan tests only in this task)
- Modify: `internal/provider/provider.go:110-116` (register `NewRepositoryRulesetResource`)

**Interfaces:**
- Consumes (Task 2): `normalizeRefPattern`, `bypassActorKey`.
- Produces:

```go
func NewRepositoryRulesetResource() resource.Resource
type RepositoryRulesetResource struct{ client *client.Client }
type repositoryRulesetResourceModel struct {
    ID, Repository  types.String
    RepositoryID    types.Int64
    RulesetID, Name, Target, Enforcement types.String
    Conditions      types.Object // {ref_name: {include: list(string), exclude: list(string)}}
    BypassActors    types.Set    // of {actor_type, role, team_id, bypass_mode}
    Rules           types.Object
}
// tfsdk names: id, repository, repository_id, ruleset_id, name, target, enforcement,
// conditions, bypass_actors, rules
func bypassActorsValidator() validator.Set     // role/team_id presence per actor_type; duplicates by bypassActorKey
func equivalentPatternsModifier() planmodifier.List
```

**Schema details:**
- **Rule flags and parameters.** The six rule flags, the `pull_request` fields and the two
  `required_status_checks` booleans are Optional + Computed with static defaults.
  - The defaults are `false`, `["merge","squash","rebase"]` for `allowed_merge_methods`, and Task 0's probed
    value for `required_approving_review_count`.
  - `required_check` is Required within `required_status_checks`, and each item's `context` is Required with
    `LengthAtLeast(1)`.
- **`conditions`.** `conditions` is Optional, and `ref_name` is Required within it.
  - `include` and `exclude` are Optional + Computed, defaulting to an empty list.
  - Each element is 1–255 characters, with `listvalidator.UniqueValues()`.
  - The list plan modifier is `equivalentPatternsModifier`.
- **`repository`.** Its description and validators are copied from `ghost_issue_label`, including the
  `replace_triggered_by` note.
- **`repository_id` and `ruleset_id`.** Both use `UseStateForUnknown`. `id` behaves as `ghost_issue_label`'s
  `id` does.
- **`target`.** Optional + Computed, defaulting to `"branch"`. Changing it updates in place, with no
  `RequiresReplace`, because Ghost re-prefixes patterns when `target` changes.
- **Tag targets.** If Task 0 showed that Ghost silently drops branch-only rules on tag targets, add a
  `ConfigValidators` entry rejecting them. Name the offending attributes in the error.

`equivalentPatternsModifier`: when the state value and the planned value are both known, and the planned
`target` is known, set the plan to the state value if every element normalises the same, pairwise and in
order. Otherwise leave the plan alone. This is Review Focus 1.

- [ ] **Step 1: Write the failing tests.**
  - `TestRulesetConfigValidation`: offline `PlanOnly` cases in the style of `TestIssueLabelConfigValidation`.
    Expected errors:
    - an empty name, or a 101-character name: length
    - `target = "push"`, or `enforcement = "on"`: value must be one of
    - a 256-character pattern: length
    - a duplicate include: duplicate
    - `required_approving_review_count = 11`: between
    - `allowed_merge_methods = []`: at least 1
    - `allowed_merge_methods = ["ff"]`: value must be one of
    - `RepositoryRole` with no role, or `Team` with no `team_id`: required
    - `OrganizationAdmin` with a `role`: not allowed
    - a `RepositoryRole` given a `team_id`: not allowed
    - two `OrganizationAdmin` actors with different modes: duplicate bypass actor
    - `required_check = [{ context = "" }]`: length
  - `TestBypassActorsValidatorUnknownTeamID`: calls `bypassActorsValidator().ValidateSet` directly with a set
    containing a Team actor whose `team_id` is unknown. Expect no diagnostics. This is Review Focus 3.
  - `TestEquivalentPatternsModifier`, calling `PlanModifyList` directly:
    - state `["refs/heads/main"]`, plan `["main"]`, target `branch`: the plan becomes the state value
    - target `tag`: the plan is unchanged
    - state `["refs/heads/main"]`, plan `["dev"]`: unchanged
    - an unknown target: unchanged
  - `TestProviderSchemaIsValid` (existing): must still pass with the new resource registered.
- [ ] **Step 2: Run them and see them fail.**
  `go test ./internal/provider/ -run 'Ruleset|BypassActors|EquivalentPatterns|ProviderSchema'`.
- [ ] **Step 3: Implement** the schema, the validators and the modifier, and register the resource.
- [ ] **Step 4: Run them and see them pass.** Also run `go test ./...` and `golangci-lint run`.
- [ ] **Step 5: Commit.** Message: `feat(provider): add ghost_repository_ruleset schema and validation`.

---

### Task 4: CRUD, import, rename handling and acceptance tests

**Files:**
- Modify: `internal/provider/ruleset_resource.go` (Create, Read, Update, Delete, ImportState, ModifyPlan, and
  `fromRemote` / `toRequest` on the model)
- Modify: `internal/provider/ruleset_resource_test.go` (acceptance tests)

**Interfaces:**
- Consumes:
  - Task 1: the client methods and `IsConflict`.
  - Task 2: all of it.
  - From `issue_label_resource.go`: `getRepository`, `sameRepository`, `client.SplitFullName`. Move
    `getRepository` to a shared free function, `lookupRepository(ctx, c *client.Client, fullName string)`,
    if both resources need it.
- Produces:

```go
func (m *repositoryRulesetResourceModel) toRequest(ctx context.Context) (client.RulesetRequest, diag.Diagnostics)
func (m *repositoryRulesetResourceModel) fromRemote(ctx context.Context, rs *client.Ruleset) diag.Diagnostics
```

`fromRemote` keeps prior values in these cases:
- **`conditions`.** If the remote include and exclude are both empty and the prior `conditions` is null, it
  stays null. If the prior has both lists empty, keep it.
- **`rules`.** If the remote rules are empty and the prior `rules` is null, it stays null. If the prior was
  non-null and `empty()`, keep it.
- **`bypass_actors`.** If `rs.BypassActors == nil`, keep the prior value.
- **Patterns.** Use `patternsFromRemote(target, prior, remote)`.

**409 handling** on Create and Update: call `ListRulesets`, find the ruleset by name, and add an error:
`a ruleset named %q already exists on %s; import it with: terraform import <address> %s`. The last value is
`rulesetID(repo, found.ID)`. If the list call fails, fall back to the message from the spec.

ModifyPlan and the rename and replace behaviour mirror `IssueLabelResource.ModifyPlan` and `Update`. Mark
`id` unknown when `repository` changes.

**Acceptance tests.** Each uses `acctest.RandomWithPrefix("tf-acc")` and a `CheckDestroy` that calls
`GetRuleset` and expects a 404. Helpers:

```go
func testAccRulesetConfig(repoName, rulesetHCL string) string // ghost_repository "test" (private, auto_init) + ruleset
func testAccCheckRulesetExists(address string) resource.TestCheckFunc // GETs it via testAccClient()
func testAccCheckRulesetDestroy(s *terraform.State) error
```

- [ ] **Step 1: Write the unit tests** for `toRequest` and `fromRemote`, using framework values built with
  `types.ObjectValueMust`:
  - `TestRulesetFromRemoteKeepsNulls`: a null prior `rules` and `conditions` with an empty remote stays null.
  - `TestRulesetFromRemoteKeepsPriorActorsWhenOmitted`.
  - `TestRulesetToRequestEmpty`: null `rules`, `conditions` and actors give a request with empty
    (non-nil) slices.
- [ ] **Step 2: Run them and see them fail. Then implement CRUD, ImportState, ModifyPlan, `toRequest` and
  `fromRemote` until the unit tests pass.**
- [ ] **Step 3: Write the acceptance tests:**
  - **`TestAccRepositoryRuleset_basic`.** Steps:
    1. A minimal ruleset (`name`, `enforcement = "active"`, with no conditions, rules or actors).
       `testAccCheckRulesetExists`, and the plan is empty (Review Focus 4).
    2. Set every field: `include = ["main","release/*"]`, `exclude = ["release/old"]`, every rule flag on,
       non-default `pull_request` values, `required_check = [{context="ci/build"}]`, and a
       `RepositoryRole` maintain actor in `pull_request` mode.
       Assert the attributes. A follow-up plan must be empty: the framework's default post-apply plan check
       covers this.
    3. Set `required_status_checks = { required_check = [] }`, which must round-trip (Review Focus 5).
    4. Switch `target = "tag"` with `include = ["v*"]`. Assert that the state keeps `v*`, and use the client
       to check that remote `include[0] == "refs/tags/v*"`.
    5. Clear everything: `rules = {}` (all false), and `conditions = { ref_name = { include = [], exclude = [] } }`.
       Remove the actors. The plan must be empty (Review Focus 4).
    6. Back to `target = "branch"` with `include = ["main"]`.
    7. `ImportState` with `ImportStateIdFunc` reading `id` from state. Import stores `refs/heads/main`, so
       verify with `ImportStateVerifyIgnore: ["conditions"]` and check the remote form explicitly.
    8. A `PlanOnly` step with the step 6 config, run against the imported state. Expect no changes (Review
       Focus 1).
    9. Out-of-band delete: a `PreConfig` calls `DeleteRuleset` through `testAccClient()`, and a `PlanOnly`
       step with `ExpectNonEmptyPlan: true` follows (Review Focus 2).
  - **`TestAccRepositoryRuleset_duplicateName`.** Two rulesets with the same name in one config, the second
    using `depends_on` the first. `ExpectError` matches `already exists.*terraform import`.
  - **`TestAccRepositoryRuleset_repositoryRename`** and **`_repositoryReplaced`.** Mirror the
    `ghost_issue_label` tests: a rename keeps `ruleset_id`, and a replacement gives a new `ruleset_id`.
  - **`TestAccRepositoryRuleset_organization`.**
    - Skip it unless `GHOST_TEST_ORGANIZATION` is set.
    - `PreConfig` gets the first team via `ListOrganizationTeams`. Fail with "organization needs at least one
      team" if there is none.
    - Configure an org repo (`owner` set as in `ghost_repository.organization` in the playground) with all
      three actor types and `bypass_mode` values. Assert the attributes and an empty plan.
- [ ] **Step 4: Run the acceptance tests.** Run
  `GHOST_TOKEN=$(sed -nE 's/^ghost_token *= *"(.*)"/\1/p' /Users/jonny/orca/projects/terraform-provider-ghost/playground/terraform.tfvars) GHOST_TEST_ORGANIZATION=pippin TF_ACC=1 go test ./internal/provider/ -run TestAccRepositoryRuleset -timeout 20m`.
  Never echo the token.
  All of them should pass. Then confirm no `tf-acc-*` repositories are left over (list them through the
  client or v3).
- [ ] **Step 5: Run the full unit suite and lint.** `go test ./...` and `golangci-lint run` should report 0
  issues.
- [ ] **Step 6: Commit.** Message: `feat(provider): add ghost_repository_ruleset resource`.

---

### Task 5: Playground, docs, roadmap and changelog

**Files:**
- Modify: `playground/main.tf`, `playground/terraform.tfvars.example`
- Create: `examples/resources/ghost_repository_ruleset/resource.tf` and `import.sh`, following
  `examples/resources/ghost_issue_label/`
- Modify: `ROADMAP.md`, `CHANGELOG.md`, `README.md:35` and `CLAUDE.md` (the acceptance-test line: add the
  optional `GHOST_TEST_ORGANIZATION`)
- Regenerate: `docs/` via `go generate ./...`

**Playground:**
- Add `ghost_repository_ruleset.minimal` and `.full` on `ghost_repository.full`. The full one sets every
  argument, with `replace_triggered_by = [ghost_repository.full.repo_id]`.
- Add `ghost_repository_ruleset.organization`:
  - `count = var.organization == null ? 0 : 1`
  - three actor types, with the Team actor included only when `var.organization_team_id != null`, using a
    `concat` of conditional lists
- Add a new variable, `organization_team_id`, with a default of `null`, and document it in
  `terraform.tfvars.example`.
- Add outputs as for the labels.

- [ ] **Step 1: Write the example, the playground additions and the tracking updates.**
  - ROADMAP #5: mark it ✅ and rewrite the notes to say "native API, string team id", removing the
    numeric-team-id note.
  - CHANGELOG Unreleased: `**New Resource:** ghost_repository_ruleset`.
- [ ] **Step 2: Validate.** `terraform -chdir=playground init -backend=false && terraform -chdir=playground validate`
  should succeed. This needs a dev override or a local build; follow the playground README.
- [ ] **Step 3: Apply the playground live.** Run `make build` and then apply the playground, once with
  `organization = "pippin"` and the team id, after the Task 4 run has had at least a minute's gap. The second
  plan should be empty. Then `terraform destroy`.
- [ ] **Step 4: Run `go generate ./...`.** Afterwards, `git status` should show only the new or changed files
  under `docs/`.
- [ ] **Step 5: Commit.** Message: `docs(provider): document ghost_repository_ruleset and add it to the playground`.

---

### Task 6: Open the PR

- [ ] **Step 1: Push and open the PR.**
  - Run `git push -u origin feat/ghost-repository-ruleset`.
  - Open the PR against `main`, titled `feat(provider): add ghost_repository_ruleset resource`.
  - If PR #4 isn't merged yet, the body states that it builds on #4, and why.
  - The body ends with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- [ ] **Step 2: Report.** Give the user the PR URL, the acceptance test results, and any probe result that
  changed the design.
