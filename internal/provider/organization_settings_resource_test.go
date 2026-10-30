package provider

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestOrganizationSettingsFromRemote(t *testing.T) {
	str := func(s string) *string { return &s }
	m := organizationSettingsResourceModel{Organization: types.StringValue("Acme")}

	m.fromRemote(&client.OrganizationSettings{
		BasePermission:                     str("write"),
		MembersCanCreatePublicRepositories: true,
		AllowPrivateForks:                  true,
		DefaultBranch:                      str("trunk"),
		Description:                        str(" "),
		Website:                            str("https://acme.test"),
		Location:                           str("Earth"),
		Email:                              str("hello@acme.test"),
	})
	want := organizationSettingsResourceModel{
		ID:                                  types.StringValue("acme"),
		Organization:                        types.StringValue("Acme"),
		BasePermission:                      types.StringValue("write"),
		MembersCanCreatePublicRepositories:  types.BoolValue(true),
		MembersCanCreatePrivateRepositories: types.BoolValue(false),
		AllowPrivateForks:                   types.BoolValue(true),
		OAuthAppRestrictions:                types.BoolValue(false),
		DefaultBranch:                       types.StringValue("trunk"),
		// Blank strings are kept as Ghost stores them.
		Description: types.StringValue(" "),
		Website:     types.StringValue("https://acme.test"),
		Location:    types.StringValue("Earth"),
		Email:       types.StringValue("hello@acme.test"),
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("fromRemote = %+v, want %+v", m, want)
	}

	// Null reads as "none" and "".
	m.fromRemote(&client.OrganizationSettings{OAuthAppRestrictions: true})
	if m.BasePermission.ValueString() != basePermissionNone || !m.OAuthAppRestrictions.ValueBool() {
		t.Errorf("base_permission = %s, oauth_app_restrictions = %s", m.BasePermission, m.OAuthAppRestrictions)
	}
	for _, s := range []types.String{m.DefaultBranch, m.Description, m.Website, m.Location, m.Email} {
		if s.IsNull() || s.ValueString() != "" {
			t.Errorf("got %s, want \"\"", s)
		}
	}
}

func TestOrganizationSettingsUpdate(t *testing.T) {
	state := organizationSettingsResourceModel{
		Organization:                        types.StringValue("acme"),
		BasePermission:                      types.StringValue("read"),
		MembersCanCreatePublicRepositories:  types.BoolValue(true),
		MembersCanCreatePrivateRepositories: types.BoolValue(true),
		AllowPrivateForks:                   types.BoolValue(false),
		OAuthAppRestrictions:                types.BoolValue(false),
		DefaultBranch:                       types.StringValue(""),
		Description:                         types.StringValue("Rockets"),
		Website:                             types.StringValue("https://acme.test"),
		Location:                            types.StringValue("Earth"),
		Email:                               types.StringValue(""),
	}

	// Nothing changed: nothing to send.
	if u := settingsUpdate(&state, &state); !u.IsEmpty() {
		t.Errorf("settingsUpdate(state, state) = %+v, want empty", u)
	}

	plan := state
	plan.BasePermission = types.StringValue(basePermissionNone)
	plan.MembersCanCreatePrivateRepositories = types.BoolValue(false)
	plan.DefaultBranch = types.StringValue("trunk")
	plan.Description = types.StringValue("")
	plan.Location = types.StringValue(" ")
	body, err := settingsUpdate(&plan, &state).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	// "none" and "" clear; false and blank strings are still sent.
	const want = `{"basePermission":null,"defaultBranch":"trunk","description":null,"location":" ","membersCanCreatePrivateRepositories":false}`
	if string(body) != want {
		t.Errorf("update = %s, want %s", body, want)
	}

	// On create, every known argument is sent; unknown ones were left out.
	create := organizationSettingsResourceModel{
		Organization:                        types.StringValue("acme"),
		BasePermission:                      types.StringValue("admin"),
		MembersCanCreatePublicRepositories:  types.BoolUnknown(),
		MembersCanCreatePrivateRepositories: types.BoolUnknown(),
		AllowPrivateForks:                   types.BoolValue(false),
		OAuthAppRestrictions:                types.BoolUnknown(),
		DefaultBranch:                       types.StringUnknown(),
		Description:                         types.StringUnknown(),
		Website:                             types.StringValue(""),
		Location:                            types.StringUnknown(),
		Email:                               types.StringUnknown(),
	}
	body, err = settingsUpdate(&create, nil).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	const wantCreate = `{"allowPrivateForks":false,"basePermission":"admin","website":null}`
	if string(body) != wantCreate {
		t.Errorf("create = %s, want %s", body, wantCreate)
	}

	// With only the organization set, there's nothing to send.
	bare := organizationSettingsResourceModel{
		Organization:                        types.StringValue("acme"),
		BasePermission:                      types.StringUnknown(),
		MembersCanCreatePublicRepositories:  types.BoolUnknown(),
		MembersCanCreatePrivateRepositories: types.BoolUnknown(),
		AllowPrivateForks:                   types.BoolUnknown(),
		OAuthAppRestrictions:                types.BoolUnknown(),
		DefaultBranch:                       types.StringUnknown(),
		Description:                         types.StringUnknown(),
		Website:                             types.StringUnknown(),
		Location:                            types.StringUnknown(),
		Email:                               types.StringUnknown(),
	}
	if u := settingsUpdate(&bare, nil); !u.IsEmpty() {
		t.Errorf("settingsUpdate(bare) = %+v, want empty", u)
	}
}

func TestOrganizationSettingsConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no organization", ``, `(?s)"organization" is required`},
		{"empty organization", `organization = ""`, `(?s)organization.*at least 1`},
		{"slash", `organization = "a/b"`, `(?s)must not contain`},
		{"computed id", `organization = "acme"` + "\n" + `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"bad base_permission", `organization = "acme"` + "\n" + `base_permission = "Write"`, `(?s)base_permission.*value must be one of`},
		{"empty base_permission", `organization = "acme"` + "\n" + `base_permission = ""`, `(?s)base_permission.*value must be one of`},
		{"long description", fmt.Sprintf("organization = \"acme\"\ndescription = %q", strings.Repeat("a", 351)), `(?s)description.*at most 350`},
		{"long location", fmt.Sprintf("organization = \"acme\"\nlocation = %q", strings.Repeat("é", 101)), `(?s)location.*at most 100`},
		{"long default_branch", fmt.Sprintf("organization = \"acme\"\ndefault_branch = %q", strings.Repeat("a", 256)), `(?s)default_branch.*at most 255`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "resource \"ghost_organization_settings\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}

	// Values at the limits, and values Ghost doesn't check, are fine. Planning
	// a new resource configures the provider but sends no request.
	if os.Getenv(envToken) == "" {
		t.Setenv(envToken, "unused")
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`resource "ghost_organization_settings" "test" {
  organization    = "acme"
  base_permission = "none"
  default_branch  = "feat/x"
  description     = %q
  location        = %q
  website         = ""
  email           = ""
}
`, strings.Repeat("a", 350), strings.Repeat("é", 100)),
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

// testAccOrganizationSettingsAll is a configuration that sets every
// argument.
type testAccOrganizationSettingsAll struct {
	org, basePermission                                  string
	createPublic, createPrivate, privateForks, oauthApps bool
	defaultBranch, description, website, location, email string
}

func (c testAccOrganizationSettingsAll) config() string {
	return fmt.Sprintf(`
resource "ghost_organization_settings" "test" {
  organization                            = %q
  base_permission                         = %q
  members_can_create_public_repositories  = %t
  members_can_create_private_repositories = %t
  allow_private_forks                     = %t
  oauth_app_restrictions                  = %t
  default_branch                          = %q
  description                             = %q
  website                                 = %q
  location                                = %q
  email                                   = %q
}
`, c.org, c.basePermission, c.createPublic, c.createPrivate, c.privateForks, c.oauthApps,
		c.defaultBranch, c.description, c.website, c.location, c.email)
}

// remote returns the settings Ghost should hold for c.
func (c testAccOrganizationSettingsAll) remote() client.OrganizationSettings {
	nullable := func(s, none string) *string {
		if s == none {
			return nil
		}
		return &s
	}

	return client.OrganizationSettings{
		BasePermission:                      nullable(c.basePermission, basePermissionNone),
		MembersCanCreatePublicRepositories:  c.createPublic,
		MembersCanCreatePrivateRepositories: c.createPrivate,
		AllowPrivateForks:                   c.privateForks,
		OAuthAppRestrictions:                c.oauthApps,
		DefaultBranch:                       nullable(c.defaultBranch, ""),
		Description:                         nullable(c.description, ""),
		Website:                             nullable(c.website, ""),
		Location:                            nullable(c.location, ""),
		Email:                               nullable(c.email, ""),
	}
}

// check verifies the state and Ghost's settings against c.
func (c testAccOrganizationSettingsAll) check() resource.TestCheckFunc {
	const rs = "ghost_organization_settings.test"
	return resource.ComposeAggregateTestCheckFunc(
		resource.TestCheckResourceAttr(rs, "id", strings.ToLower(c.org)),
		resource.TestCheckResourceAttr(rs, "organization", c.org),
		resource.TestCheckResourceAttr(rs, "base_permission", c.basePermission),
		resource.TestCheckResourceAttr(rs, "members_can_create_public_repositories", strconv.FormatBool(c.createPublic)),
		resource.TestCheckResourceAttr(rs, "members_can_create_private_repositories", strconv.FormatBool(c.createPrivate)),
		resource.TestCheckResourceAttr(rs, "allow_private_forks", strconv.FormatBool(c.privateForks)),
		resource.TestCheckResourceAttr(rs, "oauth_app_restrictions", strconv.FormatBool(c.oauthApps)),
		resource.TestCheckResourceAttr(rs, "default_branch", c.defaultBranch),
		resource.TestCheckResourceAttr(rs, "description", c.description),
		resource.TestCheckResourceAttr(rs, "website", c.website),
		resource.TestCheckResourceAttr(rs, "location", c.location),
		resource.TestCheckResourceAttr(rs, "email", c.email),
		testAccCheckOrganizationSettingsRemote(c.org, c.remote()),
	)
}

func TestAccOrganizationSettingsResource(t *testing.T) {
	org, before := testAccOrganizationSettingsFixture(t)

	first := testAccOrganizationSettingsAll{
		org: org, basePermission: "write",
		createPublic: !before.MembersCanCreatePublicRepositories, createPrivate: !before.MembersCanCreatePrivateRepositories,
		privateForks: !before.AllowPrivateForks, oauthApps: !before.OAuthAppRestrictions,
		defaultBranch: "tf-acc/trunk", description: "tf-acc " + acctest.RandString(8),
		website: "https://web.ghost.procrastinator.fyi/tf-acc", location: "tf-acc", email: "tf-acc@example.com",
	}
	second := first
	second.basePermission = basePermissionNone
	second.createPublic, second.createPrivate = !first.createPublic, !first.createPrivate
	second.privateForks, second.oauthApps = !first.privateForks, !first.oauthApps
	second.defaultBranch, second.description = "tf-acc-main", "tf-acc "+acctest.RandString(8)
	second.website, second.location, second.email = "https://web.ghost.procrastinator.fyi/tf-acc-2", " ", "tf-acc-2@example.com"
	cleared := second
	cleared.basePermission = "triage"
	cleared.defaultBranch, cleared.description, cleared.website, cleared.location, cleared.email = "", "", "", "", ""
	upper := cleared
	upper.org = strings.ToUpper(org)

	// Only the organization and the description are managed from here on.
	managed := fmt.Sprintf(`
resource "ghost_organization_settings" "test" {
  organization = %q
  description  = "tf-acc managed"
}
`, org)
	const rs = "ghost_organization_settings.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: first.config(), Check: first.check()},
			{Config: first.config(), PlanOnly: true},
			{
				// Every argument changes in place.
				Config: second.config(),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(rs, plancheck.ResourceActionUpdate)},
				},
				Check: second.check(),
			},
			{ResourceName: rs, ImportState: true, ImportStateId: org, ImportStateVerify: true},
			{
				// The slug is matched case-insensitively.
				ResourceName: rs, ImportState: true, ImportStateId: strings.ToUpper(org),
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"organization"},
			},
			{
				// "" clears a string, and a base permission comes back.
				Config: cleared.config(),
				Check:  cleared.check(),
			},
			{Config: cleared.config(), PlanOnly: true},
			{
				// A change of case is an in-place update with no API change.
				Config: upper.config(),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(rs, plancheck.ResourceActionUpdate)},
				},
				Check: upper.check(),
			},
			{
				// Ghost's changes to a managed setting show up as drift.
				PreConfig: func() {
					testAccPatchOrganizationSettings(t, org, map[string]any{"basePermission": "admin", "website": "https://web.ghost.procrastinator.fyi/drift"})
				},
				Config:             upper.config(),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: upper.config(), Check: upper.check()},
			{
				// Arguments taken out of the configuration stay as they are.
				Config: managed,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rs, "description", "tf-acc managed"),
					resource.TestCheckResourceAttr(rs, "base_permission", "triage"),
					resource.TestCheckResourceAttr(rs, "allow_private_forks", strconv.FormatBool(cleared.privateForks)),
					testAccCheckOrganizationSettingsRemote(org, func() client.OrganizationSettings {
						s, d := cleared.remote(), "tf-acc managed"
						s.Description = &d
						return s
					}()),
				),
			},
			{
				// Changes to unmanaged settings aren't drift.
				PreConfig: func() {
					testAccPatchOrganizationSettings(t, org, map[string]any{"basePermission": "maintain", "location": "tf-acc drift"})
				},
				Config: managed,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rs, "base_permission", "maintain"),
					resource.TestCheckResourceAttr(rs, "location", "tf-acc drift"),
				),
			},
		},
		// Destroying leaves the settings as they were.
		CheckDestroy: func(*terraform.State) error {
			want, d, l, b := cleared.remote(), "tf-acc managed", "tf-acc drift", "maintain"
			want.Description, want.Location, want.BasePermission = &d, &l, &b
			return testAccOrganizationSettingsEqual(org, want)
		},
	})
}

func TestAccOrganizationSettingsResource_organizationOnly(t *testing.T) {
	org, before := testAccOrganizationSettingsFixture(t)
	config := fmt.Sprintf("resource \"ghost_organization_settings\" \"test\" {\n  organization = %q\n}\n", org)

	// No arguments: nothing is sent (an empty update is a 500), and every
	// setting is read.
	want := testAccOrganizationSettingsAll{
		org: org, basePermission: basePermissionNone,
		createPublic: before.MembersCanCreatePublicRepositories, createPrivate: before.MembersCanCreatePrivateRepositories,
		privateForks: before.AllowPrivateForks, oauthApps: before.OAuthAppRestrictions,
		defaultBranch: derefString(before.DefaultBranch), description: derefString(before.Description),
		website: derefString(before.Website), location: derefString(before.Location), email: derefString(before.Email),
	}
	if before.BasePermission != nil {
		want.basePermission = *before.BasePermission
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             func(*terraform.State) error { return testAccOrganizationSettingsEqual(org, *before) },
		Steps: []resource.TestStep{
			{Config: config, Check: want.check()},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestAccOrganizationSettingsResource_defaultBranch(t *testing.T) {
	org, _ := testAccOrganizationSettingsFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	const branch = "tf-acc-trunk"

	// New repositories record the organization's default branch.
	settings := fmt.Sprintf(`
resource "ghost_organization_settings" "test" {
  organization   = %q
  default_branch = %q
}
`, org, branch)
	repo := testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q\n  depends_on = [ghost_organization_settings.test]", org))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: settings, Check: resource.TestCheckResourceAttr("ghost_organization_settings.test", "default_branch", branch)},
			{Config: settings + repo, Check: resource.TestCheckResourceAttr("ghost_repository.test", "default_branch", branch)},
		},
	})
}

func TestAccOrganizationSettingsResource_errors(t *testing.T) {
	org, _ := testAccOrganizationSettingsFixture(t)
	me := testAccTokenLogin(t)

	config := func(slug, attrs string) string {
		return fmt.Sprintf("resource \"ghost_organization_settings\" \"test\" {\n  organization = %q\n  %s\n}\n", slug, attrs)
	}
	notFound := func(slug string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)Ghost has no organization.*"` + regexp.QuoteMeta(slug) + `".*admin`)
	}
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	steps := []resource.TestStep{
		{Config: config(missing, `description = "x"`), ExpectError: notFound(missing)},
		// With nothing to change, the error comes from reading.
		{Config: config(missing, ""), ExpectError: notFound(missing)},
		// A user's login isn't an organization.
		{Config: config(me, `description = "x"`), ExpectError: notFound(me)},
		{Config: config(org, `website = "example.com"`), ExpectError: testAccWrappedRegexp("website must be a URL address")},
		{Config: config(org, `email = "not-an-email"`), ExpectError: testAccWrappedRegexp("email must be an email")},
		{
			ResourceName: "ghost_organization_settings.test", ImportState: true, ImportStateId: missing,
			Config: config(missing, ""), ExpectError: regexp.MustCompile(`(?s)Cannot import non-existent remote object`),
		},
		{
			ResourceName: "ghost_organization_settings.test", ImportState: true, ImportStateId: org + "/x",
			Config: config(org, ""), ExpectError: regexp.MustCompile(`Invalid import ID`),
		},
	}
	// Only admins see the settings, so another organization is "not found".
	if foreign := os.Getenv("GHOST_TEST_FOREIGN_ORGANIZATION"); foreign != "" {
		steps = append(steps, resource.TestStep{Config: config(foreign, `description = "x"`), ExpectError: notFound(foreign)})
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

// testAccOrganizationSettingsFixture skips the test unless
// GHOST_TEST_ORGANIZATION and TF_ACC are set. It returns the organization
// and its settings, and restores them when the test ends, even if it
// fails.
func testAccOrganizationSettingsFixture(t *testing.T) (string, *client.OrganizationSettings) {
	t.Helper()

	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test ghost_organization_settings")
	}
	testAccPreCheck(t)

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	before, err := c.GetOrganizationSettings(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, err := c.UpdateOrganizationSettings(context.Background(), org, client.UpdateOrganizationSettingsRequest{
			BasePermission:                      client.Nullable[string]{Set: true, Value: before.BasePermission},
			MembersCanCreatePublicRepositories:  client.Value(before.MembersCanCreatePublicRepositories),
			MembersCanCreatePrivateRepositories: client.Value(before.MembersCanCreatePrivateRepositories),
			AllowPrivateForks:                   client.Value(before.AllowPrivateForks),
			OAuthAppRestrictions:                client.Value(before.OAuthAppRestrictions),
			DefaultBranch:                       client.Nullable[string]{Set: true, Value: before.DefaultBranch},
			Description:                         client.Nullable[string]{Set: true, Value: before.Description},
			Website:                             client.Nullable[string]{Set: true, Value: before.Website},
			Location:                            client.Nullable[string]{Set: true, Value: before.Location},
			Email:                               client.Nullable[string]{Set: true, Value: before.Email},
		})
		if err != nil {
			t.Errorf("restoring the settings of %s: %v", org, err)
			return
		}
		if err := testAccOrganizationSettingsEqual(org, *before); err != nil {
			t.Errorf("restoring the settings of %s: %v", org, err)
		}
	})

	return org, before
}

// testAccCheckOrganizationSettingsRemote verifies, straight from the API,
// that the organization's settings are want.
func testAccCheckOrganizationSettingsRemote(org string, want client.OrganizationSettings) resource.TestCheckFunc {
	return func(*terraform.State) error { return testAccOrganizationSettingsEqual(org, want) }
}

func testAccOrganizationSettingsEqual(org string, want client.OrganizationSettings) error {
	c, err := testAccClient()
	if err != nil {
		return err
	}
	got, err := c.GetOrganizationSettings(context.Background(), org)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*got, want) {
		return fmt.Errorf("settings of %s are %s, want %s", org, testAccSettingsString(*got), testAccSettingsString(want))
	}

	return nil
}

// testAccSettingsString formats s without its email address.
func testAccSettingsString(s client.OrganizationSettings) string {
	email := "<nil>"
	if s.Email != nil {
		email = "<set>"
	}
	s.Email = nil
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return strconv.Quote(*p)
	}

	return fmt.Sprintf("{base %s, public %t, private %t, forks %t, oauth %t, branch %s, description %s, website %s, location %s, email %s}",
		str(s.BasePermission), s.MembersCanCreatePublicRepositories, s.MembersCanCreatePrivateRepositories, s.AllowPrivateForks,
		s.OAuthAppRestrictions, str(s.DefaultBranch), str(s.Description), str(s.Website), str(s.Location), email)
}
