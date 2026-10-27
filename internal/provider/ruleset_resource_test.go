package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// TestRulesetConfigValidation runs offline: the validators reject the config
// before the provider is ever configured.
func TestRulesetConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		attrs  string
		expect string
	}{
		{"empty name", `name = ""`, `(?i)length`},
		{"long name", `name = "` + strings.Repeat("x", 101) + `"`, `(?i)length`},
		{"bad target", `target = "push"`, `value must be one of`},
		{"bad enforcement", `enforcement = "on"`, `value must be one of`},
		{"long pattern", `conditions = { ref_name = { include = ["` + strings.Repeat("x", 256) + `"] } }`, `(?i)length`},
		{"empty pattern", `conditions = { ref_name = { exclude = [""] } }`, `(?i)length`},
		{"duplicate include", `conditions = { ref_name = { include = ["main", "main"] } }`, `(?i)duplicate`},
		{"too many approvals", `rules = { pull_request = { required_approving_review_count = 11 } }`, `between`},
		{"no merge methods", `rules = { pull_request = { allowed_merge_methods = [] } }`, `at least\s+1`},
		{"bad merge method", `rules = { pull_request = { allowed_merge_methods = ["ff"] } }`, `value must be\s+one of`},
		{"empty context", `rules = { required_status_checks = { required_check = [{ context = "" }] } }`, `(?i)length`},
		{"role missing", `bypass_actors = [{ actor_type = "RepositoryRole" }]`, `(?i)required`},
		{"team missing", `bypass_actors = [{ actor_type = "Team" }]`, `(?i)required`},
		{"bad actor type", `bypass_actors = [{ actor_type = "User" }]`, `value must be one of`},
		{"bad role", `bypass_actors = [{ actor_type = "RepositoryRole", role = "read" }]`, `value must be one of`},
		{"admin with role", `bypass_actors = [{ actor_type = "OrganizationAdmin", role = "admin" }]`, `(?i)not allowed`},
		{"role with team", `bypass_actors = [{ actor_type = "RepositoryRole", role = "admin", team_id = "T1" }]`, `(?i)not allowed`},
		{"duplicate actor", `bypass_actors = [
    { actor_type = "OrganizationAdmin", bypass_mode = "always" },
    { actor_type = "OrganizationAdmin", bypass_mode = "pull_request" },
  ]`, `(?i)duplicate bypass actor`},
		{"bad repository", `repository = "nope"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := map[string]string{"repository": `"alice/repo"`, "name": `"protect"`, "enforcement": `"active"`}
			key, _, _ := strings.Cut(tt.attrs, " ")
			delete(attrs, key)
			config := "resource \"ghost_repository_ruleset\" \"test\" {\n  " + tt.attrs + "\n"
			for k, v := range attrs {
				config += "  " + k + " = " + v + "\n"
			}
			config += "}\n"

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      config,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func bypassActorValue(actorType, role, teamID attr.Value) attr.Value {
	return types.ObjectValueMust(bypassActorAttrTypes, map[string]attr.Value{
		"actor_type":  actorType,
		"role":        role,
		"team_id":     teamID,
		"bypass_mode": types.StringValue("always"),
	})
}

func validateBypassActors(elems ...attr.Value) validator.SetResponse {
	set := types.SetValueMust(types.ObjectType{AttrTypes: bypassActorAttrTypes}, elems)
	var resp validator.SetResponse
	bypassActorsValidator().ValidateSet(context.Background(), validator.SetRequest{
		Path: path.Root("bypass_actors"), ConfigValue: set,
	}, &resp)

	return resp
}

func TestBypassActorsValidatorUnknownTeamID(t *testing.T) {
	resp := validateBypassActors(
		bypassActorValue(types.StringValue("Team"), types.StringNull(), types.StringUnknown()),
		bypassActorValue(types.StringValue("Team"), types.StringNull(), types.StringValue("T1")),
	)
	if resp.Diagnostics.HasError() {
		t.Errorf("unknown team_id should skip validation: %v", resp.Diagnostics)
	}

	resp = validateBypassActors(
		bypassActorValue(types.StringValue("Team"), types.StringNull(), types.StringValue("T1")),
		bypassActorValue(types.StringValue("Team"), types.StringNull(), types.StringValue("T2")),
	)
	if resp.Diagnostics.HasError() {
		t.Errorf("distinct teams should be valid: %v", resp.Diagnostics)
	}
}

func emptyBypassActors() types.Set {
	return types.SetValueMust(types.ObjectType{AttrTypes: bypassActorAttrTypes}, []attr.Value{})
}

func TestRulesetFromRemoteKeepsNulls(t *testing.T) {
	ctx := context.Background()
	m := repositoryRulesetResourceModel{
		Repository:   types.StringValue("alice/repo"),
		Conditions:   types.ObjectNull(conditionsAttrTypes),
		Rules:        types.ObjectNull(rulesAttrTypes),
		BypassActors: emptyBypassActors(),
	}
	empty := []client.BypassActor{}
	diags := m.fromRemote(ctx, &client.Ruleset{
		ID: "ruleset_x", Name: "n", Target: "branch", Enforcement: "active",
		Include: []string{}, Exclude: []string{}, Rules: []client.Rule{}, BypassActors: &empty,
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !m.Conditions.IsNull() || !m.Rules.IsNull() {
		t.Errorf("conditions/rules should stay null: %v / %v", m.Conditions, m.Rules)
	}
	if m.ID.ValueString() != "alice/repo:ruleset_x" || m.RulesetID.ValueString() != "ruleset_x" {
		t.Errorf("id = %v, ruleset_id = %v", m.ID, m.RulesetID)
	}

	// Rules written out as all false stay as they were, not null.
	allFalse, d := types.ObjectValueFrom(ctx, rulesAttrTypes, rulesetRulesModel{
		Creation: types.BoolValue(false), Update: types.BoolValue(false), Deletion: types.BoolValue(false),
		NonFastForward: types.BoolValue(false), RequiredLinearHistory: types.BoolValue(false),
		RequiredSignatures:   types.BoolValue(false),
		PullRequest:          types.ObjectNull(pullRequestAttrTypes),
		RequiredStatusChecks: types.ObjectNull(statusChecksAttrTypes),
	})
	if d.HasError() {
		t.Fatal(d)
	}
	m.Rules = allFalse
	if diags := m.fromRemote(ctx, &client.Ruleset{ID: "ruleset_x", Target: "branch"}); diags.HasError() {
		t.Fatal(diags)
	}
	if !m.Rules.Equal(allFalse) {
		t.Errorf("all-false rules changed to %v", m.Rules)
	}
}

func TestRulesetFromRemoteKeepsPriorActorsWhenOmitted(t *testing.T) {
	ctx := context.Background()
	actors := types.SetValueMust(types.ObjectType{AttrTypes: bypassActorAttrTypes}, []attr.Value{
		bypassActorValue(types.StringValue("RepositoryRole"), types.StringValue("admin"), types.StringNull()),
	})
	m := repositoryRulesetResourceModel{
		Repository:   types.StringValue("alice/repo"),
		Conditions:   types.ObjectNull(conditionsAttrTypes),
		Rules:        types.ObjectNull(rulesAttrTypes),
		BypassActors: actors,
	}
	if diags := m.fromRemote(ctx, &client.Ruleset{ID: "ruleset_x", Target: "branch"}); diags.HasError() {
		t.Fatal(diags)
	}
	if !m.BypassActors.Equal(actors) {
		t.Errorf("bypass actors changed to %v", m.BypassActors)
	}
}

func TestRulesetToRequestEmpty(t *testing.T) {
	m := repositoryRulesetResourceModel{
		Name: types.StringValue("n"), Target: types.StringValue("branch"), Enforcement: types.StringValue("active"),
		Conditions:   types.ObjectNull(conditionsAttrTypes),
		Rules:        types.ObjectNull(rulesAttrTypes),
		BypassActors: types.SetNull(types.ObjectType{AttrTypes: bypassActorAttrTypes}),
	}
	req, diags := m.toRequest(context.Background())
	if diags.HasError() {
		t.Fatal(diags)
	}
	if req.Include == nil || req.Exclude == nil || req.BypassActors == nil || req.Rules == nil ||
		len(req.Include)+len(req.Exclude)+len(req.BypassActors)+len(req.Rules) != 0 {
		t.Errorf("want empty non-nil slices, got %+v", req)
	}
}

// TestRulesetRemoteRoundTrip checks that a full remote ruleset read into the
// model produces the same request when written back.
func TestRulesetRemoteRoundTrip(t *testing.T) {
	ctx := context.Background()
	actors := []client.BypassActor{
		{ActorType: "RepositoryRole", Role: "maintain", BypassMode: "pull_request"},
		{ActorType: "Team", TeamID: "T1", BypassMode: "always"},
		{ActorType: "OrganizationAdmin", BypassMode: "always"},
	}
	rules := rulesToAPI(rulesetRules{
		Deletion: true,
		PullRequest: &client.PullRequestParameters{
			RequiredApprovingReviewCount: 2, RequireCodeOwnerReview: true, AllowedMergeMethods: []string{"squash"},
		},
		RequiredStatusChecks: &client.RequiredStatusChecksParameters{
			RequiredStatusChecks: []client.StatusCheck{{Context: "b"}, {Context: "a"}}, DoNotEnforceOnCreate: true,
		},
	})
	remote := &client.Ruleset{
		ID: "ruleset_x", Name: "n", Target: "branch", Enforcement: "evaluate",
		Include: []string{"refs/heads/main", "~DEFAULT_BRANCH"}, Exclude: []string{"refs/heads/old"},
		BypassActors: &actors, Rules: rules,
	}
	m := repositoryRulesetResourceModel{
		Repository: types.StringValue("alice/repo"),
		Conditions: types.ObjectNull(conditionsAttrTypes),
		Rules:      types.ObjectNull(rulesAttrTypes),
	}
	if diags := m.fromRemote(ctx, remote); diags.HasError() {
		t.Fatal(diags)
	}
	req, diags := m.toRequest(ctx)
	if diags.HasError() {
		t.Fatal(diags)
	}
	want := client.RulesetRequest{
		Name: "n", Target: "branch", Enforcement: "evaluate",
		Include: remote.Include, Exclude: remote.Exclude, BypassActors: actors, Rules: rules,
	}
	sortActors := func(a []client.BypassActor) {
		sort.Slice(a, func(i, j int) bool { return bypassActorKey(a[i]) < bypassActorKey(a[j]) })
	}
	sortActors(req.BypassActors)
	sortActors(want.BypassActors)
	if fmt.Sprint(req) != fmt.Sprint(want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", req, want)
	}
}

const testAccRulesetFull = `
  name        = "protect"
  enforcement = "evaluate"
  conditions = {
    ref_name = {
      include = ["main", "release/*"]
      exclude = ["release/old"]
    }
  }
  bypass_actors = [
    { actor_type = "RepositoryRole", role = "maintain", bypass_mode = "pull_request" },
  ]
  rules = {
    creation                = true
    update                  = true
    deletion                = true
    non_fast_forward        = true
    required_linear_history = true
    required_signatures     = true
    pull_request = {
      required_approving_review_count   = 2
      dismiss_stale_reviews_on_push     = true
      require_code_owner_review         = true
      require_last_push_approval        = true
      required_review_thread_resolution = true
      allowed_merge_methods             = ["squash"]
    }
    required_status_checks = {
      required_check                       = [{ context = "ci/build" }]
      strict_required_status_checks_policy = true
      do_not_enforce_on_create             = true
    }
  }`

const testAccRulesetBranchMain = `
  name        = "protect"
  enforcement = "active"
  conditions  = { ref_name = { include = ["main"] } }
  rules       = { deletion = true }`

func TestAccRepositoryRuleset_basic(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	// Every in-place update keeps the same ruleset.
	sameRulesetID := statecheck.CompareValue(compare.ValuesSame())
	rulesetIDCheck := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{sameRulesetID.AddStateValue(testAccRulesetAddress, tfjsonpath.New("ruleset_id"))}
	}
	var capturedRepository, capturedID string
	emptyAfterApply := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRulesetDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRulesetConfig(repoName, `
  name        = "protect"
  enforcement = "active"`),
				ConfigPlanChecks:  emptyAfterApply,
				ConfigStateChecks: rulesetIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRulesetExists(),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "target", "branch"),
					resource.TestCheckNoResourceAttr(testAccRulesetAddress, "conditions"),
					resource.TestCheckNoResourceAttr(testAccRulesetAddress, "rules"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "bypass_actors.#", "0"),
					resource.TestCheckResourceAttrPair(testAccRulesetAddress, "repository_id", "ghost_repository.test", "repo_id"),
				),
			},
			{
				Config:            testAccRulesetConfig(repoName, testAccRulesetFull),
				ConfigPlanChecks:  emptyAfterApply,
				ConfigStateChecks: rulesetIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRulesetExists(),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "enforcement", "evaluate"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "conditions.ref_name.include.0", "main"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "conditions.ref_name.include.1", "release/*"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "conditions.ref_name.exclude.0", "release/old"),
					resource.TestCheckTypeSetElemNestedAttrs(testAccRulesetAddress, "bypass_actors.*", map[string]string{
						"actor_type": "RepositoryRole", "role": "maintain", "bypass_mode": "pull_request",
					}),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "rules.required_signatures", "true"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "rules.pull_request.required_approving_review_count", "2"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "rules.pull_request.allowed_merge_methods.#", "1"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "rules.required_status_checks.required_check.0.context", "ci/build"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "rules.required_status_checks.do_not_enforce_on_create", "true"),
					testAccCheckRulesetRemote(func(rs *client.Ruleset) error {
						if len(rs.Rules) != 8 {
							return fmt.Errorf("remote has %d rules, want 8", len(rs.Rules))
						}
						return nil
					}),
				),
			},
			{
				// A status checks rule with no checks round-trips.
				Config: testAccRulesetConfig(repoName, `
  name        = "protect"
  enforcement = "active"
  rules = {
    required_status_checks = { required_check = [] }
  }`),
				ConfigPlanChecks:  emptyAfterApply,
				ConfigStateChecks: rulesetIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccRulesetAddress, "rules.required_status_checks.required_check.#", "0"),
					resource.TestCheckNoResourceAttr(testAccRulesetAddress, "rules.pull_request"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "bypass_actors.#", "0"),
				),
			},
			{
				Config: testAccRulesetConfig(repoName, `
  name        = "protect"
  enforcement = "active"
  target      = "tag"
  conditions  = { ref_name = { include = ["v*"] } }`),
				ConfigPlanChecks:  emptyAfterApply,
				ConfigStateChecks: rulesetIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccRulesetAddress, "target", "tag"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "conditions.ref_name.include.0", "v*"),
					testAccCheckRulesetRemote(func(rs *client.Ruleset) error {
						if len(rs.Include) != 1 || rs.Include[0] != "refs/tags/v*" {
							return fmt.Errorf("remote include = %q, want [refs/tags/v*]", rs.Include)
						}
						return nil
					}),
				),
			},
			{
				// Writing "nothing" out explicitly is kept as written.
				Config: testAccRulesetConfig(repoName, `
  name        = "protect"
  enforcement = "active"
  target      = "tag"
  conditions  = { ref_name = { include = [], exclude = [] } }
  rules       = {}`),
				ConfigPlanChecks:  emptyAfterApply,
				ConfigStateChecks: rulesetIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccRulesetAddress, "conditions.ref_name.include.#", "0"),
					resource.TestCheckResourceAttr(testAccRulesetAddress, "rules.deletion", "false"),
					testAccCheckRulesetRemote(func(rs *client.Ruleset) error {
						if len(rs.Include)+len(rs.Exclude)+len(rs.Rules) != 0 {
							return fmt.Errorf("remote should be empty, got %+v", rs)
						}
						return nil
					}),
				),
			},
			{
				Config:            testAccRulesetConfig(repoName, testAccRulesetBranchMain),
				ConfigPlanChecks:  emptyAfterApply,
				ConfigStateChecks: rulesetIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccRulesetAddress, "conditions.ref_name.include.0", "main"),
					testAccCaptureRuleset(&capturedRepository, &capturedID),
				),
			},
			{
				ResourceName: testAccRulesetAddress,
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources[testAccRulesetAddress].Primary.Attributes["id"], nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"conditions"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					// Import reads the pattern the way Ghost stores it.
					if got := states[0].Attributes["conditions.ref_name.include.0"]; got != "refs/heads/main" {
						return fmt.Errorf("imported include[0] = %q, want refs/heads/main", got)
					}
					return nil
				},
			},
			{
				// Forget the ruleset without deleting it, so the next step can
				// import it into this state.
				Config: testAccRepositoryConfig(repoName, "") + `
removed {
  from = ` + testAccRulesetAddress + `
  lifecycle {
    destroy = false
  }
}
`,
			},
			{
				ResourceName: testAccRulesetAddress,
				Config:       testAccRulesetConfig(repoName, testAccRulesetBranchMain),
				ImportState:  true,
				ImportStateIdFunc: func(*terraform.State) (string, error) {
					return rulesetID(capturedRepository, capturedID), nil
				},
				ImportStatePersist: true,
			},
			{
				// After import, one no-op update restores the configured
				// spelling of the pattern, and then the plan is empty.
				Config: testAccRulesetConfig(repoName, testAccRulesetBranchMain),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccRulesetAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: rulesetIDCheck(),
				Check:             resource.TestCheckResourceAttr(testAccRulesetAddress, "conditions.ref_name.include.0", "main"),
			},
			{
				// A ruleset deleted outside Terraform is planned for creation.
				PreConfig: func() {
					c, err := testAccClient()
					if err != nil {
						t.Fatal(err)
					}
					owner, repo, err := client.SplitFullName(capturedRepository)
					if err != nil {
						t.Fatal(err)
					}
					if err := c.DeleteRuleset(context.Background(), owner, repo, capturedID); err != nil {
						t.Fatal(err)
					}
				},
				Config:             testAccRulesetConfig(repoName, testAccRulesetBranchMain),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

func TestAccRepositoryRuleset_duplicateName(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRulesetDestroy,
		Steps: []resource.TestStep{{
			Config: testAccRulesetConfig(repoName, `
  name        = "protect"
  enforcement = "active"
}

resource "ghost_repository_ruleset" "dup" {
  repository  = ghost_repository_ruleset.test.repository
  name        = "protect"
  enforcement = "active"
  depends_on  = [ghost_repository_ruleset.test]`),
			ExpectError: regexp.MustCompile(`(?s)already exists.*terraform import`),
		}},
	})
}

// testAccRulesetReplaceTriggeredBy is a ruleset configured as the docs
// recommend, so it is recreated along with its repository.
const testAccRulesetReplaceTriggeredBy = `name        = "protect"
  enforcement = "active"
  rules       = { deletion = true }

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }`

func TestAccRepositoryRuleset_repositoryRename(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"

	sameRulesetID := statecheck.CompareValue(compare.ValuesSame())
	stateChecks := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameRulesetID.AddStateValue(testAccRulesetAddress, tfjsonpath.New("ruleset_id")),
			statecheck.CompareValuePairs(
				testAccRulesetAddress, tfjsonpath.New("repository"),
				"ghost_repository.test", tfjsonpath.New("full_name"),
				compare.ValuesSame(),
			),
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRulesetDestroy,
		Steps: []resource.TestStep{
			{
				Config:            testAccRulesetConfig(repoName, testAccRulesetReplaceTriggeredBy),
				ConfigStateChecks: stateChecks(),
			},
			{
				// Renaming the repository keeps the ruleset, even with the
				// recommended replace_triggered_by.
				Config: testAccRulesetConfig(renamed, testAccRulesetReplaceTriggeredBy),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccRulesetAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: stateChecks(),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckRulesetExists(),
					resource.TestCheckResourceAttrWith(testAccRulesetAddress, "id", func(v string) error {
						if !strings.Contains(v, "/"+renamed+":") {
							return fmt.Errorf("id %q was not updated after the repository rename", v)
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccRepositoryRuleset_repositoryReplaced(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(license string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("license_template = %q", license)) +
			testAccRulesetBlock(testAccRulesetReplaceTriggeredBy)
	}

	differentRulesetID := statecheck.CompareValue(compare.ValuesDiffer())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRulesetDestroy,
		Steps: []resource.TestStep{
			{
				Config:            config("mit"),
				ConfigStateChecks: []statecheck.StateCheck{differentRulesetID.AddStateValue(testAccRulesetAddress, tfjsonpath.New("ruleset_id"))},
			},
			{
				// Replacing the repository deletes its rulesets;
				// replace_triggered_by recreates them.
				Config: config("apache-2.0"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(testAccRulesetAddress, plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					differentRulesetID.AddStateValue(testAccRulesetAddress, tfjsonpath.New("ruleset_id")),
					statecheck.CompareValuePairs(
						testAccRulesetAddress, tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
				},
				Check: testAccCheckRulesetExists(),
			},
		},
	})
}

func TestAccRepositoryRuleset_organization(t *testing.T) {
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test organization rulesets")
	}
	testAccPreCheck(t)

	// The config needs a team ID, so look it up before the test case starts.
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	teams, err := c.ListOrganizationTeams(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) == 0 {
		t.Fatalf("organization %s needs at least one team", org)
	}
	teamID := teams[0].ID

	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRulesetDestroy,
		Steps: []resource.TestStep{{
			Config: testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q", org)) + testAccRulesetBlock(fmt.Sprintf(`
  name        = "protect"
  enforcement = "active"
  bypass_actors = [
    { actor_type = "RepositoryRole", role = "admin" },
    { actor_type = "Team", team_id = %q, bypass_mode = "pull_request" },
    { actor_type = "OrganizationAdmin", bypass_mode = "always" },
  ]
  rules = { deletion = true }`, teamID)),
			ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: resource.ComposeAggregateTestCheckFunc(
				testAccCheckRulesetExists(),
				resource.TestCheckResourceAttr(testAccRulesetAddress, "bypass_actors.#", "3"),
				resource.TestCheckTypeSetElemNestedAttrs(testAccRulesetAddress, "bypass_actors.*", map[string]string{
					"actor_type": "RepositoryRole", "role": "admin", "bypass_mode": "always",
				}),
				resource.TestCheckTypeSetElemNestedAttrs(testAccRulesetAddress, "bypass_actors.*", map[string]string{
					"actor_type": "Team", "team_id": teamID, "bypass_mode": "pull_request",
				}),
				resource.TestCheckTypeSetElemNestedAttrs(testAccRulesetAddress, "bypass_actors.*", map[string]string{
					"actor_type": "OrganizationAdmin", "bypass_mode": "always",
				}),
			),
		}},
	})
}

// testAccRulesetConfig returns a repository and a ghost_repository_ruleset.test
// in it, with attrs as the ruleset's other arguments.
func testAccRulesetConfig(repoName, attrs string) string {
	return testAccRepositoryConfig(repoName, "") + testAccRulesetBlock(attrs)
}

// testAccRulesetBlock returns a ghost_repository_ruleset.test in
// ghost_repository.test, with attrs as its other arguments.
func testAccRulesetBlock(attrs string) string {
	return fmt.Sprintf(`
resource "ghost_repository_ruleset" "test" {
  repository = ghost_repository.test.full_name
  %s
}
`, attrs)
}

// testAccGetRuleset reads the ruleset in s straight from the API.
func testAccGetRuleset(s *terraform.State) (*client.Ruleset, error) {
	rs, ok := s.RootModule().Resources[testAccRulesetAddress]
	if !ok {
		return nil, fmt.Errorf("%s not in state", testAccRulesetAddress)
	}
	c, err := testAccClient()
	if err != nil {
		return nil, err
	}
	owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
	if err != nil {
		return nil, err
	}

	return c.GetRuleset(context.Background(), owner, repo, rs.Primary.Attributes["ruleset_id"])
}

// testAccRulesetAddress is the ruleset every acceptance test configures.
const testAccRulesetAddress = "ghost_repository_ruleset.test"

// testAccCheckRulesetExists checks that the ruleset in state exists remotely
// with the name in state.
func testAccCheckRulesetExists() resource.TestCheckFunc {
	return testAccCheckRulesetRemote(func(*client.Ruleset) error { return nil })
}

// testAccCheckRulesetRemote reads the ruleset from the API and
// passes it to check.
func testAccCheckRulesetRemote(check func(*client.Ruleset) error) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		remote, err := testAccGetRuleset(s)
		if err != nil {
			return fmt.Errorf("reading %s: %w", testAccRulesetAddress, err)
		}
		if want := s.RootModule().Resources[testAccRulesetAddress].Primary.Attributes["name"]; remote.Name != want {
			return fmt.Errorf("remote name is %q, state has %q", remote.Name, want)
		}
		return check(remote)
	}
}

// testAccCaptureRuleset records the repository and ruleset_id of the ruleset, for a later PreConfig, which has no access to state.
func testAccCaptureRuleset(repository, rulesetID *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[testAccRulesetAddress]
		if !ok {
			return fmt.Errorf("%s not in state", testAccRulesetAddress)
		}
		*repository = rs.Primary.Attributes["repository"]
		*rulesetID = rs.Primary.Attributes["ruleset_id"]
		return nil
	}
}

func testAccCheckRulesetDestroy(s *terraform.State) error {
	c, err := testAccClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_repository_ruleset" {
			continue
		}
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
		if err != nil {
			return err
		}
		_, err = c.GetRuleset(context.Background(), owner, repo, rs.Primary.Attributes["ruleset_id"])
		if err == nil {
			return fmt.Errorf("ruleset %s still exists", rs.Primary.ID)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("checking ruleset %s: %w", rs.Primary.ID, err)
		}
	}

	return nil
}
