package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestOrganizationMembersDataSourceFromRemote(t *testing.T) {
	str := func(s string) *string { return &s }
	o := &client.Organization{
		Slug: "acme",
		Members: []client.OrganizationMember{
			{Username: "zed", Name: "Zed", Public: true},
			{Username: "alice", Name: "Alice", Image: str("https://img.test/a.png")},
		},
		ViewerRole: str("owner"),
	}
	outside := []client.OutsideCollaborator{
		{Username: "yan", Name: "Yan", Repositories: []client.OutsideCollaboratorRepository{
			{Slug: "web", Role: "write", Pending: true},
			{Slug: "api", Role: "read"},
		}},
		{Username: "bob", Name: "Bob", Image: str("https://img.test/b.png"), Repositories: []client.OutsideCollaboratorRepository{}},
	}
	members, outsideBefore := slices.Clone(o.Members), slices.Clone(outside[0].Repositories)

	m := organizationMembersDataSourceModel{Organization: types.StringValue("ACME"), IncludeOutsideCollaborators: types.BoolValue(true)}
	if diags := m.fromRemote(o, outside); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if !slices.Equal(o.Members, members) || !slices.Equal(outside[0].Repositories, outsideBefore) || outside[0].Username != "yan" {
		t.Errorf("fromRemote changed its input")
	}
	if m.ID.ValueString() != "acme" || m.Organization.ValueString() != "ACME" || m.ViewerRole.ValueString() != "owner" {
		t.Errorf("id = %s, organization = %s, viewer_role = %s", m.ID, m.Organization, m.ViewerRole)
	}
	if got := m.Usernames.String(); got != `["alice","zed"]` {
		t.Errorf("usernames = %s", got)
	}
	wantMembers := []string{
		`{"image":"https://img.test/a.png","name":"Alice","public":false,"username":"alice"}`,
		`{"image":<null>,"name":"Zed","public":true,"username":"zed"}`,
	}
	wantOutside := []string{
		`{"image":"https://img.test/b.png","name":"Bob","repositories":[],"username":"bob"}`,
		`{"image":<null>,"name":"Yan","repositories":[{"full_name":"acme/api","pending":false,"role":"read","slug":"api"},` +
			`{"full_name":"acme/web","pending":true,"role":"write","slug":"web"}],"username":"yan"}`,
	}
	for _, l := range []struct {
		name string
		list types.List
		want []string
	}{{"members", m.Members, wantMembers}, {"outside_collaborators", m.OutsideCollaborators, wantOutside}} {
		elems := l.list.Elements()
		if len(elems) != len(l.want) {
			t.Fatalf("%s = %s", l.name, l.list)
		}
		for i, e := range elems {
			if got := e.String(); got != l.want[i] {
				t.Errorf("%s[%d] = %s, want %s", l.name, i, got, l.want[i])
			}
		}
	}

	// An outsider's view without outside collaborators: no role, empty
	// lists, and null outside collaborators.
	outsider := organizationMembersDataSourceModel{Organization: types.StringValue("acme")}
	if diags := outsider.fromRemote(&client.Organization{Slug: "acme"}, nil); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if !outsider.ViewerRole.IsNull() || !outsider.OutsideCollaborators.IsNull() {
		t.Errorf("viewer_role = %s, outside_collaborators = %s, want null", outsider.ViewerRole, outsider.OutsideCollaborators)
	}
	for _, l := range []types.List{outsider.Members, outsider.Usernames} {
		if l.IsNull() || len(l.Elements()) != 0 {
			t.Errorf("got %s, want an empty list", l)
		}
	}

	// Requested but empty is an empty list, not null.
	empty := organizationMembersDataSourceModel{IncludeOutsideCollaborators: types.BoolValue(true)}
	if diags := empty.fromRemote(&client.Organization{Slug: "acme"}, []client.OutsideCollaborator{}); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if empty.OutsideCollaborators.IsNull() || len(empty.OutsideCollaborators.Elements()) != 0 {
		t.Errorf("outside_collaborators = %s, want an empty list", empty.OutsideCollaborators)
	}
}

func TestOutsideCollaboratorsDeniedDetail(t *testing.T) {
	err := &client.APIError{StatusCode: http.StatusNotFound, Method: "GET", Path: "/api/organizations/acme/outside-collaborators", Message: "Organization not found: acme"}
	got := outsideCollaboratorsDeniedDetail(&client.Organization{Slug: "acme"}, err)
	if !strings.Contains(got, `Only admins of the organization "acme"`) || !strings.Contains(got, "isn't a member of it") ||
		!strings.Contains(got, "Organization not found: acme") || !strings.Contains(got, "include_outside_collaborators") {
		t.Errorf("non-member detail = %q", got)
	}

	member := "member"
	got = outsideCollaboratorsDeniedDetail(&client.Organization{Slug: "acme", ViewerRole: &member}, errors.New("403 Forbidden"))
	if !strings.Contains(got, "is a member of it") || !strings.Contains(got, "403 Forbidden") {
		t.Errorf("member detail = %q", got)
	}
}

func TestOrganizationMembersDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no organization", ``, `(?s)"organization" is required`},
		{"empty organization", `organization = ""`, `(?s)organization.*at least 1`},
		{"bad bool", `organization = "acme"` + "\n" + `include_outside_collaborators = "yes"`, `(?s)Incorrect attribute value type|bool required`},
		{"computed id", `organization = "acme"` + "\n" + `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed members", `organization = "acme"` + "\n" + `members = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed usernames", `organization = "acme"` + "\n" + `usernames = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed viewer_role", `organization = "acme"` + "\n" + `viewer_role = "owner"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed outside_collaborators", `organization = "acme"` + "\n" + `outside_collaborators = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_organization_members\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testOrganizationMembersServer stubs Ghost for the organization "Acme"
// (slug acme): its profile, with viewerRole role (null when ""), and its
// outside collaborators, answered with status and body. It returns a
// provider configuration pointing at the stub, followed by the data source
// with extra arguments.
func testOrganizationMembersServer(t *testing.T, role string, status int, body, extra string) string {
	t.Helper()
	viewerRole := "null"
	if role != "" {
		viewerRole = strconv.Quote(role)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations/Acme":
			_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme","logo":null,"createdAt":"2026-10-27T18:49:54.738Z","description":null,` +
				`"website":null,"location":null,"email":null,"repositoryCount":0,"members":[` +
				`{"username":"zed","name":"Zed","image":null,"public":true},{"username":"alice","name":"Alice","image":"https://img.test/a.png","public":false}],` +
				`"pinned":[],"viewerRole":` + viewerRole + `}`))
		case "/api/organizations/acme/outside-collaborators":
			if status == 0 {
				t.Errorf("outside collaborators were listed without include_outside_collaborators")
				status = http.StatusOK
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)

	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

data "ghost_organization_members" "test" {
  organization = "Acme"
  %s
}
`, srv.URL, extra)
}

func TestOrganizationMembersDataSourceRead(t *testing.T) {
	const d = "data.ghost_organization_members.test"
	const outside = `{"collaborators":[{"username":"yan","name":"Yan","image":null,"repositories":[` +
		`{"slug":"web","role":"write","pending":true},{"slug":"api","role":"admin","pending":false}]},` +
		`{"username":"bob","name":"Bob","image":"https://img.test/b.png","repositories":[{"slug":"web","role":"read","pending":false}]}]}`

	t.Run("members only", func(t *testing.T) {
		// Status 0 fails the test if the outside collaborators are listed.
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: testOrganizationMembersServer(t, "owner", 0, "", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "id", "acme"),
					resource.TestCheckResourceAttr(d, "organization", "Acme"),
					resource.TestCheckResourceAttr(d, "viewer_role", "owner"),
					resource.TestCheckResourceAttr(d, "members.#", "2"),
					resource.TestCheckResourceAttr(d, "members.0.username", "alice"),
					resource.TestCheckResourceAttr(d, "members.0.image", "https://img.test/a.png"),
					resource.TestCheckResourceAttr(d, "members.0.public", "false"),
					resource.TestCheckResourceAttr(d, "members.1.username", "zed"),
					resource.TestCheckNoResourceAttr(d, "members.1.image"),
					resource.TestCheckResourceAttr(d, "members.1.public", "true"),
					resource.TestCheckResourceAttr(d, "usernames.#", "2"),
					resource.TestCheckResourceAttr(d, "usernames.0", "alice"),
					resource.TestCheckResourceAttr(d, "usernames.1", "zed"),
					resource.TestCheckNoResourceAttr(d, "outside_collaborators.#"),
				),
			}},
		})
	})

	t.Run("outside collaborators", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: testOrganizationMembersServer(t, "admin", http.StatusOK, outside, "include_outside_collaborators = true"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(d, "outside_collaborators.#", "2"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.0.username", "bob"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.0.image", "https://img.test/b.png"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.0.repositories.#", "1"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.username", "yan"),
					resource.TestCheckNoResourceAttr(d, "outside_collaborators.1.image"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.#", "2"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.0.slug", "api"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.0.full_name", "acme/api"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.0.role", "admin"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.0.pending", "false"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.1.slug", "web"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.1.role", "write"),
					resource.TestCheckResourceAttr(d, "outside_collaborators.1.repositories.1.pending", "true"),
				),
			}},
		})
	})

	t.Run("explicitly false", func(t *testing.T) {
		resource.UnitTest(t, resource.TestCase{
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{{
				Config: testOrganizationMembersServer(t, "", 0, "", "include_outside_collaborators = false"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(d, "viewer_role"),
					resource.TestCheckResourceAttr(d, "include_outside_collaborators", "false"),
					resource.TestCheckNoResourceAttr(d, "outside_collaborators.#"),
				),
			}},
		})
	})

	// A member who isn't an admin gets a 403, which can't be produced live:
	// the test organization can't gain a second member with a token.
	for _, tc := range []struct {
		name, role string
		status     int
		body, want string
	}{
		{"forbidden member", "member", http.StatusForbidden, `{"statusCode":403,"message":"Insufficient permissions"}`, `is a member of it.*Insufficient permissions`},
		{"non-member", "", http.StatusNotFound, `{"statusCode":404,"message":"Organization not found: acme"}`, `isn't a member of it.*Organization not found: acme`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testOrganizationMembersServer(t, tc.role, tc.status, tc.body, "include_outside_collaborators = true"),
					ExpectError: wrappedPattern(`Cannot list outside collaborators.*Only admins of the organization "acme" can list its outside collaborators.*` + tc.want + `.*include_outside_collaborators`),
				}},
			})
		})
	}
}

func TestAccOrganizationMembersDataSource(t *testing.T) {
	org, before := testAccOrganizationFixture(t)
	me := testAccTokenLogin(t)
	public := testAccMemberPublic(t, before, me)
	t.Cleanup(func() { testAccSetPublicMembership(t, org, public) })

	config := fmt.Sprintf(`
data "ghost_organization_members" "test" {
  organization = %q
}

data "ghost_organization_members" "upper" {
  organization = %q
}
`, org, strings.ToUpper(org))
	const ds, upper = "data.ghost_organization_members.test", "data.ghost_organization_members.upper"

	// expect checks both data sources against the profile o, read straight
	// from the API.
	expect := func(o *client.Organization) resource.TestCheckFunc {
		checks := []resource.TestCheckFunc{
			resource.TestCheckResourceAttr(ds, "id", o.Slug),
			resource.TestCheckResourceAttr(ds, "organization", org),
			resource.TestCheckResourceAttr(upper, "id", o.Slug),
			resource.TestCheckResourceAttr(upper, "organization", strings.ToUpper(org)),
			resource.TestCheckResourceAttr(ds, "members.#", strconv.Itoa(len(o.Members))),
			resource.TestCheckResourceAttr(ds, "usernames.#", strconv.Itoa(len(o.Members))),
			resource.TestCheckNoResourceAttr(ds, "outside_collaborators.#"),
			resource.TestCheckNoResourceAttr(upper, "outside_collaborators.#"),
		}
		if o.ViewerRole == nil {
			checks = append(checks, resource.TestCheckNoResourceAttr(ds, "viewer_role"))
		} else {
			checks = append(checks, resource.TestCheckResourceAttr(ds, "viewer_role", *o.ViewerRole))
		}
		members := slices.Clone(o.Members)
		slices.SortFunc(members, func(a, b client.OrganizationMember) int { return strings.Compare(a.Username, b.Username) })
		for i, m := range members {
			prefix := fmt.Sprintf("members.%d.", i)
			checks = append(checks,
				resource.TestCheckResourceAttr(ds, prefix+"username", m.Username),
				resource.TestCheckResourceAttr(ds, prefix+"name", m.Name),
				resource.TestCheckResourceAttr(ds, prefix+"public", strconv.FormatBool(m.Public)),
				resource.TestCheckResourceAttr(ds, fmt.Sprintf("usernames.%d", i), m.Username),
			)
			if m.Image == nil {
				checks = append(checks, resource.TestCheckNoResourceAttr(ds, prefix+"image"))
			} else {
				checks = append(checks, resource.TestCheckResourceAttr(ds, prefix+"image", *m.Image))
			}
		}
		for _, attr := range []string{"members.#", "usernames.#", "viewer_role", "members.0.username", "members.0.public"} {
			checks = append(checks, resource.TestCheckResourceAttrPair(upper, attr, ds, attr))
		}

		return resource.ComposeAggregateTestCheckFunc(checks...)
	}
	// profile reads the organization's profile when the check runs, so it
	// sees the membership changes made in PreConfig.
	profile := func(s *terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		o, err := c.GetOrganization(context.Background(), org)
		if err != nil {
			return err
		}
		return expect(o)(s)
	}
	// visibleToAnonymous checks whether an anonymous caller sees the token
	// user among the members.
	visibleToAnonymous := func(want bool) resource.TestCheckFunc {
		return func(*terraform.State) error {
			baseURL := os.Getenv(envBaseURL)
			if baseURL == "" {
				baseURL = client.DefaultBaseURL
			}
			anon, err := client.New(baseURL, "")
			if err != nil {
				return err
			}
			o, err := anon.GetOrganization(context.Background(), org)
			if err != nil {
				return err
			}
			if got := slices.ContainsFunc(o.Members, func(m client.OrganizationMember) bool { return m.Username == me }); got != want {
				return fmt.Errorf("anonymous callers see %s among the members of %s: %t, want %t", me, org, got, want)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// A member sees every member, private ones included.
				PreConfig: func() { testAccSetPublicMembership(t, org, false) },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					profile,
					resource.TestCheckTypeSetElemNestedAttrs(ds, "members.*", map[string]string{"username": me, "public": "false"}),
					resource.TestCheckTypeSetElemAttr(ds, "usernames.*", me),
					resource.TestCheckResourceAttrSet(ds, "viewer_role"),
					visibleToAnonymous(false),
				),
			},
			{
				PreConfig: func() { testAccSetPublicMembership(t, org, true) },
				Config:    config,
				Check: resource.ComposeAggregateTestCheckFunc(
					profile,
					resource.TestCheckTypeSetElemNestedAttrs(ds, "members.*", map[string]string{"username": me, "public": "true"}),
					visibleToAnonymous(true),
				),
			},
			{
				PreConfig: func() { testAccSetPublicMembership(t, org, public) },
				Config:    config,
				Check:     profile,
			},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestAccOrganizationMembersDataSource_foreign(t *testing.T) {
	foreign := os.Getenv("GHOST_TEST_FOREIGN_ORGANIZATION")
	if foreign == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_FOREIGN_ORGANIZATION (an organization the token's user isn't in) and TF_ACC to test ghost_organization_members outside an organization")
	}
	testAccPreCheck(t)
	me := testAccTokenLogin(t)
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.GetOrganization(context.Background(), foreign)
	if err != nil {
		t.Fatal(err)
	}
	if o.ViewerRole != nil {
		t.Fatalf("the token's user is a %s of %s; GHOST_TEST_FOREIGN_ORGANIZATION must be an organization they aren't in", *o.ViewerRole, foreign)
	}

	slug := strings.ToUpper(foreign[:1]) + foreign[1:]
	config := func(extra string) string {
		return fmt.Sprintf("data \"ghost_organization_members\" \"test\" {\n  organization = %q\n  %s\n}\n", slug, extra)
	}
	const ds = "data.ghost_organization_members.test"
	members := slices.Clone(o.Members)
	slices.SortFunc(members, func(a, b client.OrganizationMember) int { return strings.Compare(a.Username, b.Username) })
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr(ds, "id", o.Slug),
		resource.TestCheckResourceAttr(ds, "organization", slug),
		resource.TestCheckNoResourceAttr(ds, "viewer_role"),
		resource.TestCheckNoResourceAttr(ds, "outside_collaborators.#"),
		resource.TestCheckResourceAttr(ds, "members.#", strconv.Itoa(len(members))),
		func(s *terraform.State) error {
			if v := s.RootModule().Resources[ds].Primary.Attributes; slices.Contains(attrList(v, "usernames"), me) {
				return fmt.Errorf("%s is listed among the members of %s", me, foreign)
			}
			return nil
		},
	}
	// Outsiders see only public members.
	for i, m := range members {
		checks = append(checks,
			resource.TestCheckResourceAttr(ds, fmt.Sprintf("members.%d.username", i), m.Username),
			resource.TestCheckResourceAttr(ds, fmt.Sprintf("members.%d.public", i), "true"),
			resource.TestCheckResourceAttr(ds, fmt.Sprintf("usernames.%d", i), m.Username),
		)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(""), Check: resource.ComposeAggregateTestCheckFunc(checks...)},
			{Config: config(""), PlanOnly: true},
			{
				// Ghost hides the outside collaborators of an organization
				// the user isn't in.
				Config:      config("include_outside_collaborators = true"),
				ExpectError: wrappedPattern(`Only admins of the organization "` + o.Slug + `".*isn't a member of it.*Organization not found`),
			},
		},
	})
}

func TestAccOrganizationMembersDataSource_outsideCollaborators(t *testing.T) {
	user := testAccCollaboratorFixture(t)
	org, before := testAccOrganizationFixture(t)
	me := testAccTokenLogin(t)
	if slices.ContainsFunc(before.Members, func(m client.OrganizationMember) bool { return strings.EqualFold(m.Username, user) }) {
		t.Fatalf("%s is a member of %s; GHOST_TEST_COLLABORATOR must be outside the organization", user, org)
	}
	prefix := acctest.RandomWithPrefix("tf-acc-org-members")
	repoA, repoB := prefix+"-a", prefix+"-b"

	config := func(roleA, roleB string) string {
		cfg := fmt.Sprintf(`
resource "ghost_repository" "a" {
  owner = %[1]q
  name  = %[2]q
}

resource "ghost_repository" "b" {
  owner      = %[1]q
  name       = %[3]q
  visibility = "public"
}

resource "ghost_repository_collaborator" "a" {
  repository = ghost_repository.a.full_name
  username   = %[4]q
  role       = %[5]q
}
`, org, repoA, repoB, user, roleA)
		depends := "ghost_repository_collaborator.a"
		if roleB != "" {
			cfg += fmt.Sprintf(`
resource "ghost_repository_collaborator" "b" {
  repository = ghost_repository.b.full_name
  username   = %q
  role       = %q
}
`, user, roleB)
			depends += ", ghost_repository_collaborator.b"
		}

		return cfg + fmt.Sprintf(`
data "ghost_organization_members" "test" {
  organization                  = %q
  include_outside_collaborators = true
  depends_on                    = [%s]
}
`, strings.ToUpper(org), depends)
	}
	const ds = "data.ghost_organization_members.test"

	// expect checks that the user's entry lists exactly want (slug =>
	// role), pending and sorted by slug, that it matches the raw API, and
	// that the user isn't a member and the token's user isn't an outside
	// collaborator.
	expect := func(want map[string]string) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			attrs := s.RootModule().Resources[ds].Primary.Attributes
			if slices.Contains(attrList(attrs, "usernames"), user) {
				return fmt.Errorf("%s is listed as a member of %s", user, org)
			}
			n, _ := strconv.Atoi(attrs["outside_collaborators.#"])
			entry := -1
			for i := range n {
				switch attrs[fmt.Sprintf("outside_collaborators.%d.username", i)] {
				case strings.ToLower(user):
					entry = i
				case me:
					return fmt.Errorf("the member %s is listed as an outside collaborator", me)
				}
			}
			if entry < 0 {
				return fmt.Errorf("%s isn't among the %d outside collaborators", user, n)
			}

			p := fmt.Sprintf("outside_collaborators.%d.", entry)
			got := map[string]string{}
			var slugs []string
			for i := range len(want) + 10 {
				rp := fmt.Sprintf("%srepositories.%d.", p, i)
				slug, ok := attrs[rp+"slug"]
				if !ok {
					break
				}
				slugs = append(slugs, slug)
				if attrs[rp+"full_name"] != strings.ToLower(org)+"/"+slug || attrs[rp+"pending"] != "true" {
					return fmt.Errorf("%s: full_name = %q, pending = %q", rp, attrs[rp+"full_name"], attrs[rp+"pending"])
				}
				got[slug] = attrs[rp+"role"]
			}
			if !slices.IsSorted(slugs) {
				return fmt.Errorf("repositories aren't sorted by slug: %v", slugs)
			}
			// Other repositories the user is invited to are left alone,
			// so only the test's are compared.
			for _, slug := range []string{repoA, repoB} {
				if got[slug] != want[slug] {
					return fmt.Errorf("%s's role on %s = %q, want %q (all: %v)", user, slug, got[slug], want[slug], got)
				}
			}
			raw := testAccOutsideCollaboratorRepos(t, org, user)
			for slug, role := range got {
				if raw[slug] != role+" (pending)" {
					return fmt.Errorf("the API lists %s on %s as %q, the data source %q", user, slug, raw[slug], role)
				}
			}
			if len(raw) != len(got) {
				return fmt.Errorf("the API lists %v, the data source %v", raw, got)
			}
			return nil
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			if err := testAccCheckRepositoryCollaboratorDestroy(s); err != nil {
				return err
			}
			if err := testAccCheckRepositoryDestroy(s); err != nil {
				return err
			}
			if repos := testAccOutsideCollaboratorRepos(t, org, user); repos[repoA] != "" || repos[repoB] != "" {
				return fmt.Errorf("%s is still an outside collaborator of %s: %v", user, org, repos)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("read", "write"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ds, "id", strings.ToLower(org)),
					resource.TestCheckResourceAttr(ds, "include_outside_collaborators", "true"),
					expect(map[string]string{repoA: "read", repoB: "write"}),
				),
			},
			{
				// A role change shows up on the next read.
				Config: config("admin", "write"),
				Check:  expect(map[string]string{repoA: "admin", repoB: "write"}),
			},
			{
				// The data source is read before the invitation is
				// withdrawn, so the next step checks it.
				Config: config("admin", ""),
			},
			{
				// Withdrawing an invitation drops that repository.
				Config: config("admin", ""),
				Check:  expect(map[string]string{repoA: "admin"}),
			},
		},
	})
}

func TestAccOrganizationMembersDataSource_errors(t *testing.T) {
	org, _ := testAccOrganizationFixture(t)
	me := testAccTokenLogin(t)

	config := func(slug string) string {
		return fmt.Sprintf("data \"ghost_organization_members\" \"test\" {\n  organization = %q\n}\n", slug)
	}
	expect := func(slug string) *regexp.Regexp {
		return wrappedPattern(`Organization not found.*"` + regexp.QuoteMeta(slug) + `".*a user's login isn't an organization`)
	}
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(missing), ExpectError: expect(missing)},
			// A user's login isn't an organization.
			{Config: config(me), ExpectError: expect(me)},
			// The slug is escaped, so this can't reach another endpoint.
			{Config: config(org + "/outside-collaborators"), ExpectError: expect(org + "/outside-collaborators")},
		},
	})
}

// attrList returns the elements of the flat-mapped list attribute name.
func attrList(attrs map[string]string, name string) []string {
	n, _ := strconv.Atoi(attrs[name+".#"])
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, attrs[fmt.Sprintf("%s.%d", name, i)])
	}

	return out
}

// testAccMemberPublic returns whether username shows their membership of
// the organization o publicly. The token's user must be a member.
func testAccMemberPublic(t *testing.T, o *client.Organization, username string) bool {
	t.Helper()

	i := slices.IndexFunc(o.Members, func(m client.OrganizationMember) bool { return m.Username == username })
	if i < 0 {
		t.Fatalf("%s isn't a member of %s", username, o.Slug)
	}

	return o.Members[i].Public
}

// testAccSetPublicMembership makes the token user's membership of org
// public or private.
func testAccSetPublicMembership(t *testing.T, org string, public bool) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	method := http.MethodDelete
	if public {
		method = http.MethodPut
	}
	if err := c.Do(context.Background(), method, client.NativePath("organizations", org, "public-members"), nil, nil); err != nil {
		t.Errorf("making the membership of %s public=%t: %v", org, public, err)
	}
}

// wrappedPattern compiles the regular expression pattern so that each of
// its spaces also matches the line breaks Terraform wraps error text at.
func wrappedPattern(pattern string) *regexp.Regexp {
	return regexp.MustCompile(`(?s)` + strings.ReplaceAll(pattern, " ", `\s+`))
}
