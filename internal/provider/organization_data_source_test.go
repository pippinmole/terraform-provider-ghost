package provider

import (
	"context"
	"fmt"
	"net/http"
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

func TestOrganizationDataSourceFromRemote(t *testing.T) {
	str := func(s string) *string { return &s }
	o := &client.Organization{
		Slug:            "acme",
		Name:            "Acme Inc",
		Logo:            str("https://img.test/l.png"),
		CreatedAt:       "2026-10-27T18:49:54.738Z",
		Description:     str("Rockets"),
		Location:        str("Earth"),
		RepositoryCount: 3,
		Members: []client.OrganizationMember{
			{Username: "zed", Name: "Zed", Public: true},
			{Username: "alice", Name: "Alice", Image: str("https://img.test/a.png")},
		},
		Pinned: []client.PinnedRepository{
			{Slug: "web", Name: "Web", Visibility: "public"},
			{Slug: "api", Name: "API", Description: str("The API"), Visibility: "private"},
		},
		ViewerRole: str("owner"),
	}
	members := slices.Clone(o.Members)

	m := organizationDataSourceModel{Slug: types.StringValue("ACME")}
	if diags := m.fromRemote(o); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if !slices.Equal(o.Members, members) {
		t.Errorf("fromRemote changed its input: %+v", o.Members)
	}

	for name, got := range map[string]types.String{
		`"acme"`:                     m.ID,
		`"ACME"`:                     m.Slug,
		`"Acme Inc"`:                 m.Name,
		`"https://img.test/l.png"`:   m.Logo,
		`"2026-10-27T18:49:54.738Z"`: m.CreatedAt,
		`"Rockets"`:                  m.Description,
		`"Earth"`:                    m.Location,
		`"owner"`:                    m.ViewerRole,
	} {
		if got.String() != name {
			t.Errorf("got %s, want %s", got, name)
		}
	}
	if !m.Website.IsNull() || !m.Email.IsNull() {
		t.Errorf("website = %s, email = %s, want null", m.Website, m.Email)
	}
	if m.PublicRepositoryCount.ValueInt64() != 3 {
		t.Errorf("public_repository_count = %s", m.PublicRepositoryCount)
	}

	wantMembers := []string{
		`{"image":"https://img.test/a.png","name":"Alice","public":false,"username":"alice"}`,
		`{"image":<null>,"name":"Zed","public":true,"username":"zed"}`,
	}
	wantPinned := []string{
		`{"description":<null>,"full_name":"acme/web","name":"Web","slug":"web","visibility":"public"}`,
		`{"description":"The API","full_name":"acme/api","name":"API","slug":"api","visibility":"private"}`,
	}
	for _, l := range []struct {
		name string
		list types.List
		want []string
	}{{"members", m.Members, wantMembers}, {"pinned_repositories", m.PinnedRepositories, wantPinned}} {
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

	// An outsider's view: no role, no logo, and nothing listed gives empty
	// lists, not null.
	// Ghost stores an empty detail verbatim, so it stays "" rather than null.
	outsider := &client.Organization{Slug: "acme", Name: "Acme", CreatedAt: "2026-10-27T18:49:54.738Z", Website: str("")}
	if diags := m.fromRemote(outsider); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if !m.ViewerRole.IsNull() || !m.Logo.IsNull() || !m.Description.IsNull() || !m.Location.IsNull() {
		t.Errorf("viewer_role = %s, logo = %s, description = %s, location = %s, want null", m.ViewerRole, m.Logo, m.Description, m.Location)
	}
	if m.Website.IsNull() || m.Website.ValueString() != "" {
		t.Errorf("website = %s, want \"\"", m.Website)
	}
	for _, l := range []types.List{m.Members, m.PinnedRepositories} {
		if l.IsNull() || len(l.Elements()) != 0 {
			t.Errorf("got %s, want an empty list", l)
		}
	}
}

func TestOrganizationDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no slug", ``, `(?s)"slug" is required`},
		{"empty slug", `slug = ""`, `(?s)slug.*at least 1`},
		{"computed id", `slug = "acme"` + "\n" + `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed name", `slug = "acme"` + "\n" + `name = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed members", `slug = "acme"` + "\n" + `members = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_organization\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestAccOrganizationDataSource(t *testing.T) {
	org, before := testAccOrganizationFixture(t)
	testAccRestoreOrganizationProfile(t, org, before)
	me := testAccTokenLogin(t)
	repoName := acctest.RandomWithPrefix("tf-acc")

	config := func(visibility string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q\n  visibility = %q", org, visibility)) + fmt.Sprintf(`
data "ghost_organization" "test" {
  slug = %q
}

data "ghost_organization" "upper" {
  slug = %q
}
`, org, strings.ToUpper(org))
	}

	const ds, upper = "data.ghost_organization.test", "data.ghost_organization.upper"
	// expect checks every attribute of both data sources against o.
	expect := func(o *client.Organization) resource.TestCheckFunc {
		checks := []resource.TestCheckFunc{
			resource.TestCheckResourceAttr(ds, "id", o.Slug),
			resource.TestCheckResourceAttr(ds, "slug", org),
			resource.TestCheckResourceAttr(upper, "id", o.Slug),
			resource.TestCheckResourceAttr(upper, "slug", strings.ToUpper(org)),
			resource.TestCheckResourceAttr(ds, "name", o.Name),
			resource.TestCheckResourceAttr(ds, "created_at", o.CreatedAt),
			resource.TestCheckResourceAttr(ds, "public_repository_count", strconv.FormatInt(o.RepositoryCount, 10)),
			resource.TestCheckResourceAttr(ds, "members.#", strconv.Itoa(len(o.Members))),
			resource.TestCheckResourceAttr(ds, "pinned_repositories.#", strconv.Itoa(len(o.Pinned))),
		}
		for name, v := range map[string]*string{
			"logo": o.Logo, "description": o.Description, "website": o.Website,
			"location": o.Location, "email": o.Email, "viewer_role": o.ViewerRole,
		} {
			if v == nil {
				checks = append(checks, resource.TestCheckNoResourceAttr(ds, name))
			} else {
				checks = append(checks, resource.TestCheckResourceAttr(ds, name, *v))
			}
		}
		members := slices.Clone(o.Members)
		slices.SortFunc(members, func(a, b client.OrganizationMember) int { return strings.Compare(a.Username, b.Username) })
		for i, m := range members {
			prefix := fmt.Sprintf("members.%d.", i)
			checks = append(checks,
				resource.TestCheckResourceAttr(ds, prefix+"username", m.Username),
				resource.TestCheckResourceAttr(ds, prefix+"public", strconv.FormatBool(m.Public)),
			)
		}
		for i, r := range o.Pinned {
			prefix := fmt.Sprintf("pinned_repositories.%d.", i)
			checks = append(checks,
				resource.TestCheckResourceAttr(ds, prefix+"slug", r.Slug),
				resource.TestCheckResourceAttr(ds, prefix+"full_name", o.Slug+"/"+r.Slug),
				resource.TestCheckResourceAttr(ds, prefix+"visibility", r.Visibility),
			)
		}
		for _, attr := range []string{
			"name", "logo", "created_at", "description", "website", "location", "email",
			"public_repository_count", "members.#", "pinned_repositories.#", "viewer_role",
		} {
			checks = append(checks, resource.TestCheckResourceAttrPair(upper, attr, ds, attr))
		}

		return resource.ComposeAggregateTestCheckFunc(checks...)
	}

	pinned := []string{repoName}
	for _, r := range before.Pinned {
		if len(pinned) < 6 {
			pinned = append(pinned, r.Slug)
		}
	}
	description, website, location := "tf-acc "+acctest.RandString(8), "https://web.ghost.procrastinator.fyi/tf-acc", "tf-acc"
	changed := *before
	changed.Description, changed.Website, changed.Location = &description, &website, &location
	changed.Pinned = append([]client.PinnedRepository{{Slug: repoName, Name: repoName, Visibility: "private"}}, before.Pinned...)
	changed.Pinned = changed.Pinned[:len(pinned)]
	public := changed
	public.RepositoryCount++
	public.Pinned = slices.Clone(changed.Pinned)
	public.Pinned[0].Visibility = "public"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				// A new private repository doesn't change the public count.
				Config: config("private"),
				Check: resource.ComposeAggregateTestCheckFunc(
					expect(before),
					// A member sees every member, private ones included.
					resource.TestCheckTypeSetElemNestedAttrs(ds, "members.*", map[string]string{"username": me}),
					resource.TestCheckResourceAttrSet(ds, "viewer_role"),
				),
			},
			{
				// Pin the repository first and set the profile details. The
				// data sources are read again when the step plans.
				PreConfig: func() {
					testAccSetOrganizationPins(t, org, pinned)
					testAccPatchOrganizationSettings(t, org, map[string]any{"description": description, "website": website, "location": location})
				},
				Config: config("private"),
				Check: resource.ComposeAggregateTestCheckFunc(
					expect(&changed),
					resource.TestCheckNoResourceAttr(ds, "pinned_repositories.0.description"),
					resource.TestCheckResourceAttr(ds, "pinned_repositories.0.name", repoName),
				),
			},
			{
				// The data sources are read before the repository is made
				// public, so the next step sees the new count.
				Config: config("public"),
			},
			{
				Config: config("public"),
				Check:  expect(&public),
			},
			{
				// Restore the profile; null details read back as null.
				PreConfig: func() {
					testAccSetOrganizationPins(t, org, pinnedSlugs(before.Pinned))
					testAccPatchOrganizationSettings(t, org, map[string]any{"description": before.Description, "website": before.Website, "location": before.Location})
				},
				Config: config("public"),
				Check: func() resource.TestCheckFunc {
					restored := *before
					restored.RepositoryCount++
					return expect(&restored)
				}(),
			},
			{
				Config:   config("public"),
				PlanOnly: true,
			},
		},
	})
}

func TestAccOrganizationDataSource_foreign(t *testing.T) {
	foreign := os.Getenv("GHOST_TEST_FOREIGN_ORGANIZATION")
	if foreign == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_FOREIGN_ORGANIZATION (an organization the token's user isn't in) and TF_ACC to test ghost_organization outside an organization")
	}
	testAccPreCheck(t)
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

	// Mixed case, to match case-insensitively.
	slug := strings.ToUpper(foreign[:1]) + foreign[1:]
	config := fmt.Sprintf("data \"ghost_organization\" \"test\" {\n  slug = %q\n}\n", slug)
	const ds = "data.ghost_organization.test"
	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr(ds, "id", o.Slug),
		resource.TestCheckResourceAttr(ds, "slug", slug),
		resource.TestCheckResourceAttr(ds, "name", o.Name),
		resource.TestCheckNoResourceAttr(ds, "viewer_role"),
		resource.TestCheckResourceAttr(ds, "public_repository_count", strconv.FormatInt(o.RepositoryCount, 10)),
		resource.TestCheckResourceAttr(ds, "members.#", strconv.Itoa(len(o.Members))),
		resource.TestCheckResourceAttr(ds, "pinned_repositories.#", strconv.Itoa(len(o.Pinned))),
	}
	// Outsiders see only public members.
	for i := range o.Members {
		checks = append(checks, resource.TestCheckResourceAttr(ds, fmt.Sprintf("members.%d.public", i), "true"))
	}
	for i, r := range o.Pinned {
		checks = append(checks, resource.TestCheckResourceAttr(ds, fmt.Sprintf("pinned_repositories.%d.slug", i), r.Slug))
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config, Check: resource.ComposeAggregateTestCheckFunc(checks...)},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestAccOrganizationDataSource_errors(t *testing.T) {
	org, _ := testAccOrganizationFixture(t)
	me := testAccTokenLogin(t)

	config := func(slug string) string {
		return fmt.Sprintf("data \"ghost_organization\" \"test\" {\n  slug = %q\n}\n", slug)
	}
	expect := func(slug string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)Organization not found.*"` + regexp.QuoteMeta(slug) + `".*ghost_user`)
	}
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config(missing), ExpectError: expect(missing)},
			{Config: config(strings.ToUpper(missing)), ExpectError: expect(strings.ToUpper(missing))},
			// A user's login isn't an organization.
			{Config: config(me), ExpectError: expect(me)},
			// The slug is escaped, so this can't reach the teams endpoint.
			{Config: config(org + "/teams"), ExpectError: expect(org + "/teams")},
		},
	})
}

// testAccOrganizationFixture skips the test unless GHOST_TEST_ORGANIZATION
// and TF_ACC are set, and returns the organization and its profile.
func testAccOrganizationFixture(t *testing.T) (string, *client.Organization) {
	t.Helper()

	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test ghost_organization")
	}
	testAccPreCheck(t)

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.GetOrganization(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}

	return org, o
}

// testAccRestoreOrganizationProfile restores the organization's pins and
// profile details to o when the test ends, even if it fails.
func testAccRestoreOrganizationProfile(t *testing.T, org string, o *client.Organization) {
	t.Helper()

	t.Cleanup(func() {
		c, err := testAccClient()
		if err != nil {
			t.Error(err)
			return
		}
		ctx := context.Background()
		if err := c.Do(ctx, http.MethodPut, client.NativePath("organizations", org, "pins"), map[string]any{"repositories": pinnedSlugs(o.Pinned)}, nil); err != nil {
			t.Errorf("restoring the pins of %s: %v", org, err)
		}
		settings := map[string]any{"description": o.Description, "website": o.Website, "location": o.Location}
		if err := c.Do(ctx, http.MethodPatch, client.NativePath("organizations", org, "settings"), settings, nil); err != nil {
			t.Errorf("restoring the settings of %s: %v", org, err)
		}
	})
}

// testAccSetOrganizationPins replaces the organization's pinned
// repositories with slugs.
func testAccSetOrganizationPins(t *testing.T, org string, slugs []string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), http.MethodPut, client.NativePath("organizations", org, "pins"), map[string]any{"repositories": slugs}, nil); err != nil {
		t.Fatalf("pinning %v to %s: %v", slugs, org, err)
	}
}

// testAccPatchOrganizationSettings changes the organization's settings.
func testAccPatchOrganizationSettings(t *testing.T, org string, settings map[string]any) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Do(context.Background(), http.MethodPatch, client.NativePath("organizations", org, "settings"), settings, nil); err != nil {
		t.Fatalf("changing the settings of %s: %v", org, err)
	}
}

// pinnedSlugs returns the slugs of repos, in order.
func pinnedSlugs(repos []client.PinnedRepository) []string {
	out := []string{}
	for _, r := range repos {
		out = append(out, r.Slug)
	}

	return out
}
