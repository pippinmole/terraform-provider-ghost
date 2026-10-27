# `ghost_repository_ruleset` design

Status: approved for planning · 2026-10-27 · Roadmap Tier 1, #5

## Intent

Rulesets are Ghost's only form of branch and tag protection. People who manage a repository with
`ghost_repository` will want its protection in the same config. The resource should cover **everything
the Ghost API supports for rulesets**: every rule type, every rule parameter Ghost accepts, all three
bypass actor types, and both branch and tag targets.

## Goals

- One `ghost_repository_ruleset` resource with full CRUD and import, on personal and organization repositories.
- Plans that settle: after an apply, the next plan is empty. This holds when Ghost rewrites patterns, reorders
  rules, or fills in defaults.
- Configs Ghost would silently change are rejected at plan time with a clear error.
- Live acceptance tests for everything, including org-only bypass actors, plus unit tests for the client and
  for all the plan-time logic.

## Non-goals

- A `ghost_repository_rulesets` list data source (roadmap #37), `ghost_team` data source (#11), rule
  insights (`rule-suites`), the `rules/branches/{branch}` lookup, or the pattern `preview` endpoint.
- Org-level rulesets (not in Ghost).
- Status check `integration_id` (Ghost rejects it: "needs Ghost Apps").
- Timestamps (`createdAt`/`updatedAt`) or the v3 numeric id / web URL as attributes.

## Anti-goals

- A plan that never settles (perpetual diff) for any valid config.
- Silently accepting a config Ghost will store differently.
- Any v3 call for rulesets. v3 reports a `Team` bypass actor as an internal numeric `actor_id` that can't be
  mapped back to a team, so it can't round-trip teams.

## API facts (all probed live on 2026-10-25 and 2026-10-27)

Use the **native API only**. A PAT works for every call, including reads on private repositories. Base path:
`/api/repositories/{owner}/{repo}/rulesets`.

| Call | Notes |
| ---- | ----- |
| `POST /rulesets` | 201 with the ruleset. Admins only |
| `GET /rulesets/{id}` | 200. `bypassActors` is left out for non-admins. `{id}` is a string like `ruleset_tcql3h2wcrh4uqqxfndwsspt` |
| `GET /rulesets` | `{"rulesets": [...]}` |
| `PUT /rulesets/{id}` | **Full replace.** Every field is required: `name`, `target`, `enforcement`, `include`, `exclude`, `bypassActors`, `rules`. Empty arrays clear things |
| `DELETE /rulesets/{id}` | 204 |

The body (camelCase):

```json
{
  "name": "protect-main", "target": "branch", "enforcement": "active",
  "include": ["main", "~DEFAULT_BRANCH"], "exclude": [],
  "bypassActors": [
    {"actorType": "RepositoryRole", "role": "maintain", "bypassMode": "pull_request"},
    {"actorType": "Team", "teamId": "DG4eRcoHLcE1RfixZDetBcBzEQBBYIbb", "bypassMode": "always"},
    {"actorType": "OrganizationAdmin", "bypassMode": "always"}
  ],
  "rules": [
    {"type": "deletion"},
    {"type": "pull_request", "parameters": {
      "required_approving_review_count": 1, "dismiss_stale_reviews_on_push": false,
      "require_code_owner_review": false, "require_last_push_approval": false,
      "required_review_thread_resolution": false, "allowed_merge_methods": ["merge", "squash", "rebase"]}},
    {"type": "required_status_checks", "parameters": {
      "required_status_checks": [{"context": "ci/build"}],
      "strict_required_status_checks_policy": false, "do_not_enforce_on_create": false}}
  ]
}
```

Observed behaviour:

- **Fields and limits:**
  - `target` is `branch` or `tag`. `enforcement` is `active`, `evaluate` or `disabled`.
  - `name` is 1–100 characters and unique per repository. A duplicate gets a 409 `A ruleset named "x" already
    exists on this repository`, on POST and on a PUT rename.
  - Each pattern is 1–255 characters. An empty `include` is accepted.
- **Rules:**
  - Each rule type can appear at most once (400 otherwise).
  - Rule types: `creation`, `update`, `deletion`, `non_fast_forward`, `required_linear_history`,
    `required_signatures`, `pull_request`, `required_status_checks`.
  - Ghost returns rules in its own fixed order, whatever order they were sent in.
  - Parameters on the six parameterless rules are **silently dropped**.
- **`pull_request` parameters:**
  - Exactly the six keys shown. Unknown keys get a 400 ("Unrecognized key").
  - Ghost fills in defaults for keys left out: `false` for the booleans and all three merge methods. The default
    `required_approving_review_count` was not observed, so the builder probes it (see "Left to the builder").
  - `required_approving_review_count` can be at most 10. `allowed_merge_methods` must name at least one of
    `merge`, `squash`, `rebase`.
- **`required_status_checks` parameters:**
  - `required_status_checks` is required and may be an empty array; leaving it out gets a 400. Each item takes
    only `context` (`integration_id` gets a 400).
  - The two booleans default to `false`.
  - The OpenAPI spec wrongly says this rule takes no parameters.
- **Patterns:**
  - Ghost stores patterns in full ref form: `main` becomes `refs/heads/main` on a branch ruleset and
    `refs/tags/main` on a tag ruleset.
  - It leaves alone `~DEFAULT_BRANCH`, `~ALL`, and anything already starting with `refs/`. Even `refs/tags/v1`
    on a branch ruleset is kept as-is.
  - Changing `target` re-prefixes the stored patterns.
  - Order is preserved.
  - Matching rules: `*` matches within one path segment, `**` crosses segments.
- **Bypass actors:**
  - Order is preserved.
  - `RepositoryRole` requires `role` (`write`, `maintain` or `admin`); anything else gets a 400. `Team` requires
    `teamId`, which must be a team in the repository's org (422 `No team nope in this repository's organization`).
  - `bypassMode` is `always` or `pull_request`.
  - On a **personal** repository, only `RepositoryRole` is allowed (422 `Only repository roles can bypass
    rulesets on a personal repository`).
  - Ghost **silently drops** `role`/`teamId` when they don't apply to the actor type, and **silently
    collapses duplicates**: two `OrganizationAdmin` actors with different modes are stored as one, keeping
    the first.
- **Ids:** the team id is the string from `GET /api/organizations/{org}/teams` (`id`), and it is stable across
  team renames.
- **Rate limiting:** every request counts against the 120-per-minute PAT budget. PR #4 makes the client wait
  out 429s.

## Decided

### Schema

```hcl
resource "ghost_repository_ruleset" "example" {
  repository  = ghost_repository.example.full_name   # required, "owner/slug"
  name        = "protect-main"                        # required, 1–100 chars
  target      = "branch"                              # optional, default "branch"; "branch" | "tag"
  enforcement = "active"                              # required; "active" | "evaluate" | "disabled"
  include     = ["~DEFAULT_BRANCH", "release/*"]      # optional list, default []
  exclude     = ["release/old"]                       # optional list, default []

  bypass_actors = [                                   # optional set, default []
    { actor_type = "RepositoryRole", role = "maintain", bypass_mode = "pull_request" },
    { actor_type = "Team", team_id = "DG4e…", bypass_mode = "always" },
    { actor_type = "OrganizationAdmin" },             # bypass_mode defaults to "always"
  ]

  rules = {                                           # optional; null = no rules
    creation                = true                    # each flag optional, default false
    update                  = true
    deletion                = true
    non_fast_forward        = true
    required_linear_history = true
    required_signatures     = true

    pull_request = {                                  # optional; present = rule on
      required_approving_review_count   = 1           # 0–10
      dismiss_stale_reviews_on_push     = true
      require_code_owner_review         = true
      require_last_push_approval        = true
      required_review_thread_resolution = true
      allowed_merge_methods             = ["squash", "rebase"]   # set, non-empty
    }

    required_status_checks = {                        # optional; present = rule on
      contexts                             = ["ci/build", "ci/test"]  # required list, may be empty
      strict_required_status_checks_policy = true
      do_not_enforce_on_create             = true
    }
  }
}
```

Computed: `id` (`owner/slug:ruleset_xxx`), `ruleset_id` (the native string id), `repository_id` (numeric repo
id, as in `ghost_issue_label`).

- Use **nested attributes** (`SingleNestedAttribute`/`SetNestedAttribute`), not blocks, as shown.
- `rules` is an object with named fields, not a list, because Ghost reorders rules.
- Every optional `pull_request` and `required_status_checks` field gets a **static schema default equal to
  Ghost's own default**, so plans never show unknowns and the server never "fills in" something different.
- `bypass_actors` is a **set**, so order doesn't matter.
- `include` and `exclude` are **lists**, since Ghost keeps their order.

### Plan-time validation (beyond simple enum/length validators)

- `role` is required for `RepositoryRole` and not allowed otherwise. `team_id` is required for `Team` and not
  allowed otherwise. `OrganizationAdmin` takes neither.
- **No duplicate actors.** Identity is `actor_type` + `role`/`team_id`, ignoring `bypass_mode`, because that's
  how Ghost collapses them. The error names the duplicate.
- `include`/`exclude` values are 1–255 characters and unique within each list. `allowed_merge_methods` values
  are unique.
- Unknown values during plan (e.g. a `team_id` from another resource) skip the checks that need them.

The personal-repository restriction on actor types is **not** checked at plan time. It needs an API call, and
Ghost's 422 message is clear enough to surface as is.

### Read and normalisation

- **Patterns:** for each remote pattern, keep the prior (state or config) spelling when it normalises to the same
  ref. Normalising adds `refs/heads/` (branch) or `refs/tags/` (tag) unless the pattern starts with `refs/` or `~`.
  Otherwise use the remote value. This applies to Create/Update results and Read alike, so import shows Ghost's
  `refs/heads/...` form.
- **Rules:** map the remote rule list onto the `rules` object. When the remote has no rules and the prior `rules`
  was null, keep it null. When the prior was an object with every flag false and no nested rules, keep that.
  Both mean "no rules" and neither may cause a diff.
- **Bypass actors:** when the response leaves `bypassActors` out (the caller isn't a repository admin), keep the
  prior value instead of clearing it.
- 404 on Read removes the resource from state.

### Write path

- Create: look up the repository (v3 `GET /repos/{o}/{r}`, as `ghost_issue_label` does) to fill
  `repository_id`, then native `POST`.
- Update: native `PUT` with the **complete** object, built from the plan.
- Delete: native `DELETE`; a 404 counts as success.
- A 409 on Create or Update is shown as: "a ruleset named X already exists on <repo>; import it with
  `terraform import <address> <owner/slug>:<ruleset_id>`". The ruleset id can be found with `GET /rulesets`;
  the message should say so.
- Repository renames and replacement follow `ghost_issue_label` exactly:
  - `repository_id` plus a `ModifyPlan` that updates in place when the new name is the same repository, and
    forces a new resource otherwise.
  - The `replace_triggered_by = [ghost_repository.x.repo_id]` documentation note.
  - `id` is marked unknown when `repository` changes.
- Import: `owner/slug:ruleset_id`, split at the first `:`. Read then fills everything else, including
  `repository_id`.

### Client (`internal/client/rulesets.go`)

- Typed `Ruleset`, `RulesetRequest`, `BypassActor`, `Rule` (with raw or typed parameters), plus
  `CreateRuleset`, `GetRuleset`, `ReplaceRuleset` and `DeleteRuleset` on `NativePath(...)`.
- `ListOrganizationTeams(org)` (native `GET /api/organizations/{org}/teams`) for the acceptance tests to find a
  team id. It's also the start of the future `ghost_team` work.
- httptest unit tests in the style of `labels_test.go`.

### Testing

- **Unit:**
  - client request and response shapes
  - pattern normalisation and keep-spelling (branch, tag, `~` tokens, `refs/` passthrough, target change)
  - the rules-object ↔ rule-list mapping, including the null and all-false cases
  - bypass actor validation (every invalid combination and duplicate)
  - import id parsing
  - `bypassActors` left out of the response
- **Acceptance** (`TF_ACC`, `tf-acc-*` repositories, always destroyed and checked by a destroy check):
  1. Minimal ruleset on a personal repo, then update every field (including clearing rules and actors, and
     switching `target` to `tag`), then import and verify, then destroy.
  2. Full ruleset: every rule, every parameter at a non-default value, and a `RepositoryRole` actor. A plan
     after apply must be empty.
  3. Repository rename keeps the ruleset; repository replacement recreates it.
  4. **Org test**, skipped unless `GHOST_TEST_ORGANIZATION` is set (the owner uses `pippin`): creates an org
     repository and a ruleset with all three actor types, looking up the team id with
     `ListOrganizationTeams`. The org must have at least one team.
  5. 409 duplicate-name error message.
- Document `GHOST_TEST_ORGANIZATION` wherever the acceptance-test env vars are documented (README / CLAUDE.md
  test command).

### Repository housekeeping, in the same change

- `playground/main.tf`:
  - a minimal ruleset and a full ruleset (every argument) on `ghost_repository.full`
  - an org-repo ruleset with all three actor types, gated like `ghost_repository.organization`. Its team id
    comes from a new `organization_team_id` variable (default null; leave the team actor out when null),
    documented in `terraform.tfvars.example`
  - outputs as for the labels
- `ROADMAP.md`: mark #5 ✅ and rewrite its notes to match this design (native API, string team id).
- `CHANGELOG.md` Unreleased: `**New Resource:** ghost_repository_ruleset`.
- `go generate ./...`, and commit the regenerated docs. Give the resource doc an example that includes the
  `replace_triggered_by` note.

## Left to the builder

- Probe whether the native ruleset routes follow a renamed repository's old `owner/slug`. Either way, the
  resource always calls with the current `repository` value.
- Probe Ghost's default `required_approving_review_count` and use it as the schema default.
- Probe whether Ghost dedupes duplicate `contexts` or keeps them. If it dedupes, validate uniqueness; otherwise
  allow duplicates.
- Probe whether duplicate `include`/`exclude` patterns are kept. If they're deduped, the uniqueness validator
  above already covers it.
- Exact Go type layout, file split, and helper names, following the style of `issue_label_resource.go`.
- Wording of attribute descriptions (plain language, mention Ghost's quirks where they affect the user).
