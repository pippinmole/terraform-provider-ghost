package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

// testAccLicenseKeys is every license Ghost offered on 2026-10-28, in its
// order.
var testAccLicenseKeys = []string{
	"agpl-3.0", "apache-2.0", "bsd-2-clause", "bsd-3-clause", "bsl-1.0", "cc0-1.0", "epl-2.0",
	"gpl-2.0", "gpl-3.0", "lgpl-2.1", "mit", "mpl-2.0", "unlicense",
}

func TestLicensesDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"empty key", `key = ""`, `(?i)length`},
		{"computed id", `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed keys", `keys = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed licenses", `licenses = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_licenses\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// Valid forms pass validation. The read then fails, with no Ghost server to
// talk to.
func TestLicensesDataSourceValidForms(t *testing.T) {
	for _, attrs := range []string{``, `key = "mit"`, `key = "Apache-2.0"`} {
		t.Run(attrs, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: `
provider "ghost" {
  token    = "x"
  base_url = "http://127.0.0.1:1"
}

data "ghost_licenses" "test" {
  ` + attrs + `
}
`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`Error listing licenses`),
				}},
			})
		})
	}
}

func TestLicensesDataSourceFromRemote(t *testing.T) {
	var m licensesDataSourceModel
	summaries := []client.LicenseSummary{
		{Key: "mit", Name: "MIT License", SPDXID: "MIT"},
		{Key: "apache-2.0", Name: "Apache License 2.0", SPDXID: "Apache-2.0"},
	}
	if d := m.fromRemote(summaries, nil); d.HasError() {
		t.Fatalf("fromRemote: %v", d)
	}
	if m.ID.ValueString() != "licenses" {
		t.Errorf("id = %s", m.ID)
	}
	var keys []string
	for _, v := range m.Keys.Elements() {
		s, _ := strconv.Unquote(v.String())
		keys = append(keys, s)
	}
	if !slices.Equal(keys, []string{"mit", "apache-2.0"}) {
		t.Errorf("keys = %v (order must be kept)", keys)
	}
	if got := m.Licenses.String(); got != `[{"key":"mit","name":"MIT License","spdx_id":"MIT"},`+
		`{"key":"apache-2.0","name":"Apache License 2.0","spdx_id":"Apache-2.0"}]` {
		t.Errorf("licenses = %s", got)
	}
	if !m.License.IsNull() {
		t.Errorf("license should be null without key, got %s", m.License)
	}

	if d := m.fromRemote(nil, &client.License{
		LicenseSummary: client.LicenseSummary{Key: "unlicense", Name: "The Unlicense", SPDXID: "Unlicense"},
		HTMLURL:        "http://choosealicense.com/licenses/unlicense/",
		Permissions:    []string{"private-use", "commercial-use"},
		Limitations:    []string{"liability"},
		Body:           "This is free and unencumbered software.\n",
	}); d.HasError() {
		t.Fatalf("fromRemote: %v", d)
	}
	if m.Keys.IsNull() || len(m.Keys.Elements()) != 0 || m.Licenses.IsNull() || len(m.Licenses.Elements()) != 0 {
		t.Errorf("no licenses should be empty lists, got %s and %s", m.Keys, m.Licenses)
	}
	attrs := m.License.Attributes()
	for name, want := range map[string]string{
		"key":         `"unlicense"`,
		"spdx_id":     `"Unlicense"`,
		"html_url":    `"http://choosealicense.com/licenses/unlicense/"`,
		"permissions": `["private-use","commercial-use"]`,
		"conditions":  `[]`,
		"limitations": `["liability"]`,
		"body":        `"This is free and unencumbered software.\n"`,
		"featured":    `false`,
	} {
		if got := attrs[name].String(); got != want {
			t.Errorf("license.%s = %s, want %s", name, got, want)
		}
	}
}

func TestLicenseNotFoundDetail(t *testing.T) {
	got := licenseNotFoundDetail("isc", []client.LicenseSummary{{Key: "apache-2.0"}, {Key: "mit"}})
	want := `Ghost has no license with key "isc". Keys are matched case-insensitively; valid keys are: apache-2.0, mit.`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

const testAccLicensesAddress = "data.ghost_licenses.test"

func testAccLicensesConfig(attrs string) string {
	return "data \"ghost_licenses\" \"test\" {\n  " + attrs + "\n}\n"
}

func TestAccLicensesDataSource(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccLicensesConfig(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccLicensesAddress, "id", "licenses"),
					resource.TestCheckNoResourceAttr(testAccLicensesAddress, "license.key"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "licenses.1.key", "apache-2.0"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "licenses.1.name", "Apache License 2.0"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "licenses.1.spdx_id", "Apache-2.0"),
					testAccCheckLicenseKeys,
				),
			},
			{
				Config: testAccLicensesConfig(`key = "mit"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(testAccLicensesAddress, "license.key", "mit"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "license.name", "MIT License"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "license.spdx_id", "MIT"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "license.html_url", "http://choosealicense.com/licenses/mit/"),
					resource.TestMatchResourceAttr(testAccLicensesAddress, "license.description", regexp.MustCompile(`\S`)),
					resource.TestMatchResourceAttr(testAccLicensesAddress, "license.implementation", regexp.MustCompile(`\S`)),
					resource.TestCheckTypeSetElemAttr(testAccLicensesAddress, "license.permissions.*", "commercial-use"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "license.conditions.#", "1"),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "license.conditions.0", "include-copyright"),
					resource.TestCheckTypeSetElemAttr(testAccLicensesAddress, "license.limitations.*", "warranty"),
					resource.TestMatchResourceAttr(testAccLicensesAddress, "license.body",
						regexp.MustCompile(`^MIT License\n\nCopyright \(c\) \[year\] \[fullname\]\n`)),
					resource.TestCheckResourceAttr(testAccLicensesAddress, "license.featured", "true"),
					testAccCheckLicenseKeys,
				),
			},
		},
	})
}

// Ghost matches keys case-insensitively, so SPDX IDs work, and returns its
// own lower-case key.
func TestAccLicensesDataSource_keys(t *testing.T) {
	for _, tc := range []struct{ key, want, featured string }{
		{"MIT", "mit", "true"},
		{"Apache-2.0", "apache-2.0", "true"},
		{"GPL-3.0", "gpl-3.0", "true"},
		{"bsd-3-clause", "bsd-3-clause", "false"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: testAccLicensesConfig(fmt.Sprintf("key = %q", tc.key)),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(testAccLicensesAddress, "key", tc.key),
						resource.TestCheckResourceAttr(testAccLicensesAddress, "license.key", tc.want),
						resource.TestCheckResourceAttr(testAccLicensesAddress, "license.featured", tc.featured),
						resource.TestMatchResourceAttr(testAccLicensesAddress, "license.body", regexp.MustCompile(`\S`)),
					),
				}},
			})
		})
	}
}

func TestAccLicensesDataSource_notFound(t *testing.T) {
	steps := make([]resource.TestStep, 0, 4)
	for _, key := range []string{"tf-acc-no-such-license", "isc", "mit.txt", " mit"} {
		steps = append(steps, resource.TestStep{
			Config: testAccLicensesConfig(fmt.Sprintf("key = %q", key)),
			ExpectError: regexp.MustCompile(`(?s)License not found.*no\s+license\s+with\s+key.*case-insensitively.*` +
				`agpl-3.0,\s+apache-2.0,.*unlicense\.`),
		})
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
}

// A repository created with a key from the data source (looked up by its
// SPDX ID) starts with the license's body as its LICENSE, with [year] and
// [fullname] filled in.
func TestAccLicensesDataSource_repository(t *testing.T) {
	repoName := acctest.RandomWithPrefix("tf-acc-license")
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckRepositoryDestroy,
		Steps: []resource.TestStep{{
			Config: testAccLicensesConfig(`key = "MIT"`) + `
resource "ghost_repository" "test" {
  name             = "` + repoName + `"
  auto_init        = true
  license_template = data.ghost_licenses.test.license.key
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("ghost_repository.test", "license_template", "mit"),
				func(s *terraform.State) error {
					body := s.RootModule().Resources[testAccLicensesAddress].Primary.Attributes["license.body"]
					_, repo := testAccRepository(t, repoName)
					git := testAccGit(t, repo)
					git("clone", "-q", "--depth", "1", repo.CloneURL, ".")
					got, err := os.ReadFile(filepath.Join(git("rev-parse", "--show-toplevel"), "LICENSE"))
					if err != nil {
						return err
					}
					return testAccCheckLicenseText(body, string(got))
				},
			),
		}},
	})
}

// testAccCheckLicenseText checks that got is body with [year] replaced by
// this year (or last year, should the test cross midnight on 31 December)
// and [fullname] by a non-empty single-line name.
func testAccCheckLicenseText(body, got string) error {
	pattern := regexp.QuoteMeta(body)
	pattern = strings.ReplaceAll(pattern, regexp.QuoteMeta("[year]"), `(\d{4})`)
	pattern = strings.ReplaceAll(pattern, regexp.QuoteMeta("[fullname]"), `[^\n\[\]]+`)
	m := regexp.MustCompile(`\A` + pattern + `\z`).FindStringSubmatch(got)
	if m == nil {
		return fmt.Errorf("LICENSE (%d bytes) isn't license.body (%d bytes) with [year] and [fullname] filled in:\n%s",
			len(got), len(body), got)
	}
	year := time.Now().Year()
	for _, y := range m[1:] {
		if y != strconv.Itoa(year) && y != strconv.Itoa(year-1) {
			return fmt.Errorf("LICENSE year is %s, want %d", y, year)
		}
	}
	return nil
}

// testAccCheckLicenseKeys checks that keys and licenses list the known
// licenses in order.
func testAccCheckLicenseKeys(s *terraform.State) error {
	attrs := s.RootModule().Resources[testAccLicensesAddress].Primary.Attributes
	if attrs["keys.#"] != strconv.Itoa(len(testAccLicenseKeys)) || attrs["licenses.#"] != attrs["keys.#"] {
		return fmt.Errorf("keys.# = %s and licenses.# = %s, want %d", attrs["keys.#"], attrs["licenses.#"], len(testAccLicenseKeys))
	}
	for i, want := range testAccLicenseKeys {
		if k := attrs[fmt.Sprintf("keys.%d", i)]; k != want {
			return fmt.Errorf("keys.%d = %q, want %q", i, k, want)
		}
		if k := attrs[fmt.Sprintf("licenses.%d.key", i)]; k != want {
			return fmt.Errorf("licenses.%d.key = %q, want %q", i, k, want)
		}
		if attrs[fmt.Sprintf("licenses.%d.name", i)] == "" || attrs[fmt.Sprintf("licenses.%d.spdx_id", i)] == "" {
			return fmt.Errorf("licenses.%d has an empty name or spdx_id", i)
		}
	}
	return nil
}
