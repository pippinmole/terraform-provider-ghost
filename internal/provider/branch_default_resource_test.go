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

// TestBranchDefaultConfigValidation runs offline: the attribute validators
// reject the config before the provider is ever configured.
func TestBranchDefaultConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"empty branch", `branch = ""`, `(?i)length`},
		{"full ref", `branch = "refs/heads/main"`, `(?s)without the refs/heads/.*"main"`},
		{"tag ref", `branch = "refs/tags/v1"`, `"refs/tags/v1" is a full ref`},
		{"colon", `branch = "a:b"`, `must not contain ":"`},
		{"bad repository", `repository = "nope"`, `must be "owner/slug"`},
		{"nested repository", `repository = "a/b/c"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := map[string]string{"repository": `"alice/repo"`, "branch": `"feat"`}
			key, _, _ := strings.Cut(tt.config, " ")
			delete(attrs, key)
			config := "resource \"ghost_branch_default\" \"test\" {\n  " + tt.config + "\n"
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

func TestAccBranchDefaultResource(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccBranchDefaultConfig(repoName, "ghost_branch.a.branch"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"ghost_branch_default.test", tfjsonpath.New("id"),
						"ghost_repository.test", tfjsonpath.New("full_name"),
						compare.ValuesSame(),
					),
					statecheck.CompareValuePairs(
						"ghost_branch_default.test", tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_branch_default.test", "branch", "feat/a"),
					resource.TestCheckResourceAttr("ghost_branch_default.test", "previous_branch", "main"),
					testAccCheckDefaultBranch("feat/a"),
				),
			},
			{
				ResourceName: "ghost_branch_default.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["ghost_repository.test"].Primary.Attributes["full_name"], nil
				},
				ImportStateVerify: true,
				// Only known when Terraform changed the default itself.
				ImportStateVerifyIgnore: []string{"previous_branch"},
			},
			{
				// Changing the branch updates the setting in place.
				Config: testAccBranchDefaultConfig(repoName, "ghost_branch.b.branch"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_branch_default.test", "branch", "b"),
					resource.TestCheckResourceAttr("ghost_branch_default.test", "previous_branch", "main"),
					testAccCheckDefaultBranch("b"),
				),
			},
			{
				// A default branch changed outside Terraform is put back.
				PreConfig: func() { testAccSetDefaultBranch(t, repoName, "main") },
				Config:    testAccBranchDefaultConfig(repoName, "ghost_branch.b.branch"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionUpdate)},
				},
				Check: testAccCheckDefaultBranch("b"),
			},
			{
				// A literal name of an existing branch works too.
				Config: testAccBranchDefaultConfig(repoName, `"main"`),
				Check:  testAccCheckDefaultBranch("main"),
			},
			// The final destroy deletes ghost_branch.a and .b after this
			// resource, which only works because main is the default.
			{Config: testAccBranchDefaultConfig(repoName, "ghost_branch.b.branch")},
		},
	})
}

// TestAccBranchDefaultResource_destroyRestores covers the destroy-order trap:
// the default branch can't be deleted, and this resource is destroyed before
// the ghost_branch it references, so Delete must move the default back.
func TestAccBranchDefaultResource_destroyRestores(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccBranchDefaultConfig(repoName, "ghost_branch.a.branch"),
				Check:  testAccCheckDefaultBranch("feat/a"),
			},
			{
				// Switching the default to b and deleting a in one apply fails:
				// Terraform updates a resource only after destroying what it
				// depended on, so a is deleted while it is still the default.
				Config:      testAccRepositoryConfig(repoName, "auto_init = true") + testAccBranchDefaultBranches("b") + testAccBranchDefaultBlock("ghost_branch.b.branch"),
				ExpectError: regexp.MustCompile(`(?s)default branch.*separate apply`),
			},
			{
				// Switching first, in its own apply, works.
				Config: testAccBranchDefaultConfig(repoName, "ghost_branch.b.branch"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionUpdate)},
				},
				Check: testAccCheckDefaultBranch("b"),
			},
			{
				Config: testAccRepositoryConfig(repoName, "auto_init = true") + testAccBranchDefaultBranches("b") + testAccBranchDefaultBlock("ghost_branch.b.branch"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_branch.a", plancheck.ResourceActionDestroy),
						plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckDefaultBranch("b"),
					testAccCheckBranchGone("feat/a"),
				),
			},
			{
				// Removing the resource and its branch, keeping the repository:
				// the default goes back to main and the branch can be deleted.
				Config: testAccRepositoryConfig(repoName, "auto_init = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckDefaultBranch("main"),
					testAccCheckBranchGone("b"),
				),
			},
		},
	})
}

func TestAccBranchDefaultResource_repositoryRename(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{Config: testAccBranchDefaultConfig(repoName, "ghost_branch.a.branch")},
			{
				// Renaming the repository keeps the setting.
				Config: testAccBranchDefaultConfig(renamed, "ghost_branch.a.branch"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"ghost_branch_default.test", tfjsonpath.New("id"),
						"ghost_repository.test", tfjsonpath.New("full_name"),
						compare.ValuesSame(),
					),
				},
				Check: testAccCheckDefaultBranch("feat/a"),
			},
		},
	})
}

func TestAccBranchDefaultResource_otherRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(target string) string {
		return testAccRepositoryConfig(repoName, "auto_init = true") +
			strings.Replace(testAccRepositoryConfig(repoName+"-other", "auto_init = true"), `"test"`, `"other"`, 1) + fmt.Sprintf(`
resource "ghost_branch" "a" {
  repository = ghost_repository.test.full_name
  branch     = "a"
}

resource "ghost_branch" "other" {
  repository = ghost_repository.other.full_name
  branch     = "a"
}

resource "ghost_branch_default" "test" {
  repository = %[1]s.full_name
  branch     = "a"
  depends_on = [ghost_branch.a, ghost_branch.other]
}
`, target)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{Config: config("ghost_repository.test"), Check: testAccCheckDefaultBranch("a")},
			{
				// A different repository replaces the resource, and the first
				// repository gets its old default back.
				Config: config("ghost_repository.other"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckDefaultBranch("main"),
					testAccCheckRepositoryDefaultBranch("ghost_repository.other", "a"),
					resource.TestCheckResourceAttrPair("ghost_branch_default.test", "repository_id", "ghost_repository.other", "repo_id"),
				),
			},
		},
	})
}

// TestAccBranchDefaultResource_withRepository checks that leaving
// ghost_repository.default_branch unset doesn't fight this resource.
func TestAccBranchDefaultResource_withRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(description string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("auto_init = true\n  description = %q", description)) +
			testAccBranchDefaultBranches("feat/a") + testAccBranchDefaultBlock("ghost_branch.a.branch")
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				Config: config("one"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// The repository's computed default_branch follows along.
				Config: config("two"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction("ghost_branch_default.test", plancheck.ResourceActionNoop),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_repository.test", "default_branch", "feat/a"),
					testAccCheckDefaultBranch("feat/a"),
				),
			},
		},
	})
}

func TestAccBranchDefaultResource_emptyRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				// An empty repository has no branch to make the default.
				Config:      testAccRepositoryConfig(repoName, "") + testAccBranchDefaultBlock(`"main"`),
				ExpectError: regexp.MustCompile(`(?s)no commits yet.*auto_init`),
			},
		},
	})
}

func TestAccBranchDefaultResource_errors(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				// Names are case-sensitive and the branch must exist.
				Config:      testAccRepositoryConfig(repoName, "auto_init = true") + testAccBranchDefaultBlock(`"Main"`),
				ExpectError: regexp.MustCompile(`(?s)Branch "Main" doesn't exist.*ghost_branch`),
			},
			{
				Config: testAccRepositoryConfig(repoName, "auto_init = true") + `
data "ghost_user" "me" {}

resource "ghost_branch_default" "test" {
  repository = "${data.ghost_user.me.login}/tf-acc-does-not-exist"
  branch     = "main"
}
`,
				ExpectError: regexp.MustCompile(`Repository .*tf-acc-does-not-exist not found`),
			},
			{
				Config: testAccRepositoryConfig(repoName, "auto_init = true") + testAccBranchDefaultBlock(`"main"`),
				Check:  testAccCheckDefaultBranch("main"),
			},
			{
				// A branch that doesn't exist fails the update and leaves the
				// default branch alone.
				Config:      testAccRepositoryConfig(repoName, "auto_init = true") + testAccBranchDefaultBlock(`"tf-acc-nope"`),
				ExpectError: regexp.MustCompile(`(?s)Branch "tf-acc-nope" doesn't exist`),
			},
			{
				ResourceName:  "ghost_branch_default.test",
				ImportState:   true,
				ImportStateId: "owner/repo:main",
				ExpectError:   regexp.MustCompile(`import ID must be "owner/repo"`),
			},
			{
				Config: testAccRepositoryConfig(repoName, "auto_init = true") + testAccBranchDefaultBlock(`"main"`),
				Check:  testAccCheckDefaultBranch("main"),
			},
		},
	})
}

// testAccBranchDefaultConfig returns an initialised repository with branches
// feat/a and b, and a ghost_branch_default.test whose branch is the given
// expression.
func testAccBranchDefaultConfig(repoName, branch string) string {
	return testAccRepositoryConfig(repoName, "auto_init = true") +
		testAccBranchDefaultBranches("feat/a", "b") + testAccBranchDefaultBlock(branch)
}

// testAccBranchDefaultBranches returns ghost_branch resources in
// ghost_repository.test, named after the last path segment of each branch.
func testAccBranchDefaultBranches(branches ...string) string {
	var config string
	for _, b := range branches {
		name := b[strings.LastIndex(b, "/")+1:]
		config += fmt.Sprintf(`
resource "ghost_branch" %q {
  repository = ghost_repository.test.full_name
  branch     = %q
}
`, name, b)
	}
	return config
}

func testAccBranchDefaultBlock(branch string) string {
	return fmt.Sprintf(`
resource "ghost_branch_default" "test" {
  repository = ghost_repository.test.full_name
  branch     = %s
}
`, branch)
}

// testAccCheckDefaultBranch checks the default branch of ghost_repository.test.
func testAccCheckDefaultBranch(want string) resource.TestCheckFunc {
	return testAccCheckRepositoryDefaultBranch("ghost_repository.test", want)
}

// testAccCheckRepositoryDefaultBranch checks the default branch of the
// repository at address, as the native branch list reports it.
func testAccCheckRepositoryDefaultBranch(address, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[address]
		if !ok {
			return fmt.Errorf("%s not in state", address)
		}
		c, err := testAccClient()
		if err != nil {
			return err
		}
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["full_name"])
		if err != nil {
			return err
		}
		branches, err := c.ListBranches(context.Background(), owner, repo)
		if err != nil {
			return err
		}
		if got := stringOrEmpty(branches.DefaultBranch); got != want {
			return fmt.Errorf("default branch of %s/%s is %q, want %q", owner, repo, got, want)
		}
		return nil
	}
}
