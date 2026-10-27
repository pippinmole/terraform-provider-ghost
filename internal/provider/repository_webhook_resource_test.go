package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

const (
	testAccWebhookAddress = "ghost_repository_webhook.test"
	// testAccWebhookURL is Ghost's own web host, which answers a POST with a
	// 200, so deliveries succeed without a third-party request bin.
	testAccWebhookURL  = "https://web.ghost.procrastinator.fyi/"
	testAccWebhookURL2 = "https://web.ghost.procrastinator.fyi/?tf-acc=2"
)

func TestParseRepositoryWebhookID(t *testing.T) {
	repo, id, err := parseRepositoryWebhookID("alice/repo:whk_abc")
	if err != nil || repo != "alice/repo" || id != "whk_abc" {
		t.Errorf("got %q, %q, %v", repo, id, err)
	}

	for _, bad := range []string{"", "alice/repo", "alice/repo:", "alice:whk_1", "a/b/c:whk_1", ":whk_1", "alice/repo:a:b", "alice/repo:a/b"} {
		if _, _, err := parseRepositoryWebhookID(bad); err == nil {
			t.Errorf("parseRepositoryWebhookID(%q) should fail", bad)
		}
	}
}

func TestValidateWebhookURL(t *testing.T) {
	for _, ok := range []string{"https://a.b/", "http://a.b:8080/x?y=1#z", "https://EXAMPLE.com/Hook/", "https://a.b/" + strings.Repeat("x", webhookURLMaxLength-len("https://a.b/"))} {
		if err := validateWebhookURL(ok); err != nil {
			t.Errorf("validateWebhookURL(%.40q) = %v", ok, err)
		}
	}

	bad := map[string]string{
		"ftp://a.b/":       "https:// or http://",
		"a.b":              "full URL",
		"":                 "full URL",
		"https://":         "full URL",
		"https://u:p@a.b/": "username or password",
		"https://u@a.b/":   "username or password",
		" https://a.b":     "whitespace",
		"https://a.b\n":    "whitespace",
		"https://a.b/" + strings.Repeat("x", webhookURLMaxLength): "at most 2048",
	}
	for in, want := range bad {
		if err := validateWebhookURL(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("validateWebhookURL(%.40q) = %v, want %q", in, err, want)
		}
	}
}

func TestWebhookEventsList(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range client.WebhookEvents {
		if seen[e] {
			t.Errorf("duplicate event %q", e)
		}
		seen[e] = true
	}
	if len(client.WebhookEvents) != 37 {
		t.Errorf("got %d events, want 37", len(client.WebhookEvents))
	}
}

func TestWebhookModelFromRemote(t *testing.T) {
	ctx := context.Background()
	reason := "410 Gone"
	m := webhookModel{Secret: types.StringValue("whsec_prior")}
	diags := m.fromRemote(ctx, &client.Webhook{
		ID: "whk_1", URL: "https://EXAMPLE.com/x/", Events: []string{"push", "label.created"},
		Active: false, DisabledReason: &reason, CreatedAt: "2026-10-27T00:00:00Z",
	})
	if diags.HasError() {
		t.Fatal(diags)
	}
	want, _ := types.SetValueFrom(ctx, types.StringType, []string{"label.created", "push"})
	if m.WebhookID.ValueString() != "whk_1" || m.URL.ValueString() != "https://EXAMPLE.com/x/" || !m.Events.Equal(want) ||
		m.Active.ValueBool() || m.DisabledReason.ValueString() != reason || m.Secret.ValueString() != "whsec_prior" {
		t.Errorf("unexpected model: %+v", m)
	}

	// An enabled hook has no reason, and a returned secret replaces the prior one.
	_ = m.fromRemote(ctx, &client.Webhook{ID: "whk_1", Active: true, Secret: "whsec_new"})
	if !m.DisabledReason.IsNull() || m.Secret.ValueString() != "whsec_new" {
		t.Errorf("unexpected model: %+v", m)
	}

	// An imported hook has no secret.
	imported := webhookModel{Secret: types.StringNull()}
	_ = imported.fromRemote(ctx, &client.Webhook{ID: "whk_1"})
	if !imported.Secret.IsNull() {
		t.Errorf("secret should stay null after import, got %v", imported.Secret)
	}
}

func TestWebhookEventListOrder(t *testing.T) {
	ctx := context.Background()
	set, _ := types.SetValueFrom(ctx, types.StringType, []string{"member.removed", "push", "label.created"})
	m := webhookModel{Events: set}
	got, diags := m.eventList(ctx)
	if diags.HasError() || strings.Join(got, ",") != "push,label.created,member.removed" {
		t.Errorf("eventList = %v, %v", got, diags)
	}
}

func TestWebhookWantsRotation(t *testing.T) {
	s := func(v string) types.String {
		if v == "" {
			return types.StringNull()
		}
		return types.StringValue(v)
	}
	tests := []struct {
		state, plan string
		want        bool
	}{
		{"", "", false},
		{"", "1", true},
		{"1", "1", false},
		{"1", "2", true},
		{"1", "", false},
	}
	for _, tt := range tests {
		plan, state := webhookModel{SecretRotation: s(tt.plan)}, webhookModel{SecretRotation: s(tt.state)}
		if got := plan.wantsRotation(&state); got != tt.want {
			t.Errorf("%q -> %q: got %v, want %v", tt.state, tt.plan, got, tt.want)
		}
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	body := `{"event":"ping"}`
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte(body))
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !verifyWebhookSignature("whsec_test", body, header) {
		t.Error("valid signature rejected")
	}
	if verifyWebhookSignature("whsec_other", body, header) || verifyWebhookSignature("whsec_test", body+" ", header) {
		t.Error("invalid signature accepted")
	}
}

// TestRepositoryWebhookConfigValidation runs offline: the attribute
// validators reject the config before the provider is ever configured.
func TestRepositoryWebhookConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"bad repository", `repository = "nope"`, `must be "owner/slug"`},
		{"ftp url", `url = "ftp://example.com/"`, `https:// or http://`},
		{"userinfo", `url = "https://u:p@example.com/"`, `username or password`},
		{"whitespace", `url = " https://example.com/"`, `whitespace`},
		{"too long", `url = "https://example.com/` + strings.Repeat("x", webhookURLMaxLength) + `"`, `at most 2048`},
		{"no events", `events = []`, `(?i)at least 1`},
		{"unknown event", `events = ["pushh"]`, `(?s)value must be one of.*"push"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := map[string]string{"repository": `"alice/repo"`, "url": `"https://example.com/"`, "events": `["push"]`}
			key, _, _ := strings.Cut(tt.config, " ")
			delete(attrs, key)
			config := "resource \"ghost_repository_webhook\" \"test\" {\n  " + tt.config + "\n"
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

func TestAccRepositoryWebhookResource(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	sameID := statecheck.CompareValue(compare.ValuesSame())
	sameSecret := statecheck.CompareValue(compare.ValuesSame())
	same := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameID.AddStateValue(testAccWebhookAddress, tfjsonpath.New("webhook_id")),
			sameSecret.AddStateValue(testAccWebhookAddress, tfjsonpath.New("secret")),
		}
	}
	update := resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionUpdate)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{
			{
				// Every argument.
				Config: testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url             = %q
  events          = ["issue.opened", "label.created"]
  active          = true
  secret_rotation = "1"`, testAccWebhookURL)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: append(same(),
					statecheck.CompareValuePairs(
						testAccWebhookAddress, tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
				),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(testAccWebhookAddress, "webhook_id", regexp.MustCompile(`^whk_`)),
					resource.TestMatchResourceAttr(testAccWebhookAddress, "id", regexp.MustCompile(`/`+repoName+`:whk_`)),
					resource.TestMatchResourceAttr(testAccWebhookAddress, "secret", regexp.MustCompile(`^whsec_.{32}$`)),
					resource.TestCheckResourceAttr(testAccWebhookAddress, "url", testAccWebhookURL),
					resource.TestCheckResourceAttr(testAccWebhookAddress, "events.#", "2"),
					resource.TestCheckTypeSetElemAttr(testAccWebhookAddress, "events.*", "label.created"),
					resource.TestCheckResourceAttr(testAccWebhookAddress, "active", "true"),
					resource.TestCheckNoResourceAttr(testAccWebhookAddress, "disabled_reason"),
					resource.TestCheckResourceAttrSet(testAccWebhookAddress, "created_at"),
					testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "issue.opened", "label.created"),
				),
			},
			{
				ResourceName:      testAccWebhookAddress,
				ImportState:       true,
				ImportStateIdFunc: testAccRepositoryWebhookImportID,
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
				ResourceName:  testAccWebhookAddress,
				ImportState:   true,
				ImportStateId: "nope",
				ExpectError:   regexp.MustCompile(`import ID must be "owner/repo:webhook_id"`),
			},
			{
				// URL and events change in place; a duplicate event is
				// harmless in a set.
				Config: testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url             = %q
  events          = ["issue.opened", "release.published", "release.published"]
  secret_rotation = "1"`, testAccWebhookURL2)),
				ConfigPlanChecks:  update,
				ConfigStateChecks: same(),
				Check:             testAccCheckRepositoryWebhookRemote(testAccWebhookURL2, true, "issue.opened", "release.published"),
			},
			{
				Config: testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url             = %q
  events          = ["issue.opened", "release.published"]
  active          = false
  secret_rotation = "1"`, testAccWebhookURL2)),
				ConfigPlanChecks:  update,
				ConfigStateChecks: same(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(testAccWebhookAddress, "disabled_reason"),
					testAccCheckRepositoryWebhookRemote(testAccWebhookURL2, false, "issue.opened", "release.published"),
				),
			},
			{
				// Removing active turns it back on (the default).
				Config: testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url    = %q
  events = ["issue.opened", "release.published"]`, testAccWebhookURL2)),
				ConfigPlanChecks:  update,
				ConfigStateChecks: same(),
				Check:             testAccCheckRepositoryWebhookRemote(testAccWebhookURL2, true, "issue.opened", "release.published"),
			},
		},
	})
}

func TestAccRepositoryWebhookResource_inactiveAtCreate(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{{
			// Ghost ignores active on create, so Create turns it off after.
			Config: testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url    = %q
  events = ["push"]
  active = false`, testAccWebhookURL)),
			ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(testAccWebhookAddress, "active", "false"),
				resource.TestCheckResourceAttrSet(testAccWebhookAddress, "secret"),
				testAccCheckRepositoryWebhookRemote(testAccWebhookURL, false, "push"),
			),
		}},
	})
}

// TestAccRepositoryWebhookResource_secret proves the secret in state signs
// real deliveries, before and after rotation.
func TestAccRepositoryWebhookResource_secret(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	var oldSecret string
	config := func(rotation, extra string) string {
		return testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url             = %q
  events          = ["label.created"]
  secret_rotation = %q`, testAccWebhookURL, rotation)) + extra
	}
	label := `
resource "ghost_issue_label" "trigger" {
  repository = ghost_repository.test.full_name
  name       = "tf-acc-trigger"
  depends_on = [ghost_repository_webhook.test]
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{
			{
				Config: config("a", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckWebhookDelivery("ping", true, nil),
					func(s *terraform.State) error {
						oldSecret = s.RootModule().Resources[testAccWebhookAddress].Primary.Attributes["secret"]
						return nil
					},
				),
			},
			{
				// Changing secret_rotation replaces the secret in place.
				Config: config("b", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(testAccWebhookAddress, "secret", regexp.MustCompile(`^whsec_`)),
					func(s *terraform.State) error {
						if s.RootModule().Resources[testAccWebhookAddress].Primary.Attributes["secret"] == oldSecret {
							return fmt.Errorf("secret was not rotated")
						}
						return nil
					},
				),
			},
			{
				// A label delivery is signed with the new secret only.
				Config: config("b", label),
				Check:  testAccCheckWebhookDelivery("label.created", true, &oldSecret),
			},
			{
				// Removing secret_rotation keeps the secret.
				Config: testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url    = %q
  events = ["label.created"]`, testAccWebhookURL)) + label,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionUpdate),
						plancheck.ExpectKnownValue(testAccWebhookAddress, tfjsonpath.New("secret"), knownNonEmptyString{}),
					},
				},
			},
		},
	})
}

func TestAccRepositoryWebhookResource_drift(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url    = %q
  events = ["push", "issue.opened"]`, testAccWebhookURL))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Changed outside Terraform: put back in place.
				PreConfig: func() {
					off := false
					u := testAccWebhookURL2
					testAccUpdateWebhook(t, repoName, client.UpdateWebhookRequest{URL: &u, Events: []string{"star.created"}, Active: &off})
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "push", "issue.opened"),
			},
			{
				// Deleted outside Terraform: created again.
				PreConfig: func() { testAccDeleteWebhooks(t, repoName) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "push", "issue.opened"),
			},
		},
	})
}

func TestAccRepositoryWebhookResource_repositoryRename(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"
	block := fmt.Sprintf(`
  url    = %q
  events = ["push"]

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }`, testAccWebhookURL)
	sameID := statecheck.CompareValue(compare.ValuesSame())
	sameSecret := statecheck.CompareValue(compare.ValuesSame())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccRepositoryWebhookConfig(repoName, block),
				ConfigStateChecks: []statecheck.StateCheck{
					sameID.AddStateValue(testAccWebhookAddress, tfjsonpath.New("webhook_id")),
					sameSecret.AddStateValue(testAccWebhookAddress, tfjsonpath.New("secret")),
				},
			},
			{
				// Renaming the repository keeps the webhook and its secret.
				Config: testAccRepositoryWebhookConfig(renamed, block),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionUpdate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					sameID.AddStateValue(testAccWebhookAddress, tfjsonpath.New("webhook_id")),
					sameSecret.AddStateValue(testAccWebhookAddress, tfjsonpath.New("secret")),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(testAccWebhookAddress, "id", regexp.MustCompile(`/`+renamed+`:whk_`)),
					testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "push"),
				),
			},
		},
	})
}

func TestAccRepositoryWebhookResource_repositoryReplaced(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	otherName := repoName + "-other"
	config := func(license, repository string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("license_template = %q", license)) +
			fmt.Sprintf(`
resource "ghost_repository" "other" {
  name = %q
}

resource "ghost_repository_webhook" "test" {
  repository = %s.full_name
  url        = %q
  events     = ["push"]

  lifecycle {
    replace_triggered_by = [ghost_repository.test.repo_id]
  }
}
`, otherName, repository, testAccWebhookURL)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{
			{Config: config("mit", "ghost_repository.test")},
			{
				// Replacing the repository deletes its webhooks;
				// replace_triggered_by recreates them.
				Config: config("apache-2.0", "ghost_repository.test"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("ghost_repository.test", plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionReplace),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						testAccWebhookAddress, tfjsonpath.New("repository_id"),
						"ghost_repository.test", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
				},
				Check: testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "push"),
			},
			{
				// Pointing it at a different repository replaces it.
				Config: config("apache-2.0", "ghost_repository.other"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(testAccWebhookAddress, plancheck.ResourceActionReplace)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.CompareValuePairs(
						testAccWebhookAddress, tfjsonpath.New("repository_id"),
						"ghost_repository.other", tfjsonpath.New("repo_id"),
						compare.ValuesSame(),
					),
				},
				Check: testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "push"),
			},
		},
	})
}

// TestAccRepositoryWebhookResource_errors covers what only Ghost can check.
func TestAccRepositoryWebhookResource_errors(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(url string) string {
		return testAccRepositoryWebhookConfig(repoName, fmt.Sprintf(`
  url    = %q
  events = ["push"]`, url))
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{
			{Config: config("https://localhost/hook"), ExpectError: regexp.MustCompile(`private or reserved address`)},
			{Config: config("https://10.0.0.1/hook"), ExpectError: regexp.MustCompile(`private or reserved address`)},
			{Config: config("https://tf-acc-nonexistent.invalid/"), ExpectError: regexp.MustCompile(`does not resolve`)},
			{
				Config: testAccRepositoryConfig(repoName, "") + fmt.Sprintf(`
resource "ghost_repository_webhook" "test" {
  repository = "${ghost_repository.test.owner}/tf-acc-does-not-exist"
  url        = %q
  events     = ["push"]
}
`, testAccWebhookURL),
				ExpectError: regexp.MustCompile(`Repository .*tf-acc-does-not-exist not found`),
			},
			{Config: config(testAccWebhookURL)},
		},
	})
}

func TestAccRepositoryWebhookResource_organization(t *testing.T) {
	org := os.Getenv("GHOST_TEST_ORGANIZATION")
	if org == "" || os.Getenv(resource.EnvTfAcc) == "" {
		t.Skip("set GHOST_TEST_ORGANIZATION and TF_ACC to test webhooks on organization repositories")
	}
	repoName := acctest.RandomWithPrefix("tf-acc")
	config := func(events string) string {
		return testAccRepositoryConfig(repoName, fmt.Sprintf("owner = %q", org)) + testAccRepositoryWebhookBlock(fmt.Sprintf(`
  url    = %q
  events = %s`, testAccWebhookURL, events))
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryWebhookDestroy,
		Steps: []resource.TestStep{
			{
				Config:           config(`["push"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestMatchResourceAttr(testAccWebhookAddress, "id", regexp.MustCompile(`^`+org+`/`)),
					testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "push"),
				),
			},
			{
				ResourceName:            testAccWebhookAddress,
				ImportState:             true,
				ImportStateIdFunc:       testAccRepositoryWebhookImportID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secret"},
			},
			{
				Config:           config(`["push", "member.added"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
				Check:            testAccCheckRepositoryWebhookRemote(testAccWebhookURL, true, "push", "member.added"),
			},
		},
	})
}

// knownNonEmptyString checks a planned value is a known, non-empty string.
type knownNonEmptyString struct{}

func (knownNonEmptyString) CheckValue(v any) error {
	if s, ok := v.(string); !ok || s == "" {
		return fmt.Errorf("expected a known, non-empty string")
	}
	return nil
}

func (knownNonEmptyString) String() string { return "known non-empty string" }

// verifyWebhookSignature reports whether header is the X-Hub-Signature-256
// Ghost sends for body signed with secret.
func verifyWebhookSignature(secret, body, header string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hmac.Equal([]byte(header), []byte("sha256="+hex.EncodeToString(mac.Sum(nil))))
}

// testAccCheckWebhookDelivery waits for a delivery of event to the webhook in
// state and checks it is signed with the secret in state (never printed),
// and not with notSecret when given. With succeeded, the endpoint must also
// have answered 2xx.
func testAccCheckWebhookDelivery(event string, succeeded bool, notSecret *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := s.RootModule().Resources[testAccWebhookAddress]
		secret := rs.Primary.Attributes["secret"]
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
		if err != nil {
			return err
		}
		c, err := testAccClient()
		if err != nil {
			return err
		}
		hooks := c.RepositoryWebhooks(owner, repo)

		deadline := time.Now().Add(60 * time.Second)
		for {
			deliveries, err := hooks.ListDeliveries(context.Background(), rs.Primary.Attributes["webhook_id"])
			if err != nil {
				return err
			}
			for _, d := range deliveries {
				if d.Event != event || len(d.Attempts) == 0 {
					continue
				}
				a := d.Attempts[0]
				if succeeded && (a.StatusCode == nil || *a.StatusCode < 200 || *a.StatusCode > 299) {
					continue
				}
				header := a.RequestHeaders["X-Hub-Signature-256"]
				if !verifyWebhookSignature(secret, d.Body, header) {
					return fmt.Errorf("%s delivery %s is not signed with the secret in state", event, d.ID)
				}
				if notSecret != nil && verifyWebhookSignature(*notSecret, d.Body, header) {
					return fmt.Errorf("%s delivery %s is signed with the old secret", event, d.ID)
				}
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("no %s delivery (succeeded=%v) within 60s", event, succeeded)
			}
			time.Sleep(4 * time.Second)
		}
	}
}

// testAccCheckRepositoryWebhookRemote checks, straight from the API, the URL,
// active flag and events of the webhook in state.
func testAccCheckRepositoryWebhookRemote(url string, active bool, events ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		hook, err := testAccGetRepositoryWebhook(s)
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

func testAccGetRepositoryWebhook(s *terraform.State) (*client.Webhook, error) {
	rs, ok := s.RootModule().Resources[testAccWebhookAddress]
	if !ok {
		return nil, fmt.Errorf("%s not in state", testAccWebhookAddress)
	}
	owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
	if err != nil {
		return nil, err
	}
	c, err := testAccClient()
	if err != nil {
		return nil, err
	}
	return c.RepositoryWebhooks(owner, repo).Get(context.Background(), rs.Primary.Attributes["webhook_id"])
}

func testAccRepositoryWebhookImportID(s *terraform.State) (string, error) {
	rs, ok := s.RootModule().Resources[testAccWebhookAddress]
	if !ok {
		return "", fmt.Errorf("%s not in state", testAccWebhookAddress)
	}
	return rs.Primary.Attributes["repository"] + ":" + rs.Primary.Attributes["webhook_id"], nil
}

// testAccUpdateWebhook changes the only webhook of repoName outside Terraform.
func testAccUpdateWebhook(t *testing.T, repoName string, req client.UpdateWebhookRequest) {
	t.Helper()
	hooks := testAccRepositoryWebhooks(t, repoName)
	list, err := hooks.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("listing webhooks: %d, %v", len(list), err)
	}
	if _, err := hooks.Update(context.Background(), list[0].ID, req); err != nil {
		t.Fatalf("updating webhook: %v", err)
	}
}

// testAccDeleteWebhooks deletes every webhook of repoName outside Terraform.
func testAccDeleteWebhooks(t *testing.T, repoName string) {
	t.Helper()
	hooks := testAccRepositoryWebhooks(t, repoName)
	list, err := hooks.List(context.Background())
	if err != nil {
		t.Fatalf("listing webhooks: %v", err)
	}
	for _, h := range list {
		if err := hooks.Delete(context.Background(), h.ID); err != nil {
			t.Fatalf("deleting webhook: %v", err)
		}
	}
}

func testAccRepositoryWebhooks(t *testing.T, repoName string) *client.Webhooks {
	t.Helper()
	c, repo := testAccRepository(t, repoName)
	owner, slug, _ := client.SplitFullName(repo.FullName)
	return c.RepositoryWebhooks(owner, slug)
}

// testAccRepositoryWebhookConfig returns a repository and a
// ghost_repository_webhook.test on it, with attrs as the webhook's other
// arguments.
func testAccRepositoryWebhookConfig(repoName, attrs string) string {
	return testAccRepositoryConfig(repoName, "") + testAccRepositoryWebhookBlock(attrs)
}

func testAccRepositoryWebhookBlock(attrs string) string {
	return fmt.Sprintf(`
resource "ghost_repository_webhook" "test" {
  repository = ghost_repository.test.full_name
  %s
}
`, attrs)
}

func testAccCheckRepositoryWebhookDestroy(s *terraform.State) error {
	c, err := testAccClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_repository_webhook" {
			continue
		}
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
		if err != nil {
			return err
		}
		_, err = c.RepositoryWebhooks(owner, repo).Get(context.Background(), rs.Primary.Attributes["webhook_id"])
		if err == nil {
			return fmt.Errorf("webhook %s still exists", rs.Primary.ID)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("checking webhook %s: %w", rs.Primary.ID, err)
		}
	}

	return nil
}
