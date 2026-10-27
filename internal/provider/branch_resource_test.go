package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestParseBranchID(t *testing.T) {
	tests := []struct{ id, repository, branch string }{
		{"alice/repo:main", "alice/repo", "main"},
		{"alice/repo:feat/login", "alice/repo", "feat/login"},
	}
	for _, tt := range tests {
		repository, branch, err := parseBranchID(tt.id)
		if err != nil || repository != tt.repository || branch != tt.branch {
			t.Errorf("parseBranchID(%q) = %q, %q, %v", tt.id, repository, branch, err)
		}
	}

	for _, id := range []string{"", "alice/repo", "alice/repo:", "alice:main", "a/b/c:main", ":main", "alice/repo:a:b"} {
		if _, _, err := parseBranchID(id); err == nil {
			t.Errorf("parseBranchID(%q): expected error", id)
		}
	}
}

// TestBranchConfigValidation runs offline: the attribute validators reject
// the config before the provider is ever configured.
func TestBranchConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"empty branch", `branch = ""`, `(?i)length`},
		{"full ref", `branch = "refs/heads/main"`, `(?s)without the refs/heads/.*"main"`},
		{"colon", `branch = "a:b"`, `must not contain ":"`},
		{"empty source", `source = ""`, `(?i)length`},
		{"bad repository", `repository = "nope"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := map[string]string{"repository": `"alice/repo"`, "branch": `"feat"`}
			key, _, _ := strings.Cut(tt.config, " ")
			delete(attrs, key)
			config := "resource \"ghost_branch\" \"test\" {\n  " + tt.config + "\n"
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

func TestAccBranchResource(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				// A "/" in the name exercises path escaping on read and delete.
				Config: testAccBranchConfig(repoName, `branch = "feat/tf-acc"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch.test", plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchExists("ghost_branch.test"),
					resource.TestCheckResourceAttrPair("ghost_branch.test", "repository", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrPair("ghost_branch.test", "repository_id", "ghost_repository.test", "repo_id"),
					resource.TestCheckResourceAttrWith("ghost_branch.test", "id", func(v string) error {
						if !strings.HasSuffix(v, "/"+repoName+":feat/tf-acc") {
							return fmt.Errorf("unexpected id %q", v)
						}
						return nil
					}),
					resource.TestMatchResourceAttr("ghost_branch.test", "sha", regexp.MustCompile(`^[0-9a-f]{40}$`)),
					resource.TestCheckNoResourceAttr("ghost_branch.test", "source"),
				),
			},
			{
				ResourceName: "ghost_branch.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["ghost_repository.test"].Primary.Attributes["full_name"] + ":feat/tf-acc", nil
				},
				ImportStateVerify: true,
			},
			{
				// A branch started at another branch points at the same commit.
				Config: testAccBranchConfig(repoName, `branch = "feat/tf-acc"`) + `
resource "ghost_branch" "from_branch" {
  repository = ghost_repository.test.full_name
  branch     = "from-branch"
  source     = ghost_branch.test.branch
}
`,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"ghost_branch.from_branch", tfjsonpath.New("sha"),
						"ghost_branch.test", tfjsonpath.New("sha"),
						compare.ValuesSame(),
					),
				},
				Check: testAccCheckBranchExists("ghost_branch.from_branch"),
			},
			{
				// Branches can't be renamed, so a new name replaces the branch.
				Config: testAccBranchConfig(repoName, `branch = "feat/tf-acc-renamed"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch.test", plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchExists("ghost_branch.test"),
					testAccCheckBranchGone("feat/tf-acc"),
				),
			},
			{
				Config: testAccBranchConfig(repoName, `branch = "feat/tf-acc-renamed"`) + `
resource "ghost_branch" "dup" {
  repository = ghost_branch.test.repository
  branch     = ghost_branch.test.branch
}
`,
				ExpectError: regexp.MustCompile(`(?s)already exists.*terraform import`),
			},
		},
	})
}

func TestAccBranchResource_emptyRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				Config:      testAccRepositoryConfig(repoName, "") + testAccBranchBlock(`branch = "feat"`),
				ExpectError: regexp.MustCompile(`auto_init`),
			},
		},
	})
}

func TestAccBranchResource_repositoryRename(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{Config: testAccBranchConfig(repoName, testAccBranchReplaceTriggeredBy)},
			{
				// Renaming the repository keeps the branch, even with the
				// recommended replace_triggered_by.
				Config: testAccBranchConfig(renamed, testAccBranchReplaceTriggeredBy),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch.test", plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"ghost_branch.test", tfjsonpath.New("repository"),
						"ghost_repository.test", tfjsonpath.New("full_name"),
						compare.ValuesSame(),
					),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchExists("ghost_branch.test"),
					resource.TestCheckResourceAttrWith("ghost_branch.test", "id", func(v string) error {
						if !strings.HasSuffix(v, "/"+renamed+":feat") {
							return fmt.Errorf("id %q was not updated after the repository rename", v)
						}
						return nil
					}),
				),
			},
		},
	})
}

// testAccCheckBranchExists checks that the branch in state exists remotely
// and points at the SHA in state.
func testAccCheckBranchExists(address string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("%s not in state", address)
		}
		c, err := testAccClient()
		if err != nil {
			return err
		}
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
		if err != nil {
			return err
		}
		branch := rs.Primary.Attributes["branch"]
		branches, err := c.ListBranches(context.Background(), owner, repo)
		if err != nil {
			return fmt.Errorf("listing branches of %s/%s: %w", owner, repo, err)
		}
		if !branches.Has(branch) {
			return fmt.Errorf("branch %s not found in %v", rs.Primary.ID, branches.Names)
		}
		status, err := c.GetCommitStatus(context.Background(), owner, repo, branch)
		if err != nil {
			return fmt.Errorf("reading %s: %w", rs.Primary.ID, err)
		}
		if status.SHA != rs.Primary.Attributes["sha"] {
			return fmt.Errorf("sha in state is %s, remote is %s", rs.Primary.Attributes["sha"], status.SHA)
		}
		return nil
	}
}

// testAccCheckBranchGone checks that ghost_repository.test has no branch
// with the given name.
func testAccCheckBranchGone(branch string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		owner, repo, err := client.SplitFullName(s.RootModule().Resources["ghost_repository.test"].Primary.Attributes["full_name"])
		if err != nil {
			return err
		}
		branches, err := c.ListBranches(context.Background(), owner, repo)
		if err != nil {
			return err
		}
		if branches.Has(branch) {
			return fmt.Errorf("branch %s still exists in %s/%s", branch, owner, repo)
		}
		return nil
	}
}

// testAccBranchReplaceTriggeredBy is a branch configured as the docs
// recommend, so it is recreated along with its repository.
const testAccBranchReplaceTriggeredBy = `branch = "feat"

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }`

// testAccBranchConfig returns an initialised repository and a
// ghost_branch.test in it, with attrs as the branch's other arguments.
func testAccBranchConfig(repoName, attrs string) string {
	return testAccRepositoryConfig(repoName, "auto_init = true") + testAccBranchBlock(attrs)
}

// testAccBranchBlock returns a ghost_branch.test in ghost_repository.test,
// with attrs as its other arguments.
func testAccBranchBlock(attrs string) string {
	return fmt.Sprintf(`
resource "ghost_branch" "test" {
  repository = ghost_repository.test.full_name
  %s
}
`, attrs)
}

func testAccCheckBranchDestroy(s *terraform.State) error {
	c, err := testAccClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_branch" {
			continue
		}
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
		if err != nil {
			return err
		}
		branches, err := c.ListBranches(context.Background(), owner, repo)
		if client.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking branch %s: %w", rs.Primary.ID, err)
		}
		if branches.Has(rs.Primary.Attributes["branch"]) {
			return fmt.Errorf("branch %s still exists", rs.Primary.ID)
		}
	}

	return testAccCheckRepositoryDestroy(s)
}
