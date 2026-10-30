package provider

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// testAccPublicMembershipFixture skips the test unless
// GHOST_TEST_ORGANIZATION and TF_ACC are set, and returns the organization
// and the token's user, who must be a member of it. The membership is put
// back the way it was when the test ends.
func testAccPublicMembershipFixture(t *testing.T) (org, me string) {
	t.Helper()

	org = os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test ghost_organization_public_membership")
	}
	testAccPreCheck(t)
	me = testAccTokenLogin(t)

	public := testAccLivePublic(t, org, me)
	t.Cleanup(func() {
		testAccSetPublicMembership(t, org, public)
		if got := testAccLivePublic(t, org, me); got != public {
			t.Errorf("membership of %s is public=%t after the test, want %t", org, got, public)
		}
	})

	return org, me
}

// testAccLivePublic reads, straight from the API, whether login's
// membership of org is public.
func testAccLivePublic(t *testing.T, org, login string) bool {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.GetOrganization(context.Background(), org)
	if err != nil {
		t.Fatal(err)
	}

	return testAccMemberPublic(t, o, login)
}

// testAccCheckMembershipPublic checks, straight from the API, that login's
// membership of org is public or private as want says.
func testAccCheckMembershipPublic(t *testing.T, org, login string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := testAccLivePublic(t, org, login); got != want {
			return fmt.Errorf("%s's membership of %s is public=%t, want %t", login, org, got, want)
		}
		return nil
	}
}

func testAccPublicMembershipConfig(org, publicLine string) string {
	return fmt.Sprintf(`
resource "ghost_organization_public_membership" "test" {
  organization = %q
  %s
}
`, org, publicLine)
}

func TestAccOrganizationPublicMembershipResource(t *testing.T) {
	org, me := testAccPublicMembershipFixture(t)
	testAccSetPublicMembership(t, org, false)
	const addr = testPublicMembershipAddress

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckMembershipPublic(t, org, me, false),
		Steps: []resource.TestStep{
			{
				Config: testAccPublicMembershipConfig(org, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", strings.ToLower(org)),
					resource.TestCheckResourceAttr(addr, "organization", org),
					resource.TestCheckResourceAttr(addr, "public", "true"),
					resource.TestCheckResourceAttr(addr, "username", me),
					testAccCheckMembershipPublic(t, org, me, true),
				),
			},
			{Config: testAccPublicMembershipConfig(org, ""), PlanOnly: true},
			{
				Config: testAccPublicMembershipConfig(org, "public = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "public", "false"),
					testAccCheckMembershipPublic(t, org, me, false),
				),
			},
			{
				Config: testAccPublicMembershipConfig(org, "public = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: testAccCheckMembershipPublic(t, org, me, true),
			},
			{
				// A case-only change is in place.
				Config: testAccPublicMembershipConfig(strings.ToUpper(org), "public = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", strings.ToLower(org)),
					resource.TestCheckResourceAttr(addr, "organization", strings.ToUpper(org)),
					testAccCheckMembershipPublic(t, org, me, true),
				),
			},
			{Config: testAccPublicMembershipConfig(strings.ToUpper(org), "public = true"), PlanOnly: true},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateId:           org,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"organization"},
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateId:           strings.ToUpper(org),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"organization"},
			},
		},
	})
}

// Destroying the resource makes the membership private, even when it was
// private before Terraform.
func TestAccOrganizationPublicMembershipResource_destroy(t *testing.T) {
	org, me := testAccPublicMembershipFixture(t)
	testAccSetPublicMembership(t, org, false)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccPublicMembershipConfig(org, ""),
				Check:  testAccCheckMembershipPublic(t, org, me, true),
			},
			{
				Config: `locals { removed = true }`,
				Check:  testAccCheckMembershipPublic(t, org, me, false),
			},
		},
	})
}

// A membership flipped outside Terraform shows up as drift and is put back,
// in both directions.
func TestAccOrganizationPublicMembershipResource_drift(t *testing.T) {
	org, me := testAccPublicMembershipFixture(t)
	const addr = testPublicMembershipAddress

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckMembershipPublic(t, org, me, false),
		Steps: []resource.TestStep{
			{Config: testAccPublicMembershipConfig(org, "public = true")},
			{
				PreConfig: func() { testAccSetPublicMembership(t, org, false) },
				Config:    testAccPublicMembershipConfig(org, "public = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				}},
				Check: testAccCheckMembershipPublic(t, org, me, true),
			},
			{Config: testAccPublicMembershipConfig(org, "public = false")},
			{
				PreConfig:          func() { testAccSetPublicMembership(t, org, true) },
				Config:             testAccPublicMembershipConfig(org, "public = false"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: testAccPublicMembershipConfig(org, "public = false"),
				Check:  testAccCheckMembershipPublic(t, org, me, false),
			},
		},
	})
}

// Both calls are idempotent: setting the flag it already has succeeds, and
// creating the resource with the current value works.
func TestAccOrganizationPublicMembershipResource_idempotent(t *testing.T) {
	org, me := testAccPublicMembershipFixture(t)
	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	for _, public := range []bool{true, true, false, false} {
		if err := c.SetOrganizationMembershipPublic(context.Background(), org, public); err != nil {
			t.Fatalf("setting public=%t again: %v", public, err)
		}
		if got := testAccLivePublic(t, org, me); got != public {
			t.Fatalf("public=%t after setting %t", got, public)
		}
	}

	// Already private.
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckMembershipPublic(t, org, me, false),
		Steps: []resource.TestStep{
			{
				Config: testAccPublicMembershipConfig(org, "public = false"),
				Check:  testAccCheckMembershipPublic(t, org, me, false),
			},
			{Config: testAccPublicMembershipConfig(org, "public = false"), PlanOnly: true},
		},
	})
}

// An organization the token's user isn't in is refused with an
// explanation, and its members are left alone.
func TestAccOrganizationPublicMembershipResource_nonMember(t *testing.T) {
	foreign := os.Getenv("GHOST_TEST_FOREIGN_ORGANIZATION")
	if foreign == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_FOREIGN_ORGANIZATION (an organization the token's user isn't in) and TF_ACC to test ghost_organization_public_membership outside an organization")
	}
	testAccPreCheck(t)
	me := testAccTokenLogin(t)
	members := func() []string {
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
		out := make([]string, 0, len(o.Members))
		for _, m := range o.Members {
			out = append(out, fmt.Sprintf("%s:%t", m.Username, m.Public))
		}
		return out
	}
	before := members()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccPublicMembershipConfig(foreign, ""),
			ExpectError: wrappedPattern(fmt.Sprintf(`Organization membership not found.*%s, the token's user, isn't a member of %s`, me, foreign)),
		}},
	})

	if after := members(); !slices.Equal(before, after) {
		t.Errorf("members of %s changed from %v to %v", foreign, before, after)
	}
}

func TestAccOrganizationPublicMembershipResource_unknownOrganization(t *testing.T) {
	if os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set TF_ACC to run acceptance tests")
	}
	testAccPreCheck(t)
	org := acctest.RandomWithPrefix("tf-acc-no-such-org")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      testAccPublicMembershipConfig(org, ""),
			ExpectError: wrappedPattern(fmt.Sprintf(`Organization membership not found.*Organization %s not found`, org)),
		}},
	})
}
