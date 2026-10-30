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

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestRepositoryStatusContextsDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no repository", ``, `(?s)"repository" is required`},
		{"no slash", `repository = "alice"`, `must be "owner/slug"`},
		{"two slashes", `repository = "alice/repo/x"`, `must be "owner/slug"`},
		{"empty", `repository = ""`, `must be "owner/slug"`},
		{"computed contexts", `repository = "alice/repo"` + "\n" + `contexts = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed repository_id", `repository = "alice/repo"` + "\n" + `repository_id = 1`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_repository_status_contexts\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testStatusContextsServer serves the repository lookup, which reports the
// repository as renamed to Alice/renamed, and the native status-contexts
// route, which answers with status and body.
func testStatusContextsServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/alice/repo":
			_, _ = w.Write([]byte(`{"id":7,"name":"renamed","full_name":"Alice/renamed","owner":{"login":"Alice"}}`))
		case "/api/repositories/Alice/renamed/status-contexts":
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
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

data "ghost_repository_status_contexts" "test" {
  repository = "alice/repo"
}
`, srv.URL)
}

// Contexts are kept in Ghost's order and case, read from the repository's
// current name.
func TestRepositoryStatusContextsDataSourceRead(t *testing.T) {
	fifty := make([]string, 50)
	for i := range fifty {
		fifty[i] = fmt.Sprintf(`"bulk/%02d"`, 50-i)
	}
	const d = "data.ghost_repository_status_contexts.test"

	for _, tc := range []struct {
		name  string
		body  string
		check resource.TestCheckFunc
	}{
		{"order and case", `{"contexts":["ci/build","CI/Build","default","a/b"]}`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "id", "Alice/renamed"),
			resource.TestCheckResourceAttr(d, "repository", "alice/repo"),
			resource.TestCheckResourceAttr(d, "repository_id", "7"),
			resource.TestCheckResourceAttr(d, "contexts.#", "4"),
			resource.TestCheckResourceAttr(d, "contexts.0", "ci/build"),
			resource.TestCheckResourceAttr(d, "contexts.1", "CI/Build"),
			resource.TestCheckResourceAttr(d, "contexts.2", "default"),
			resource.TestCheckResourceAttr(d, "contexts.3", "a/b"),
		)},
		{"fifty", `{"contexts":[` + strings.Join(fifty, ",") + `]}`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "contexts.#", "50"),
			resource.TestCheckResourceAttr(d, "contexts.0", "bulk/50"),
			resource.TestCheckResourceAttr(d, "contexts.49", "bulk/01"),
		)},
		// An empty list, not null, so length() and for expressions work.
		{"empty", `{"contexts":[]}`, resource.TestCheckResourceAttr(d, "contexts.#", "0")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: testStatusContextsServer(t, http.StatusOK, tc.body),
					Check:  tc.check,
				}},
			})
		})
	}
}

// A token that can only read the repository gets a 403, which explains that
// write access is needed.
func TestRepositoryStatusContextsDataSourceForbidden(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testStatusContextsServer(t, http.StatusForbidden,
				`{"statusCode":403,"error":"Error","message":"Insufficient permissions for this repository"}`),
			ExpectError: regexp.MustCompile(`(?s)Not allowed to list status contexts.*Alice/renamed.*needs\s+write\s+access.*Insufficient\s+permissions`),
		}},
	})
}

// Contexts are repository-wide (from any commit), most recently reported
// first, case-sensitive, and "default" for a status reported without one.
func TestAccRepositoryStatusContextsDataSource_basic(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-status-contexts")
	config := testAccRepositoryConfig(repoName, "auto_init = true") + `
data "ghost_repository_status_contexts" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository.test]
}
`
	const d = testAccStatusContextsData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					testAccCheckStatusContexts(),
				),
			},
			{
				PreConfig: func() {
					testAccPostStatuses(t, testAccTokenLogin(t)+"/"+repoName, "ci/build", "CI/Build", "ci/test")
					// A second commit: contexts aren't per commit.
					testAccPushCommit(t, repoName, "main", "")
					testAccPostStatuses(t, testAccTokenLogin(t)+"/"+repoName, "")
				},
				Config: config,
				Check:  testAccCheckStatusContexts("default", "ci/test", "CI/Build", "ci/build"),
			},
		},
	})
}

// Reporting a context again moves it to the front without repeating it.
func TestAccRepositoryStatusContextsDataSource_order(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-status-contexts")
	config := testAccRepositoryConfig(repoName, "auto_init = true") + `
data "ghost_repository_status_contexts" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository.test]
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() { testAccPostStatuses(t, testAccTokenLogin(t)+"/"+repoName, "a", "b", "c") },
				Config:    config,
				Check:     testAccCheckStatusContexts("c", "b", "a"),
			},
			{
				PreConfig: func() { testAccPostStatuses(t, testAccTokenLogin(t)+"/"+repoName, "a") },
				Config:    config,
				Check:     testAccCheckStatusContexts("a", "c", "b"),
			},
		},
	})
}

func TestAccRepositoryStatusContextsDataSource_emptyRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-status-contexts")
	const d = testAccStatusContextsData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{{
			Config: testAccRepositoryConfig(repoName, "") + `
data "ghost_repository_status_contexts" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository.test]
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
				testAccCheckStatusContexts(),
			),
		}},
	})
}

// The data source follows a repository rename, and matches the owner and
// slug case-insensitively, reporting the current full name as id.
func TestAccRepositoryStatusContextsDataSource_repositoryRename(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-status-contexts")
	owner := testAccTokenLogin(t)
	oldFullName := owner + "/" + repoName
	config := func(name, repository string) string {
		return fmt.Sprintf(`
resource "ghost_repository" "test" {
  name      = %q
  auto_init = true
}

data "ghost_repository_status_contexts" "test" {
  repository = %q
  depends_on = [ghost_repository.test]
}
`, name, repository)
	}
	const d = testAccStatusContextsData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(repoName, strings.ToUpper(oldFullName)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", oldFullName),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					testAccCheckStatusContexts(),
				),
			},
			{
				PreConfig: func() { testAccPostStatuses(t, oldFullName, "ci/build") },
				Config:    config(repoName+"-renamed", oldFullName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", oldFullName+"-renamed"),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					testAccCheckStatusContexts("ci/build"),
				),
			},
		},
	})
}

func TestAccRepositoryStatusContextsDataSource_organization(t *testing.T) {
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION to test status contexts of organization repositories")
	}
	repoName := acctest.RandomWithPrefix("tf-acc-status-contexts")
	config := fmt.Sprintf(`
resource "ghost_repository" "test" {
  owner     = %q
  name      = %q
  auto_init = true
}

data "ghost_repository_status_contexts" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository.test]
}
`, org, repoName)
	const d = testAccStatusContextsData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: config, Check: testAccCheckStatusContexts()},
			{
				PreConfig: func() { testAccPostStatuses(t, org+"/"+repoName, "ci/org") },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
					testAccCheckStatusContexts("ci/org"),
				),
			},
		},
	})
}

// The contexts feed a ruleset's required checks with no perpetual diff, and
// requiring a check doesn't add it to the contexts.
func TestAccRepositoryStatusContextsDataSource_ruleset(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-status-contexts")
	repoConfig := testAccRepositoryConfig(repoName, "auto_init = true")
	config := repoConfig + `
data "ghost_repository_status_contexts" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository.test]
}
` + testAccRulesetBlock(`name        = "tf-acc-required-checks"
  enforcement = "active"
  conditions = {
    ref_name = { include = ["~DEFAULT_BRANCH"] }
  }
  rules = {
    required_status_checks = {
      required_check = concat(
        [for c in data.ghost_repository_status_contexts.test.contexts : { context = c }],
        [{ context = "never/reported" }],
      )
    }
  }`)
	const r = "ghost_repository_ruleset.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: repoConfig},
			{
				PreConfig: func() { testAccPostStatuses(t, testAccTokenLogin(t)+"/"+repoName, "ci/build", "ci/test") },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(r, "rules.required_status_checks.required_check.#", "3"),
					resource.TestCheckResourceAttr(r, "rules.required_status_checks.required_check.0.context", "ci/test"),
					resource.TestCheckResourceAttr(r, "rules.required_status_checks.required_check.1.context", "ci/build"),
					resource.TestCheckResourceAttr(r, "rules.required_status_checks.required_check.2.context", "never/reported"),
				),
			},
			{
				// Reading again: no diff, and the required checks didn't
				// become contexts.
				Config: config,
				Check:  testAccCheckStatusContexts("ci/test", "ci/build"),
			},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestAccRepositoryStatusContextsDataSource_notFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "ghost_repository_status_contexts" "test" {
  repository = "tf-acc-no-such-owner-x9/tf-acc-missing"
}
`,
			ExpectError: regexp.MustCompile(`(?s)Repository not found.*tf-acc-no-such-owner-x9/tf-acc-missing`),
		}},
	})
}

// testAccStatusContextsData is the data source the acceptance tests check.
const testAccStatusContextsData = "data.ghost_repository_status_contexts.test"

// testAccCheckStatusContexts checks that testAccStatusContextsData lists
// exactly want, in order.
func testAccCheckStatusContexts(want ...string) resource.TestCheckFunc {
	const address = testAccStatusContextsData
	checks := []resource.TestCheckFunc{resource.TestCheckResourceAttr(address, "contexts.#", fmt.Sprint(len(want)))}
	for i, v := range want {
		checks = append(checks, resource.TestCheckResourceAttr(address, fmt.Sprintf("contexts.%d", i), v))
	}
	return resource.ComposeAggregateTestCheckFunc(checks...)
}

// testAccPostStatuses reports a success status for each of contexts, in
// order, on the tip of main in the repository fullName. An empty
// context reports one without a context, which Ghost stores as "default".
func testAccPostStatuses(t *testing.T, fullName string, contexts ...string) {
	t.Helper()
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	owner, repo, err := client.SplitFullName(fullName)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	commit, err := c.GetCommitStatus(ctx, owner, repo, "refs/heads/main")
	if err != nil {
		t.Fatalf("resolving main in %s: %v", fullName, err)
	}
	for _, sc := range contexts {
		if _, err := c.CreateCommitStatus(ctx, owner, repo, commit.SHA, client.CreateCommitStatusRequest{
			State: "success", Context: sc, Description: "tf-acc",
		}); err != nil {
			t.Fatalf("reporting status %q on %s: %v", sc, fullName, err)
		}
	}
}
