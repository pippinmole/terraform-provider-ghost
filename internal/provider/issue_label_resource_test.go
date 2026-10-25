package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

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

func TestParseIssueLabelID(t *testing.T) {
	tests := []struct{ id, repository, name string }{
		{"alice/repo:bug", "alice/repo", "bug"},
		{"alice/repo:priority: high", "alice/repo", "priority: high"},
		{"alice/repo:a/b", "alice/repo", "a/b"},
	}
	for _, tt := range tests {
		repository, name, err := parseIssueLabelID(tt.id)
		if err != nil || repository != tt.repository || name != tt.name {
			t.Errorf("parseIssueLabelID(%q) = %q, %q, %v", tt.id, repository, name, err)
		}
	}

	for _, id := range []string{"", "alice/repo", "alice/repo:", "alice:bug", "a/b/c:bug", ":bug"} {
		if _, _, err := parseIssueLabelID(id); err == nil {
			t.Errorf("parseIssueLabelID(%q): expected error", id)
		}
	}
}

func TestLabelColorFromRemote(t *testing.T) {
	tests := []struct {
		name   string
		prior  types.String
		remote string
		want   types.String
	}{
		{"hash and upper case kept", types.StringValue("#FF00AA"), "ff00aa", types.StringValue("#FF00AA")},
		{"canonical kept", types.StringValue("ff00aa"), "ff00aa", types.StringValue("ff00aa")},
		{"changed outside terraform", types.StringValue("#FF00AA"), "00ff00", types.StringValue("00ff00")},
		{"null prior", types.StringNull(), "ededed", types.StringValue("ededed")},
		{"unknown prior", types.StringUnknown(), "ededed", types.StringValue("ededed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := labelColorFromRemote(tt.prior, tt.remote); !got.Equal(tt.want) {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSameRepository(t *testing.T) {
	tests := []struct {
		name    string
		stateID types.Int64
		repoID  int64
		want    bool
	}{
		{"null state", types.Int64Null(), 8, true},
		{"unknown state", types.Int64Unknown(), 8, true},
		{"same", types.Int64Value(7), 7, true},
		{"different", types.Int64Value(7), 8, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameRepository(tt.stateID, &client.Repository{ID: tt.repoID}); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIssueLabelConfigValidation runs offline: the attribute validators
// reject the config before the provider is ever configured.
func TestIssueLabelConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"leading whitespace", `name = " bug"`, `whitespace`},
		{"trailing whitespace", `name = "bug "`, `whitespace`},
		{"dot", `name = "."`, `name`},
		{"dot dot", `name = ".."`, `name`},
		{"too long", `name = "` + strings.Repeat("x", 51) + `"`, `length`},
		{"bad color", `color = "zzz"`, `six hex digits`},
		{"short color", `color = "#12345"`, `six hex digits`},
		{"bad repository", `repository = "nope"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := map[string]string{"repository": `"alice/repo"`, "name": `"bug"`}
			key, _, _ := strings.Cut(tt.config, " ")
			delete(attrs, key)
			config := "resource \"ghost_issue_label\" \"test\" {\n  " + tt.config + "\n"
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

func TestAccIssueLabelResource(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")

	// Updates, including a case-only rename, must keep the same label.
	sameLabelID := statecheck.CompareValue(compare.ValuesSame())
	labelIDCheck := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameLabelID.AddStateValue("ghost_issue_label.test", tfjsonpath.New("label_id")),
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIssueLabelDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccIssueLabelConfig(repoName, `
  name        = "Bug Report"
  color       = "#FF00AA"
  description = "d"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_issue_label.test", plancheck.ResourceActionCreate)},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: labelIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_issue_label.test", "name", "Bug Report"),
					resource.TestCheckResourceAttr("ghost_issue_label.test", "color", "#FF00AA"),
					resource.TestCheckResourceAttr("ghost_issue_label.test", "description", "d"),
					resource.TestCheckResourceAttrPair("ghost_issue_label.test", "repository", "ghost_repository.test", "full_name"),
					resource.TestCheckResourceAttrSet("ghost_issue_label.test", "label_id"),
					resource.TestCheckResourceAttrSet("ghost_issue_label.test", "node_id"),
					resource.TestCheckResourceAttrSet("ghost_issue_label.test", "url"),
				),
			},
			{
				ResourceName: "ghost_issue_label.test",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["ghost_repository.test"].Primary.Attributes["full_name"] + ":Bug Report", nil
				},
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"color"},
			},
			{
				// A case-only rename updates the label in place, and removing
				// the description clears it.
				Config: testAccIssueLabelConfig(repoName, `
  name  = "bug report"
  color = "00ff00"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_issue_label.test", plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: labelIDCheck(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_issue_label.test", "name", "bug report"),
					resource.TestCheckResourceAttr("ghost_issue_label.test", "color", "00ff00"),
					resource.TestCheckNoResourceAttr("ghost_issue_label.test", "description"),
					resource.TestCheckResourceAttrWith("ghost_issue_label.test", "id", func(v string) error {
						if !strings.HasSuffix(v, ":bug report") {
							return fmt.Errorf("id %q was not updated after the rename", v)
						}
						return nil
					}),
				),
			},
			{
				Config: testAccIssueLabelConfig(repoName, `
  name  = "bug report"
  color = "00ff00"
}

resource "ghost_issue_label" "dup" {
  repository = ghost_issue_label.test.repository
  name       = ghost_issue_label.test.name`),
				ExpectError: regexp.MustCompile(`(?s)already exists.*terraform import`),
			},
		},
	})
}

func TestAccIssueLabelResource_repositoryRename(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc")
	renamed := repoName + "-renamed"

	sameLabelID := statecheck.CompareValue(compare.ValuesSame())
	stateChecks := func() []statecheck.StateCheck {
		return []statecheck.StateCheck{
			sameLabelID.AddStateValue("ghost_issue_label.test", tfjsonpath.New("label_id")),
			statecheck.CompareValuePairs(
				"ghost_issue_label.test", tfjsonpath.New("repository"),
				"ghost_repository.test", tfjsonpath.New("full_name"),
				compare.ValuesSame(),
			),
		}
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckIssueLabelDestroy,
		Steps: []resource.TestStep{
			{
				Config:            testAccIssueLabelConfig(repoName, `name = "bug"`),
				ConfigStateChecks: stateChecks(),
				Check:             resource.TestCheckResourceAttrSet("ghost_issue_label.test", "repository_id"),
			},
			{
				// Renaming the repository keeps the label, and its issues.
				Config: testAccIssueLabelConfig(renamed, `name = "bug"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ghost_issue_label.test", plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: stateChecks(),
				Check: resource.TestCheckResourceAttrWith("ghost_issue_label.test", "id", func(v string) error {
					if !strings.HasSuffix(v, "/"+renamed+":bug") {
						return fmt.Errorf("id %q was not updated after the repository rename", v)
					}
					return nil
				}),
			},
		},
	})
}

// testAccIssueLabelConfig returns a repository and a ghost_issue_label.test
// in it, with attrs as the label's other arguments.
func testAccIssueLabelConfig(repoName, attrs string) string {
	return testAccRepositoryConfig(repoName, "") + fmt.Sprintf(`
resource "ghost_issue_label" "test" {
  repository = ghost_repository.test.full_name
  %s
}
`, attrs)
}

func testAccCheckIssueLabelDestroy(s *terraform.State) error {
	c, err := testAccClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "ghost_issue_label" {
			continue
		}
		owner, repo, err := client.SplitFullName(rs.Primary.Attributes["repository"])
		if err != nil {
			return err
		}
		name := rs.Primary.Attributes["name"]
		_, err = c.GetLabel(context.Background(), owner, repo, name)
		if err == nil {
			return fmt.Errorf("label %s still exists", rs.Primary.ID)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("checking label %s: %w", rs.Primary.ID, err)
		}
	}

	return testAccCheckRepositoryDestroy(s)
}
