package provider

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestBranchesFilter(t *testing.T) {
	names := []string{"Upper", "a", "feat/x", "main"}
	protected := map[string]client.ProtectedRef{
		"main":   {Rules: []string{"deletion"}},
		"feat/x": {Rules: []string{}},
		"gone":   {Rules: []string{"deletion"}},
	}

	tests := []struct {
		name                      string
		onlyProtected, onlyNonPro bool
		want                      string
	}{
		{"no filter", false, false, "Upper a feat/x main"},
		{"only protected", true, false, "feat/x main"},
		{"only non-protected", false, true, "Upper a"},
		{"both", true, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, b := range filterBranches(names, protected, tt.onlyProtected, tt.onlyNonPro) {
				got = append(got, b.Name)
				if _, ok := protected[b.Name]; ok != (b.Protected != nil) {
					t.Errorf("%s: protected = %v", b.Name, b.Protected)
				}
			}
			if strings.Join(got, " ") != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBranchesSetBranches(t *testing.T) {
	branches := []listedBranch{
		{Name: "feat/x"},
		{Name: "main", Protected: &client.ProtectedRef{Rules: []string{"pull_request", "deletion", "pull_request"}}, SHA: "abc"},
		{Name: "release", Protected: &client.ProtectedRef{Rules: []string{}}},
	}

	m := branchesDataSourceModel{DefaultBranch: types.StringValue("main")}
	if diags := m.setBranches(branches); diags.HasError() {
		t.Fatalf("setBranches: %v", diags)
	}

	want := []string{
		`{"default":false,"name":"feat/x","protected":false,"rules":[],"sha":<null>}`,
		`{"default":true,"name":"main","protected":true,"rules":["deletion","pull_request"],"sha":"abc"}`,
		`{"default":false,"name":"release","protected":true,"rules":[],"sha":<null>}`,
	}
	elems := m.Branches.Elements()
	if len(elems) != len(want) {
		t.Fatalf("branches = %s", m.Branches)
	}
	for i, e := range elems {
		if got := e.String(); got != want[i] {
			t.Errorf("branches[%d] = %s\nwant %s", i, got, want[i])
		}
	}
	if got := m.Names.String(); got != `["feat/x","main","release"]` {
		t.Errorf("names = %s", got)
	}
	if got := branches[1].Protected.Rules; strings.Join(got, ",") != "pull_request,deletion,pull_request" {
		t.Errorf("setBranches changed its input: %v", got)
	}

	// An empty repository has no default branch and gives empty lists, not null.
	m = branchesDataSourceModel{DefaultBranch: types.StringNull()}
	if diags := m.setBranches(nil); diags.HasError() {
		t.Fatalf("setBranches: %v", diags)
	}
	for name, l := range map[string]types.List{"branches": m.Branches, "names": m.Names} {
		if l.IsNull() || len(l.Elements()) != 0 {
			t.Errorf("%s = %s, want an empty list", name, l)
		}
	}
}

func TestBranchesDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no repository", ``, `(?s)"repository" is required`},
		{"no slash", `repository = "alice"`, `must be "owner/slug"`},
		{"two slashes", `repository = "alice/repo/x"`, `must be "owner/slug"`},
		{"empty", `repository = ""`, `must be "owner/slug"`},
		{"both filters", `repository = "alice/repo"` + "\n" + `only_protected_branches = true` + "\n" +
			`only_non_protected_branches = true`, `(?s)Conflicting filters`},
		{"computed names", `repository = "alice/repo"` + "\n" + `names = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_branches\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// A false filter alongside a true one is not a conflict.
func TestBranchesDataSourceFalseFilterIsNoConflict(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			// The read fails, without a token, only after validation passes.
			Config: `
provider "ghost" {
  token    = "x"
  base_url = "http://127.0.0.1:1"
}

data "ghost_branches" "test" {
  repository                  = "alice/repo"
  only_protected_branches     = true
  only_non_protected_branches = false
}
`,
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`Error reading repository`),
		}},
	})
}

func TestAccBranchesDataSource_basic(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-branches")
	config := func(dataArgs string) string {
		return testAccRepositoryConfig(repoName, `auto_init = true
  visibility = "public"`) + `
resource "ghost_branch" "one" {
  repository = ghost_repository.test.full_name
  branch     = "feat/one"
}

resource "ghost_branch" "deep" {
  repository = ghost_repository.test.full_name
  branch     = "feat/two/deep"
}

resource "ghost_branch" "release" {
  repository = ghost_repository.test.full_name
  branch     = "release"
}

resource "ghost_repository_ruleset" "main_and_release" {
  repository  = ghost_repository.test.full_name
  name        = "tf-acc-main-and-release"
  enforcement = "active"
  conditions = {
    ref_name = { include = ["refs/heads/main", "refs/heads/release"] }
  }
  rules = {
    deletion         = true
    non_fast_forward = true
  }
}

resource "ghost_repository_ruleset" "main" {
  repository  = ghost_repository.test.full_name
  name        = "tf-acc-main"
  enforcement = "active"
  conditions = {
    ref_name = { include = ["refs/heads/main"] }
  }
  rules = {
    deletion                = true
    required_linear_history = true
  }
}

# Evaluate mode doesn't protect anything.
resource "ghost_repository_ruleset" "evaluate" {
  repository  = ghost_repository.test.full_name
  name        = "tf-acc-evaluate"
  enforcement = "evaluate"
  conditions = {
    ref_name = { include = ["refs/heads/feat/one"] }
  }
  rules = {
    deletion = true
  }
}

data "ghost_branches" "test" {
  repository = ghost_repository.test.full_name
  ` + dataArgs + `

  depends_on = [
    ghost_branch.one, ghost_branch.deep, ghost_branch.release,
    ghost_repository_ruleset.main_and_release, ghost_repository_ruleset.main, ghost_repository_ruleset.evaluate,
  ]
}
`
	}
	const d = testAccBranchesData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					resource.TestCheckResourceAttr(d, "default_branch", "main"),
					testAccCheckBranchesList("names", "feat/one", "feat/two/deep", "main", "release"),
					resource.TestCheckResourceAttr(d, "branches.#", "4"),
					testAccCheckListedBranch(0, "feat/one", false, false),
					testAccCheckListedBranch(1, "feat/two/deep", false, false),
					testAccCheckListedBranch(2, "main", true, true, "deletion", "non_fast_forward", "required_linear_history"),
					testAccCheckListedBranch(3, "release", false, true, "deletion", "non_fast_forward"),
					resource.TestCheckNoResourceAttr(d, "branches.0.sha"),
					resource.TestCheckNoResourceAttr(d, "branches.2.sha"),
				),
			},
			{
				Config: config("only_protected_branches = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchesList("names", "main", "release"),
					testAccCheckListedBranch(0, "main", true, true, "deletion", "non_fast_forward", "required_linear_history"),
				),
			},
			{
				Config: config("only_non_protected_branches = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchesList("names", "feat/one", "feat/two/deep"),
					resource.TestCheckResourceAttr(d, "default_branch", "main"),
				),
			},
		},
	})
}

func TestAccBranchesDataSource_includeSHA(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-branches")
	config := func(includeSHA bool) string {
		return testAccBranchConfig(repoName, `branch = "feat/x"`) + fmt.Sprintf(`
data "ghost_branches" "test" {
  repository  = ghost_repository.test.full_name
  include_sha = %t
  depends_on  = [ghost_branch.test]
}
`, includeSHA)
	}
	const d = testAccBranchesData
	var pushed string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(false),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchesList("names", "feat/x", "main"),
					resource.TestCheckNoResourceAttr(d, "branches.0.sha"),
					resource.TestCheckNoResourceAttr(d, "branches.1.sha"),
				),
			},
			{
				// Both branches start at the same commit.
				Config: config(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d, "branches.0.sha", "ghost_branch.test", "sha"),
					resource.TestCheckResourceAttrPair(d, "branches.1.sha", "ghost_branch.test", "sha"),
					resource.TestMatchResourceAttr(d, "branches.0.sha", regexp.MustCompile(`^[0-9a-f]{40}$`)),
				),
			},
			{
				// A push moves the branch; the data source follows it.
				PreConfig: func() { pushed = testAccPushCommit(t, repoName, "feat/x", "") },
				Config:    config(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(d, "branches.0.sha", func(v string) error {
						if v != pushed {
							return fmt.Errorf("feat/x sha = %s, want the pushed commit %s", v, pushed)
						}
						return nil
					}),
					resource.TestCheckResourceAttrWith(d, "branches.1.sha", func(v string) error {
						if v == pushed {
							return fmt.Errorf("main sha = %s, want it not to have moved", v)
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccBranchesDataSource_emptyRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-branches")
	const d = testAccBranchesData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{{
			Config: testAccRepositoryConfig(repoName, "") + `
data "ghost_branches" "test" {
  repository  = ghost_repository.test.full_name
  include_sha = true
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
				resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
				resource.TestCheckNoResourceAttr(d, "default_branch"),
				resource.TestCheckResourceAttr(d, "names.#", "0"),
				resource.TestCheckResourceAttr(d, "branches.#", "0"),
			),
		}},
	})
}

// The first push to an empty repository makes its branch HEAD, while the
// stored default_branch stays "main". The data source reports HEAD, which is
// also what ~DEFAULT_BRANCH in a ruleset matches.
func TestAccBranchesDataSource_headDiffersFromDefault(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-branches")
	config := testAccRepositoryConfig(repoName, `visibility = "public"`) + `
resource "ghost_repository_ruleset" "default" {
  repository  = ghost_repository.test.full_name
  name        = "tf-acc-default"
  enforcement = "active"
  conditions = {
    ref_name = { include = ["~DEFAULT_BRANCH"] }
  }
  rules = {
    deletion = true
  }
}

data "ghost_branches" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository_ruleset.default]
}

data "ghost_repository" "test" {
  full_name  = ghost_repository.test.full_name
  depends_on = [ghost_repository_ruleset.default]
}
`
	const d = testAccBranchesData

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(d, "default_branch"),
					resource.TestCheckResourceAttr(d, "names.#", "0"),
				),
			},
			{
				PreConfig: func() { testAccPushInitialCommit(t, repoName, "dev", "feat/x") },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "default_branch", "dev"),
					resource.TestCheckResourceAttr("data.ghost_repository.test", "default_branch", "main"),
					testAccCheckBranchesList("names", "dev", "feat/x"),
					testAccCheckListedBranch(0, "dev", true, true, "deletion"),
					testAccCheckListedBranch(1, "feat/x", false, false),
				),
			},
		},
	})
}

// The data source follows a repository rename, and matches the owner and
// slug case-insensitively, reporting the current full name as id.
func TestAccBranchesDataSource_repositoryRename(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-branches")
	owner := testAccTokenLogin(t)
	oldFullName := owner + "/" + repoName
	config := func(name, repository string) string {
		return fmt.Sprintf(`
resource "ghost_repository" "test" {
  name      = %q
  auto_init = true
}

data "ghost_branches" "test" {
  repository = %q
  depends_on = [ghost_repository.test]
}
`, name, repository)
	}
	const d = testAccBranchesData

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
					testAccCheckBranchesList("names", "main"),
				),
			},
			{
				Config: config(repoName+"-renamed", oldFullName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", oldFullName+"-renamed"),
					resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					testAccCheckBranchesList("names", "main"),
				),
			},
		},
	})
}

func TestAccBranchesDataSource_notFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
data "ghost_branches" "test" {
  repository = "tf-acc-no-such-owner-x9/tf-acc-missing"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Repository not found.*tf-acc-no-such-owner-x9/tf-acc-missing`),
			},
		},
	})
}

// testAccBranchesData is the data source the acceptance tests check.
const testAccBranchesData = "data.ghost_branches.test"

// testAccCheckBranchesList checks that the list attribute key of
// testAccBranchesData is exactly want, in order.
func testAccCheckBranchesList(key string, want ...string) resource.TestCheckFunc {
	const address = testAccBranchesData
	checks := []resource.TestCheckFunc{resource.TestCheckResourceAttr(address, key+".#", fmt.Sprint(len(want)))}
	for i, v := range want {
		checks = append(checks, resource.TestCheckResourceAttr(address, fmt.Sprintf("%s.%d", key, i), v))
	}
	return resource.ComposeAggregateTestCheckFunc(checks...)
}

// testAccCheckListedBranch checks branches[i] of testAccBranchesData.
func testAccCheckListedBranch(i int, name string, isDefault, protected bool, rules ...string) resource.TestCheckFunc {
	prefix := fmt.Sprintf("branches.%d.", i)
	return resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr(testAccBranchesData, prefix+"name", name),
		resource.TestCheckResourceAttr(testAccBranchesData, prefix+"default", fmt.Sprint(isDefault)),
		resource.TestCheckResourceAttr(testAccBranchesData, prefix+"protected", fmt.Sprint(protected)),
		testAccCheckBranchesList(prefix+"rules", rules...),
	)
}

// testAccPushInitialCommit pushes a first commit to the empty repository
// repoName, on each of branches. The first one becomes git HEAD.
func testAccPushInitialCommit(t *testing.T, repoName string, branches ...string) {
	t.Helper()
	_, repo := testAccRepository(t, repoName)
	git := testAccGit(t, repo)

	git("init", "-q", "-b", branches[0])
	git("commit", "-q", "--allow-empty", "-m", "tf-acc initial push")
	refs := make([]string, len(branches))
	for i, b := range branches {
		refs[i] = "HEAD:refs/heads/" + b
	}
	git(append([]string{"push", "-q", repo.CloneURL}, refs...)...)
}
