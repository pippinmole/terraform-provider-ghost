package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// testAccRulesetsData is the data source the tests check.
const testAccRulesetsData = "data.ghost_repository_rulesets.test"

func TestRepositoryRulesetsDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no repository", ``, `(?s)"repository" is required`},
		{"no slash", `repository = "alice"`, `must be "owner/slug"`},
		{"two slashes", `repository = "alice/repo/x"`, `must be "owner/slug"`},
		{"empty", `repository = ""`, `must be "owner/slug"`},
		{"computed rulesets", `repository = "alice/repo"` + "\n" + `rulesets = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed repository_id", `repository = "alice/repo"` + "\n" + `repository_id = 1`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_repository_rulesets\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testRulesetsServer serves the repository lookup, which reports the
// repository as renamed to Alice/renamed, the native rulesets list (status
// and native) and the v3 one (v3). Any other request fails the test, which
// proves the reads use the current name and make no per-ruleset calls.
func testRulesetsServer(t *testing.T, status int, native, v3 string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/alice/repo":
			_, _ = w.Write([]byte(`{"id":7,"name":"renamed","full_name":"Alice/renamed","owner":{"login":"Alice"}}`))
		case "/api/repositories/Alice/renamed/rulesets":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(native))
		case "/api/v3/repos/Alice/renamed/rulesets":
			_, _ = w.Write([]byte(v3))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)

	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

data "ghost_repository_rulesets" "test" {
  repository = "alice/repo"
}
`, srv.URL)
}

// v3Summary returns a v3 ruleset summary as JSON.
func v3Summary(id int, name string) string {
	return fmt.Sprintf(`{"id":%d,"name":%q,"target":"branch","enforcement":"active","node_id":"RRS_%d",`+
		`"_links":{"html":{"href":"https://web.example/Alice/renamed/rules/%d"}}}`, id, name, id, id)
}

const fullNativeRuleset = `{"id":"ruleset_full","name":"full","target":"branch","enforcement":"active",` +
	`"include":["~DEFAULT_BRANCH","refs/heads/release/*"],"exclude":["refs/heads/release/old"],` +
	`"bypassActors":[{"actorType":"RepositoryRole","role":"admin","bypassMode":"always"},` +
	`{"actorType":"Team","teamId":"team_1","bypassMode":"pull_request"}],` +
	`"rules":[{"type":"creation"},{"type":"update"},{"type":"deletion"},{"type":"non_fast_forward"},` +
	`{"type":"required_linear_history"},{"type":"required_signatures"},` +
	`{"type":"pull_request","parameters":{"allowed_merge_methods":["squash","rebase"],"require_code_owner_review":true,` +
	`"require_last_push_approval":true,"dismiss_stale_reviews_on_push":true,"required_approving_review_count":2,` +
	`"required_review_thread_resolution":true}},` +
	`{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"ci/build"},{"context":"ci/test"}],` +
	`"do_not_enforce_on_create":true,"strict_required_status_checks_policy":true}}]}`

func TestRepositoryRulesetsDataSourceRead(t *testing.T) {
	const d = testAccRulesetsData

	for _, tc := range []struct {
		name   string
		native string
		v3     string
		check  resource.TestCheckFunc
	}{
		{"every field", `{"rulesets":[` + fullNativeRuleset + `]}`, `[` + v3Summary(76, "full") + `]`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "id", "Alice/renamed"),
			resource.TestCheckResourceAttr(d, "repository", "alice/repo"),
			resource.TestCheckResourceAttr(d, "repository_id", "7"),
			resource.TestCheckResourceAttr(d, "rulesets.#", "1"),
			resource.TestCheckResourceAttr(d, "rulesets.0.ruleset_id", "ruleset_full"),
			resource.TestCheckResourceAttr(d, "rulesets.0.numeric_id", "76"),
			resource.TestCheckResourceAttr(d, "rulesets.0.node_id", "RRS_76"),
			resource.TestCheckResourceAttr(d, "rulesets.0.html_url", "https://web.example/Alice/renamed/rules/76"),
			resource.TestCheckResourceAttr(d, "rulesets.0.name", "full"),
			resource.TestCheckResourceAttr(d, "rulesets.0.target", "branch"),
			resource.TestCheckResourceAttr(d, "rulesets.0.enforcement", "active"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.#", "2"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.0", "~DEFAULT_BRANCH"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.1", "refs/heads/release/*"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.exclude.#", "1"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.exclude.0", "refs/heads/release/old"),
			resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.#", "2"),
			resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.0.actor_type", "RepositoryRole"),
			resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.0.role", "admin"),
			resource.TestCheckNoResourceAttr(d, "rulesets.0.bypass_actors.0.team_id"),
			resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.0.bypass_mode", "always"),
			resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.1.actor_type", "Team"),
			resource.TestCheckNoResourceAttr(d, "rulesets.0.bypass_actors.1.role"),
			resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.1.team_id", "team_1"),
			resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.1.bypass_mode", "pull_request"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.creation", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.update", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.deletion", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.non_fast_forward", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_linear_history", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_signatures", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.required_approving_review_count", "2"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.dismiss_stale_reviews_on_push", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.require_code_owner_review", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.require_last_push_approval", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.required_review_thread_resolution", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.allowed_merge_methods.#", "2"),
			resource.TestCheckTypeSetElemAttr(d, "rulesets.0.rules.pull_request.allowed_merge_methods.*", "squash"),
			resource.TestCheckTypeSetElemAttr(d, "rulesets.0.rules.pull_request.allowed_merge_methods.*", "rebase"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.required_check.#", "2"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.required_check.0.context", "ci/build"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.required_check.1.context", "ci/test"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.strict_required_status_checks_policy", "true"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.do_not_enforce_on_create", "true"),
		)},
		// Ghost leaves bypassActors out for callers who aren't admins: null,
		// not an empty list. With no rules, every flag is false.
		{"not an admin, no rules", `{"rulesets":[{"id":"ruleset_tag","name":"tags","target":"tag","enforcement":"evaluate",` +
			`"include":["refs/tags/v*","~ALL"],"exclude":[],"rules":[]}]}`, `[` + v3Summary(1, "tags") + `]`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "rulesets.#", "1"),
			resource.TestCheckResourceAttr(d, "rulesets.0.target", "tag"),
			resource.TestCheckResourceAttr(d, "rulesets.0.enforcement", "evaluate"),
			resource.TestCheckNoResourceAttr(d, "rulesets.0.bypass_actors"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.0", "refs/tags/v*"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.1", "~ALL"),
			resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.exclude.#", "0"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.creation", "false"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.update", "false"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.deletion", "false"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.non_fast_forward", "false"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_linear_history", "false"),
			resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_signatures", "false"),
			resource.TestCheckNoResourceAttr(d, "rulesets.0.rules.pull_request"),
			resource.TestCheckNoResourceAttr(d, "rulesets.0.rules.required_status_checks"),
		)},
		// An admin sees [] when there are no bypass actors. A ruleset with no
		// v3 summary keeps the v3 fields null, and native order wins.
		{"order and missing summary", `{"rulesets":[` +
			`{"id":"ruleset_a","name":"a","target":"branch","enforcement":"active","include":[],"exclude":[],"bypassActors":[],"rules":[]},` +
			`{"id":"ruleset_b","name":"b","target":"branch","enforcement":"disabled","include":[],"exclude":[],"bypassActors":[],"rules":[]}]}`,
			`[` + v3Summary(9, "b") + `,` + v3Summary(8, "gone") + `]`, resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(d, "rulesets.#", "2"),
				resource.TestCheckResourceAttr(d, "rulesets.0.ruleset_id", "ruleset_a"),
				resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.#", "0"),
				resource.TestCheckNoResourceAttr(d, "rulesets.0.numeric_id"),
				resource.TestCheckNoResourceAttr(d, "rulesets.0.node_id"),
				resource.TestCheckNoResourceAttr(d, "rulesets.0.html_url"),
				resource.TestCheckResourceAttr(d, "rulesets.1.ruleset_id", "ruleset_b"),
				resource.TestCheckResourceAttr(d, "rulesets.1.numeric_id", "9"),
			)},
		// An empty list, not null, so length() and for expressions work.
		{"empty", `{"rulesets":[]}`, `[]`, resource.TestCheckResourceAttr(d, "rulesets.#", "0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: testRulesetsServer(t, http.StatusOK, tc.native, tc.v3),
					Check:  tc.check,
				}},
			})
		})
	}
}

func TestRepositoryRulesetsDataSourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		native string
		expect string
	}{
		{"unknown rule type", http.StatusOK,
			`{"rulesets":[{"id":"ruleset_x","name":"queue","target":"branch","enforcement":"active","rules":[{"type":"merge_queue"}]}]}`,
			`(?s)Unsupported ruleset.*"queue".*"merge_queue"`},
		{"server error", http.StatusInternalServerError, `{"statusCode":500,"message":"boom"}`, `(?s)Error listing rulesets.*boom`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testRulesetsServer(t, tc.status, tc.native, `[]`),
					ExpectError: regexp.MustCompile(tc.expect),
				}},
			})
		})
	}
}

// rulesetItems joins on the name, keeps native order and leaves the v3
// fields null without a summary.
func TestRulesetItemsJoin(t *testing.T) {
	ctx := context.Background()
	list, diags := rulesetItems(ctx,
		[]client.Ruleset{{ID: "ruleset_a", Name: "a"}, {ID: "ruleset_b", Name: "b"}},
		[]client.RulesetSummary{{ID: 2, Name: "b", NodeID: "RRS_b"}, {ID: 3, Name: "extra"}},
	)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var items []rulesetItemModel
	if diags := list.ElementsAs(ctx, &items, false); diags.HasError() {
		t.Fatal(diags)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].RulesetID != types.StringValue("ruleset_a") || !items[0].NumericID.IsNull() || !items[0].NodeID.IsNull() || !items[0].HTMLURL.IsNull() {
		t.Errorf("item 0 = %+v, want ruleset_a with null v3 fields", items[0])
	}
	if items[1].RulesetID != types.StringValue("ruleset_b") || items[1].NumericID != types.Int64Value(2) ||
		items[1].NodeID != types.StringValue("RRS_b") || !items[1].HTMLURL.IsNull() {
		t.Errorf("item 1 = %+v, want ruleset_b joined to summary 2", items[1])
	}
	if !items[0].BypassActors.IsNull() || items[0].Conditions.IsNull() || items[0].Rules.IsNull() {
		t.Errorf("item 0 = %+v, want null bypass_actors and set conditions and rules", items[0])
	}
}

// testAccRulesetsConfig returns a public repository, the rulesets given as
// resource blocks rN (an empty body leaves rN out, so the others keep their
// addresses), and the data source, which depends on all of them. Each
// ruleset depends on the previous one, so Ghost lists them in order.
func testAccRulesetsConfig(repoName, repository string, rulesets ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
resource "ghost_repository" "test" {
  name       = %q
  visibility = "public"
  auto_init  = true
}
`, repoName)
	deps := []string{"ghost_repository.test"}
	for i, body := range rulesets {
		if body == "" {
			continue
		}
		dependsOn := ""
		if len(deps) > 1 {
			dependsOn = "depends_on = [" + deps[len(deps)-1] + "]"
		}
		fmt.Fprintf(&b, `
resource "ghost_repository_ruleset" "r%d" {
  repository = ghost_repository.test.full_name
  %s
  %s
}
`, i, body, dependsOn)
		deps = append(deps, fmt.Sprintf("ghost_repository_ruleset.r%d", i))
	}
	fmt.Fprintf(&b, `
data "ghost_repository_rulesets" "test" {
  repository = %s
  depends_on = [%s]
}
`, repository, strings.Join(deps, ", "))

	return b.String()
}

// The fixture rulesets of TestAccRepositoryRulesetsDataSource_basic.
const (
	testAccRulesetsAll = `
  name        = "tf-acc-all"
  enforcement = "active"
  conditions  = { ref_name = { include = ["main", "release/*"], exclude = ["release/old"] } }
  bypass_actors = [
    { actor_type = "RepositoryRole", role = "admin" },
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
      required_approving_review_count   = 1
      dismiss_stale_reviews_on_push     = true
      require_code_owner_review         = true
      require_last_push_approval        = true
      required_review_thread_resolution = true
      allowed_merge_methods             = ["squash"]
    }
    required_status_checks = {
      required_check                       = [{ context = "ci/build" }, { context = "ci/test" }]
      strict_required_status_checks_policy = true
      do_not_enforce_on_create             = true
    }
  }`
	testAccRulesetsEvaluate = `
  name        = "tf-acc-evaluate"
  enforcement = "evaluate"
  conditions  = { ref_name = { include = ["~DEFAULT_BRANCH"] } }
  rules       = { deletion = true }`
	testAccRulesetsDisabled = `
  name        = "tf-acc-disabled"
  enforcement = "disabled"
  conditions  = { ref_name = { include = ["~ALL"] } }`
	testAccRulesetsTags = `
  name        = "tf-acc-tags"
  target      = "tag"
  enforcement = "active"
  conditions  = { ref_name = { include = ["v*"] } }
  rules       = { update = true, deletion = true }`
)

// testAccCheckRulesetsItem checks that item i of the data source is the
// ghost_repository_ruleset rN, with Ghost's v3 fields filled in.
func testAccCheckRulesetsItem(i, n int) resource.TestCheckFunc {
	p := fmt.Sprintf("rulesets.%d.", i)
	r := fmt.Sprintf("ghost_repository_ruleset.r%d", n)

	return resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttrPair(testAccRulesetsData, p+"ruleset_id", r, "ruleset_id"),
		resource.TestCheckResourceAttrPair(testAccRulesetsData, p+"name", r, "name"),
		resource.TestCheckResourceAttrPair(testAccRulesetsData, p+"target", r, "target"),
		resource.TestCheckResourceAttrPair(testAccRulesetsData, p+"enforcement", r, "enforcement"),
		resource.TestMatchResourceAttr(testAccRulesetsData, p+"numeric_id", regexp.MustCompile(`^[1-9][0-9]*$`)),
		resource.TestMatchResourceAttr(testAccRulesetsData, p+"node_id", regexp.MustCompile(`^RRS_`)),
		resource.TestMatchResourceAttr(testAccRulesetsData, p+"html_url", regexp.MustCompile(`^https://\S+/rules/[1-9][0-9]*$`)),
	)
}

// Every kind of ruleset, then one deleted, then none.
func TestAccRepositoryRulesetsDataSource_basic(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-rulesets")
	const d = testAccRulesetsData
	repository := "ghost_repository.test.full_name"
	emptyPlan := resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config:           testAccRulesetsConfig(repoName, repository, testAccRulesetsAll, testAccRulesetsEvaluate, testAccRulesetsDisabled, testAccRulesetsTags),
				ConfigPlanChecks: emptyPlan,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					resource.TestCheckResourceAttr(d, "rulesets.#", "4"),
					testAccCheckRulesetsItem(0, 0),
					testAccCheckRulesetsItem(1, 1),
					testAccCheckRulesetsItem(2, 2),
					testAccCheckRulesetsItem(3, 3),
					// Patterns as Ghost stores them.
					resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.#", "2"),
					resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.0", "refs/heads/main"),
					resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.include.1", "refs/heads/release/*"),
					resource.TestCheckResourceAttr(d, "rulesets.0.conditions.ref_name.exclude.0", "refs/heads/release/old"),
					resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.#", "2"),
					resource.TestCheckTypeSetElemNestedAttrs(d, "rulesets.0.bypass_actors.*", map[string]string{
						"actor_type": "RepositoryRole", "role": "admin", "bypass_mode": "always",
					}),
					resource.TestCheckTypeSetElemNestedAttrs(d, "rulesets.0.bypass_actors.*", map[string]string{
						"actor_type": "RepositoryRole", "role": "maintain", "bypass_mode": "pull_request",
					}),
					// The rules have the resource's shape, value for value.
					resource.TestCheckResourceAttrPair(d, "rulesets.0.rules.creation", "ghost_repository_ruleset.r0", "rules.creation"),
					resource.TestCheckResourceAttrPair(d, "rulesets.0.rules.update", "ghost_repository_ruleset.r0", "rules.update"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.deletion", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.non_fast_forward", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_linear_history", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_signatures", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.required_approving_review_count", "1"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.dismiss_stale_reviews_on_push", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.require_code_owner_review", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.require_last_push_approval", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.required_review_thread_resolution", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.allowed_merge_methods.#", "1"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.pull_request.allowed_merge_methods.0", "squash"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.required_check.#", "2"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.required_check.0.context", "ci/build"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.required_check.1.context", "ci/test"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.strict_required_status_checks_policy", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.0.rules.required_status_checks.do_not_enforce_on_create", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.1.conditions.ref_name.include.0", "~DEFAULT_BRANCH"),
					resource.TestCheckResourceAttr(d, "rulesets.1.rules.deletion", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.1.rules.update", "false"),
					resource.TestCheckNoResourceAttr(d, "rulesets.1.rules.pull_request"),
					// The token is an admin, so no actors is [], not null.
					resource.TestCheckResourceAttr(d, "rulesets.2.bypass_actors.#", "0"),
					resource.TestCheckResourceAttr(d, "rulesets.2.conditions.ref_name.include.0", "~ALL"),
					resource.TestCheckResourceAttr(d, "rulesets.2.conditions.ref_name.exclude.#", "0"),
					resource.TestCheckResourceAttr(d, "rulesets.2.rules.creation", "false"),
					resource.TestCheckResourceAttr(d, "rulesets.2.rules.deletion", "false"),
					resource.TestCheckNoResourceAttr(d, "rulesets.2.rules.required_status_checks"),
					resource.TestCheckResourceAttr(d, "rulesets.3.conditions.ref_name.include.0", "refs/tags/v*"),
					resource.TestCheckResourceAttr(d, "rulesets.3.rules.update", "true"),
					resource.TestCheckResourceAttr(d, "rulesets.3.rules.deletion", "true"),
				),
			},
			// A data source is read before the destroys in its step, so each
			// deletion is checked in the step after it.
			{Config: testAccRulesetsConfig(repoName, repository, testAccRulesetsAll, "", testAccRulesetsDisabled, testAccRulesetsTags)},
			{
				// r1 (evaluate) is gone; the others keep their place.
				Config:           testAccRulesetsConfig(repoName, repository, testAccRulesetsAll, "", testAccRulesetsDisabled, testAccRulesetsTags),
				ConfigPlanChecks: emptyPlan,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "rulesets.#", "3"),
					resource.TestCheckResourceAttr(d, "rulesets.0.name", "tf-acc-all"),
					resource.TestCheckResourceAttr(d, "rulesets.1.name", "tf-acc-disabled"),
					resource.TestCheckResourceAttr(d, "rulesets.2.name", "tf-acc-tags"),
					resource.TestCheckResourceAttrPair(d, "rulesets.0.ruleset_id", "ghost_repository_ruleset.r0", "ruleset_id"),
				),
			},
			{Config: testAccRulesetsConfig(repoName, repository)},
			{
				Config:           testAccRulesetsConfig(repoName, repository),
				ConfigPlanChecks: emptyPlan,
				Check:            resource.TestCheckResourceAttr(d, "rulesets.#", "0"),
			},
		},
	})
}

// A renamed repository is still found by its old name (in any case).
func TestAccRepositoryRulesetsDataSource_repositoryRename(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-rulesets")
	owner := testAccTokenLogin(t)
	oldFullName := owner + "/" + repoName
	const d = testAccRulesetsData
	ruleset := `
  name        = "tf-acc-rename"
  enforcement = "active"
  rules       = { deletion = true }`

	var rulesetID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRulesetsConfig(repoName, fmt.Sprintf("%q", strings.ToUpper(oldFullName)), ruleset),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", oldFullName),
					resource.TestCheckResourceAttr(d, "rulesets.#", "1"),
					testAccCheckRulesetsItem(0, 0),
					resource.TestCheckResourceAttrWith(d, "rulesets.0.ruleset_id", func(v string) error {
						rulesetID = v
						return nil
					}),
				),
			},
			{
				Config: testAccRulesetsConfig(repoName+"-renamed", fmt.Sprintf("%q", oldFullName), ruleset),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", oldFullName+"-renamed"),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					resource.TestCheckResourceAttr(d, "rulesets.#", "1"),
					resource.TestCheckResourceAttrWith(d, "rulesets.0.ruleset_id", func(v string) error {
						if v != rulesetID {
							return fmt.Errorf("ruleset_id = %s after the rename, want %s", v, rulesetID)
						}
						return nil
					}),
					resource.TestMatchResourceAttr(d, "rulesets.0.html_url",
						regexp.MustCompile("/"+regexp.QuoteMeta(oldFullName+"-renamed")+`/rules/\d+$`)),
				),
			},
		},
	})
}

// Every bypass actor type, in an organization repository.
func TestAccRepositoryRulesetsDataSource_organization(t *testing.T) {
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test organization rulesets")
	}
	testAccPreCheck(t)

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

	repoName := acctest.RandomWithPrefix("tf-acc-rulesets")
	const d = testAccRulesetsData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "ghost_repository" "test" {
  owner = %q
  name  = %q
}

resource "ghost_repository_ruleset" "r0" {
  repository  = ghost_repository.test.full_name
  name        = "tf-acc-org"
  enforcement = "active"
  bypass_actors = [
    { actor_type = "RepositoryRole", role = "admin" },
    { actor_type = "Team", team_id = %q, bypass_mode = "pull_request" },
    { actor_type = "OrganizationAdmin", bypass_mode = "always" },
  ]
  rules = { deletion = true }
}

data "ghost_repository_rulesets" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository_ruleset.r0]
}
`, org, repoName, teamID),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
				resource.TestCheckResourceAttr(d, "rulesets.#", "1"),
				testAccCheckRulesetsItem(0, 0),
				resource.TestCheckResourceAttr(d, "rulesets.0.bypass_actors.#", "3"),
				resource.TestCheckTypeSetElemNestedAttrs(d, "rulesets.0.bypass_actors.*", map[string]string{
					"actor_type": "RepositoryRole", "role": "admin", "bypass_mode": "always",
				}),
				resource.TestCheckTypeSetElemNestedAttrs(d, "rulesets.0.bypass_actors.*", map[string]string{
					"actor_type": "Team", "team_id": teamID, "bypass_mode": "pull_request",
				}),
				resource.TestCheckTypeSetElemNestedAttrs(d, "rulesets.0.bypass_actors.*", map[string]string{
					"actor_type": "OrganizationAdmin", "bypass_mode": "always",
				}),
			),
		}},
	})
}

func TestAccRepositoryRulesetsDataSource_notFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "ghost_repository_rulesets" "test" {
  repository = "tf-acc-no-such-owner-x9/tf-acc-missing"
}
`,
			ExpectError: regexp.MustCompile(`(?s)Repository not found.*tf-acc-no-such-owner-x9/tf-acc-missing`),
		}},
	})
}
