package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestOrganizationTeamsDataSourceFromRemote(t *testing.T) {
	teams := []client.TeamSummary{
		{ID: "T3", Name: "Alpha Squad", Slug: "zeta", MemberCount: 7, RepositoryCount: 0},
		{ID: "T1", Name: "Zed", Slug: "a-team", MemberCount: 0, RepositoryCount: 1},
		{ID: "T2", Name: "Middle", Slug: "mid", MemberCount: 1, RepositoryCount: 7},
	}
	input := slices.Clone(teams)

	m := organizationTeamsDataSourceModel{Organization: types.StringValue("Acme")}
	if diags := m.fromRemote(teams); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if m.ID.ValueString() != "Acme" {
		t.Errorf("id = %s, want the organization as configured", m.ID)
	}
	if !slices.Equal(teams, input) {
		t.Errorf("fromRemote changed its input: %+v", teams)
	}

	want := []string{
		`{"id":"T1","member_count":0,"name":"Zed","repository_count":1,"slug":"a-team"}`,
		`{"id":"T2","member_count":1,"name":"Middle","repository_count":7,"slug":"mid"}`,
		`{"id":"T3","member_count":7,"name":"Alpha Squad","repository_count":0,"slug":"zeta"}`,
	}
	elems := m.Teams.Elements()
	if len(elems) != len(want) {
		t.Fatalf("teams = %s", m.Teams)
	}
	for i, e := range elems {
		if got := e.String(); got != want[i] {
			t.Errorf("teams[%d] = %s, want %s", i, got, want[i])
		}
	}

	// No teams gives an empty list, not null.
	for _, in := range [][]client.TeamSummary{nil, {}} {
		if diags := m.fromRemote(in); diags.HasError() {
			t.Fatalf("fromRemote: %v", diags)
		}
		if m.Teams.IsNull() || len(m.Teams.Elements()) != 0 {
			t.Errorf("teams = %s, want an empty list", m.Teams)
		}
	}
}

func TestOrganizationTeamsDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no organization", ``, `(?s)"organization" is required`},
		{"empty organization", `organization = ""`, `(?s)organization.*at least 1`},
		{"computed id", `organization = "acme"` + "\n" + `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed teams", `organization = "acme"` + "\n" + `teams = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_organization_teams\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestAccOrganizationTeamsDataSource(t *testing.T) {
	org, _ := testAccTeamFixture(t)
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	teams, err := c.ListOrganizationTeams(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(teams, func(a, b client.TeamSummary) int { return strings.Compare(a.Slug, b.Slug) })
	first := teams[0]
	repoName := acctest.RandomWithPrefix("tf-acc")

	config := testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q", org)) + fmt.Sprintf(`
data "ghost_organization_teams" "test" {
  organization = %[1]q
}

data "ghost_organization_teams" "upper" {
  organization = %[2]q
}

data "ghost_team" "cross" {
  organization = %[1]q
  id           = data.ghost_organization_teams.test.teams[0].id
}
`, org, strings.ToUpper(org))

	const list, upper, cross = "data.ghost_organization_teams.test", "data.ghost_organization_teams.upper", "data.ghost_team.cross"
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr(list, "id", org),
		resource.TestCheckResourceAttr(list, "organization", org),
		resource.TestCheckResourceAttr(list, "teams.#", strconv.Itoa(len(teams))),
		resource.TestCheckResourceAttr(upper, "id", strings.ToUpper(org)),
		resource.TestCheckResourceAttrPair(upper, "teams.#", list, "teams.#"),
		resource.TestCheckResourceAttrPair(cross, "id", list, "teams.0.id"),
		resource.TestCheckResourceAttrPair(cross, "slug", list, "teams.0.slug"),
		resource.TestCheckResourceAttrPair(cross, "name", list, "teams.0.name"),
		resource.TestCheckResourceAttrPair(cross, "members.#", list, "teams.0.member_count"),
		resource.TestCheckResourceAttrPair(cross, "repositories.#", list, "teams.0.repository_count"),
	}
	for i, team := range teams {
		prefix := fmt.Sprintf("teams.%d.", i)
		checks = append(checks,
			resource.TestCheckResourceAttr(list, prefix+"id", team.ID),
			resource.TestCheckResourceAttr(list, prefix+"slug", team.Slug),
			resource.TestCheckResourceAttr(list, prefix+"name", team.Name),
			resource.TestCheckResourceAttr(list, prefix+"member_count", strconv.FormatInt(team.MemberCount, 10)),
			resource.TestCheckResourceAttr(list, prefix+"repository_count", strconv.FormatInt(team.RepositoryCount, 10)),
			resource.TestCheckResourceAttrPair(upper, prefix+"id", list, prefix+"id"),
			resource.TestCheckResourceAttrPair(upper, prefix+"slug", list, prefix+"slug"),
		)
	}

	repoCount := strconv.FormatInt(first.RepositoryCount+1, 10)
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
				// Give the first team a role on the new repository. The
				// data sources are read again when the step plans.
				PreConfig: func() { testAccSetTeamRepositoryRole(t, org, repoName, first.ID, "read") },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(list, "teams.0.repository_count", repoCount),
					resource.TestCheckResourceAttr(upper, "teams.0.repository_count", repoCount),
					resource.TestCheckResourceAttr(list, "teams.0.member_count", strconv.FormatInt(first.MemberCount, 10)),
					resource.TestCheckResourceAttrPair(cross, "repositories.#", list, "teams.0.repository_count"),
					resource.TestCheckTypeSetElemNestedAttrs(cross, "repositories.*", map[string]string{"slug": repoName, "role": "read"}),
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

func TestAccOrganizationTeamsDataSource_errors(t *testing.T) {
	testAccTeamFixture(t)
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	me, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	config := func(org string) string {
		return fmt.Sprintf("data \"ghost_organization_teams\" \"test\" {\n  organization = %q\n}\n", org)
	}
	expect := func(org string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)Organization not found.*"` + regexp.QuoteMeta(org) + `".*member`)
	}
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	steps := []resource.TestStep{
		{Config: config(missing), ExpectError: expect(missing)},
		// A user's login isn't an organization.
		{Config: config(me.Login), ExpectError: expect(me.Login)},
	}
	// An organization that exists but the token's user isn't a member of
	// looks missing too.
	if foreign := os.Getenv("GHOST_TEST_FOREIGN_ORGANIZATION"); foreign != "" {
		steps = append(steps, resource.TestStep{Config: config(foreign), ExpectError: expect(foreign)})
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}
