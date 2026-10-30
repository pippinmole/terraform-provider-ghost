package provider

import (
	"context"
	"fmt"
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

const testAccTeamRepositoryAddress = "ghost_team_repository.test"

func TestParseTeamRepositoryID(t *testing.T) {
	repo, team, err := parseTeamRepositoryID("acme/repo:T1")
	if err != nil || repo != "acme/repo" || team != "T1" {
		t.Errorf("got %q, %q, %v", repo, team, err)
	}
	if got := teamRepositoryID(repo, team); got != "acme/repo:T1" {
		t.Errorf("teamRepositoryID = %q", got)
	}

	for _, bad := range []string{"", "acme/repo", "acme/repo:", "acme:T1", "a/b/c:T1", ":T1", "acme/repo:a:b", "acme/repo:a/b"} {
		if _, _, err := parseTeamRepositoryID(bad); err == nil {
			t.Errorf("parseTeamRepositoryID(%q) should fail", bad)
		}
	}
}

func TestRepositoryTeamRole(t *testing.T) {
	write := "write"
	teams := []client.RepositoryTeam{{ID: "T1", Role: nil}, {ID: "T2", Role: &write}}

	if got := repositoryTeamRole(teams, "T2"); got == nil || *got != "write" {
		t.Errorf("T2 role = %v, want write", got)
	}
	if got := repositoryTeamRole(teams, "T1"); got != nil {
		t.Errorf("T1 role = %q, want nil (no access)", *got)
	}
	if got := repositoryTeamRole(teams, "T3"); got != nil {
		t.Errorf("T3 role = %q, want nil (not in the organization)", *got)
	}
}

func TestTeamByIDOrSlug(t *testing.T) {
	teams := []client.TeamSummary{{ID: "core", Slug: "other"}, {ID: "T2", Slug: "core"}}

	if got := teamByIDOrSlug(teams, "core"); got == nil || got.ID != "core" {
		t.Errorf("an ID match should win over a slug match, got %+v", got)
	}
	if got := teamByIDOrSlug(teams, "OTHER"); got == nil || got.ID != "core" {
		t.Errorf("slugs should match case-insensitively, got %+v", got)
	}
	if got := teamByIDOrSlug(teams, "t2"); got != nil {
		t.Errorf("IDs should match case-sensitively, got %+v", got)
	}
}

func TestSlugFromRemote(t *testing.T) {
	tests := []struct {
		current types.String
		want    string
	}{
		{types.StringNull(), "core"},
		{types.StringUnknown(), "core"},
		{types.StringValue("CORE"), "CORE"},
		{types.StringValue("old"), "core"},
	}
	for _, tt := range tests {
		if got := slugFromRemote(tt.current, "core"); got.ValueString() != tt.want {
			t.Errorf("slugFromRemote(%s) = %s, want %s", tt.current, got, tt.want)
		}
	}
}

func TestTeamRepositoryConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"neither team", `repository = "acme/repo"` + "\n" + `role = "read"`, `(?s)one \(and only one\) of\s+\[team_id,team_slug\]`},
		{"both teams", `repository = "acme/repo"` + "\n" + `role = "read"` + "\n" + `team_id = "T1"` + "\n" + `team_slug = "core"`, `(?s)one \(and only one\) of\s+\[team_id,team_slug\]`},
		{"empty team_id", `repository = "acme/repo"` + "\n" + `role = "read"` + "\n" + `team_id = ""`, `(?s)team_id.*at least 1`},
		{"empty team_slug", `repository = "acme/repo"` + "\n" + `role = "read"` + "\n" + `team_slug = ""`, `(?s)team_slug.*at least 1`},
		{"colon in team_id", `repository = "acme/repo"` + "\n" + `role = "read"` + "\n" + `team_id = "a:b"`, `must not contain ":"`},
		{"bad role", `repository = "acme/repo"` + "\n" + `role = "pull"` + "\n" + `team_id = "T1"`, `(?s)value must be one of.*"read".*"admin"`},
		{"role case", `repository = "acme/repo"` + "\n" + `role = "Write"` + "\n" + `team_id = "T1"`, `(?s)value must be one of`},
		{"no role", `repository = "acme/repo"` + "\n" + `team_id = "T1"`, `(?s)"role" is required`},
		{"bad repository", `repository = "repo"` + "\n" + `role = "read"` + "\n" + `team_id = "T1"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "resource \"ghost_team_repository\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestAccTeamRepositoryResource(t *testing.T) {
	org, team := testAccTeamFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"
	config := func(name, role string) string {
		return testAccTeamRepositoryConfig(org, name, fmt.Sprintf("team_id = %q\n  role = %q", team.ID, role))
	}
	sameRepoID := statecheck.CompareValue(compare.ValuesSame())

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(repoName, "read"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						testAccTeamRepositoryAddress, tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
					sameRepoID.AddStateValue(testAccTeamRepositoryAddress, tfjsonpath.New("repository_id")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "id", org+"/"+repoName+":"+team.ID),
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "team_id", team.ID),
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "team_slug", team.Slug),
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "role", "read"),
					testAccCheckTeamRepositoryRemote(team.ID, "read"),
				),
			},
			{
				ResourceName:      testAccTeamRepositoryAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// The import ID can name the team by its slug, in any case.
				ResourceName:      testAccTeamRepositoryAddress,
				ImportState:       true,
				ImportStateId:     org + "/" + repoName + ":" + strings.ToUpper(team.Slug),
				ImportStateVerify: true,
			},
			{
				Config: config(repoName, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckTeamRepositoryRemote(team.ID, "write"),
			},
			{
				// A role changed outside Terraform is set back.
				PreConfig: func() { testAccSetTeamRepositoryRole(t, org, repoName, team.ID, "maintain") },
				Config:    config(repoName, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionUpdate)},
				},
				Check: testAccCheckTeamRepositoryRemote(team.ID, "write"),
			},
			{
				// Access removed outside Terraform is given back.
				PreConfig: func() { testAccRemoveTeamRepository(t, org, repoName, team.ID) },
				Config:    config(repoName, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionCreate)},
				},
				Check: testAccCheckTeamRepositoryRemote(team.ID, "write"),
			},
			{
				// Renaming the repository keeps the team's access.
				Config: config(renamed, "write"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					sameRepoID.AddStateValue(testAccTeamRepositoryAddress, tfjsonpath.New("repository_id")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "id", org+"/"+renamed+":"+team.ID),
					testAccCheckTeamRepositoryRemote(team.ID, "write"),
				),
			},
		},
	})
}

func TestAccTeamRepositoryResource_teamSlug(t *testing.T) {
	org, team := testAccTeamFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	upper := strings.ToUpper(team.Slug)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				// The slug is matched case-insensitively and kept as configured.
				Config: testAccTeamRepositoryConfig(org, repoName, fmt.Sprintf("team_slug = %q\n  role = \"triage\"", upper)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "team_id", team.ID),
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "team_slug", upper),
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "id", org+"/"+repoName+":"+team.ID),
					testAccCheckTeamRepositoryRemote(team.ID, "triage"),
				),
			},
			{
				// Naming the same team by its ID changes nothing.
				Config: testAccTeamRepositoryConfig(org, repoName, fmt.Sprintf("team_id = %q\n  role = \"triage\"", team.ID)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionNoop)},
				},
			},
			{
				// And back to the slug, in its stored spelling.
				Config: testAccTeamRepositoryConfig(org, repoName, fmt.Sprintf("team_slug = %q\n  role = \"admin\"", team.Slug)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamRepositoryAddress, "team_slug", team.Slug),
					testAccCheckTeamRepositoryRemote(team.ID, "admin"),
				),
			},
			{
				ResourceName:      testAccTeamRepositoryAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// TestAccTeamRepositoryResource_teamChange needs a second team, which can't
// be created with a token.
func TestAccTeamRepositoryResource_teamChange(t *testing.T) {
	org, first := testAccTeamFixture(t)
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	teams, err := c.ListOrganizationTeams(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	var second *client.TeamSummary
	for i := range teams {
		if teams[i].ID != first.ID {
			second = &teams[i]
			break
		}
	}
	if second == nil {
		t.Skipf("organization %s needs a second team to test changing the team", org)
	}
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(teamID string) string {
		return testAccTeamRepositoryConfig(org, repoName, fmt.Sprintf("team_id = %q\n  role = \"write\"", teamID))
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: config(first.ID), Check: testAccCheckTeamRepositoryRemote(first.ID, "write")},
			{
				Config: config(second.ID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckTeamRepositoryRemote(second.ID, "write"),
					testAccCheckTeamRepositoryRemoteNone(first.ID),
				),
			},
		},
	})
}

func TestAccTeamRepositoryResource_repositoryReplaced(t *testing.T) {
	org, team := testAccTeamFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	otherName := repoName + "-other"
	config := func(license, repository string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q\n  license_template = %q", org, license)) +
			fmt.Sprintf(`
resource "ghost_repository" "other" {
  owner = %q
  name  = %q
}

resource "ghost_team_repository" "test" {
  repository = %s.full_name
  team_id    = %q
  role       = "read"

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }
}
`, org, otherName, repository, team.ID)
	}
	samePair := func(repo string) statecheck.StateCheck {
		return statecheck.CompareValuePairs(
			testAccTeamRepositoryAddress, tfjsonpath.New("repository_id"),
			repo, tfjsonpath.New("repo_id"),
			compare.ValuesSame(),
		)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: config("mit", "ghost_repository.test")},
			{
				// Replacing the repository drops its team access;
				// replace_triggered_by gives it back.
				Config: config("apache-2.0", "ghost_repository.test"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{samePair("ghost_repository.test")},
				Check:             testAccCheckTeamRepositoryRemote(team.ID, "read"),
			},
			{
				// Pointing it at a different repository replaces it.
				Config: config("apache-2.0", "ghost_repository.other"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamRepositoryAddress, plancheck.ResourceActionReplace)},
				},
				ConfigStateChecks: []statecheck.StateCheck{samePair("ghost_repository.other")},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckTeamRepositoryRemote(team.ID, "read"),
					testAccCheckTeamRepositoryRemoteNoneOn(org, repoName, team.ID),
				),
			},
		},
	})
}

// TestAccTeamRepositoryResource_errors covers what only Ghost can check.
func TestAccTeamRepositoryResource_errors(t *testing.T) {
	org, team := testAccTeamFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	userRepoName := repoName + "-user"
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	// The team resource is left out, so only the repositories exist.
	repos := testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q", org)) + fmt.Sprintf(`
resource "ghost_repository" "user" {
  name = %q
}
`, userRepoName)
	block := func(repository, attrs string) string {
		return repos + fmt.Sprintf(`
resource "ghost_team_repository" "test" {
  repository = %s
  %s
}
`, repository, attrs)
	}
	withTeam := block("ghost_repository.test.full_name", fmt.Sprintf("team_id = %q\n  role = \"read\"", team.ID))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: repos},
			{
				Config:      block("ghost_repository.user.full_name", fmt.Sprintf("team_id = %q\n  role = \"read\"", team.ID)),
				ExpectError: testAccWrappedRegexp(`Only repositories owned by an organization can be shared with teams`),
			},
			{
				Config:      block("ghost_repository.test.full_name", fmt.Sprintf("team_id = %q\n  role = \"read\"", missing)),
				ExpectError: testAccWrappedRegexp(`no team with the id "` + missing + `"`),
			},
			{
				Config:      block("ghost_repository.test.full_name", fmt.Sprintf("team_slug = %q\n  role = \"read\"", missing)),
				ExpectError: testAccWrappedRegexp(`no team with the slug "` + missing + `"`),
			},
			{
				Config:      block(fmt.Sprintf("%q", org+"/"+missing), fmt.Sprintf("team_id = %q\n  role = \"read\"", team.ID)),
				ExpectError: regexp.MustCompile(`Repository .*` + missing + ` not found`),
			},
			{
				// Importing a team that has no access to the repository.
				Config:        withTeam,
				ResourceName:  testAccTeamRepositoryAddress,
				ImportState:   true,
				ImportStateId: org + "/" + repoName + ":" + team.ID,
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
			{
				Config:        withTeam,
				ResourceName:  testAccTeamRepositoryAddress,
				ImportState:   true,
				ImportStateId: org + "/" + repoName + ":" + missing,
				ExpectError:   testAccWrappedRegexp(`no team with the ID or slug`),
			},
			{
				Config:        withTeam,
				ResourceName:  testAccTeamRepositoryAddress,
				ImportState:   true,
				ImportStateId: org + "/" + repoName,
				ExpectError:   testAccWrappedRegexp(`import ID must be "owner/repo:team_id"`),
			},
			{Config: repos},
		},
	})
}

// testAccTeamRepositoryConfig is an organization repository and, when attrs
// is set, a ghost_team_repository on it.
func testAccTeamRepositoryConfig(org, repoName, attrs string) string {
	config := testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q", org))
	if attrs == "" {
		return config
	}

	return config + fmt.Sprintf(`
resource "ghost_team_repository" "test" {
  repository = ghost_repository.test.full_name
  %s
}
`, attrs)
}

// testAccCheckTeamRepositoryRemote verifies, straight from the API, that the
// team has role on the repository in ghost_team_repository.test's state.
func testAccCheckTeamRepositoryRemote(teamID, role string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[testAccTeamRepositoryAddress]
		if !ok {
			return fmt.Errorf("%s not in state", testAccTeamRepositoryAddress)
		}
		got, err := testAccRepositoryTeamRole(rs.Primary.Attributes["repository"], teamID)
		if err != nil {
			return err
		}
		if got != role {
			return fmt.Errorf("team %s has role %q on %s, want %q", teamID, got, rs.Primary.Attributes["repository"], role)
		}

		return nil
	}
}

// testAccCheckTeamRepositoryRemoteNone verifies that the team has no access
// to the repository in ghost_team_repository.test's state.
func testAccCheckTeamRepositoryRemoteNone(teamID string) resource.TestCheckFunc {
	return testAccCheckTeamRepositoryRemote(teamID, "")
}

// testAccCheckTeamRepositoryRemoteNoneOn verifies that the team has no access
// to org/repo.
func testAccCheckTeamRepositoryRemoteNoneOn(org, repo, teamID string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := testAccRepositoryTeamRole(org+"/"+repo, teamID)
		if err != nil {
			return err
		}
		if got != "" {
			return fmt.Errorf("team %s still has role %q on %s/%s", teamID, got, org, repo)
		}

		return nil
	}
}

// testAccRepositoryTeamRole returns the team's role on the repository, or ""
// for none.
func testAccRepositoryTeamRole(fullName, teamID string) (string, error) {
	c, err := testAccClient()
	if err != nil {
		return "", err
	}
	owner, repo, err := client.SplitFullName(fullName)
	if err != nil {
		return "", err
	}
	teams, err := c.ListRepositoryTeams(context.Background(), owner, repo)
	if err != nil {
		return "", fmt.Errorf("listing the teams of %s: %w", fullName, err)
	}
	if role := repositoryTeamRole(teams, teamID); role != nil {
		return *role, nil
	}

	return "", nil
}

// testAccRemoveTeamRepository removes the team's access to org/repo.
func testAccRemoveTeamRepository(t *testing.T, org, repo, teamID string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveRepositoryTeam(context.Background(), org, repo, teamID); err != nil {
		t.Fatalf("removing team %s from %s/%s: %v", teamID, org, repo, err)
	}
}

// testAccCheckTeamRepositoryDestroy checks that no ghost_team_repository in
// state still has access, and that the repositories are gone.
func testAccCheckTeamRepositoryDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_team_repository" {
			continue
		}
		role, err := testAccRepositoryTeamRole(rs.Primary.Attributes["repository"], rs.Primary.Attributes["team_id"])
		if client.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if role != "" {
			return fmt.Errorf("team %s still has role %q on %s", rs.Primary.Attributes["team_id"], role, rs.Primary.Attributes["repository"])
		}
	}

	return testAccCheckRepositoryDestroy(s)
}

// testAccWrappedRegexp matches text that Terraform may have wrapped onto
// several lines.
func testAccWrappedRegexp(text string) *regexp.Regexp {
	return regexp.MustCompile(strings.Join(strings.Fields(regexp.QuoteMeta(text)), `\s+`))
}
