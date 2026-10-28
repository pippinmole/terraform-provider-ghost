package provider

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
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
		{"tag ref", `branch = "refs/tags/v1"`, `"refs/tags/v1" is a full ref`},
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
		status, err := c.GetCommitStatus(context.Background(), owner, repo, "refs/heads/"+branch)
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

func TestAccBranchResource_repositoryReplaced(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(license string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("auto_init = true\n  license_template = %q", license)) +
			testAccBranchBlock(testAccBranchReplaceTriggeredBy)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{Config: config("mit")},
			{
				// Changing a create-only argument replaces the repository,
				// which deletes its branches; replace_triggered_by recreates them.
				Config: config("apache-2.0"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction("ghost_branch.test", plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"ghost_branch.test", tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
				},
				Check: testAccCheckBranchExists("ghost_branch.test"),
			},
		},
	})
}

func TestAccBranchResource_sourceAndDrift(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	var pushed string

	base := testAccBranchConfig(repoName, `branch = "base"`) + `
resource "ghost_branch" "from_sha" {
  repository = ghost_repository.test.full_name
  branch     = "from-sha"
  source     = ghost_branch.test.sha
}
`
	withTag := base + `
resource "ghost_branch" "from_tag" {
  repository = ghost_repository.test.full_name
  branch     = "from-tag"
  source     = "tf-acc-tag"
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{
				// A branch started at a commit SHA.
				Config: base,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						"ghost_branch.from_sha", tfjsonpath.New("sha"),
						"ghost_branch.test", tfjsonpath.New("sha"),
						compare.ValuesSame(),
					),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchExists("ghost_branch.test"),
					testAccCheckBranchExists("ghost_branch.from_sha"),
				),
			},
			{
				// A push to a branch updates its sha on refresh without a
				// diff, and a branch can start at a tag. A source taken from
				// that sha changes with it, so from_sha is started again.
				PreConfig: func() { pushed = testAccPushCommit(t, repoName, "base", "tf-acc-tag") },
				Config:    withTag,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_branch.test", plancheck.ResourceActionNoop),
						plancheck.ExpectResourceAction("ghost_branch.from_sha", plancheck.ResourceActionReplace),
						plancheck.ExpectResourceAction("ghost_branch.from_tag", plancheck.ResourceActionCreate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchExists("ghost_branch.test"),
					testAccCheckBranchExists("ghost_branch.from_sha"),
					testAccCheckBranchExists("ghost_branch.from_tag"),
					resource.TestCheckResourceAttrWith("ghost_branch.test", "sha", func(v string) error {
						if v != pushed {
							return fmt.Errorf("sha = %s, want the pushed commit %s", v, pushed)
						}
						return nil
					}),
					resource.TestCheckResourceAttrWith("ghost_branch.from_tag", "sha", func(v string) error {
						if v != pushed {
							return fmt.Errorf("sha = %s, want the tagged commit %s", v, pushed)
						}
						return nil
					}),
				),
			},
			{
				// A branch deleted outside Terraform is created again.
				PreConfig: func() { testAccDeleteBranch(t, repoName, "from-tag") },
				Config:    withTag,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch.from_tag", plancheck.ResourceActionCreate)},
				},
				Check: testAccCheckBranchExists("ghost_branch.from_tag"),
			},
			{
				// Setting source on a branch that had none (e.g. after an
				// import) only records it.
				Config: strings.Replace(withTag, `branch = "base"`, "branch = \"base\"\n  source = \"main\"", 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch.test", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_branch.test", "source", "main"),
					resource.TestCheckResourceAttrPtr("ghost_branch.test", "sha", &pushed),
				),
			},
			{
				// Changing a set source starts the branch again.
				Config: withTag + `
resource "ghost_branch" "moved" {
  repository = ghost_repository.test.full_name
  branch     = "moved"
  source     = "main"
}
`,
			},
			{
				Config: withTag + `
resource "ghost_branch" "moved" {
  repository = ghost_repository.test.full_name
  branch     = "moved"
  source     = "tf-acc-tag"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch.moved", plancheck.ResourceActionReplace)},
				},
				Check: resource.TestCheckResourceAttrPtr("ghost_branch.moved", "sha", &pushed),
			},
			{
				Config: withTag + `
resource "ghost_branch" "missing_source" {
  repository = ghost_repository.test.full_name
  branch     = "missing-source"
  source     = "tf-acc-no-such-ref"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Can't start the branch at "tf-acc-no-such-ref"`),
			},
			{
				ResourceName:  "ghost_branch.test",
				ImportState:   true,
				ImportStateId: "not-an-id",
				ExpectError:   regexp.MustCompile(`import ID must be "owner/repo:branch"`),
			},
		},
	})
}

func TestAccBranchResource_defaultBranch(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{Config: testAccBranchConfig(repoName, `branch = "feat"`)},
			{
				// Ghost won't delete the default branch.
				PreConfig:   func() { testAccSetDefaultBranch(t, repoName, "feat") },
				Config:      testAccRepositoryConfig(repoName, "auto_init = true"),
				ExpectError: regexp.MustCompile(`(?s)default branch.*Change the default branch`),
			},
			{
				PreConfig: func() { testAccSetDefaultBranch(t, repoName, "main") },
				Config:    testAccRepositoryConfig(repoName, "auto_init = true"),
				Check:     testAccCheckBranchGone("feat"),
			},
		},
	})
}

func TestAccBranchResource_otherRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(target string) string {
		return testAccBranchConfig(repoName, `branch = "feat"`) +
			strings.Replace(testAccRepositoryConfig(repoName+"-other", "auto_init = true"), `"test"`, `"other"`, 1) +
			fmt.Sprintf(`
resource "ghost_branch" "moving" {
  repository = %s.full_name
  branch     = "moving"
}
`, target)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBranchDestroy,
		Steps: []resource.TestStep{
			{Config: config("ghost_repository.test")},
			{
				// Pointing a branch at a different repository replaces it.
				Config: config("ghost_repository.other"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_branch.moving", plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckBranchExists("ghost_branch.moving"),
					testAccCheckBranchGone("moving"),
					resource.TestCheckResourceAttrPair("ghost_branch.moving", "repository_id", "ghost_repository.other", "repo_id"),
				),
			},
		},
	})
}

// testAccRepository returns a client and the repository repoName owned by
// the user the acceptance tests authenticate as.
func testAccRepository(t *testing.T, repoName string) (*client.Client, *client.Repository) {
	t.Helper()
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	user, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("reading the authenticated user: %v", err)
	}
	repo, err := c.GetRepository(context.Background(), user.Login, repoName)
	if err != nil {
		t.Fatalf("reading repository %s: %v", repoName, err)
	}
	return c, repo
}

// testAccPushCommit pushes an empty commit to branch over HTTPS, tags it with
// tag, and returns its SHA. The token is passed in the environment, never on
// the command line, and is masked in any output.
func testAccPushCommit(t *testing.T, repoName, branch, tag string) string {
	t.Helper()

	return testAccPushSignedCommit(t, repoName, branch, tag, "")
}

// testAccPushSignedCommit is testAccPushCommit with the commit SSH-signed by
// the private key file signingKey, or unsigned when it is "". An empty tag
// pushes no tag.
func testAccPushSignedCommit(t *testing.T, repoName, branch, tag, signingKey string) string {
	t.Helper()
	_, repo := testAccRepository(t, repoName)
	token := os.Getenv(envToken)
	auth := base64.StdEncoding.EncodeToString([]byte(repo.Owner.Login + ":" + token))
	dir := t.TempDir()

	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth,
			"GIT_AUTHOR_NAME=tf-acc", "GIT_AUTHOR_EMAIL=tf-acc@example.invalid",
			"GIT_COMMITTER_NAME=tf-acc", "GIT_COMMITTER_EMAIL=tf-acc@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			masked := strings.NewReplacer(token, "***", auth, "***").Replace(string(out))
			t.Fatalf("git %s: %v\n%s", args[0], err, masked)
		}
		return strings.TrimSpace(string(out))
	}

	git("clone", "-q", "--branch", branch, repo.CloneURL, ".")
	if signingKey == "" {
		git("commit", "-q", "--allow-empty", "-m", "tf-acc push")
	} else {
		git("-c", "gpg.format=ssh", "-c", "user.signingkey="+signingKey,
			"commit", "-q", "-S", "--allow-empty", "-m", "tf-acc signed push")
	}
	refs := []string{"HEAD:refs/heads/" + branch}
	if tag != "" {
		git("tag", tag)
		refs = append(refs, "refs/tags/"+tag)
	}
	git(append([]string{"push", "-q", "origin"}, refs...)...)
	return git("rev-parse", "HEAD")
}

// testAccDeleteBranch deletes a branch outside Terraform.
func testAccDeleteBranch(t *testing.T, repoName, branch string) {
	t.Helper()
	c, repo := testAccRepository(t, repoName)
	owner, slug, _ := client.SplitFullName(repo.FullName)
	if err := c.DeleteBranch(context.Background(), owner, slug, branch); err != nil {
		t.Fatalf("deleting branch %s: %v", branch, err)
	}
}

// testAccSetDefaultBranch sets a repository's default branch outside
// Terraform.
func testAccSetDefaultBranch(t *testing.T, repoName, branch string) {
	t.Helper()
	c, repo := testAccRepository(t, repoName)
	owner, slug, _ := client.SplitFullName(repo.FullName)
	if _, err := c.UpdateRepository(context.Background(), owner, slug, client.UpdateRepositoryRequest{DefaultBranch: &branch}); err != nil {
		t.Fatalf("setting the default branch to %s: %v", branch, err)
	}
}
