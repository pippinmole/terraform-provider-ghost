package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// testAccCodeownersBad is a CODEOWNERS file with one error of each kind
// Ghost reports on a user-owned repository, and lines that are fine (owner
// case, duplicates, no owners, comments). Line numbers matter to the checks.
const testAccCodeownersBad = `# tf-acc CODEOWNERS
*            @%[1]s
docs/        @tf-acc-no-such-user-x9
/src/        @%[1]s/no-such-team
[bad         @%[1]s
x.txt        notanowner
!neg.txt     @%[1]s
z.txt        tf-acc@example.com
dup.txt      @%[1]s @%[1]s
none.txt
upper.txt    @%[2]s
	/tab/	@tf-acc-no-such-user-x9 # trailing comment
`

// TestAccCodeownersErrorsDataSource_basic covers every error kind on a user
// repository, which file wins, the ref forms and an unknown ref.
func TestAccCodeownersErrorsDataSource_basic(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-codeowners")
	login := testAccTokenLogin(t)
	fullName := login + "/" + repoName
	bad := fmt.Sprintf(testAccCodeownersBad, login, strings.ToUpper(login))

	data := func(name, ref string) string {
		refLine := ""
		if ref != "" {
			refLine = fmt.Sprintf("ref = %q", ref)
		}
		return fmt.Sprintf(`
data "ghost_codeowners_errors" %q {
  repository = ghost_repository.test.full_name
  %s
  depends_on = [ghost_repository.test]
}
`, name, refLine)
	}
	repo := testAccRepositoryConfig(repoName, `visibility = "public"
  auto_init  = true`)
	config := repo + data("test", "") + data("root", "root-only") + data("docs", "refs/heads/docs-only") +
		data("all", "all/three") + data("rootdocs", "root-docs") + data("tag", "v1") +
		data("lower", "lower-case") + data("empty", "empty-file")
	const d = "data.ghost_codeowners_errors."

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				// An auto_init repository has no CODEOWNERS file.
				Config: repo + data("test", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d+"test", "id", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrPair(d+"test", "repository_id", "ghost_repository.test", "repo_id"),
					resource.TestCheckNoResourceAttr(d+"test", "ref"),
					resource.TestCheckNoResourceAttr(d+"test", "path"),
					resource.TestCheckResourceAttr(d+"test", "errors.#", "0"),
				),
			},
			{
				PreConfig: func() {
					c := testAccCodeownersPusher(t, fullName)
					c("main", "main", "v1", map[string]string{".github/CODEOWNERS": bad})
					c("main", "root-only", "", map[string]string{".github/CODEOWNERS": "", "CODEOWNERS": "* @tf-acc-root-x9\n"})
					c("main", "docs-only", "", map[string]string{".github/CODEOWNERS": "", "docs/CODEOWNERS": "* @tf-acc-docs-x9\n"})
					c("main", "all/three", "", map[string]string{"CODEOWNERS": "* @tf-acc-root-x9\n", "docs/CODEOWNERS": "* @tf-acc-docs-x9\n"})
					c("root-only", "root-docs", "", map[string]string{"docs/CODEOWNERS": "* @tf-acc-docs-x9\n"})
					c("main", "lower-case", "", map[string]string{".github/CODEOWNERS": "", ".github/codeowners": "* @tf-acc-lower-x9\n"})
					c("main", "empty-file", "", map[string]string{".github/CODEOWNERS": "\n"})
				},
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d+"test", "path", ".github/CODEOWNERS"),
					resource.TestCheckResourceAttr(d+"test", "errors.#", "7"),
					testAccCheckCodeownersError(d+"test", 0, 3, 14, "Unknown owner", "docs/        @tf-acc-no-such-user-x9",
						"Unknown owner on line 3: make sure @tf-acc-no-such-user-x9 exists and has write access to the repository"),
					// A team is unknown on a repository owned by a user.
					testAccCheckCodeownersError(d+"test", 1, 4, 14, "Unknown team", fmt.Sprintf("/src/        @%s/no-such-team", login), ""),
					testAccCheckCodeownersError(d+"test", 2, 5, 1, "Invalid pattern", "[bad         @"+login, "Invalid pattern on line 5"),
					testAccCheckCodeownersError(d+"test", 3, 6, 14, "Invalid owner", "x.txt        notanowner", "Invalid owner on line 6"),
					testAccCheckCodeownersError(d+"test", 4, 7, 1, "Invalid pattern", "!neg.txt     @"+login, ""),
					testAccCheckCodeownersError(d+"test", 5, 8, 14, "Unknown owner", "z.txt        tf-acc@example.com",
						"Unknown owner on line 8: make sure tf-acc@example.com is verified and has write access to the repository"),
					// Columns count a tab as one character, and source keeps it.
					testAccCheckCodeownersError(d+"test", 6, 12, 8, "Unknown owner", "\t/tab/\t@tf-acc-no-such-user-x9 # trailing comment", ""),

					// .github/CODEOWNERS, then CODEOWNERS, then docs/CODEOWNERS:
					// only the first is checked.
					resource.TestCheckResourceAttr(d+"root", "path", "CODEOWNERS"),
					testAccCheckCodeownersError(d+"root", 0, 1, 3, "Unknown owner", "* @tf-acc-root-x9", ""),
					resource.TestCheckResourceAttr(d+"root", "errors.0.path", "CODEOWNERS"),
					resource.TestCheckResourceAttr(d+"docs", "ref", "refs/heads/docs-only"),
					resource.TestCheckResourceAttr(d+"docs", "path", "docs/CODEOWNERS"),
					resource.TestCheckResourceAttr(d+"docs", "errors.0.path", "docs/CODEOWNERS"),
					resource.TestCheckResourceAttr(d+"all", "path", ".github/CODEOWNERS"),
					resource.TestCheckResourceAttr(d+"all", "errors.#", "7"),
					resource.TestCheckResourceAttr(d+"rootdocs", "path", "CODEOWNERS"),
					resource.TestCheckResourceAttr(d+"rootdocs", "errors.#", "1"),
					// A tag name, and the file name is case-sensitive.
					resource.TestCheckResourceAttr(d+"tag", "errors.#", "7"),
					resource.TestCheckNoResourceAttr(d+"lower", "path"),
					resource.TestCheckResourceAttr(d+"lower", "errors.#", "0"),
					resource.TestCheckResourceAttr(d+"empty", "path", ".github/CODEOWNERS"),
					resource.TestCheckResourceAttr(d+"empty", "errors.#", "0"),
				),
			},
			{
				Config:      repo + data("test", "tf-acc-no-such-ref"),
				ExpectError: regexp.MustCompile(`(?s)Ref not found.*no branch, tag or commit\s+named\s+"tf-acc-no-such-ref"`),
			},
			{
				Config:      repo + data("test", "refs/tags/v1"),
				ExpectError: regexp.MustCompile(`(?s)Ref not found.*name a tag without\s+refs/tags/`),
			},
		},
	})
}

// A pending invitation isn't write access, so the invitee is an owner
// without write access.
func TestAccCodeownersErrorsDataSource_collaborator(t *testing.T) {
	collaborator := testAccCollaboratorFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc-codeowners")
	fullName := testAccTokenLogin(t) + "/" + repoName
	config := testAccRepositoryConfig(repoName, "auto_init = true") + fmt.Sprintf(`
resource "ghost_repository_collaborator" "test" {
  repository = ghost_repository.test.full_name
  username   = %q
  role       = "write"
}

data "ghost_codeowners_errors" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [ghost_repository_collaborator.test]
}
`, collaborator)
	const d = "data.ghost_codeowners_errors.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					testAccCodeownersPusher(t, fullName)("main", "main", "",
						map[string]string{".github/CODEOWNERS": "*.md @" + collaborator + "\n"})
				},
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "errors.#", "1"),
					testAccCheckCodeownersError(d, 0, 1, 6, "Owner without write access", "*.md @"+collaborator,
						fmt.Sprintf("Owner without write access on line 1: make sure @%s has write access to the repository", collaborator)),
				),
			},
		},
	})
}

// On an organization repository a team without write access is an error
// until ghost_team_repository grants it, and team names are
// case-insensitive.
func TestAccCodeownersErrorsDataSource_organization(t *testing.T) {
	org, team := testAccTeamFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc-codeowners")
	repo := testAccRepositoryConfig(repoName, fmt.Sprintf("owner     = %q\n  auto_init = true", org))
	data := func(dependsOn string) string {
		return fmt.Sprintf(`
data "ghost_codeowners_errors" "test" {
  repository = ghost_repository.test.full_name
  depends_on = [%s]
}
`, dependsOn)
	}
	// The data source depends on the grant, so it reads after the grant is
	// created or changed.
	grant := func(role string) string {
		return repo + fmt.Sprintf(`
resource "ghost_team_repository" "test" {
  repository = ghost_repository.test.full_name
  team_id    = %q
  role       = %q
}
`, team.ID, role) + data("ghost_team_repository.test")
	}
	owners := fmt.Sprintf("/a/ @%s/%s\n/b/ @%s/%s\n/c/ @%s\n", org, team.Slug, strings.ToUpper(org), strings.ToUpper(team.Slug), org)
	const d = "data.ghost_codeowners_errors.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: repo + data("ghost_repository.test")},
			{
				PreConfig: func() {
					testAccCodeownersPusher(t, org+"/"+repoName)("main", "main", "", map[string]string{"docs/CODEOWNERS": owners})
				},
				Config: repo + data("ghost_repository.test"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttr(d, "path", "docs/CODEOWNERS"),
					resource.TestCheckResourceAttr(d, "errors.#", "3"),
					testAccCheckCodeownersError(d, 0, 1, 5, "Team without write access", fmt.Sprintf("/a/ @%s/%s", org, team.Slug), ""),
					testAccCheckCodeownersError(d, 1, 2, 5, "Team without write access", "", ""),
					// The organization itself isn't an owner.
					testAccCheckCodeownersError(d, 2, 3, 5, "Unknown owner", "", ""),
				),
			},
			{
				// Read access isn't enough.
				Config: grant("read"),
				Check:  resource.TestCheckResourceAttr(d, "errors.#", "3"),
			},
			{
				// Write access clears both spellings of the team at once.
				Config: grant("write"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "errors.#", "1"),
					testAccCheckCodeownersError(d, 0, 3, 5, "Unknown owner", "/c/ @"+org, ""),
				),
			},
		},
	})
}

// An empty repository has no CODEOWNERS file on its default branch, and no
// branch to name as ref.
func TestAccCodeownersErrorsDataSource_emptyRepository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-codeowners")
	repo := testAccRepositoryConfig(repoName, "")
	data := func(ref string) string {
		return repo + fmt.Sprintf(`
data "ghost_codeowners_errors" "test" {
  repository = ghost_repository.test.full_name
  %s
  depends_on = [ghost_repository.test]
}
`, ref)
	}
	const d = "data.ghost_codeowners_errors.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: data(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(d, "id", "ghost_repository.test", "full_name"),
					resource.TestCheckNoResourceAttr(d, "path"),
					resource.TestCheckResourceAttr(d, "errors.#", "0"),
				),
			},
			{
				Config:      data(`ref = "main"`),
				ExpectError: regexp.MustCompile(`(?s)Ref not found.*named "main", or it is an empty\s+repository`),
			},
		},
	})
}

// The data source follows a repository rename, and matches the owner and
// slug case-insensitively, reporting the current full name as id.
func TestAccCodeownersErrorsDataSource_repositoryRename(t *testing.T) {
	testAccRepositoriesPreCheck(t)
	repoName := acctest.RandomWithPrefix("tf-acc-codeowners")
	oldFullName := testAccTokenLogin(t) + "/" + repoName
	config := func(name, repository string) string {
		return fmt.Sprintf(`
resource "ghost_repository" "test" {
  name      = %q
  auto_init = true
}

data "ghost_codeowners_errors" "test" {
  repository = %q
  ref        = "main"
  depends_on = [ghost_repository.test]
}
`, name, repository)
	}
	const d = "data.ghost_codeowners_errors.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(repoName, strings.ToUpper(oldFullName)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", oldFullName),
					resource.TestCheckNoResourceAttr(d, "path"),
				),
			},
			{
				PreConfig: func() {
					testAccCodeownersPusher(t, oldFullName)("main", "main", "", map[string]string{"CODEOWNERS": "* @tf-acc-no-such-user-x9\n"})
				},
				Config: config(repoName+"-renamed", oldFullName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", oldFullName+"-renamed"),
					resource.TestCheckResourceAttr(d, "repository", oldFullName),
					resource.TestCheckResourceAttrPair(d, "repository_id", "ghost_repository.test", "repo_id"),
					resource.TestCheckResourceAttr(d, "path", "CODEOWNERS"),
					resource.TestCheckResourceAttr(d, "errors.#", "1"),
				),
			},
		},
	})
}

func TestAccCodeownersErrorsDataSource_notFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: `
data "ghost_codeowners_errors" "test" {
  repository = "tf-acc-no-such-owner-x9/tf-acc-missing"
}
`,
			ExpectError: regexp.MustCompile(`(?s)Repository not found.*tf-acc-no-such-owner-x9/tf-acc-missing`),
		}},
	})
}

// testAccCheckCodeownersError checks errors[i] of the data source address.
// An empty source or message isn't checked.
func testAccCheckCodeownersError(address string, i, line, column int, kind, source, message string) resource.TestCheckFunc {
	prefix := fmt.Sprintf("errors.%d.", i)
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr(address, prefix+"line", fmt.Sprint(line)),
		resource.TestCheckResourceAttr(address, prefix+"column", fmt.Sprint(column)),
		resource.TestCheckResourceAttr(address, prefix+"kind", kind),
		resource.TestCheckNoResourceAttr(address, prefix+"suggestion"),
		resource.TestMatchResourceAttr(address, prefix+"message", regexp.MustCompile(fmt.Sprintf(`^%s on line %d`, regexp.QuoteMeta(kind), line))),
	}
	if source != "" {
		checks = append(checks, resource.TestCheckResourceAttr(address, prefix+"source", source))
	}
	if message != "" {
		checks = append(checks, resource.TestCheckResourceAttr(address, prefix+"message", message))
	}
	return resource.ComposeAggregateTestCheckFunc(checks...)
}

// testAccCodeownersPusher returns a function that commits files on top of
// branch base of the repository fullName and pushes the commit to branch,
// tagging it with tag unless that is "". A file whose content is "" is
// deleted. Pushes go over HTTPS with the test token (see testAccGit).
func testAccCodeownersPusher(t *testing.T, fullName string) func(base, branch, tag string, files map[string]string) {
	t.Helper()
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	owner, slug, err := client.SplitFullName(fullName)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.GetRepository(context.Background(), owner, slug)
	if err != nil {
		t.Fatalf("reading repository %s: %v", fullName, err)
	}
	// Authenticate as the token's user, not the owner: for an organization
	// repository that is the organization.
	pusher := *repo
	pusher.Owner.Login = testAccTokenLogin(t)

	return func(base, branch, tag string, files map[string]string) {
		t.Helper()
		git := testAccGit(t, &pusher)
		git("clone", "-q", "--branch", base, repo.CloneURL, ".")
		dir := git("rev-parse", "--show-toplevel")
		// Deletions first: on a case-insensitive file system, deleting
		// .github/CODEOWNERS would also delete a new .github/codeowners.
		for name, content := range files {
			if content == "" {
				git("rm", "-q", "--ignore-unmatch", name)
			}
		}
		for name, content := range files {
			if content == "" {
				continue
			}
			file := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		git("add", "-A")
		git("commit", "-q", "-m", "tf-acc CODEOWNERS on "+branch)
		refs := []string{"HEAD:refs/heads/" + branch}
		if tag != "" {
			git("tag", tag)
			refs = append(refs, "refs/tags/"+tag)
		}
		git(append([]string{"push", "-q", "origin"}, refs...)...)
	}
}
