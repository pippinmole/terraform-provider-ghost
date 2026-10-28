package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestCodeownersErrorsDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no repository", ``, `(?s)"repository" is required`},
		{"no slash", `repository = "alice"`, `must be "owner/slug"`},
		{"two slashes", `repository = "alice/repo/x"`, `must be "owner/slug"`},
		{"empty ref", `repository = "alice/repo"` + "\n" + `ref = ""`, `(?s)string length must be at least 1`},
		{"computed path", `repository = "alice/repo"` + "\n" + `path = "CODEOWNERS"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed errors", `repository = "alice/repo"` + "\n" + `errors = []`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed id", `repository = "alice/repo"` + "\n" + `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_codeowners_errors\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testCodeownersServer serves the repository lookup, which reports the
// repository as renamed to Alice/renamed, and the native codeowners route,
// which answers with status and body after checking the ref query (wantRef,
// or none when it is ""). A status of 0 makes the repository lookup 404, and
// then the codeowners route must not be called. Output n is the length of
// errors, which fails for a null list.
func testCodeownersServer(t *testing.T, wantRef string, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/repos/alice/repo":
			if status == 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":7,"name":"renamed","full_name":"Alice/renamed","owner":{"login":"Alice"}}`))
		case "/api/repositories/Alice/renamed/codeowners":
			if status == 0 {
				t.Errorf("checked the CODEOWNERS file of a repository that wasn't found")
			}
			if got, ok := r.URL.Query()["ref"]; wantRef == "" && ok || wantRef != "" && (len(got) != 1 || got[0] != wantRef) {
				t.Errorf("ref = %v (raw query %q), want %q", got, r.URL.RawQuery, wantRef)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)

	refLine := ""
	if wantRef != "" {
		refLine = fmt.Sprintf("ref = %q", wantRef)
	}

	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

data "ghost_codeowners_errors" "test" {
  repository = "alice/repo"
  %s
}

output "n" {
  value = length(data.ghost_codeowners_errors.test.errors)
}
`, srv.URL, refLine)
}

func TestCodeownersErrorsDataSourceRead(t *testing.T) {
	const d = "data.ghost_codeowners_errors.test"
	const twoErrors = `{"path":".github/CODEOWNERS","errors":[` +
		`{"line":3,"column":13,"kind":"Unknown owner","source":"docs/       @nobody","suggestion":null,` +
		`"message":"Unknown owner on line 3: make sure @nobody exists and has write access to the repository","path":".github/CODEOWNERS"},` +
		`{"line":6,"column":1,"kind":"Invalid pattern","source":"[bad @alice","suggestion":"\\[bad",` +
		`"message":"Invalid pattern on line 6","path":".github/CODEOWNERS"}]}`

	for _, tc := range []struct {
		name, ref, body string
		check           resource.TestCheckFunc
	}{
		{"errors", "", twoErrors, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "id", "Alice/renamed"),
			resource.TestCheckResourceAttr(d, "repository", "alice/repo"),
			resource.TestCheckResourceAttr(d, "repository_id", "7"),
			resource.TestCheckNoResourceAttr(d, "ref"),
			resource.TestCheckResourceAttr(d, "path", ".github/CODEOWNERS"),
			resource.TestCheckResourceAttr(d, "errors.#", "2"),
			resource.TestCheckResourceAttr(d, "errors.0.line", "3"),
			resource.TestCheckResourceAttr(d, "errors.0.column", "13"),
			resource.TestCheckResourceAttr(d, "errors.0.kind", "Unknown owner"),
			resource.TestCheckResourceAttr(d, "errors.0.source", "docs/       @nobody"),
			resource.TestCheckNoResourceAttr(d, "errors.0.suggestion"),
			resource.TestCheckResourceAttr(d, "errors.0.message",
				"Unknown owner on line 3: make sure @nobody exists and has write access to the repository"),
			resource.TestCheckResourceAttr(d, "errors.0.path", ".github/CODEOWNERS"),
			resource.TestCheckResourceAttr(d, "errors.1.line", "6"),
			resource.TestCheckResourceAttr(d, "errors.1.column", "1"),
			resource.TestCheckResourceAttr(d, "errors.1.kind", "Invalid pattern"),
			resource.TestCheckResourceAttr(d, "errors.1.suggestion", `\[bad`),
			resource.TestCheckOutput("n", "2"),
		)},
		{"ref with slash", "feat/x y", `{"path":"CODEOWNERS","errors":[]}`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "ref", "feat/x y"),
			// The id is the repository, whatever the ref.
			resource.TestCheckResourceAttr(d, "id", "Alice/renamed"),
			resource.TestCheckResourceAttr(d, "path", "CODEOWNERS"),
			resource.TestCheckOutput("n", "0"),
		)},
		{"awkward ref", "feat/a b&c", `{"path":"CODEOWNERS","errors":[]}`, resource.TestCheckResourceAttr(d, "ref", "feat/a b&c")},
		// An empty file is valid: only path tells it from no file.
		{"empty file", "", `{"path":"docs/CODEOWNERS","errors":[]}`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "path", "docs/CODEOWNERS"),
			resource.TestCheckOutput("n", "0"),
		)},
		// No file: path is null, errors an empty list (not null).
		{"no file", "", `{"path":null,"errors":[]}`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckNoResourceAttr(d, "path"),
			resource.TestCheckOutput("n", "0"),
		)},
		{"null errors", "", `{"path":"docs/CODEOWNERS","errors":null}`, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "path", "docs/CODEOWNERS"),
			resource.TestCheckOutput("n", "0"),
		)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config: testCodeownersServer(t, tc.ref, http.StatusOK, tc.body),
					Check:  tc.check,
				}},
			})
		})
	}
}

func TestCodeownersErrorsDataSourceReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name, ref, body string
		status          int
		expect          string
	}{
		{"unknown ref", "nope", `{"statusCode":404,"message":"Branch not found: nope"}`, http.StatusNotFound,
			`(?s)Ref not found.*Alice/renamed has no branch, tag or commit named "nope".*Branch not found: nope`},
		{"tag as full ref", "refs/tags/v1", `{"statusCode":404,"message":"Branch not found: refs/tags/v1"}`, http.StatusNotFound,
			`(?s)Ref not found.*name a tag without refs/tags/`},
		{"wrong case", "Main", `{"statusCode":404,"message":"Branch not found: Main"}`, http.StatusNotFound,
			`(?s)Ref not found.*ref\s+=\s+"Main".*case-sensitive`},
		{"empty repository", "main", `{"statusCode":404,"message":"Branch not found: main"}`, http.StatusNotFound,
			`(?s)Ref not found.*empty\s+repository`},
		// Without a ref, a 404 (the repository deleted between the calls)
		// isn't blamed on the ref.
		{"not found without ref", "", `{"statusCode":404,"message":"Repository not found"}`, http.StatusNotFound,
			`(?s)Error checking the CODEOWNERS file.*Repository not\s+found`},
		{"unknown repository", "", "", 0,
			`(?s)Repository not found.*repository\s+=\s+"alice/repo".*No repository named alice/repo`},
		{"forbidden", "", `{"statusCode":403,"message":"boom"}`, http.StatusForbidden,
			`(?s)Error checking the CODEOWNERS file.*boom`},
		{"server error", "", `{"statusCode":500,"message":"boom"}`, http.StatusInternalServerError,
			`(?s)Error checking the CODEOWNERS file.*boom`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testCodeownersServer(t, tc.ref, tc.status, tc.body),
					ExpectError: regexp.MustCompile(tc.expect),
				}},
			})
		})
	}
}

func TestRefNotFoundDetail(t *testing.T) {
	for _, tc := range []struct {
		ref, want, notWant string
	}{
		{"refs/tags/v1", "name a tag without refs/tags/", "case-sensitive"},
		{"refs/heads/x", "", "refs/tags/"},
		{"Main", "case-sensitive", "refs/tags/"},
		{"main", "", "case-sensitive"},
		{"a1b2c3d", "", "case-sensitive"},
		{"refs/tags/V1", "name a tag without refs/tags/", "case-sensitive"},
		{"refs/remotes/origin/main", "name a tag without refs/tags/", ""},
	} {
		got := refNotFoundDetail("a/b", tc.ref, fmt.Errorf("404"))
		if !strings.Contains(got, `named "`+tc.ref+`"`) || (tc.want != "" && !strings.Contains(got, tc.want)) ||
			(tc.notWant != "" && strings.Contains(got, tc.notWant)) {
			t.Errorf("refNotFoundDetail(%q) = %q", tc.ref, got)
		}
	}
}
