package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestTeamDataSourceFromRemote(t *testing.T) {
	team := &client.Team{
		ID: "T1", Name: "Core Team", Slug: "core-team", ViewerCanManage: true,
		Members: []client.TeamMember{
			{Username: "zed", Name: "Zed", Image: strPtr("https://img.test/z.png"), Maintainer: true},
			{Username: "alice", Name: "Alice", Image: nil, Maintainer: false},
			{Username: "bob", Name: "Bob", Maintainer: true},
		},
		Repositories: []client.TeamRepository{
			{Slug: "web", Name: "Web", Visibility: "public", Role: "read"},
			{Slug: "api", Name: "API", Visibility: "private", Role: "maintain"},
		},
	}

	var m teamDataSourceModel
	if diags := m.fromRemote("Acme", team); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if m.ID.ValueString() != "T1" || m.Slug.ValueString() != "core-team" || m.Name.ValueString() != "Core Team" || !m.ViewerCanManage.ValueBool() {
		t.Errorf("unexpected identity: %+v", m)
	}

	members := m.Members.Elements()
	if len(members) != 3 {
		t.Fatalf("members = %s", m.Members)
	}
	if got := members[0].String(); !strings.Contains(got, `"username":"alice"`) || !strings.Contains(got, `"image":<null>`) {
		t.Errorf("first member = %s, want alice with a null image", got)
	}
	if got := members[2].String(); !strings.Contains(got, `"username":"zed"`) || !strings.Contains(got, `"maintainer":true`) {
		t.Errorf("last member = %s", got)
	}
	if got := m.Maintainers.String(); got != `["bob","zed"]` {
		t.Errorf("maintainers = %s", got)
	}

	repos := m.Repositories.Elements()
	if len(repos) != 2 {
		t.Fatalf("repositories = %s", m.Repositories)
	}
	if got := repos[0].String(); !strings.Contains(got, `"full_name":"Acme/api"`) || !strings.Contains(got, `"role":"maintain"`) {
		t.Errorf("first repository = %s", got)
	}

	// A team with no members or repositories gets empty lists, not nulls.
	if diags := m.fromRemote("acme", &client.Team{ID: "T2"}); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	for name, l := range map[string]interface{ IsNull() bool }{"members": m.Members, "maintainers": m.Maintainers, "repositories": m.Repositories} {
		if l.IsNull() {
			t.Errorf("%s is null, want an empty list", name)
		}
	}
	if len(m.Members.Elements()) != 0 || len(m.Maintainers.Elements()) != 0 || len(m.Repositories.Elements()) != 0 {
		t.Errorf("want empty lists, got %s %s %s", m.Members, m.Maintainers, m.Repositories)
	}
}

func TestTeamDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"neither", `organization = "acme"`, `(?s)Exactly one of these attributes must be configured`},
		{"both", `organization = "acme"` + "\n" + `slug = "core"` + "\n" + `id = "T1"`, `(?s)Exactly one of these attributes must be configured`},
		{"no organization", `slug = "core"`, `(?s)"organization" is required`},
		{"empty organization", `organization = ""` + "\n" + `slug = "core"`, `(?s)organization.*at least 1`},
		{"empty slug", `organization = "acme"` + "\n" + `slug = ""`, `(?s)slug.*at least 1`},
		{"empty id", `organization = "acme"` + "\n" + `id = ""`, `(?s)id.*at least 1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_team\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testAccTeamFixture skips the test unless GHOST_TEST_ORGANIZATION and TF_ACC
// are set, then returns the organization and its first team, read straight
// from the API. Teams can't be created with a token, so the tests read an
// existing one.
func testAccTeamFixture(t *testing.T) (string, *client.Team) {
	t.Helper()

	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test ghost_team")
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
	team, err := c.GetTeam(context.Background(), org, teams[0].Slug)
	if err != nil {
		t.Fatal(err)
	}

	return org, team
}

func TestAccTeamDataSource(t *testing.T) {
	org, team := testAccTeamFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")

	config := testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q", org)) + fmt.Sprintf(`
data "ghost_team" "by_slug" {
  organization = %[1]q
  slug         = %[2]q
}

data "ghost_team" "by_id" {
  organization = %[1]q
  id           = %[3]q
}

data "ghost_team" "upper_case" {
  organization = %[4]q
  slug         = %[5]q
}
`, org, team.Slug, team.ID, strings.ToUpper(org), strings.ToUpper(team.Slug))

	const bySlug, byID, upper = "data.ghost_team.by_slug", "data.ghost_team.by_id", "data.ghost_team.upper_case"
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr(bySlug, "id", team.ID),
		resource.TestCheckResourceAttr(bySlug, "slug", team.Slug),
		resource.TestCheckResourceAttr(bySlug, "name", team.Name),
		resource.TestCheckResourceAttr(bySlug, "viewer_can_manage", strconv.FormatBool(team.ViewerCanManage)),
		resource.TestCheckResourceAttr(bySlug, "members.#", strconv.Itoa(len(team.Members))),
		resource.TestCheckResourceAttr(upper, "id", team.ID),
		resource.TestCheckResourceAttr(upper, "slug", team.Slug),
		resource.TestCheckResourceAttr(upper, "organization", strings.ToUpper(org)),
	}
	maintainers := 0
	for _, m := range team.Members {
		checks = append(checks, resource.TestCheckTypeSetElemNestedAttrs(bySlug, "members.*", map[string]string{
			"username": m.Username, "name": m.Name, "maintainer": strconv.FormatBool(m.Maintainer),
		}))
		if m.Maintainer {
			maintainers++
			checks = append(checks, resource.TestCheckTypeSetElemAttr(bySlug, "maintainers.*", m.Username))
		}
	}
	checks = append(checks, resource.TestCheckResourceAttr(bySlug, "maintainers.#", strconv.Itoa(maintainers)))
	for _, attr := range []string{"id", "slug", "name", "viewer_can_manage", "members.#", "maintainers.#", "repositories.#"} {
		checks = append(checks, resource.TestCheckResourceAttrPair(byID, attr, bySlug, attr))
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.ComposeAggregateTestCheckFunc(checks...),
			},
			{
				// Give the team a role on the new repository. The data
				// sources are read again when the step plans.
				PreConfig: func() { testAccSetTeamRepositoryRole(t, org, repoName, team.ID, "write") },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckTypeSetElemNestedAttrs(bySlug, "repositories.*", map[string]string{
						"slug": repoName, "name": repoName, "full_name": org + "/" + repoName, "visibility": "private", "role": "write",
					}),
					resource.TestCheckTypeSetElemNestedAttrs(byID, "repositories.*", map[string]string{
						"slug": repoName, "role": "write",
					}),
					resource.TestCheckTypeSetElemNestedAttrs(upper, "repositories.*", map[string]string{
						"full_name": strings.ToUpper(org) + "/" + repoName,
					}),
				),
			},
		},
	})
}

func TestAccTeamDataSource_errors(t *testing.T) {
	org, team := testAccTeamFixture(t)
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	config := func(org, attr, value string) string {
		return fmt.Sprintf(`
data "ghost_team" "test" {
  organization = %q
  %s = %q
}
`, org, attr, value)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config(org, "slug", missing),
				ExpectError: regexp.MustCompile(`(?s)Team not found.*with slug "` + missing + `".*display name or ID`),
			},
			{
				// Ghost can't get a team by ID: point the user at `id`.
				Config:      config(org, "slug", team.ID),
				ExpectError: regexp.MustCompile(`(?s)Team not found.*with slug`),
			},
			{
				Config:      config(missing, "slug", team.Slug),
				ExpectError: regexp.MustCompile(`(?s)Team not found.*Organization not found`),
			},
			{
				Config:      config(org, "id", missing),
				ExpectError: regexp.MustCompile(`(?s)Team not found.*with ID "` + missing + `"`),
			},
		},
	})
}

// testAccSetTeamRepositoryRole gives the team teamID the role on org/repo.
func testAccSetTeamRepositoryRole(t *testing.T, org, repo, teamID, role string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"role": role}
	if err := c.Do(context.Background(), http.MethodPut, client.NativePath("repositories", org, repo, "teams", teamID), body, nil); err != nil {
		t.Fatalf("giving team %s the %s role on %s/%s: %v", teamID, role, org, repo, err)
	}
}
