package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const testPublicMembershipAddress = "ghost_organization_public_membership.test"

// testPublicMembershipServer is a stub Ghost whose token user is "bob".
// Organizations in public are ones bob is a member of, with his flag;
// "other" exists without bob, and anything else is unknown. putStatus and
// putBody, when set, answer every PUT and DELETE instead.
type testPublicMembershipServer struct {
	t         *testing.T
	putStatus int
	putBody   string

	mu      sync.Mutex
	public  map[string]bool
	methods []string
	url     string
}

func newTestPublicMembershipServer(t *testing.T, public map[string]bool) *testPublicMembershipServer {
	s := &testPublicMembershipServer{t: t, public: public}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.url = srv.URL

	return s
}

func (s *testPublicMembershipServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	notFound := func(org string) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"statusCode":404,"error":"Error","message":"Organization not found: %s"}`, org)
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/organizations/"), "/")
	org := strings.ToLower(parts[0])
	public, member := s.public[org]

	switch {
	case r.URL.Path == "/api/v3/user" && r.Method == http.MethodGet:
		_, _ = w.Write([]byte(`{"login":"bob","id":1}`))
	case len(parts) == 1 && r.Method == http.MethodGet:
		switch {
		case member:
			body, _ := json.Marshal(map[string]any{
				"slug": org, "viewerRole": "owner",
				"members": []map[string]any{{"username": "alice", "public": true}, {"username": "Bob", "public": public}},
			})
			_, _ = w.Write(body)
		case org == "other":
			_, _ = w.Write([]byte(`{"slug":"other","viewerRole":null,"members":[{"username":"carol","public":true}]}`))
		default:
			notFound(parts[0])
		}
	case len(parts) == 2 && parts[1] == "public-members" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		s.methods = append(s.methods, r.Method+" "+org)
		switch {
		case s.putStatus != 0:
			w.WriteHeader(s.putStatus)
			_, _ = w.Write([]byte(s.putBody))
		case !member:
			notFound(parts[0])
		default:
			s.public[org] = r.Method == http.MethodPut
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *testPublicMembershipServer) set(org string, public bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.public[org] = public
}

func (s *testPublicMembershipServer) leave(org string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.public, org)
}

func (s *testPublicMembershipServer) calls() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return strings.Join(s.methods, ", ")
}

// config returns a configuration for organization org; publicLine is the
// optional public argument, e.g. "public = false".
func (s *testPublicMembershipServer) config(org, publicLine string) string {
	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

resource "ghost_organization_public_membership" "test" {
  organization = %q
  %s
}
`, s.url, org, publicLine)
}

func TestOrganizationPublicMembershipConfigValidation(t *testing.T) {
	for _, tc := range []struct{ name, config, expect string }{
		{"no organization", `public = true`, `(?s)"organization" is required`},
		{"empty organization", `organization = ""`, `(?s)length must be at least 1`},
		{"slash", `organization = "a/b"`, `must not contain ":" or "/"`},
		{"username", "organization = \"acme\"\nusername = \"bob\"", `(?s)Invalid Configuration for Read-Only Attribute`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "resource \"ghost_organization_public_membership\" \"test\" {\n" + tc.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.expect),
				}},
			})
		})
	}
}

// Create makes the membership public by default, public = false updates it
// in place, a case-only change sends nothing, and destroy always makes it
// private.
func TestOrganizationPublicMembershipResourceLifecycle(t *testing.T) {
	s := newTestPublicMembershipServer(t, map[string]bool{"acme": false})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			want := "PUT acme, DELETE acme, PUT acme, DELETE acme"
			if got := s.calls(); got != want {
				return fmt.Errorf("calls = %q, want %q", got, want)
			}
			if s.public["acme"] {
				return fmt.Errorf("membership still public after destroy")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: s.config("acme", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testPublicMembershipAddress, "id", "acme"),
					resource.TestCheckResourceAttr(testPublicMembershipAddress, "organization", "acme"),
					resource.TestCheckResourceAttr(testPublicMembershipAddress, "public", "true"),
					resource.TestCheckResourceAttr(testPublicMembershipAddress, "username", "bob"),
				),
			},
			{
				Config: s.config("acme", "public = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testPublicMembershipAddress, plancheck.ResourceActionUpdate),
				}},
				Check: resource.TestCheckResourceAttr(testPublicMembershipAddress, "public", "false"),
			},
			{
				Config: s.config("ACME", "public = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testPublicMembershipAddress, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testPublicMembershipAddress, "id", "acme"),
					resource.TestCheckResourceAttr(testPublicMembershipAddress, "organization", "ACME"),
					resource.TestCheckResourceAttr(testPublicMembershipAddress, "public", "true"),
				),
			},
			{
				// Only the case changes: nothing is sent.
				Config: s.config("acme", "public = true"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testPublicMembershipAddress, plancheck.ResourceActionUpdate),
				}},
			},
			{
				ResourceName:      testPublicMembershipAddress,
				ImportState:       true,
				ImportStateId:     "ACME",
				ImportStateVerify: true,
				// The import keeps the ID's case.
				ImportStateVerifyIgnore: []string{"organization"},
			},
		},
	})
}

// Changes outside Terraform show up as drift, and leaving the organization
// drops the resource.
func TestOrganizationPublicMembershipResourceDrift(t *testing.T) {
	s := newTestPublicMembershipServer(t, map[string]bool{"acme": false})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: s.config("acme", "public = false")},
			{
				PreConfig:          func() { s.set("acme", true) },
				Config:             s.config("acme", "public = false"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: s.config("acme", "public = false"),
				Check: func(*terraform.State) error {
					if s.public["acme"] {
						return fmt.Errorf("apply didn't make the membership private again")
					}
					return nil
				},
			},
			{
				PreConfig: func() { s.leave("acme") },
				Config:    s.config("acme", "public = false"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testPublicMembershipAddress, plancheck.ResourceActionCreate),
				}},
				ExpectError: regexp.MustCompile(`Organization membership not found`),
			},
		},
	})
}

// Another organization replaces the resource, making the old membership
// private.
func TestOrganizationPublicMembershipResourceReplace(t *testing.T) {
	s := newTestPublicMembershipServer(t, map[string]bool{"acme": false, "beta": false})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: s.config("acme", "")},
			{
				Config: s.config("beta", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(testPublicMembershipAddress, plancheck.ResourceActionDestroyBeforeCreate),
				}},
				Check: func(*terraform.State) error {
					if want := "PUT acme, DELETE acme, PUT beta"; s.calls() != want {
						return fmt.Errorf("calls = %q, want %q", s.calls(), want)
					}
					return nil
				},
			},
		},
	})
}

// A 404 is explained as a non-membership or an unknown organization, and
// other errors name the user and organization.
func TestOrganizationPublicMembershipResourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name, org    string
		status       int
		body, expect string
	}{
		{"not a member", "other", 0, "",
			`(?s)Organization membership not found.*bob, the token's user, isn't a member of other`},
		{"unknown organization", "nope", 0, "",
			`(?s)Organization membership not found.*Organization nope not found.*Organization not found: nope`},
		{"forbidden", "acme", http.StatusForbidden, `{"statusCode":403,"error":"Error","message":"Personal access tokens cannot use this route; sign in instead"}`,
			`(?s)Error setting organization membership visibility.*Making bob's membership of acme public.*Personal\s+access\s+tokens`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestPublicMembershipServer(t, map[string]bool{"acme": false})
			s.putStatus, s.putBody = tc.status, tc.body
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      s.config(tc.org, ""),
					ExpectError: regexp.MustCompile(tc.expect),
				}},
			})
		})
	}
}

// Destroy ignores a 404: the organization is gone or the user left it.
func TestOrganizationPublicMembershipResourceDestroyAfterLeaving(t *testing.T) {
	s := newTestPublicMembershipServer(t, map[string]bool{"acme": false})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: s.config("acme", "")},
			{
				// Leave after the refresh, so destroy hits the 404.
				Config:  s.config("acme", ""),
				Destroy: true,
				PreConfig: func() {
					s.mu.Lock()
					s.putStatus, s.putBody = http.StatusNotFound, `{"statusCode":404,"error":"Error","message":"Organization not found: acme"}`
					s.mu.Unlock()
				},
			},
		},
	})
}

func TestOrganizationPublicMembershipImportID(t *testing.T) {
	s := newTestPublicMembershipServer(t, map[string]bool{"acme": true})
	for _, id := range []string{"a/b", "a:b"} {
		t.Run(id, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:        s.config("acme", ""),
					ResourceName:  testPublicMembershipAddress,
					ImportState:   true,
					ImportStateId: id,
					ExpectError:   regexp.MustCompile(`Expected an organization slug`),
				}},
			})
		})
	}
}
