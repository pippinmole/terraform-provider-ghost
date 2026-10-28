package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

const testAccPinsAddress = "ghost_organization_pinned_repositories.test"

func TestOrganizationPinnedRepositoriesFromRemote(t *testing.T) {
	m := organizationPinnedRepositoriesResourceModel{Organization: types.StringValue("ACME")}
	o := &client.Organization{Pinned: []client.PinnedRepository{{Slug: "web"}, {Slug: "api"}, {Slug: "a.b_c"}}}
	if diags := m.fromRemote(o); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if m.ID.ValueString() != "acme" || m.Organization.ValueString() != "ACME" {
		t.Errorf("id = %s, organization = %s", m.ID, m.Organization)
	}
	if got := m.Repositories.String(); got != `["web","api","a.b_c"]` {
		t.Errorf("repositories = %s", got)
	}

	// Nothing pinned is an empty list, not null.
	if diags := m.fromRemote(&client.Organization{}); diags.HasError() {
		t.Fatalf("fromRemote: %v", diags)
	}
	if m.Repositories.IsNull() || len(m.Repositories.Elements()) != 0 {
		t.Errorf("repositories = %s, want an empty list", m.Repositories)
	}
}

func TestPinsError(t *testing.T) {
	org := &client.APIError{StatusCode: http.StatusNotFound, Message: "Organization not found: acme"}
	repo := &client.APIError{StatusCode: http.StatusNotFound, Message: "Repository not found"}
	other := &client.APIError{StatusCode: http.StatusForbidden, Message: "Forbidden"}

	if !isOrganizationNotFound(org) || isOrganizationNotFound(repo) || isOrganizationNotFound(other) {
		t.Error("isOrganizationNotFound misclassified an error")
	}
	for err, want := range map[error]string{
		org:   `no organization "acme" that you're an admin of`,
		repo:  `ghost_repository.<name>.slug`,
		other: "Forbidden",
	} {
		if got := pinsError("acme", err); !strings.Contains(got, want) {
			t.Errorf("pinsError(%v) = %q, want it to contain %q", err, got, want)
		}
	}
}

func TestOrganizationPinnedRepositoriesConfigValidation(t *testing.T) {
	seven := `["a", "b", "c", "d", "e", "f", "g"]`
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no organization", `repositories = []`, `"organization" is required`},
		{"empty organization", `organization = ""` + "\n" + `repositories = []`, `organization.*at least 1`},
		{"organization with slash", `organization = "a/b"` + "\n" + `repositories = []`, `must not contain`},
		{"no repositories", `organization = "acme"`, `"repositories" is required`},
		{"too many", `organization = "acme"` + "\n" + `repositories = ` + seven, `at most 6`},
		{"duplicates", `organization = "acme"` + "\n" + `repositories = ["a", "a"]`, `duplicate values`},
		{"empty slug", `organization = "acme"` + "\n" + `repositories = [""]`, `at least 1`},
		{"upper case", `organization = "acme"` + "\n" + `repositories = ["Web"]`, `must be a repository slug`},
		{"owner prefix", `organization = "acme"` + "\n" + `repositories = ["acme/web"]`, `must be a repository slug`},
		{"computed id", `organization = "acme"` + "\n" + `repositories = []` + "\n" + `id = "x"`, `Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "resource \"ghost_organization_pinned_repositories\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`(?s)` + tt.expect),
				}},
			})
		})
	}
}

// testAccPinsFixture skips the test unless GHOST_TEST_ORGANIZATION and
// TF_ACC are set, records the organization's pins and restores them when
// the test ends, even if it fails.
func testAccPinsFixture(t *testing.T) (string, []string) {
	t.Helper()

	org, before := testAccOrganizationFixture(t)
	testAccRestoreOrganizationProfile(t, org, before)

	return org, pinnedSlugs(before.Pinned)
}

// testAccPinsConfig declares one public repository per name in org, as
// ghost_repository.r[index(local.names, "<name>")], and pins pins, which are HCL expressions.
func testAccPinsConfig(org string, names []string, organization string, pins ...string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}

	return fmt.Sprintf(`
locals {
  names = [%s]
}

resource "ghost_repository" "r" {
  count      = length(local.names)
  name       = local.names[count.index]
  owner      = %q
  visibility = "public"
}

resource "ghost_organization_pinned_repositories" "test" {
  organization = %q
  repositories = [%s]
}
`, strings.Join(quoted, ", "), org, organization, strings.Join(pins, ", "))
}

// testAccPinRef returns the HCL reference to the slug of the repository named name.
func testAccPinRef(name string) string {
	return fmt.Sprintf("ghost_repository.r[index(local.names, %q)].slug", name)
}

// testAccCheckPinsRemote checks, straight from the API, that org's pinned
// repositories are want, in order.
func testAccCheckPinsRemote(org string, want []string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, err := testAccClient()
		if err != nil {
			return err
		}
		o, err := c.GetOrganization(context.Background(), org)
		if err != nil {
			return err
		}
		if got := pinnedSlugs(o.Pinned); !slices.Equal(got, want) {
			return fmt.Errorf("pins of %s = %v, want %v", org, got, want)
		}

		return nil
	}
}

// testAccCheckPinsState checks the resource's repositories, in order.
func testAccCheckPinsState(want []string) resource.TestCheckFunc {
	checks := []resource.TestCheckFunc{resource.TestCheckResourceAttr(testAccPinsAddress, "repositories.#", fmt.Sprint(len(want)))}
	for i, s := range want {
		checks = append(checks, resource.TestCheckResourceAttr(testAccPinsAddress, fmt.Sprintf("repositories.%d", i), s))
	}

	return resource.ComposeAggregateTestCheckFunc(checks...)
}

func TestAccOrganizationPinnedRepositoriesResource(t *testing.T) {
	org, _ := testAccPinsFixture(t)
	prefix := acctest.RandomWithPrefix("tf-acc-pin")
	a, b, c, d := prefix+"-a", prefix+"-b", prefix+"-c", prefix+"-d"
	names := []string{a, b, c, d}
	config := func(pins ...string) string {
		refs := make([]string, len(pins))
		for i, p := range pins {
			refs[i] = testAccPinRef(p)
		}
		return testAccPinsConfig(org, names, org, refs...)
	}
	both := func(want ...string) resource.TestCheckFunc {
		return resource.ComposeAggregateTestCheckFunc(testAccCheckPinsState(want), testAccCheckPinsRemote(org, want))
	}
	update := plancheck.ExpectResourceAction(testAccPinsAddress, plancheck.ResourceActionUpdate)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			// Destroying the resource unpins everything.
			if err := testAccCheckPinsRemote(org, []string{})(s); err != nil {
				return err
			}
			return testAccCheckRepositoryDestroy(s)
		},
		Steps: []resource.TestStep{
			{
				Config: config(a, b),
				Check: resource.ComposeAggregateTestCheckFunc(
					both(a, b),
					resource.TestCheckResourceAttr(testAccPinsAddress, "id", strings.ToLower(org)),
					resource.TestCheckResourceAttr(testAccPinsAddress, "organization", org),
				),
			},
			{Config: config(a, b), PlanOnly: true},
			{
				// A new order is an in-place update.
				Config:           config(b, a),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{update}},
				Check:            both(b, a),
			},
			{
				Config:           config(b, c, d),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{update}},
				Check:            both(b, c, d),
			},
			{
				ResourceName:      testAccPinsAddress,
				ImportState:       true,
				ImportStateId:     org,
				ImportStateVerify: true,
			},
			{
				ResourceName:            testAccPinsAddress,
				ImportState:             true,
				ImportStateId:           strings.ToUpper(org),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"organization"},
			},
			{
				// A pin changed outside Terraform is drift, put back on apply.
				PreConfig:          func() { testAccSetOrganizationPins(t, org, []string{a}) },
				Config:             config(b, c, d),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: config(b, c, d), Check: both(b, c, d)},
			{
				// An empty list unpins everything.
				Config: config(),
				Check:  both(),
			},
			{Config: config(), PlanOnly: true},
			{
				// End with pins, so CheckDestroy proves destroy clears them.
				Config: config(d),
				Check:  both(d),
			},
		},
	})
}

func TestAccOrganizationPinnedRepositoriesResource_maxPrivateAndCase(t *testing.T) {
	org, _ := testAccPinsFixture(t)
	prefix := acctest.RandomWithPrefix("tf-acc-pin")
	names := make([]string, 6)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	// Not alphabetical, to prove Ghost keeps the order given.
	order := []string{names[3], names[0], names[5], names[1], names[4], names[2]}
	refs := make([]string, len(order))
	for i, n := range order {
		refs[i] = testAccPinRef(n)
	}
	private := prefix + "-private"
	config := func(organization string, pins ...string) string {
		return testAccPinsConfig(org, names, organization, pins...) + fmt.Sprintf(`
resource "ghost_repository" "private" {
  name       = %q
  owner      = %q
  visibility = "private"
}
`, private, org)
	}
	upper := strings.ToUpper(org)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(org, refs...),
				Check:  resource.ComposeAggregateTestCheckFunc(testAccCheckPinsState(order), testAccCheckPinsRemote(org, order)),
			},
			{Config: config(org, refs...), PlanOnly: true},
			{
				// A private repository can be pinned, and an admin reads it back.
				Config: config(org, "ghost_repository.private.slug", testAccPinRef(names[0])),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckPinsState([]string{private, names[0]}),
					testAccCheckPinsRemote(org, []string{private, names[0]}),
				),
			},
			{
				// Only the organization's case changes: an in-place update.
				Config: config(upper, "ghost_repository.private.slug", testAccPinRef(names[0])),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testAccPinsAddress, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccPinsAddress, "organization", upper),
					resource.TestCheckResourceAttr(testAccPinsAddress, "id", strings.ToLower(org)),
					testAccCheckPinsRemote(org, []string{private, names[0]}),
				),
			},
			{Config: config(upper, "ghost_repository.private.slug", testAccPinRef(names[0])), PlanOnly: true},
		},
	})
}

func TestAccOrganizationPinnedRepositoriesResource_renameAndDelete(t *testing.T) {
	org, _ := testAccPinsFixture(t)
	prefix := acctest.RandomWithPrefix("tf-acc-pin")
	keep, renamed := prefix+"-keep", prefix+"-renamed"
	config := func(name string, withGone bool) string {
		pins := `[ghost_repository.renamed.slug, ghost_repository.keep.slug]`
		gone := ""
		if withGone {
			gone = fmt.Sprintf(`
resource "ghost_repository" "gone" {
  name       = %q
  owner      = %q
  visibility = "public"
}
`, prefix+"-gone", org)
			pins = `[ghost_repository.renamed.slug, ghost_repository.keep.slug, ghost_repository.gone.slug]`
		}
		return fmt.Sprintf(`
resource "ghost_repository" "keep" {
  name       = %q
  owner      = %q
  visibility = "public"
}

resource "ghost_repository" "renamed" {
  name       = %q
  owner      = %q
  visibility = "public"
}
%s
resource "ghost_organization_pinned_repositories" "test" {
  organization = %q
  repositories = %s
}
`, keep, org, name, org, gone, org, pins)
	}
	gone := prefix + "-gone"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: config(prefix+"-old", true),
				Check:  testAccCheckPinsRemote(org, []string{prefix + "-old", keep, gone}),
			},
			{
				// Ghost unpins a deleted repository, so the next plan wants
				// it back. Deleting it with Terraform drops it from both.
				PreConfig: func() {
					c, err := testAccClient()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.DeleteRepository(context.Background(), org, gone); err != nil {
						t.Fatalf("deleting %s: %v", gone, err)
					}
				},
				Config:             config(prefix+"-old", true),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config(prefix+"-old", false),
				Check:  testAccCheckPinsRemote(org, []string{prefix + "-old", keep}),
			},
			{
				// A rename keeps the pin under the new slug, which the
				// reference picks up in the same apply.
				Config: config(renamed, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckPinsState([]string{renamed, keep}),
					testAccCheckPinsRemote(org, []string{renamed, keep}),
				),
			},
			{Config: config(renamed, false), PlanOnly: true},
		},
	})
}

func TestAccOrganizationPinnedRepositoriesResource_errors(t *testing.T) {
	org, before := testAccPinsFixture(t)
	me := testAccTokenLogin(t)
	missingOrg := acctest.RandomWithPrefix("tf-acc-missing")
	missingRepo := acctest.RandomWithPrefix("tf-acc-missing")
	repo := acctest.RandomWithPrefix("tf-acc-pin")
	// A different name, so the organization repository of the earlier
	// step, destroyed in the same apply, can't be the one found.
	userRepo := acctest.RandomWithPrefix("tf-acc-pin-user")

	plain := func(organization string, pins ...string) string {
		return fmt.Sprintf("resource \"ghost_organization_pinned_repositories\" \"test\" {\n  organization = %q\n  repositories = [%s]\n}\n",
			organization, strings.Join(pins, ", "))
	}
	orgError := func(o string) *regexp.Regexp {
		return testAccWrappedRegexp(fmt.Sprintf(`Ghost has no organization %q that you're an admin of`, o))
	}
	repoError := testAccWrappedRegexp("isn't a repository of")
	unchanged := testAccCheckPinsRemote(org, before)

	steps := []resource.TestStep{
		{Config: plain(missingOrg), ExpectError: orgError(missingOrg)},
		// A user's login isn't an organization.
		{Config: plain(me), ExpectError: orgError(me)},
		{Config: plain(org, fmt.Sprintf("%q", missingRepo)), ExpectError: repoError},
		{
			// One unknown slug rejects the whole list.
			Config:      testAccPinsConfig(org, []string{repo}, org, testAccPinRef(repo), fmt.Sprintf("%q", missingRepo)),
			ExpectError: repoError,
		},
		{
			// The upper-case name isn't the slug, which is caught at plan time.
			Config:      plain(org, fmt.Sprintf("%q", strings.ToUpper(repo))),
			ExpectError: regexp.MustCompile(`must be a repository slug`),
		},
		{
			// The user's own repository can't be pinned to the organization.
			Config:      plain(org, "ghost_repository.test.slug") + testAccRepositoryConfig(userRepo, `visibility = "public"`),
			ExpectError: repoError,
		},
		{
			ResourceName:  testAccPinsAddress,
			Config:        plain(org),
			ImportState:   true,
			ImportStateId: missingOrg,
			ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
		},
		{
			ResourceName:  testAccPinsAddress,
			Config:        plain(org),
			ImportState:   true,
			ImportStateId: org + "/x",
			ExpectError:   regexp.MustCompile(`Invalid import ID`),
		},
	}
	if foreign := os.Getenv("GHOST_TEST_FOREIGN_ORGANIZATION"); foreign != "" {
		steps = append(steps, resource.TestStep{Config: plain(foreign), ExpectError: orgError(foreign)})
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(s *terraform.State) error {
			if err := unchanged(s); err != nil {
				return err
			}
			return testAccCheckRepositoryDestroy(s)
		},
		Steps: steps,
	})
}
