package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

const testAccSubscriptionAddress = "ghost_repository_subscription.test"

func TestRepositorySubscriptionConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"bad level", `repository = "acme/repo"` + "\n" + `level = "watching"`, `(?s)value must be one of.*"all".*"ignore".*"participating"`},
		{"level case", `repository = "acme/repo"` + "\n" + `level = "All"`, `(?s)value must be one of`},
		{"empty level", `repository = "acme/repo"` + "\n" + `level = ""`, `(?s)value must be one of`},
		{"no level", `repository = "acme/repo"`, `(?s)"level" is required`},
		{"no repository", `level = "all"`, `(?s)"repository" is required`},
		{"bad repository", `repository = "repo"` + "\n" + `level = "all"`, `must be "owner/slug"`},
		{"nested repository", `repository = "a/b/c"` + "\n" + `level = "all"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "resource \"ghost_repository_subscription\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testSubscriptionServer is a stub Ghost with one repository, acme/repo (id
// 7), whose subscription PUT answers with putStatus and putBody unless
// putStatus is 0, in which case it stores the level. It records every PUT
// body.
type testSubscriptionServer struct {
	t         *testing.T
	putStatus int
	putBody   string

	mu    sync.Mutex
	level string
	puts  []string
}

func (s *testSubscriptionServer) config(level string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v3/repos/acme/repo" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":7,"name":"repo","full_name":"acme/repo","owner":{"login":"acme"}}`))
		case r.URL.Path == "/api/repositories/acme/repo/subscription" && r.Method == http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"level":%q}`, s.level)
		case r.URL.Path == "/api/repositories/acme/repo/subscription" && r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			s.puts = append(s.puts, strings.TrimSpace(string(body)))
			if s.putStatus != 0 {
				w.WriteHeader(s.putStatus)
				_, _ = w.Write([]byte(s.putBody))
				return
			}
			var level string
			_, _ = fmt.Sscanf(strings.TrimSpace(string(body)), `{"level":%q}`, &level)
			s.level = level
			_, _ = w.Write(body)
		default:
			s.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"Repository not found"}`))
		}
	}))
	s.t.Cleanup(srv.Close)

	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

resource "ghost_repository_subscription" "test" {
  repository = "acme/repo"
  level      = %q
}
`, srv.URL, level)
}

// Create sends the level, and destroy sets it back to participating.
func TestRepositorySubscriptionResourceLifecycle(t *testing.T) {
	s := &testSubscriptionServer{t: t, level: client.WatchParticipating}
	config := s.config("ignore")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if want := []string{`{"level":"ignore"}`, `{"level":"participating"}`}; strings.Join(s.puts, " ") != strings.Join(want, " ") {
				return fmt.Errorf("PUT bodies = %v, want %v", s.puts, want)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: config,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(testAccSubscriptionAddress, "id", "acme/repo"),
				resource.TestCheckResourceAttr(testAccSubscriptionAddress, "repository_id", "7"),
				resource.TestCheckResourceAttr(testAccSubscriptionAddress, "level", "ignore"),
			),
		}},
	})
}

// Ghost's PUT errors are reported with the repository and level.
func TestRepositorySubscriptionResourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		expect string
	}{
		{"not found", http.StatusNotFound, `{"statusCode":404,"error":"Error","message":"Repository not found"}`,
			`(?s)Repository not found.*acme/repo not found, or the token's user can't see it.*Repository not found`},
		{"bad request", http.StatusBadRequest, `{"message":["level must be one of the following values: participating, all, ignore"],"error":"Bad Request","statusCode":400}`,
			`(?s)Error setting the repository subscription.*watch level of acme/repo to "all".*level must be one of`},
		{"forbidden", http.StatusForbidden, `{"statusCode":403,"error":"Error","message":"Personal access tokens cannot use this route; sign in instead"}`,
			`(?s)Error setting the repository subscription.*Personal\s+access\s+tokens\s+cannot\s+use\s+this\s+route`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &testSubscriptionServer{t: t, level: client.WatchParticipating, putStatus: tc.status, putBody: tc.body}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      s.config("all"),
					ExpectError: regexp.MustCompile(tc.expect),
				}},
			})
		})
	}
}

func TestAccRepositorySubscriptionResource(t *testing.T) {
	owner := testAccSubscriptionFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"
	fullName := owner + "/" + repoName
	sameRepoID := statecheck.CompareValue(compare.ValuesSame())
	update := resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSubscriptionAddress, plancheck.ResourceActionUpdate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				// A new repository starts at participating.
				Config: testAccRepositoryConfig(repoName, ""),
				Check:  testAccCheckSubscriptionLevel(fullName, client.WatchParticipating),
			},
			{
				Config: testAccRepositorySubscriptionConfig(repoName, "all"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						testAccSubscriptionAddress, tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
					sameRepoID.AddStateValue(testAccSubscriptionAddress, tfjsonpath.New("repository_id")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "id", fullName),
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "repository", fullName),
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "level", "all"),
					testAccCheckSubscriptionLevel(fullName, client.WatchAll),
				),
			},
			{
				ResourceName:      testAccSubscriptionAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Ghost stores ignore and reads it back.
				Config:           testAccRepositorySubscriptionConfig(repoName, "ignore"),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "level", "ignore"),
					testAccCheckSubscriptionLevel(fullName, client.WatchIgnore),
				),
			},
			{
				// participating can be managed explicitly too.
				Config:           testAccRepositorySubscriptionConfig(repoName, "participating"),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "level", "participating"),
					testAccCheckSubscriptionLevel(fullName, client.WatchParticipating),
				),
			},
			{
				// A level changed outside Terraform is set back.
				PreConfig:        func() { testAccSetSubscriptionLevel(t, fullName, client.WatchAll) },
				Config:           testAccRepositorySubscriptionConfig(repoName, "participating"),
				ConfigPlanChecks: update,
				Check:            testAccCheckSubscriptionLevel(fullName, client.WatchParticipating),
			},
			{
				PreConfig:        func() { testAccSetSubscriptionLevel(t, fullName, client.WatchIgnore) },
				Config:           testAccRepositorySubscriptionConfig(repoName, "all"),
				ConfigPlanChecks: update,
				Check:            testAccCheckSubscriptionLevel(fullName, client.WatchAll),
			},
			{
				// Renaming the repository keeps the level, under the new
				// name.
				Config:           testAccRepositorySubscriptionConfig(renamed, "all"),
				ConfigPlanChecks: update,
				ConfigStateChecks: []statecheck.StateCheck{
					sameRepoID.AddStateValue(testAccSubscriptionAddress, tfjsonpath.New("repository_id")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "id", owner+"/"+renamed),
					testAccCheckSubscriptionLevel(owner+"/"+renamed, client.WatchAll),
				),
			},
			{
				// Destroying the subscription sets the level back to
				// participating; the repository has to outlive it to check.
				Config: testAccRepositoryConfig(renamed, ""),
				Check:  testAccCheckSubscriptionLevel(owner+"/"+renamed, client.WatchParticipating),
			},
		},
	})
}

// Pointing the subscription at another repository replaces it, which sets
// the old repository back to participating; replacing the repository itself
// drops its level.
func TestAccRepositorySubscriptionResource_repositoryReplaced(t *testing.T) {
	owner := testAccSubscriptionFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	otherName := repoName + "-other"
	config := func(license, repository string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("license_template = %q", license)) + fmt.Sprintf(`
resource "ghost_repository" "other" {
  name = %q
}

resource "ghost_repository_subscription" "test" {
  repository = %s.full_name
  level      = "all"

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }
}
`, otherName, repository)
	}
	samePair := func(repo string) statecheck.StateCheck {
		return statecheck.CompareValuePairs(
			testAccSubscriptionAddress, tfjsonpath.New("repository_id"),
			repo, tfjsonpath.New("repo_id"),
			compare.ValuesSame(),
		)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: config("mit", "ghost_repository.test")},
			{
				// A new repository under the same name starts at
				// participating; replace_triggered_by watches it again.
				Config: config("apache-2.0", "ghost_repository.test"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(testAccSubscriptionAddress, plancheck.ResourceActionReplace),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{samePair("ghost_repository.test")},
				Check:             testAccCheckSubscriptionLevel(owner+"/"+repoName, client.WatchAll),
			},
			{
				Config: config("apache-2.0", "ghost_repository.other"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSubscriptionAddress, plancheck.ResourceActionReplace)},
				},
				ConfigStateChecks: []statecheck.StateCheck{samePair("ghost_repository.other")},
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckSubscriptionLevel(owner+"/"+otherName, client.WatchAll),
					testAccCheckSubscriptionLevel(owner+"/"+repoName, client.WatchParticipating),
				),
			},
		},
	})
}

// A repository deleted outside Terraform drops the subscription from state.
func TestAccRepositorySubscriptionResource_repositoryDeleted(t *testing.T) {
	owner := testAccSubscriptionFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: testAccRepositorySubscriptionConfig(repoName, "ignore")},
			{
				PreConfig: func() {
					c, err := testAccClient()
					if err != nil {
						t.Fatal(err)
					}
					if err := c.DeleteRepository(context.Background(), owner, repoName); err != nil {
						t.Fatalf("deleting %s/%s: %v", owner, repoName, err)
					}
				},
				Config: testAccRepositorySubscriptionConfig(repoName, "ignore"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionCreate),
						plancheck.ExpectResourceAction(testAccSubscriptionAddress, plancheck.ResourceActionCreate),
					},
				},
				// A new repository under the old name starts at
				// participating, so the level is sent again.
				Check: testAccCheckSubscriptionLevel(owner+"/"+repoName, client.WatchIgnore),
			},
		},
	})
}

// Organization repositories, private ones included, work the same.
func TestAccRepositorySubscriptionResource_organization(t *testing.T) {
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION to test subscriptions to organization repositories")
	}
	repoName := acctest.RandomWithPrefix("tf-acc")
	fullName := org + "/" + repoName
	extra := fmt.Sprintf("owner = %q\n  visibility = \"private\"", org)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRepositoryConfig(repoName, extra) + `
resource "ghost_repository_subscription" "test" {
  repository = ghost_repository.test.full_name
  level      = "all"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "id", fullName),
					testAccCheckSubscriptionLevel(fullName, client.WatchAll),
				),
			},
			{
				ResourceName:      testAccSubscriptionAddress,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: testAccRepositoryConfig(repoName, extra),
				Check:  testAccCheckSubscriptionLevel(fullName, client.WatchParticipating),
			},
		},
	})
}

// Someone else's public repository can be watched, and is set back on
// destroy.
func TestAccRepositorySubscriptionResource_foreign(t *testing.T) {
	foreign := os.Getenv("GHOST_TEST_FOREIGN_REPOSITORY")
	if foreign == "" {
		t.Skip("set GHOST_TEST_FOREIGN_REPOSITORY to another owner's public repository, e.g. ghost/ghost, to test watching it")
	}
	testAccPreCheck(t)
	if level := testAccSubscriptionLevel(t, foreign); level != client.WatchParticipating {
		t.Skipf("the token's user already has level %q on %s; the test would reset it", level, foreign)
	}
	config := fmt.Sprintf(`
resource "ghost_repository_subscription" "test" {
  repository = %q
  level      = "all"
}
`, foreign)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			return testAccCheckSubscriptionLevel(foreign, client.WatchParticipating)(nil)
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "id", foreign),
					resource.TestCheckResourceAttrSet(testAccSubscriptionAddress, "repository_id"),
					testAccCheckSubscriptionLevel(foreign, client.WatchAll),
				),
			},
		},
	})
}

// TestAccRepositorySubscriptionResource_errors covers what only Ghost can
// check, and how it matches names.
func TestAccRepositorySubscriptionResource_errors(t *testing.T) {
	owner := testAccSubscriptionFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	repo := testAccRepositoryConfig(repoName, "")
	block := func(repository string) string {
		return repo + fmt.Sprintf(`
resource "ghost_repository_subscription" "test" {
  repository = %q
  level      = "all"
}
`, repository)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{
			{Config: repo},
			{
				Config:      block(owner + "/" + missing),
				ExpectError: wrappedPattern(`Repository ` + owner + `/` + missing + ` not found, or the token's user can't see it`),
			},
			{
				// Owners and slugs are matched case-insensitively, and the
				// configured spelling is kept.
				Config: block(strings.ToUpper(owner + "/" + repoName)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccSubscriptionAddress, "id", strings.ToUpper(owner+"/"+repoName)),
					testAccCheckSubscriptionLevel(owner+"/"+repoName, client.WatchAll),
				),
			},
			{
				// Changing only the case is the same repository, so it's
				// updated in place.
				Config: block(owner + "/" + repoName),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccSubscriptionAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.TestCheckResourceAttr(testAccSubscriptionAddress, "id", owner+"/"+repoName),
			},
			{
				Config:        block(owner + "/" + repoName),
				ResourceName:  testAccSubscriptionAddress,
				ImportState:   true,
				ImportStateId: owner + "/" + missing,
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
			{
				Config:        block(owner + "/" + repoName),
				ResourceName:  testAccSubscriptionAddress,
				ImportState:   true,
				ImportStateId: repoName,
				ExpectError:   testAccWrappedRegexp(`import ID must be the repository's full name, "owner/slug"`),
			},
			{Config: repo},
		},
	})
}

// testAccSubscriptionFixture skips the test unless TF_ACC is set, and returns
// the login of the token's user.
func testAccSubscriptionFixture(t *testing.T) string {
	t.Helper()

	if os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set TF_ACC to run acceptance tests")
	}
	testAccPreCheck(t)

	return testAccTokenLogin(t)
}

// testAccRepositorySubscriptionConfig is a repository and a
// ghost_repository_subscription on it.
func testAccRepositorySubscriptionConfig(repoName, level string) string {
	return testAccRepositoryConfig(repoName, "") + fmt.Sprintf(`
resource "ghost_repository_subscription" "test" {
  repository = ghost_repository.test.full_name
  level      = %q
}
`, level)
}

// testAccSubscriptionLevelErr reads the token user's level on fullName
// straight from the API.
func testAccSubscriptionLevelErr(fullName string) (string, error) {
	c, err := testAccClient()
	if err != nil {
		return "", err
	}
	owner, repo, err := client.SplitFullName(fullName)
	if err != nil {
		return "", err
	}
	s, err := c.GetRepositorySubscription(context.Background(), owner, repo)
	if err != nil {
		return "", fmt.Errorf("reading the subscription to %s: %w", fullName, err)
	}

	return s.Level, nil
}

func testAccSubscriptionLevel(t *testing.T, fullName string) string {
	t.Helper()

	level, err := testAccSubscriptionLevelErr(fullName)
	if err != nil {
		t.Fatal(err)
	}

	return level
}

// testAccCheckSubscriptionLevel verifies, straight from the API, the token
// user's level on fullName.
func testAccCheckSubscriptionLevel(fullName, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := testAccSubscriptionLevelErr(fullName)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("level on %s = %q, want %q", fullName, got, want)
		}

		return nil
	}
}

// testAccSetSubscriptionLevel sets the token user's level on fullName
// outside Terraform.
func testAccSetSubscriptionLevel(t *testing.T, fullName, level string) {
	t.Helper()

	c, err := testAccClient()
	if err != nil {
		t.Fatal(err)
	}
	owner, repo, _ := client.SplitFullName(fullName)
	if _, err := c.SetRepositorySubscription(context.Background(), owner, repo, level); err != nil {
		t.Fatalf("setting the level on %s to %s: %v", fullName, level, err)
	}
}
