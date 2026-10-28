package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

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

const testAccCollaboratorAddress = "ghost_repository_collaborator.test"

func TestParseRepositoryCollaboratorID(t *testing.T) {
	repo, user, err := parseRepositoryCollaboratorID("acme/repo:Alice")
	if err != nil || repo != "acme/repo" || user != "Alice" {
		t.Errorf("got %q, %q, %v", repo, user, err)
	}
	if got := repositoryCollaboratorID(repo, user); got != "acme/repo:Alice" {
		t.Errorf("repositoryCollaboratorID = %q", got)
	}

	for _, bad := range []string{"", "acme/repo", "acme/repo:", ":alice", "acme:alice", "a/b/c:alice", "acme/repo:a:b", "acme/repo:a/b"} {
		if _, _, err := parseRepositoryCollaboratorID(bad); err == nil {
			t.Errorf("parseRepositoryCollaboratorID(%q) should fail", bad)
		}
	}
}

func TestFindCollaborator(t *testing.T) {
	collaborators := []client.Collaborator{{Username: "alice"}, {Username: "pippinextra", Role: "write"}}

	if got := findCollaborator(collaborators, "PippinExtra"); got == nil || got.Role != "write" {
		t.Errorf("usernames should match case-insensitively, got %+v", got)
	}
	if got := findCollaborator(collaborators, "bob"); got != nil {
		t.Errorf("bob should not be found, got %+v", got)
	}
}

func TestRepositoryCollaboratorFromRemote(t *testing.T) {
	expires := "2026-11-03T23:05:30.815Z"
	pending := &client.Collaborator{Username: "pippinextra", Role: "write", Status: client.CollaboratorPending,
		InvitedAt: "2026-10-27T23:05:30.815Z", ExpiresAt: &expires}

	m := repositoryCollaboratorResourceModel{Repository: types.StringValue("acme/repo"), Username: types.StringValue("PippinExtra")}
	m.fromRemote(pending)
	if m.Username.ValueString() != "PippinExtra" {
		t.Errorf("the configured spelling should be kept, got %s", m.Username)
	}
	if m.ID.ValueString() != "acme/repo:pippinextra" || m.Role.ValueString() != "write" || m.Status.ValueString() != "pending" ||
		m.InvitedAt.ValueString() != pending.InvitedAt || m.ExpiresAt.ValueString() != expires {
		t.Errorf("unexpected model: %+v", m)
	}

	accepted := &client.Collaborator{Username: "pippinextra", Role: "admin", Status: client.CollaboratorAccepted, InvitedAt: "x"}
	m = repositoryCollaboratorResourceModel{Repository: types.StringValue("acme/repo"), Username: types.StringNull()}
	m.fromRemote(accepted)
	if m.Username.ValueString() != "pippinextra" || !m.ExpiresAt.IsNull() || m.Status.ValueString() != "accepted" {
		t.Errorf("unexpected model: %+v", m)
	}
}

func TestRepositoryCollaboratorConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"bad role", `repository = "acme/repo"` + "\n" + `username = "alice"` + "\n" + `role = "pull"`, `(?s)value must be one of.*"read".*"admin"`},
		{"role case", `repository = "acme/repo"` + "\n" + `username = "alice"` + "\n" + `role = "Write"`, `(?s)value must be one of`},
		{"no role", `repository = "acme/repo"` + "\n" + `username = "alice"`, `(?s)"role" is required`},
		{"no username", `repository = "acme/repo"` + "\n" + `role = "read"`, `(?s)"username" is required`},
		{"empty username", `repository = "acme/repo"` + "\n" + `username = ""` + "\n" + `role = "read"`, `(?s)username.*at least 1`},
		{"colon in username", `repository = "acme/repo"` + "\n" + `username = "a:b"` + "\n" + `role = "read"`, `must not contain ":" or "/"`},
		{"slash in username", `repository = "acme/repo"` + "\n" + `username = "a/b"` + "\n" + `role = "read"`, `must not contain ":" or "/"`},
		{"bad repository", `repository = "repo"` + "\n" + `username = "alice"` + "\n" + `role = "read"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "resource \"ghost_repository_collaborator\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestAccRepositoryCollaboratorResource(t *testing.T) {
	user := testAccCollaboratorFixture(t)
	owner := testAccTokenLogin(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"
	config := func(name, username, role string) string {
		return testAccRepositoryCollaboratorConfig(name, "", username, role)
	}
	sameRepoID := statecheck.CompareValue(compare.ValuesSame())
	sameInvitedAt := statecheck.CompareValue(compare.ValuesSame())
	newInvitedAt := statecheck.CompareValue(compare.ValuesDiffer())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryCollaboratorDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(repoName, user, "read"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						testAccCollaboratorAddress, tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
					sameRepoID.AddStateValue(testAccCollaboratorAddress, tfjsonpath.New("repository_id")),
					sameInvitedAt.AddStateValue(testAccCollaboratorAddress, tfjsonpath.New("invited_at")),
					newInvitedAt.AddStateValue(testAccCollaboratorAddress, tfjsonpath.New("invited_at")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "id", owner+"/"+repoName+":"+strings.ToLower(user)),
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "username", user),
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "role", "read"),
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "status", "pending"),
					resource.TestMatchResourceAttr(testAccCollaboratorAddress, "invited_at", regexp.MustCompile(`^\d{4}-\d\d-\d\dT`)),
					resource.TestMatchResourceAttr(testAccCollaboratorAddress, "expires_at", regexp.MustCompile(`^\d{4}-\d\d-\d\dT`)),
					testAccCheckCollaboratorRemote(user, "read"),
				),
			},
			{
				ResourceName:      testAccCollaboratorAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// The import ID's username is matched case-insensitively and
				// kept as given.
				ResourceName:            testAccCollaboratorAddress,
				ImportState:             true,
				ImportStateId:           owner + "/" + repoName + ":" + strings.ToUpper(user),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"username"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if got := states[0].Attributes["username"]; got != strings.ToUpper(user) {
						return fmt.Errorf("username = %q, want %q", got, strings.ToUpper(user))
					}
					return nil
				},
			},
			{
				// Changing the role of a pending invitation keeps its dates.
				Config: config(repoName, user, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					sameInvitedAt.AddStateValue(testAccCollaboratorAddress, tfjsonpath.New("invited_at")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "status", "pending"),
					testAccCheckCollaboratorRemote(user, "write"),
				),
			},
			{
				// Changing only the username's case updates state in place.
				Config: config(repoName, strings.ToUpper(user), "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					sameInvitedAt.AddStateValue(testAccCollaboratorAddress, tfjsonpath.New("invited_at")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "username", strings.ToUpper(user)),
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "id", owner+"/"+repoName+":"+strings.ToLower(user)),
				),
			},
			{
				// A role changed outside Terraform is set back.
				PreConfig: func() { testAccSetCollaborator(t, owner+"/"+repoName, user, "maintain") },
				Config:    config(repoName, user, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionUpdate)},
				},
				Check: testAccCheckCollaboratorRemote(user, "write"),
			},
			{
				// An invitation withdrawn outside Terraform is sent again.
				PreConfig: func() { testAccRemoveCollaborator(t, owner+"/"+repoName, user) },
				Config:    config(repoName, user, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionCreate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					newInvitedAt.AddStateValue(testAccCollaboratorAddress, tfjsonpath.New("invited_at")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "status", "pending"),
					testAccCheckCollaboratorRemote(user, "write"),
				),
			},
			{
				// Renaming the repository keeps the invitation.
				Config: config(renamed, user, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					sameRepoID.AddStateValue(testAccCollaboratorAddress, tfjsonpath.New("repository_id")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccCollaboratorAddress, "id", owner+"/"+renamed+":"+strings.ToLower(user)),
					testAccCheckCollaboratorRemote(user, "write"),
				),
			},
		},
	})
}

// TestAccRepositoryCollaboratorResource_usernameChange has only one real
// second user, so it changes the username to one that doesn't exist: the
// plan replaces the resource, the old invitation is withdrawn, and the new
// one fails.
func TestAccRepositoryCollaboratorResource_usernameChange(t *testing.T) {
	user := testAccCollaboratorFixture(t)
	owner := testAccTokenLogin(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	missing := acctest.RandomWithPrefix("tf-acc-nouser")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryCollaboratorDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRepositoryCollaboratorConfig(repoName, "", user, "read"),
				Check:  testAccCheckCollaboratorRemote(user, "read"),
			},
			{
				Config: testAccRepositoryCollaboratorConfig(repoName, "", missing, "read"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionReplace)},
				},
				ExpectError: testAccWrappedRegexp(`Ghost has no user "` + missing + `"`),
			},
			{
				PreConfig: func() {
					if role := testAccCollaboratorRole(t, owner+"/"+repoName, user); role != "" {
						t.Errorf("the replaced invitation of %s should have been withdrawn, but it has role %q", user, role)
					}
				},
				Config: testAccRepositoryCollaboratorConfig(repoName, "", user, "read"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionCreate)},
				},
				Check: testAccCheckCollaboratorRemote(user, "read"),
			},
		},
	})
}

func TestAccRepositoryCollaboratorResource_repositoryReplaced(t *testing.T) {
	user := testAccCollaboratorFixture(t)
	owner := testAccTokenLogin(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	otherName := repoName + "-other"
	config := func(license, repository string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("license_template = %q", license)) + fmt.Sprintf(`
resource "ghost_repository" "other" {
  name = %q
}

resource "ghost_repository_collaborator" "test" {
  repository = %s.full_name
  username   = %q
  role       = "read"

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }
}
`, otherName, repository, user)
	}
	samePair := func(repo string) statecheck.StateCheck {
		return statecheck.CompareValuePairs(
			testAccCollaboratorAddress, tfjsonpath.New("repository_id"),
			repo, tfjsonpath.New("repo_id"),
			compare.ValuesSame(),
		)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryCollaboratorDestroy,
		Steps: []resource.TestStep{
			{Config: config("mit", "ghost_repository.test")},
			{
				// Replacing the repository drops its invitations;
				// replace_triggered_by sends a new one.
				Config: config("apache-2.0", "ghost_repository.test"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{samePair("ghost_repository.test")},
				Check:             testAccCheckCollaboratorRemote(user, "read"),
			},
			{
				// Pointing it at a different repository replaces it.
				Config: config("apache-2.0", "ghost_repository.other"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccCollaboratorAddress, plancheck.ResourceActionReplace)},
				},
				ConfigStateChecks: []statecheck.StateCheck{samePair("ghost_repository.other")},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckCollaboratorRemote(user, "read"),
					func(*terraform.State) error {
						if role := testAccCollaboratorRole(t, owner+"/"+repoName, user); role != "" {
							return fmt.Errorf("%s is still invited to %s with role %q", user, repoName, role)
						}
						return nil
					},
				),
			},
		},
	})
}

// TestAccRepositoryCollaboratorResource_errors covers what only Ghost can
// check. It never sends a successful invitation.
func TestAccRepositoryCollaboratorResource_errors(t *testing.T) {
	user := testAccCollaboratorFixture(t)
	owner := testAccTokenLogin(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	repo := testAccRepositoryConfig(repoName, "")
	block := func(repository, username string) string {
		return repo + fmt.Sprintf(`
resource "ghost_repository_collaborator" "test" {
  repository = %s
  username   = %q
  role       = "read"
}
`, repository, username)
	}
	withUser := block("ghost_repository.test.full_name", user)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryCollaboratorDestroy,
		Steps: []resource.TestStep{
			{Config: repo},
			{
				Config:      block("ghost_repository.test.full_name", strings.ToUpper(owner)),
				ExpectError: testAccWrappedRegexp(`The owner already has every permission on this repository`),
			},
			{
				Config:      block(fmt.Sprintf("%q", owner+"/"+missing), user),
				ExpectError: regexp.MustCompile(`Repository .*` + missing + ` not found`),
			},
			{
				// Importing a user who isn't invited.
				Config:        withUser,
				ResourceName:  testAccCollaboratorAddress,
				ImportState:   true,
				ImportStateId: owner + "/" + repoName + ":" + user,
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
			{
				Config:        withUser,
				ResourceName:  testAccCollaboratorAddress,
				ImportState:   true,
				ImportStateId: owner + "/" + repoName,
				ExpectError:   testAccWrappedRegexp(`import ID must be "owner/repo:username"`),
			},
			{Config: repo},
		},
	})
}

func TestAccRepositoryCollaboratorResource_organization(t *testing.T) {
	user := testAccCollaboratorFixture(t)
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION to test collaborators on organization repositories")
	}
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			if err := testAccCheckRepositoryCollaboratorDestroy(s); err != nil {
				return err
			}
			if repos := testAccOutsideCollaboratorRepos(t, org, user); len(repos) != 0 {
				return fmt.Errorf("%s is still an outside collaborator of %s: %v", user, org, repos)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: testAccRepositoryCollaboratorConfig(repoName, fmt.Sprintf("owner = %q", org), user, "triage"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(testAccCollaboratorAddress, "id", org+"/"+repoName+":"+strings.ToLower(user)),
				resource.TestCheckResourceAttr(testAccCollaboratorAddress, "status", "pending"),
				testAccCheckCollaboratorRemote(user, "triage"),
				func(*terraform.State) error {
					// A user outside the organization becomes an outside
					// collaborator, pending until they accept.
					repos := testAccOutsideCollaboratorRepos(t, org, user)
					if got := repos[repoName]; got != "triage (pending)" {
						return fmt.Errorf("outside collaborator entry for %s = %q, want \"triage (pending)\" (all: %v)", repoName, got, repos)
					}
					return nil
				},
			),
		}},
	})
}

// testAccCollaboratorFixture returns the username to invite, or skips the
// test. The invitation is never accepted, so tests expect it to stay pending.
func testAccCollaboratorFixture(t *testing.T) string {
	t.Helper()

	user := os.Getenv("GHOST_TEST_COLLABORATOR")
	if user == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_COLLABORATOR (a second Ghost username) and TF_ACC to test ghost_repository_collaborator")
	}
	testAccPreCheck(t)

	return user
}

// testAccTokenLogin returns the login of the token's user.
func testAccTokenLogin(t *testing.T) string {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("getting the token's user: %v", err)
	}

	return u.Login
}

// testAccRepositoryCollaboratorConfig is a repository with extra arguments
// and a ghost_repository_collaborator on it.
func testAccRepositoryCollaboratorConfig(repoName, extra, username, role string) string {
	return testAccRepositoryConfig(repoName, extra) + fmt.Sprintf(`
resource "ghost_repository_collaborator" "test" {
  repository = ghost_repository.test.full_name
  username   = %q
  role       = %q
}
`, username, role)
}

// testAccCheckCollaboratorRemote verifies, straight from the API, that the
// user is invited to, or collaborating on, the repository in
// ghost_repository_collaborator.test's state with role.
func testAccCheckCollaboratorRemote(username, role string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[testAccCollaboratorAddress]
		if !ok {
			return fmt.Errorf("%s not in state", testAccCollaboratorAddress)
		}
		got, err := testAccCollaboratorRoleErr(rs.Primary.Attributes["repository"], username)
		if err != nil {
			return err
		}
		if got != role {
			return fmt.Errorf("%s has role %q on %s, want %q", username, got, rs.Primary.Attributes["repository"], role)
		}

		return nil
	}
}

// testAccCollaboratorRole returns the user's role on the repository, or ""
// when they aren't invited.
func testAccCollaboratorRole(t *testing.T, fullName, username string) string {
	t.Helper()

	role, err := testAccCollaboratorRoleErr(fullName, username)
	if err != nil {
		t.Fatal(err)
	}

	return role
}

func testAccCollaboratorRoleErr(fullName, username string) (string, error) {
	c, err := testAccClient()
	if err != nil {
		return "", err
	}
	owner, repo, err := client.SplitFullName(fullName)
	if err != nil {
		return "", err
	}
	collaborators, err := c.ListRepositoryCollaborators(context.Background(), owner, repo)
	if err != nil {
		return "", fmt.Errorf("listing the collaborators of %s: %w", fullName, err)
	}
	if found := findCollaborator(collaborators, username); found != nil {
		return found.Role, nil
	}

	return "", nil
}

// testAccSetCollaborator invites the user to fullName with role, or changes
// their role, outside Terraform.
func testAccSetCollaborator(t *testing.T, fullName, username, role string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	owner, repo, _ := client.SplitFullName(fullName)
	if _, err := c.SetRepositoryCollaborator(context.Background(), owner, repo, username, role); err != nil {
		t.Fatalf("setting %s's role on %s: %v", username, fullName, err)
	}
}

// testAccRemoveCollaborator withdraws the user's invitation outside Terraform.
func testAccRemoveCollaborator(t *testing.T, fullName, username string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	owner, repo, _ := client.SplitFullName(fullName)
	if err := c.RemoveRepositoryCollaborator(context.Background(), owner, repo, username); err != nil {
		t.Fatalf("removing %s from %s: %v", username, fullName, err)
	}
}

// testAccOutsideCollaboratorRepos returns the organization repositories the
// user is an outside collaborator of, as slug => "role" or "role (pending)".
func testAccOutsideCollaboratorRepos(t *testing.T, org, username string) map[string]string {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	collaborators, err := c.ListOutsideCollaborators(context.Background(), org)
	if err != nil {
		t.Fatalf("listing the outside collaborators of %s: %v", org, err)
	}

	repos := map[string]string{}
	for _, oc := range collaborators {
		if !strings.EqualFold(oc.Username, username) {
			continue
		}
		for _, r := range oc.Repositories {
			repos[r.Slug] = r.Role
			if r.Pending {
				repos[r.Slug] += " (pending)"
			}
		}
	}

	return repos
}

// testAccCheckRepositoryCollaboratorDestroy checks, straight from the API,
// that every ghost_repository_collaborator in state was removed or its
// invitation withdrawn, and that the repositories are gone.
func testAccCheckRepositoryCollaboratorDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_repository_collaborator" {
			continue
		}
		role, err := testAccCollaboratorRoleErr(rs.Primary.Attributes["repository"], rs.Primary.Attributes["username"])
		if client.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if role != "" {
			return fmt.Errorf("%s is still invited to %s with role %q", rs.Primary.Attributes["username"], rs.Primary.Attributes["repository"], role)
		}
	}

	return testAccCheckRepositoryDestroy(s)
}
