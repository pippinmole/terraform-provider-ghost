package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
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

// Organization webhooks have no name, so each test marks its URLs with a
// tf-acc query string. If a run aborts, delete the hooks of
// GET /api/organizations/<org>/webhooks whose URL contains "tf-acc".
const testAccOrgWebhookAddress = "ghost_organization_webhook.test"

func TestParseOrganizationWebhookID(t *testing.T) {
	org, id, err := parseOrganizationWebhookID("pippin:whk_abc")
	if err != nil || org != "pippin" || id != "whk_abc" {
		t.Errorf("got %q, %q, %v", org, id, err)
	}
	if got := organizationWebhookID("PipPin", "whk_abc"); got != "pippin:whk_abc" {
		t.Errorf("organizationWebhookID = %q", got)
	}

	for _, bad := range []string{"", "pippin", "pippin:", ":whk_1", "pippin:a:b", "a/b:whk_1", "pippin:a/b"} {
		if _, _, err := parseOrganizationWebhookID(bad); err == nil {
			t.Errorf("parseOrganizationWebhookID(%q) should fail", bad)
		}
	}
}

// TestOrganizationWebhookConfigValidation runs offline: the attribute
// validators reject the config before the provider is ever configured.
func TestOrganizationWebhookConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"empty organization", `organization = ""`, `(?i)at least 1`},
		{"slash in organization", `organization = "a/b"`, `must not contain ":" or "/"`},
		{"ftp url", `url = "ftp://example.com/"`, `https:// or http://`},
		{"userinfo", `url = "https://u:p@example.com/"`, `username or password`},
		{"too long", `url = "https://example.com/` + strings.Repeat("x", webhookURLMaxLength) + `"`, `at most 2048`},
		{"no events", `events = []`, `(?i)at least 1`},
		{"unknown event", `events = ["pushh"]`, `(?s)value must be one of.*"push"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := map[string]string{"organization": `"acme"`, "url": `"https://example.com/"`, "events": `["push"]`}
			key, _, _ := strings.Cut(tt.config, " ")
			delete(attrs, key)
			config := "resource \"ghost_organization_webhook\" \"test\" {\n  " + tt.config + "\n"
			for k, v := range attrs {
				config += "  " + k + " = " + v + "\n"
			}
			config += "}\n"

			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      config,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestAccOrganizationWebhookResource(t *testing.T) {
	org, url1, url2 := testAccOrganizationWebhookFixture(t)
	sameID := statecheck.CompareValue(compare.ValuesSame())
	sameSecret := statecheck.CompareValue(compare.ValuesSame())
	same := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameID.AddStateValue(testAccOrgWebhookAddress, tfjsonpath.New("webhook_id")),
			sameSecret.AddStateValue(testAccOrgWebhookAddress, tfjsonpath.New("secret")),
		}
	}
	update := resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccOrgWebhookAddress, plancheck.ResourceActionUpdate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationWebhookDestroy,
		Steps: []resource.TestStep{
			{
				// Every argument.
				Config: testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url             = %q
  events          = ["issue.opened", "label.created"]
  active          = true
  secret_rotation = "1"`, url1)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccOrgWebhookAddress, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: same(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(testAccOrgWebhookAddress, "webhook_id", regexp.MustCompile(`^whk_`)),
					resource.TestMatchResourceAttr(testAccOrgWebhookAddress, "id", regexp.MustCompile(`^`+regexp.QuoteMeta(strings.ToLower(org))+`:whk_`)),
					resource.TestCheckResourceAttr(testAccOrgWebhookAddress, "organization", org),
					resource.TestMatchResourceAttr(testAccOrgWebhookAddress, "secret", regexp.MustCompile(`^whsec_.{32}$`)),
					resource.TestCheckResourceAttr(testAccOrgWebhookAddress, "url", url1),
					resource.TestCheckResourceAttr(testAccOrgWebhookAddress, "events.#", "2"),
					resource.TestCheckTypeSetElemAttr(testAccOrgWebhookAddress, "events.*", "label.created"),
					resource.TestCheckResourceAttr(testAccOrgWebhookAddress, "active", "true"),
					resource.TestCheckNoResourceAttr(testAccOrgWebhookAddress, "disabled_reason"),
					resource.TestCheckResourceAttrSet(testAccOrgWebhookAddress, "created_at"),
					testAccCheckOrganizationWebhookRemote(url1, true, "issue.opened", "label.created"),
				),
			},
			{
				ResourceName:      testAccOrgWebhookAddress,
				ImportState:       true,
				ImportStateIdFunc: testAccOrganizationWebhookImportID(strings.ToLower),
				ImportStateVerify: true,
				// Ghost never returns the secret again.
				ImportStateVerifyIgnore: []string{"secret", "secret_rotation"},
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if s, ok := states[0].Attributes["secret"]; ok && s != "" {
						return fmt.Errorf("imported secret should be null")
					}
					return nil
				},
			},
			{
				// The slug is case-insensitive; the ID is lower case.
				ResourceName:            testAccOrgWebhookAddress,
				ImportState:             true,
				ImportStateIdFunc:       testAccOrganizationWebhookImportID(strings.ToUpper),
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret", "secret_rotation", "organization"},
			},
			{
				ResourceName:  testAccOrgWebhookAddress,
				ImportState:   true,
				ImportStateId: "nope",
				ExpectError:   regexp.MustCompile(`import ID must be "organization:webhook_id"`),
			},
			{
				ResourceName:  testAccOrgWebhookAddress,
				ImportState:   true,
				ImportStateId: org + "/repo:whk_1",
				ExpectError:   regexp.MustCompile(`import ID must be "organization:webhook_id"`),
			},
			{
				ResourceName:  testAccOrgWebhookAddress,
				ImportState:   true,
				ImportStateId: org + ":whk_tfaccdoesnotexist",
				ExpectError:   regexp.MustCompile(`Cannot import non-existent remote object`),
			},
			{
				// URL and events change in place; a duplicate event is
				// harmless in a set.
				Config: testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url             = %q
  events          = ["issue.opened", "release.published", "release.published"]
  secret_rotation = "1"`, url2)),
				ConfigPlanChecks:  update,
				ConfigStateChecks: same(),
				Check:             testAccCheckOrganizationWebhookRemote(url2, true, "issue.opened", "release.published"),
			},
			{
				Config: testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url             = %q
  events          = ["issue.opened", "release.published"]
  active          = false
  secret_rotation = "1"`, url2)),
				ConfigPlanChecks:  update,
				ConfigStateChecks: same(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(testAccOrgWebhookAddress, "disabled_reason"),
					testAccCheckOrganizationWebhookRemote(url2, false, "issue.opened", "release.published"),
				),
			},
			{
				// Removing active turns it back on (the default).
				Config: testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url    = %q
  events = ["issue.opened", "release.published"]`, url2)),
				ConfigPlanChecks:  update,
				ConfigStateChecks: same(),
				Check:             testAccCheckOrganizationWebhookRemote(url2, true, "issue.opened", "release.published"),
			},
			{
				// A change of case only is an update, not a new webhook.
				Config: testAccOrganizationWebhookConfig(strings.ToUpper(org), fmt.Sprintf(`
  url    = %q
  events = ["issue.opened", "release.published"]`, url2)),
				ConfigPlanChecks:  update,
				ConfigStateChecks: same(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccOrgWebhookAddress, "organization", strings.ToUpper(org)),
					resource.TestMatchResourceAttr(testAccOrgWebhookAddress, "id", regexp.MustCompile(`^`+regexp.QuoteMeta(strings.ToLower(org))+`:whk_`)),
					testAccCheckOrganizationWebhookRemote(url2, true, "issue.opened", "release.published"),
				),
			},
		},
	})
}

func TestAccOrganizationWebhookResource_inactiveAtCreate(t *testing.T) {
	org, url1, _ := testAccOrganizationWebhookFixture(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationWebhookDestroy,
		Steps: []resource.TestStep{{
			// Ghost ignores active on create, so Create turns it off after.
			Config: testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url    = %q
  events = ["push"]
  active = false`, url1)),
			ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(testAccOrgWebhookAddress, "active", "false"),
				resource.TestCheckResourceAttrSet(testAccOrgWebhookAddress, "secret"),
				testAccCheckOrganizationWebhookRemote(url1, false, "push"),
			),
		}},
	})
}

// TestAccOrganizationWebhookResource_secret proves the secret in state signs
// real deliveries, before and after rotation, and that the webhook receives
// the events of the organization's repositories.
func TestAccOrganizationWebhookResource_secret(t *testing.T) {
	org, url1, _ := testAccOrganizationWebhookFixture(t)
	repoName := acctest.RandomWithPrefix("tf-acc")
	var oldSecret string
	config := func(rotation, extra string) string {
		return testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url             = %q
  events          = ["label.created"]
  secret_rotation = %q`, url1, rotation)) + extra
	}
	label := testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q", org)) + `
resource "ghost_issue_label" "trigger" {
  repository = ghost_repository.test.full_name
  name       = "tf-acc-trigger"
  depends_on = [ghost_organization_webhook.test]
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationWebhookDestroy,
		Steps: []resource.TestStep{
			{
				Config: config("a", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckOrganizationWebhookDelivery("ping", true, nil),
					func(s *terraform.State) error {
						oldSecret = s.RootModule().Resources[testAccOrgWebhookAddress].Primary.Attributes["secret"]
						return nil
					},
				),
			},
			{
				// Changing secret_rotation replaces the secret in place.
				Config: config("b", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccOrgWebhookAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(testAccOrgWebhookAddress, "secret", regexp.MustCompile(`^whsec_`)),
					func(s *terraform.State) error {
						if s.RootModule().Resources[testAccOrgWebhookAddress].Primary.Attributes["secret"] == oldSecret {
							return fmt.Errorf("secret was not rotated")
						}
						return nil
					},
				),
			},
			{
				// A label on one of the organization's repositories is
				// delivered, signed with the new secret only.
				Config: config("b", label),
				Check:  testAccCheckOrganizationWebhookDelivery("label.created", true, &oldSecret),
			},
			{
				// Removing secret_rotation keeps the secret.
				Config: testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url    = %q
  events = ["label.created"]`, url1)) + label,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccOrgWebhookAddress, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(testAccOrgWebhookAddress, tfjsonpath.New("secret"), knownNonEmptyString{}),
					},
				},
			},
		},
	})
}

func TestAccOrganizationWebhookResource_drift(t *testing.T) {
	org, url1, url2 := testAccOrganizationWebhookFixture(t)
	config := testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url    = %q
  events = ["push", "issue.opened"]`, url1))
	hooks := func(t *testing.T) *client.Webhooks {
		c, err := testAccClient()
		if err != nil {
			t.Fatal(err)
		}
		return c.OrganizationWebhooks(org)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationWebhookDestroy,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Changed outside Terraform: put back in place.
				PreConfig: func() {
					off := false
					u := url2
					h := hooks(t)
					for _, hook := range testAccMarkedOrganizationWebhooks(t, h, url1) {
						if _, err := h.Update(context.Background(), hook.ID, client.UpdateWebhookRequest{URL: &u, Events: []string{"star.created"}, Active: &off}); err != nil {
							t.Fatalf("updating webhook: %v", err)
						}
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccOrgWebhookAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckOrganizationWebhookRemote(url1, true, "push", "issue.opened"),
			},
			{
				// Deleted outside Terraform: created again.
				PreConfig: func() {
					h := hooks(t)
					for _, hook := range testAccMarkedOrganizationWebhooks(t, h, url1) {
						if err := h.Delete(context.Background(), hook.ID); err != nil {
							t.Fatalf("deleting webhook: %v", err)
						}
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccOrgWebhookAddress, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckOrganizationWebhookRemote(url1, true, "push", "issue.opened"),
			},
		},
	})
}

// TestAccOrganizationWebhookResource_errors covers what only Ghost can check.
func TestAccOrganizationWebhookResource_errors(t *testing.T) {
	org, url1, _ := testAccOrganizationWebhookFixture(t)
	missing := acctest.RandomWithPrefix("tf-acc-missing")
	config := func(org, url string) string {
		return testAccOrganizationWebhookConfig(org, fmt.Sprintf(`
  url    = %q
  events = ["push"]`, url))
	}
	login := testAccTokenLogin(t)
	notFound := func(org string) *regexp.Regexp {
		return testAccWrappedRegexp(fmt.Sprintf(`Ghost has no organization %q that you're a member of`, org))
	}

	steps := []resource.TestStep{
		{Config: config(org, "https://localhost/hook"), ExpectError: regexp.MustCompile(`private or reserved address`)},
		{Config: config(org, "https://10.0.0.1/hook"), ExpectError: regexp.MustCompile(`private or reserved address`)},
		{Config: config(org, "https://tf-acc-nonexistent.invalid/"), ExpectError: regexp.MustCompile(`does not resolve`)},
		{Config: config(org, url1)},
		{
			// Another organization forces a new webhook.
			Config: config(missing, url1),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccOrgWebhookAddress, plancheck.ResourceActionReplace)},
			},
			ExpectError: notFound(missing),
		},
		{
			// A user's login isn't an organization.
			Config:      config(login, url1),
			ExpectError: notFound(login),
		},
	}
	if foreign := os.Getenv("GHOST_TEST_FOREIGN_ORGANIZATION"); foreign != "" {
		steps = append(steps, resource.TestStep{Config: config(foreign, url1), ExpectError: notFound(foreign)})
	}
	steps = append(steps, resource.TestStep{
		Config:           config(org, url1),
		ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
		Check:            testAccCheckOrganizationWebhookRemote(url1, true, "push"),
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckOrganizationWebhookDestroy,
		Steps:                    steps,
	})
}

// testAccOrganizationWebhookFixture skips unless GHOST_TEST_ORGANIZATION and
// TF_ACC are set, and returns the organization and two delivery URLs marked
// for this test. Hooks with these URLs left behind are deleted at cleanup.
func testAccOrganizationWebhookFixture(t *testing.T) (org, url1, url2 string) {
	t.Helper()

	org = os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test ghost_organization_webhook")
	}
	testAccPreCheck(t)

	marker := acctest.RandomWithPrefix("tf-acc")
	url1 = testAccWebhookURL + "?" + marker
	url2 = testAccWebhookURL + "?" + marker + "-2"

	t.Cleanup(func() {
		c, err := testAccClient()
		if err != nil {
			t.Error(err)
			return
		}
		hooks := c.OrganizationWebhooks(org)
		for _, h := range testAccMarkedOrganizationWebhooks(t, hooks, url1) {
			t.Errorf("webhook %s was left behind; deleting it", h.ID)
			_ = hooks.Delete(context.Background(), h.ID)
		}
	})

	return org, url1, url2
}

// testAccMarkedOrganizationWebhooks returns the hooks whose URL starts with
// url1, i.e. carries the test's marker.
func testAccMarkedOrganizationWebhooks(t *testing.T, hooks *client.Webhooks, url1 string) []client.Webhook {
	t.Helper()
	list, err := hooks.List(context.Background())
	if err != nil {
		t.Fatalf("listing webhooks: %v", err)
	}
	var marked []client.Webhook
	for _, h := range list {
		if strings.HasPrefix(h.URL, url1) {
			marked = append(marked, h)
		}
	}
	return marked
}

// testAccCheckOrganizationWebhookDelivery is testAccCheckWebhookDelivery for
// the organization webhook in state.
func testAccCheckOrganizationWebhookDelivery(event string, succeeded bool, notSecret *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := s.RootModule().Resources[testAccOrgWebhookAddress]
		c, err := testAccClient()
		if err != nil {
			return err
		}
		return testAccWaitWebhookDelivery(c.OrganizationWebhooks(rs.Primary.Attributes["organization"]), rs.Primary.Attributes, event, succeeded, notSecret)
	}
}

// testAccCheckOrganizationWebhookRemote checks, straight from the API, the
// URL, active flag and events of the webhook in state.
func testAccCheckOrganizationWebhookRemote(url string, active bool, events ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[testAccOrgWebhookAddress]
		if !ok {
			return fmt.Errorf("%s not in state", testAccOrgWebhookAddress)
		}
		c, err := testAccClient()
		if err != nil {
			return err
		}
		hook, err := c.OrganizationWebhooks(rs.Primary.Attributes["organization"]).Get(context.Background(), rs.Primary.Attributes["webhook_id"])
		if err != nil {
			return err
		}
		got := map[string]bool{}
		for _, e := range hook.Events {
			got[e] = true
		}
		match := len(hook.Events) == len(events)
		for _, e := range events {
			match = match && got[e]
		}
		if hook.URL != url || hook.Active != active || !match {
			return fmt.Errorf("remote url/active/events = %q/%v/%v, want %q/%v/%v", hook.URL, hook.Active, hook.Events, url, active, events)
		}
		return nil
	}
}

func testAccOrganizationWebhookImportID(orgCase func(string) string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[testAccOrgWebhookAddress]
		if !ok {
			return "", fmt.Errorf("%s not in state", testAccOrgWebhookAddress)
		}
		return orgCase(rs.Primary.Attributes["organization"]) + ":" + rs.Primary.Attributes["webhook_id"], nil
	}
}

// testAccOrganizationWebhookConfig returns a ghost_organization_webhook.test
// on org, with attrs as its other arguments.
func testAccOrganizationWebhookConfig(org, attrs string) string {
	return fmt.Sprintf(`
resource "ghost_organization_webhook" "test" {
  organization = %q
  %s
}
`, org, attrs)
}

func testAccCheckOrganizationWebhookDestroy(s *terraform.State) error {
	c, err := testAccClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_organization_webhook" {
			continue
		}
		_, err := c.OrganizationWebhooks(rs.Primary.Attributes["organization"]).Get(context.Background(), rs.Primary.Attributes["webhook_id"])
		if err == nil {
			return fmt.Errorf("webhook %s still exists", rs.Primary.ID)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("checking webhook %s: %w", rs.Primary.ID, err)
		}
	}

	return nil
}
