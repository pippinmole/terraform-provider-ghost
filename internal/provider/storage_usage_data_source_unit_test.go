package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestStorageUsageDataSourceConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"empty owner", `owner = ""`, `(?s)string length must be at least 1`},
		{"slash", `owner = "alice/repo"`, `must be a single login`},
		{"space", `owner = "alice "`, `must be a single login`},
		{"computed id", `id = "x"`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed used_bytes", `used_bytes = 1`, `(?s)Invalid Configuration for Read-Only Attribute`},
		{"computed fork", `fork = { used_bytes = 1, quota_bytes = 2 }`, `(?s)Invalid Configuration for Read-Only Attribute`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      "data \"ghost_storage_usage\" \"test\" {\n" + tt.config + "\n}\n",
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// testStorageUsageServer serves /user as Alice, the user bob (and a 404 for
// the user nobody), and the storage route for wantPath, which answers with status and body. Any other request fails the
// test.
func testStorageUsageServer(t *testing.T, wantPath string, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/user":
			_, _ = w.Write([]byte(`{"login":"Alice","id":1}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/users/bob":
			_, _ = w.Write([]byte(`{"login":"bob","id":2,"type":"User"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/users/nobody":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodGet && r.URL.EscapedPath() == wantPath:
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)

	return srv.URL
}

func testStorageUsageConfig(baseURL, ownerLine string) string {
	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

data "ghost_storage_usage" "test" {
  %s
}

output "headroom" {
  value = data.ghost_storage_usage.test.quota_bytes - data.ghost_storage_usage.test.used_bytes
}
`, baseURL, ownerLine)
}

func TestStorageUsageDataSourceRead(t *testing.T) {
	const d = "data.ghost_storage_usage.test"
	const quotas = `{"usedBytes":12,"quotaBytes":2147483648,"maxAssetBytes":2147483648,` +
		`"fork":{"usedBytes":21023934,"quotaBytes":5368709120},"lfs":{"usedBytes":0,"quotaBytes":2147483648},` +
		`"asset":{"usedBytes":332169,"quotaBytes":2147483648}}`

	for _, tc := range []struct {
		name, ownerLine, path, body string
		check                       resource.TestCheckFunc
	}{
		{"default owner", "", "/api/storage/Alice", quotas, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "id", "Alice"),
			resource.TestCheckResourceAttr(d, "owner", "Alice"),
			resource.TestCheckResourceAttr(d, "used_bytes", "12"),
			resource.TestCheckResourceAttr(d, "quota_bytes", "2147483648"),
			resource.TestCheckResourceAttr(d, "max_asset_bytes", "2147483648"),
			resource.TestCheckResourceAttr(d, "fork.used_bytes", "21023934"),
			resource.TestCheckResourceAttr(d, "fork.quota_bytes", "5368709120"),
			resource.TestCheckResourceAttr(d, "lfs.used_bytes", "0"),
			resource.TestCheckResourceAttr(d, "lfs.quota_bytes", "2147483648"),
			resource.TestCheckResourceAttr(d, "asset.used_bytes", "332169"),
			resource.TestCheckResourceAttr(d, "asset.quota_bytes", "2147483648"),
			resource.TestCheckOutput("headroom", "2147483636"),
		)},
		// The owner is kept as written: Ghost matches it case-insensitively
		// and doesn't return its canonical case.
		{"explicit owner", `owner = "My-Org"`, "/api/storage/My-Org", quotas, resource.ComposeAggregateTestCheckFunc(
			resource.TestCheckResourceAttr(d, "id", "My-Org"),
			resource.TestCheckResourceAttr(d, "owner", "My-Org"),
			resource.TestCheckResourceAttr(d, "used_bytes", "12"),
		)},
		{"no quotas", `owner = "alice"`, "/api/storage/alice",
			`{"usedBytes":1,"quotaBytes":null,"maxAssetBytes":5,"fork":{"usedBytes":2,"quotaBytes":null},` +
				`"lfs":{"usedBytes":3,"quotaBytes":null},"asset":{"usedBytes":4,"quotaBytes":null}}`,
			resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(d, "used_bytes", "1"),
				resource.TestCheckNoResourceAttr(d, "quota_bytes"),
				resource.TestCheckResourceAttr(d, "max_asset_bytes", "5"),
				resource.TestCheckResourceAttr(d, "fork.used_bytes", "2"),
				resource.TestCheckNoResourceAttr(d, "fork.quota_bytes"),
				resource.TestCheckResourceAttr(d, "lfs.used_bytes", "3"),
				resource.TestCheckNoResourceAttr(d, "lfs.quota_bytes"),
				resource.TestCheckResourceAttr(d, "asset.used_bytes", "4"),
				resource.TestCheckNoResourceAttr(d, "asset.quota_bytes"),
			)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := testStorageUsageServer(t, tc.path, http.StatusOK, tc.body)
			config := testStorageUsageConfig(url, tc.ownerLine)
			if tc.name == "no quotas" {
				// headroom can't be computed from a null quota.
				config = regexp.MustCompile(`(?s)output "headroom".*`).ReplaceAllString(config, "")
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps:                    []resource.TestStep{{Config: config, Check: tc.check}},
			})
		})
	}
}

func TestStorageUsageDataSourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name, owner string
		status      int
		body        string
		expect      string
	}{
		{"not shown", "bob", http.StatusNotFound, `{"statusCode":404,"message":"User bob not found"}`,
			`(?s)Storage usage not found.*only\s+to\s+the\s+account\s+itself:\s+"bob"\s+must\s+be.*User\s+bob\s+not\s+found`},
		{"unknown", "nobody", http.StatusNotFound, `{"statusCode":404,"message":"User nobody not found"}`,
			`(?s)Storage usage not found.*No\s+user\s+or\s+organization\s+named\s+"nobody"\s+exists`},
		{"server error", "bob", http.StatusInternalServerError, `{"statusCode":500,"message":"boom"}`, `(?s)Error reading storage usage.*boom`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := testStorageUsageServer(t, "/api/storage/"+tc.owner, tc.status, tc.body)
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testStorageUsageConfig(url, fmt.Sprintf("owner = %q", tc.owner)),
					ExpectError: regexp.MustCompile(tc.expect),
				}},
			})
		})
	}
}
