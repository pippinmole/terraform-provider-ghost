package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func TestGitignoreTemplatesDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"empty name", `name = ""`, `(?i)length`},
		{"computed id", `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed names", `names = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_gitignore_templates\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// Valid forms pass validation. The read then fails, with no Ghost server to
// talk to.
func TestGitignoreTemplatesDataSourceValidForms(t *testing.T) {
	for _, attrs := range []string{``, `name = "Go"`, `name = "c++"`} {
		t.Run(attrs, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: `
provider "ghost" {
  token    = "x"
  base_url = "http://127.0.0.1:1"
}

data "ghost_gitignore_templates" "test" {
  ` + attrs + `
}
`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`Error listing .gitignore templates`),
				}},
			})
		})
	}
}

func TestGitignoreTemplatesDataSourceFromRemote(t *testing.T) {
	var m gitignoreTemplatesDataSourceModel
	if d := m.fromRemote([]string{"Go", "Zig", "bun"}, nil); d.HasError() {
		t.Fatalf("fromRemote: %v", d)
	}
	if m.ID.ValueString() != "gitignore_templates" {
		t.Errorf("id = %s", m.ID)
	}
	var names []string
	for _, v := range m.Names.Elements() {
		s, _ := strconv.Unquote(v.String())
		names = append(names, s)
	}
	if !slices.Equal(names, []string{"Go", "Zig", "bun"}) {
		t.Errorf("names = %v (order must be kept)", names)
	}
	if !m.Template.IsNull() {
		t.Errorf("template should be null without name, got %s", m.Template)
	}

	if d := m.fromRemote(nil, &client.GitignoreTemplate{Name: "Go", Source: "*.exe\n"}); d.HasError() {
		t.Fatalf("fromRemote: %v", d)
	}
	if m.Names.IsNull() || len(m.Names.Elements()) != 0 {
		t.Errorf("no names should be an empty list, got %s", m.Names)
	}
	attrs := m.Template.Attributes()
	if attrs["name"].String() != `"Go"` || attrs["source"].String() != `"*.exe\n"` {
		t.Errorf("unexpected template: %s", m.Template)
	}
}

const testAccGitignoreAddress = "data.ghost_gitignore_templates.test"

func testAccGitignoreConfig(attrs string) string {
	return "data \"ghost_gitignore_templates\" \"test\" {\n  " + attrs + "\n}\n"
}

func TestAccGitignoreTemplatesDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccGitignoreConfig(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccGitignoreAddress, "id", "gitignore_templates"),
					resource.TestCheckNoResourceAttr(testAccGitignoreAddress, "template.name"),
					testAccCheckGitignoreNames,
				),
			},
			{
				Config: testAccGitignoreConfig(`name = "Go"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccGitignoreAddress, "template.name", "Go"),
					resource.TestMatchResourceAttr(testAccGitignoreAddress, "template.source", regexp.MustCompile(`(?m)^\*\.exe$`)),
					testAccCheckGitignoreNames,
				),
			},
		},
	})
}

// Ghost matches template names case-insensitively and returns its own
// spelling, and names with "+" and "." work.
func TestAccGitignoreTemplatesDataSource_names(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"go", "Go"},
		{"GO", "Go"},
		{"jenkins_home", "JENKINS_HOME"},
		{"C++", "C++"},
		{"ecu.test", "ecu.test"},
		{"objective-c", "Objective-C"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: testAccGitignoreConfig(fmt.Sprintf("name = %q", tc.name)),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(testAccGitignoreAddress, "name", tc.name),
						resource.TestCheckResourceAttr(testAccGitignoreAddress, "template.name", tc.want),
						resource.TestMatchResourceAttr(testAccGitignoreAddress, "template.source", regexp.MustCompile(`\S`)),
					),
				}},
			})
		})
	}
}

func TestAccGitignoreTemplatesDataSource_notFound(t *testing.T) {
	steps := make([]resource.TestStep, 0, 3)
	for _, name := range []string{"tf-acc-no-such-template", "Go.gitignore", " Go "} {
		steps = append(steps, resource.TestStep{
			Config:      testAccGitignoreConfig(fmt.Sprintf("name = %q", name)),
			ExpectError: regexp.MustCompile(`(?s)Gitignore template not found.*no\s+.gitignore\s+template\s+named.*case-insensitively`),
		})
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

// A repository created with a name from the data source (in another case)
// starts with exactly the template's source as its .gitignore.
func TestAccGitignoreTemplatesDataSource_repository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-gitignore")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{{
			Config: testAccGitignoreConfig(`name = "go"`) + `
resource "ghost_repository" "test" {
  name               = "` + repoName + `"
  auto_init          = true
  gitignore_template = data.ghost_gitignore_templates.test.template.name
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ghost_repository.test", "gitignore_template", "Go"),
				func(s *terraform.State) error {
					want := s.RootModule().Resources[testAccGitignoreAddress].Primary.Attributes["template.source"]
					_, repo := testAccRepository(t, repoName)
					git := testAccGit(t, repo)
					git("clone", "-q", "--depth", "1", repo.CloneURL, ".")
					got, err := os.ReadFile(filepath.Join(git("rev-parse", "--show-toplevel"), ".gitignore"))
					if err != nil {
						return err
					}
					if string(got) != want {
						return fmt.Errorf(".gitignore (%d bytes) differs from template.source (%d bytes)", len(got), len(want))
					}
					return nil
				},
			),
		}},
	})
}

// testAccCheckGitignoreNames checks that names holds the well-known
// templates, in byte order, with no duplicates.
func testAccCheckGitignoreNames(s *terraform.State) error {
	attrs := s.RootModule().Resources[testAccGitignoreAddress].Primary.Attributes
	n, err := strconv.Atoi(attrs["names.#"])
	if err != nil || n < 100 {
		return fmt.Errorf("names.# = %q, want at least 100", attrs["names.#"])
	}
	names := make([]string, n)
	for i := range names {
		names[i] = attrs[fmt.Sprintf("names.%d", i)]
	}
	if !slices.IsSorted(names) || len(slices.Compact(slices.Clone(names))) != n {
		return fmt.Errorf("names are not byte-sorted and unique: %v", names)
	}
	for _, want := range []string{"Go", "C++", "JENKINS_HOME", "ecu.test"} {
		if !slices.Contains(names, want) {
			return fmt.Errorf("names is missing %q", want)
		}
	}
	return nil
}
