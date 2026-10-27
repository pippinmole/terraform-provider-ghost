package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

const testAccTeamMembershipAddress = "ghost_team_membership.test"

func TestParseTeamMembershipID(t *testing.T) {
	org, team, user, err := parseTeamMembershipID("acme:T1:alice")
	if err != nil || org != "acme" || team != "T1" || user != "alice" {
		t.Errorf("got %q, %q, %q, %v", org, team, user, err)
	}
	if got := teamMembershipID(org, team, user); got != "acme:T1:alice" {
		t.Errorf("teamMembershipID = %q", got)
	}
	for _, id := range []string{"", "acme", "acme:T1", "acme::alice", ":T1:alice", "acme:T1:", "a:b:c:d", "a/b:c:d"} {
		if _, _, _, err := parseTeamMembershipID(id); err == nil {
			t.Errorf("parseTeamMembershipID(%q) succeeded, want an error", id)
		}
	}
}

func TestFindTeamMember(t *testing.T) {
	members := []client.TeamMember{{Username: "alice", Maintainer: true}, {Username: "bob"}}
	if m := findTeamMember(members, "ALICE"); m == nil || m.Username != "alice" {
		t.Errorf("findTeamMember(ALICE) = %+v", m)
	}
	if m := findTeamMember(members, "carol"); m != nil {
		t.Errorf("findTeamMember(carol) = %+v, want nil", m)
	}
}

func TestTeamMembershipFromRemote(t *testing.T) {
	m := teamMembershipResourceModel{
		Organization: types.StringValue("Acme"),
		TeamID:       types.StringValue("T1"),
		Username:     types.StringValue("Alice"),
	}
	m.fromRemote(&client.TeamMember{Username: "alice", Maintainer: true})
	if m.Username.ValueString() != "Alice" || m.Role.ValueString() != "maintainer" || m.ID.ValueString() != "Acme:T1:alice" {
		t.Errorf("unexpected model: %+v", m)
	}

	m.fromRemote(&client.TeamMember{Username: "bob"})
	if m.Username.ValueString() != "bob" || m.Role.ValueString() != "member" || m.ID.ValueString() != "Acme:T1:bob" {
		t.Errorf("unexpected model: %+v", m)
	}
}

func TestTeamMembershipConfigValidation(t *testing.T) {
	base := `organization = "acme"` + "\n" + `username = "alice"` + "\n"
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"neither team", base, `(?s)one \(and only one\) of\s+\[team_id,team_slug\]`},
		{"both teams", base + `team_id = "T1"` + "\n" + `team_slug = "core"`, `(?s)one \(and only one\) of\s+\[team_id,team_slug\]`},
		{"empty team_id", base + `team_id = ""`, `(?s)team_id.*at least 1`},
		{"colon in team_slug", base + `team_slug = "a:b"`, `must not contain ":" or "/"`},
		{"bad role", base + `team_id = "T1"` + "\n" + `role = "admin"`, `(?s)value must be one of.*"member".*"maintainer"`},
		{"role case", base + `team_id = "T1"` + "\n" + `role = "Maintainer"`, `(?s)value must be one of`},
		{"no organization", `username = "alice"` + "\n" + `team_id = "T1"`, `(?s)"organization" is required`},
		{"no username", `organization = "acme"` + "\n" + `team_id = "T1"`, `(?s)"username" is required`},
		{"empty username", `organization = "acme"` + "\n" + `username = ""` + "\n" + `team_id = "T1"`, `(?s)username.*at least 1`},
		{"slash in username", `organization = "acme"` + "\n" + `username = "a/b"` + "\n" + `team_id = "T1"`, `must not contain ":" or "/"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "resource \"ghost_team_membership\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestAccTeamMembershipResource(t *testing.T) {
	org, team, user := testAccTeamMembershipFixture(t)
	config := func(role string) string {
		attrs := fmt.Sprintf("team_id = %q", team.ID)
		if role != "" {
			attrs += fmt.Sprintf("\n  role = %q", role)
		}
		return testAccTeamMembershipConfig(org, user, attrs)
	}
	update := resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamMembershipAddress, plancheck.ResourceActionUpdate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamMembershipDestroy,
		Steps: []resource.TestStep{
			{
				// role defaults to member.
				Config: config(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "id", org+":"+team.ID+":"+user),
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "team_id", team.ID),
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "team_slug", team.Slug),
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "role", "member"),
					testAccCheckTeamMembershipRemote(org, team.ID, user, "member"),
				),
			},
			{
				ResourceName:      testAccTeamMembershipAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// The import ID can name the team by its slug, in any case.
				ResourceName:      testAccTeamMembershipAddress,
				ImportState:       true,
				ImportStateId:     org + ":" + strings.ToUpper(team.Slug) + ":" + user,
				ImportStateVerify: true,
			},
			{
				Config:           config("maintainer"),
				ConfigPlanChecks: update,
				Check:            testAccCheckTeamMembershipRemote(org, team.ID, user, "maintainer"),
			},
			{
				// Adding a member again doesn't dismiss a maintainer, so this
				// proves the provider does.
				Config:           config("member"),
				ConfigPlanChecks: update,
				Check:            testAccCheckTeamMembershipRemote(org, team.ID, user, "member"),
			},
			{
				// A maintainer appointed outside Terraform is dismissed.
				PreConfig:        func() { testAccSetTeamMember(t, org, team.Slug, user, "maintainer") },
				Config:           config("member"),
				ConfigPlanChecks: update,
				Check:            testAccCheckTeamMembershipRemote(org, team.ID, user, "member"),
			},
			{
				// A member removed outside Terraform is added back.
				PreConfig: func() { testAccRemoveTeamMember(t, org, team.Slug, user) },
				Config:    config("member"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamMembershipAddress, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckTeamMembershipRemote(org, team.ID, user, "member"),
			},
			{
				Config: config("maintainer"),
				Check:  testAccCheckTeamMembershipRemote(org, team.ID, user, "maintainer"),
			},
			{
				// A maintainer dismissed outside Terraform is appointed again.
				PreConfig:        func() { testAccSetTeamMember(t, org, team.Slug, user, "member") },
				Config:           config("maintainer"),
				ConfigPlanChecks: update,
				Check:            testAccCheckTeamMembershipRemote(org, team.ID, user, "maintainer"),
			},
		},
	})
}

func TestAccTeamMembershipResource_teamSlug(t *testing.T) {
	org, team, user := testAccTeamMembershipFixture(t)
	upper := strings.ToUpper(team.Slug)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamMembershipDestroy,
		Steps: []resource.TestStep{
			{
				// The user is already a maintainer: the resource adopts them.
				PreConfig: func() { testAccSetTeamMember(t, org, team.Slug, user, "maintainer") },
				Config:    testAccTeamMembershipConfig(org, user, fmt.Sprintf("team_slug = %q\n  role = \"maintainer\"", upper)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "team_id", team.ID),
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "team_slug", upper),
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "role", "maintainer"),
					testAccCheckTeamMembershipRemote(org, team.ID, user, "maintainer"),
				),
			},
			{
				// Naming the same team by its ID changes nothing.
				Config: testAccTeamMembershipConfig(org, user, fmt.Sprintf("team_id = %q\n  role = \"maintainer\"", team.ID)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamMembershipAddress, plancheck.ResourceActionNoop)},
				},
			},
			{
				// And back to the slug in its stored spelling, as a member.
				Config: testAccTeamMembershipConfig(org, user, fmt.Sprintf("team_slug = %q", team.Slug)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamMembershipAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "team_slug", team.Slug),
					testAccCheckTeamMembershipRemote(org, team.ID, user, "member"),
				),
			},
			{
				ResourceName:      testAccTeamMembershipAddress,
				ImportState:       true,
				ImportStateId:     org + ":" + team.Slug + ":" + user,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccTeamMembershipResource_caseChanges(t *testing.T) {
	org, team, user := testAccTeamMembershipFixture(t)
	upperUser, upperOrg := strings.ToUpper(user), strings.ToUpper(org)
	attrs := fmt.Sprintf("team_id = %q", team.ID)
	inPlace := resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamMembershipAddress, plancheck.ResourceActionUpdate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamMembershipDestroy,
		Steps: []resource.TestStep{
			{Config: testAccTeamMembershipConfig(org, user, attrs)},
			{
				Config:           testAccTeamMembershipConfig(org, upperUser, attrs),
				ConfigPlanChecks: inPlace,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "username", upperUser),
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "id", org+":"+team.ID+":"+user),
					testAccCheckTeamMembershipRemote(org, team.ID, user, "member"),
				),
			},
			{
				Config:           testAccTeamMembershipConfig(upperOrg, upperUser, attrs),
				ConfigPlanChecks: inPlace,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "organization", upperOrg),
					resource.TestCheckResourceAttr(testAccTeamMembershipAddress, "id", upperOrg+":"+team.ID+":"+user),
					testAccCheckTeamMembershipRemote(org, team.ID, user, "member"),
				),
			},
		},
	})
}

// TestAccTeamMembershipResource_teamChange needs a second team, which can't
// be created with a token.
func TestAccTeamMembershipResource_teamChange(t *testing.T) {
	org, first, user := testAccTeamMembershipFixture(t)
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
		t.Skipf("organization %s needs a second team to test changing the team, and teams can't be created with a token", org)
	}
	testAccRestoreTeamMembership(t, org, second.Slug, user)
	testAccRemoveTeamMember(t, org, second.Slug, user)
	config := func(teamID string) string {
		return testAccTeamMembershipConfig(org, user, fmt.Sprintf("team_id = %q", teamID))
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamMembershipDestroy,
		Steps: []resource.TestStep{
			{Config: config(first.ID), Check: testAccCheckTeamMembershipRemote(org, first.ID, user, "member")},
			{
				Config: config(second.ID),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccTeamMembershipAddress, plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckTeamMembershipRemote(org, second.ID, user, "member"),
					testAccCheckTeamMembershipRemote(org, first.ID, user, ""),
				),
			},
		},
	})
}

// TestAccTeamMembershipResource_errors covers what only Ghost can check.
func TestAccTeamMembershipResource_errors(t *testing.T) {
	org, team, user := testAccTeamMembershipFixture(t)
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	withTeam := testAccTeamMembershipConfig(org, user, fmt.Sprintf("team_id = %q", team.ID))

	steps := []resource.TestStep{
		{
			Config:      testAccTeamMembershipConfig(org, missing, fmt.Sprintf("team_id = %q", team.ID)),
			ExpectError: testAccWrappedRegexp(`Ghost has no user "` + missing + `"`),
		},
		{
			Config:      testAccTeamMembershipConfig(org, user, fmt.Sprintf("team_id = %q", missing)),
			ExpectError: testAccWrappedRegexp(`no team with the id "` + missing + `"`),
		},
		{
			Config:      testAccTeamMembershipConfig(org, user, fmt.Sprintf("team_slug = %q", missing)),
			ExpectError: testAccWrappedRegexp(`no team with the slug "` + missing + `"`),
		},
		{
			Config:      testAccTeamMembershipConfig(missing, user, fmt.Sprintf("team_id = %q", team.ID)),
			ExpectError: testAccWrappedRegexp(`Organization ` + missing + ` not found, or you're not a member of it`),
		},
		{
			// Importing a membership that doesn't exist: the fixture took the
			// user out of the team.
			Config:        withTeam,
			ResourceName:  testAccTeamMembershipAddress,
			ImportState:   true,
			ImportStateId: org + ":" + team.ID + ":" + user,
			ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
		},
		{
			Config:        withTeam,
			ResourceName:  testAccTeamMembershipAddress,
			ImportState:   true,
			ImportStateId: org + ":" + missing + ":" + user,
			ExpectError:   testAccWrappedRegexp(`no team with the ID or slug`),
		},
		{
			Config:        withTeam,
			ResourceName:  testAccTeamMembershipAddress,
			ImportState:   true,
			ImportStateId: org + ":" + team.ID,
			ExpectError:   testAccWrappedRegexp(`import ID must be "organization:team_id:username"`),
		},
	}

	// A user outside the organization can't join its teams.
	if outsider := testAccOptionalCollaborator(); outsider != "" {
		steps = append(steps, resource.TestStep{
			Config:      testAccTeamMembershipConfig(org, outsider, fmt.Sprintf("team_id = %q", team.ID)),
			ExpectError: testAccWrappedRegexp(outsider + ` isn't a member of organization ` + org),
		})
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckTeamMembershipDestroy,
		Steps:                    steps,
	})

	if outsider := testAccOptionalCollaborator(); outsider != "" {
		if err := testAccCheckTeamMembershipRemote(org, team.ID, outsider, "")(nil); err != nil {
			t.Error(err)
		}
	}
}

// testAccOptionalCollaborator returns GHOST_TEST_COLLABORATOR, a user who
// isn't a member of GHOST_TEST_ORGANIZATION, or "" when it isn't set.
func testAccOptionalCollaborator() string {
	return os.Getenv("GHOST_TEST_COLLABORATOR")
}

// testAccTeamMembershipFixture returns the organization, its first team and
// the token's user, who is the only organization member the tests can rely
// on: Ghost only adds an organization's members to its teams, and they can't
// be invited with a token. It takes the user out of the team so the tests
// start from nothing, and puts their membership back as it was afterwards.
// Tests using it mustn't run in parallel with other tests of that team.
func testAccTeamMembershipFixture(t *testing.T) (string, *client.Team, string) {
	t.Helper()

	org, team := testAccTeamFixture(t)
	user := testAccTokenLogin(t)
	testAccRestoreTeamMembership(t, org, team.Slug, user)
	testAccRemoveTeamMember(t, org, team.Slug, user)

	return org, team, user
}

// testAccRestoreTeamMembership records the user's membership of the team now
// and restores it when the test ends. It never touches the organization's
// own membership.
func testAccRestoreTeamMembership(t *testing.T, org, teamSlug, user string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	team, err := c.GetTeam(context.Background(), org, teamSlug)
	if err != nil {
		t.Fatal(err)
	}
	role := ""
	if m := findTeamMember(team.Members, user); m != nil {
		role = teamRoleMember
		if m.Maintainer {
			role = teamRoleMaintainer
		}
	}

	t.Cleanup(func() {
		if role == "" {
			if err := c.RemoveTeamMember(context.Background(), org, teamSlug, user); err != nil {
				t.Errorf("restoring team %s: removing %s: %v", teamSlug, user, err)
			}
			return
		}
		if err := testAccSetTeamMemberErr(c, org, teamSlug, user, role); err != nil {
			t.Errorf("restoring team %s: %v", teamSlug, err)
		}
	})
}

// testAccTeamMembershipConfig is a ghost_team_membership of the user in org,
// with attrs naming the team and role.
func testAccTeamMembershipConfig(org, user, attrs string) string {
	return fmt.Sprintf(`
resource "ghost_team_membership" "test" {
  organization = %q
  username     = %q
  %s
}
`, org, user, attrs)
}

// testAccCheckTeamMembershipRemote verifies, straight from the API, that the
// user has role ("member" or "maintainer") on the team, or isn't in it when
// role is "".
func testAccCheckTeamMembershipRemote(org, teamID, user, role string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := testAccTeamMemberRole(org, teamID, user)
		if err != nil {
			return err
		}
		if got != role {
			return fmt.Errorf("%s has role %q on team %s, want %q", user, got, teamID, role)
		}

		return nil
	}
}

// testAccTeamMemberRole returns the user's role on the team, or "" when
// they're not in it.
func testAccTeamMemberRole(org, teamID, user string) (string, error) {
	c, err := testAccClient()
	if err != nil {
		return "", err
	}
	team, err := c.GetTeamByID(context.Background(), org, teamID)
	if err != nil {
		return "", fmt.Errorf("reading team %s: %w", teamID, err)
	}
	m := findTeamMember(team.Members, user)
	switch {
	case m == nil:
		return "", nil
	case m.Maintainer:
		return teamRoleMaintainer, nil
	default:
		return teamRoleMember, nil
	}
}

// testAccSetTeamMember puts the user in the team with role, outside
// Terraform.
func testAccSetTeamMember(t *testing.T, org, teamSlug, user, role string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := testAccSetTeamMemberErr(c, org, teamSlug, user, role); err != nil {
		t.Fatal(err)
	}
}

func testAccSetTeamMemberErr(c *client.Client, org, teamSlug, user, role string) error {
	ctx := context.Background()
	if err := c.AddTeamMember(ctx, org, teamSlug, user); err != nil {
		return fmt.Errorf("adding %s to team %s: %w", user, teamSlug, err)
	}
	var err error
	if role == teamRoleMaintainer {
		err = c.AppointTeamMaintainer(ctx, org, teamSlug, user)
	} else {
		err = c.DismissTeamMaintainer(ctx, org, teamSlug, user)
	}
	if err != nil {
		return fmt.Errorf("making %s a %s of team %s: %w", user, role, teamSlug, err)
	}

	return nil
}

// testAccRemoveTeamMember takes the user out of the team, outside Terraform.
func testAccRemoveTeamMember(t *testing.T, org, teamSlug, user string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveTeamMember(context.Background(), org, teamSlug, user); err != nil {
		t.Fatalf("removing %s from team %s: %v", user, teamSlug, err)
	}
}

// testAccCheckTeamMembershipDestroy checks that no ghost_team_membership in
// state is still in its team.
func testAccCheckTeamMembershipDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_team_membership" {
			continue
		}
		a := rs.Primary.Attributes
		role, err := testAccTeamMemberRole(a["organization"], a["team_id"], a["username"])
		if client.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if role != "" {
			return fmt.Errorf("%s is still a %s of team %s", a["username"], role, a["team_id"])
		}
	}

	return nil
}
