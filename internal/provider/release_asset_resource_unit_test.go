package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/pippinmole/terraform-provider-ghost/internal/client"
)

func sha256Hex(b string) string {
	h := sha256.Sum256([]byte(b))

	return hex.EncodeToString(h[:])
}

func TestParseReleaseAssetID(t *testing.T) {
	repository, id, err := parseReleaseAssetID("alice/repo:9007199254740993")
	if err != nil || repository != "alice/repo" || id != 9007199254740993 {
		t.Errorf("got %q, %d, %v", repository, id, err)
	}
	for _, bad := range []string{"", "alice/repo", "alice/repo:", "alice/repo:abc", ":1", "alice:1", "a/b/c:1", "alice/repo:0", "alice/repo:-1", "alice/repo:1:2"} {
		if _, _, err := parseReleaseAssetID(bad); err == nil || !strings.Contains(err.Error(), "owner/repo:asset_id") {
			t.Errorf("parseReleaseAssetID(%q) err = %v", bad, err)
		}
	}
}

func TestAssetNameProblem(t *testing.T) {
	for _, ok := range []string{"a.txt", "a b.txt", "?x", "%2F", ".env", "CON", "ü", strings.Repeat("é", 255), strings.Repeat("a", 255), "..."} {
		if p := assetNameProblem(ok); p != "" {
			t.Errorf("assetNameProblem(%q) = %q, want accepted", ok, p)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 256), " a", "a ", "a\n", "a/b", `a\b`, "a\tb", "\x00", "\x7f", ".", ".."} {
		if p := assetNameProblem(bad); p == "" {
			t.Errorf("assetNameProblem(%q) accepted", bad)
		}
	}
}

func TestAssetContentTypePattern(t *testing.T) {
	for _, ok := range []string{"text/plain", "application/vnd.foo+json", "Text/Plain", "x-a/b.c-d"} {
		if !assetContentTypePattern.MatchString(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"nonsense", "text/", "/plain", "text/plain; charset=utf-8", "text/plain ", "a/b/c", ""} {
		if assetContentTypePattern.MatchString(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReleaseAssetFromRemote(t *testing.T) {
	label := "x"
	asset := &client.ReleaseAsset{ID: 5, Name: "n", ContentType: "text/plain", Size: 3, BrowserDownloadURL: "https://w/o/r/releases/download/v1/a%20b"}
	for _, tt := range []struct {
		prior  types.String
		remote *string
		want   types.String
	}{
		{types.StringNull(), nil, types.StringNull()},
		{types.StringValue(""), nil, types.StringValue("")},
		{types.StringNull(), &label, types.StringValue("x")},
	} {
		m := releaseAssetResourceModel{Repository: types.StringValue("alice/renamed"), Label: tt.prior, ContentSHA256: types.StringValue("h")}
		a := *asset
		a.Label = tt.remote
		m.fromRemote(&a)
		if !m.Label.Equal(tt.want) {
			t.Errorf("label %v from %v = %v", tt.remote, tt.prior, m.Label)
		}
		if m.ID.ValueString() != "alice/renamed:5" || m.BrowserDownloadURL.ValueString() != asset.BrowserDownloadURL || m.ContentSHA256.ValueString() != "h" {
			t.Errorf("model = %+v", m)
		}
	}
}

func TestPlanContentHash(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(file, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		m       releaseAssetResourceModel
		sum     string
		known   bool
		wantErr string
	}{
		{"content", sourceModel("héllo\n", "", ""), sha256Hex("héllo\n"), true, ""},
		{"base64", sourceModel("", base64.StdEncoding.EncodeToString([]byte{0, 1, 2}), ""), sha256Hex("\x00\x01\x02"), true, ""},
		{"file", sourceModel("", "", file), sha256Hex("abc"), true, ""},
		{"missing file", sourceModel("", "", filepath.Join(dir, "nope")), "", false, ""},
		{"unknown file", releaseAssetResourceModel{File: types.StringUnknown(), Content: types.StringNull(), ContentBase64: types.StringNull()}, "", false, ""},
		{"empty file", sourceModel("", "", empty), "", false, "Empty asset"},
		{"bad base64", sourceModel("", "not base64!", ""), "", false, "Invalid base64"},
		{"directory", sourceModel("", "", dir), "", false, "Can't read file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum, _, known, diags := planContentHash(tt.m)
			if tt.wantErr != "" {
				if !diags.HasError() || !strings.Contains(diags.Errors()[0].Summary(), tt.wantErr) {
					t.Fatalf("diags = %v, want %q", diags, tt.wantErr)
				}
				return
			}
			if diags.HasError() || sum != tt.sum || known != tt.known {
				t.Errorf("got %q, %v, %v; want %q, %v", sum, known, diags, tt.sum, tt.known)
			}
		})
	}
}

// sourceModel returns a model whose content comes from whichever of content,
// contentBase64 and file is non-empty.
func sourceModel(content, contentBase64, file string) releaseAssetResourceModel {
	str := func(s string) types.String {
		if s == "" {
			return types.StringNull()
		}
		return types.StringValue(s)
	}

	return releaseAssetResourceModel{Content: str(content), ContentBase64: str(contentBase64), File: str(file)}
}

// assetModel returns a fully known ghost_release_asset in alice/repo (ID 2)
// with the given content source.
func assetModel(src releaseAssetResourceModel, sum string) releaseAssetResourceModel {
	m := src
	m.ID = types.StringValue("alice/repo:5")
	m.Repository = types.StringValue("alice/repo")
	m.RepositoryID = types.Int64Value(2)
	m.ReleaseID = types.Int64Value(1)
	m.Name = types.StringValue("a.txt")
	m.Label = types.StringNull()
	m.ContentType = types.StringValue(defaultAssetContentType)
	m.ContentSHA256 = types.StringValue(sum)
	m.Size = types.Int64Value(1)
	m.AssetID = types.Int64Value(5)
	for _, s := range []*types.String{&m.NodeID, &m.URL, &m.BrowserDownloadURL, &m.CreatedAt, &m.UpdatedAt} {
		*s = types.StringValue("v")
	}
	m.DownloadCount = types.Int64Value(0)

	return m
}

type modifyPlanResult struct {
	plan    releaseAssetResourceModel
	replace bool
	diags   diag.Diagnostics
}

// modifyReleaseAssetPlan runs ModifyPlan from state (nil to create) to plan
// (nil to destroy), the computed attributes of plan given as Terraform
// would: unknown, or the state's where UseStateForUnknown applies.
func modifyReleaseAssetPlan(t *testing.T, c *client.Client, state, plan *releaseAssetResourceModel) modifyPlanResult {
	t.Helper()
	ctx := context.Background()
	r := &ReleaseAssetResource{client: c}
	var schemaResp fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)
	null := tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)

	req := fwresource.ModifyPlanRequest{
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: null},
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: null},
	}
	if state != nil {
		if diags := req.State.Set(ctx, state); diags.HasError() {
			t.Fatal(diags)
		}
	}
	if plan != nil {
		p := *plan
		p.ContentSHA256 = types.StringUnknown()
		p.Size = types.Int64Unknown()
		if diags := req.Plan.Set(ctx, &p); diags.HasError() {
			t.Fatal(diags)
		}
	}
	resp := &fwresource.ModifyPlanResponse{Plan: req.Plan}
	r.ModifyPlan(ctx, req, resp)

	var res modifyPlanResult
	res.diags = resp.Diagnostics
	if plan != nil && !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.Plan.Get(ctx, &res.plan)...)
	}
	for _, p := range resp.RequiresReplace {
		if p.Equal(path.Root("content_sha256")) || p.Equal(path.Root("repository")) {
			res.replace = true
		}
	}

	return res
}

// repoServer serves alice/repo (ID 2), alice/renamed (ID 2) and bob/other
// (ID 3), and counts the requests.
func repoServer(t *testing.T) (*client.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch strings.ToLower(r.URL.Path) {
		case "/api/v3/repos/alice/repo", "/api/v3/repos/alice/renamed":
			_, _ = io.WriteString(w, `{"id":2,"full_name":"alice/renamed"}`)
		case "/api/v3/repos/bob/other":
			_, _ = io.WriteString(w, `{"id":3,"full_name":"bob/other"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := client.New(srv.URL, "x")
	if err != nil {
		t.Fatal(err)
	}

	return c, &calls
}

func TestReleaseAssetModifyPlan(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fileX := write("x", "x")
	fileX2 := write("x2", "x")
	fileY := write("y", "y")
	missing := filepath.Join(dir, "missing")
	c, _ := repoServer(t)

	t.Run("create hashes every source", func(t *testing.T) {
		for _, src := range []releaseAssetResourceModel{
			sourceModel("x", "", ""), sourceModel("", base64.StdEncoding.EncodeToString([]byte("x")), ""), sourceModel("", "", fileX),
		} {
			plan := assetModel(src, "")
			res := modifyReleaseAssetPlan(t, c, nil, &plan)
			if res.diags.HasError() || res.plan.ContentSHA256.ValueString() != sha256Hex("x") || res.plan.Size.ValueInt64() != 1 {
				t.Errorf("got %v %v %v", res.plan.ContentSHA256, res.plan.Size, res.diags)
			}
		}
	})

	t.Run("create with missing file", func(t *testing.T) {
		plan := assetModel(sourceModel("", "", missing), "")
		res := modifyReleaseAssetPlan(t, c, nil, &plan)
		if res.diags.HasError() || !res.plan.ContentSHA256.IsUnknown() || !res.plan.Size.IsUnknown() {
			t.Errorf("got %v %v %v", res.plan.ContentSHA256, res.plan.Size, res.diags)
		}
	})

	t.Run("empty content is an error", func(t *testing.T) {
		plan := assetModel(sourceModel("", "", write("empty", "")), "")
		res := modifyReleaseAssetPlan(t, c, nil, &plan)
		if !res.diags.HasError() {
			t.Error("no error")
		}
	})

	tests := []struct {
		name        string
		state, plan releaseAssetResourceModel
		replace     bool
		unknownHash bool
	}{
		{"unchanged", sourceModel("x", "", ""), sourceModel("x", "", ""), false, false},
		{"content changed", sourceModel("x", "", ""), sourceModel("y", "", ""), true, false},
		{"file changed on disk", sourceModel("", "", fileY), sourceModel("", "", fileY), true, false},
		{"content to file, same bytes", sourceModel("x", "", ""), sourceModel("", "", fileX), false, false},
		{"file to base64, same bytes", sourceModel("", "", fileX), sourceModel("", base64.StdEncoding.EncodeToString([]byte("x")), ""), false, false},
		{"file path changed, same bytes", sourceModel("", "", fileX), sourceModel("", "", fileX2), false, false},
		{"source changed, different bytes", sourceModel("x", "", ""), sourceModel("", "", fileY), true, false},
		{"missing file, source unchanged", sourceModel("", "", missing), sourceModel("", "", missing), false, false},
		{"missing file, source changed", sourceModel("x", "", ""), sourceModel("", "", missing), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The state of "file changed on disk" was uploaded from "x".
			state := assetModel(tt.state, sha256Hex("x"))
			plan := assetModel(tt.plan, "")
			res := modifyReleaseAssetPlan(t, c, &state, &plan)
			if res.diags.HasError() {
				t.Fatal(res.diags)
			}
			if res.replace != tt.replace {
				t.Errorf("replace = %v, want %v", res.replace, tt.replace)
			}
			if res.plan.ContentSHA256.IsUnknown() != tt.unknownHash {
				t.Errorf("content_sha256 = %v", res.plan.ContentSHA256)
			}
			if !tt.replace && !res.plan.ContentSHA256.Equal(state.ContentSHA256) {
				t.Errorf("content_sha256 = %v, want the state's", res.plan.ContentSHA256)
			}
		})
	}

	t.Run("repository", func(t *testing.T) {
		for _, tt := range []struct {
			repository string
			replace    bool
			lookups    int32
		}{
			{"alice/renamed", false, 1},
			{"Alice/Repo", false, 1},
			{"bob/other", true, 1},
			{"alice/gone", true, 1},
		} {
			c, calls := repoServer(t)
			state := assetModel(sourceModel("x", "", ""), sha256Hex("x"))
			plan := state
			plan.Repository = types.StringValue(tt.repository)
			res := modifyReleaseAssetPlan(t, c, &state, &plan)
			if res.diags.HasError() || res.replace != tt.replace || calls.Load() != tt.lookups {
				t.Errorf("%s: replace = %v, lookups = %d, diags = %v", tt.repository, res.replace, calls.Load(), res.diags)
			}
			if !tt.replace && (!res.plan.ID.IsUnknown() || !res.plan.BrowserDownloadURL.IsUnknown()) {
				t.Errorf("%s: id and browser_download_url should be unknown", tt.repository)
			}
		}
	})

	t.Run("name change is in place", func(t *testing.T) {
		state := assetModel(sourceModel("x", "", ""), sha256Hex("x"))
		plan := state
		plan.Name = types.StringValue("b.txt")
		res := modifyReleaseAssetPlan(t, c, &state, &plan)
		if res.replace || !res.plan.BrowserDownloadURL.IsUnknown() || !res.plan.UpdatedAt.IsUnknown() || res.plan.ID.IsUnknown() {
			t.Errorf("plan = %+v", res.plan)
		}
	})

	t.Run("label empty and null are the same", func(t *testing.T) {
		state := assetModel(sourceModel("x", "", ""), sha256Hex("x"))
		plan := state
		plan.Label = types.StringValue("")
		res := modifyReleaseAssetPlan(t, c, &state, &plan)
		if res.replace || res.plan.UpdatedAt.IsUnknown() {
			t.Errorf("plan = %+v", res.plan)
		}
	})

	t.Run("destroy", func(t *testing.T) {
		state := assetModel(sourceModel("", "", missing), sha256Hex("x"))
		if res := modifyReleaseAssetPlan(t, nil, &state, nil); len(res.diags) != 0 {
			t.Errorf("diags = %v", res.diags)
		}
	})
}

// TestReleaseAssetConfigValidation runs offline: the validators reject the
// config before the provider is configured.
func TestReleaseAssetConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config string
		expect string
	}{
		{"no source", `content = null`, `(?s)Exactly one of these attributes must be configured`},
		{"two sources", `file = "x"`, `(?s)Exactly one of these attributes must be configured`},
		{"three sources", "file = \"x\"\n  content_base64 = \"eA==\"", `(?s)Exactly one of these attributes must be configured`},
		{"missing name", `name = null`, `(?i)required`},
		{"missing repository", `repository = null`, `(?i)required`},
		{"missing release", `release_id = null`, `(?i)required`},
		{"release zero", `release_id = 0`, `(?i)at least 1`},
		{"slash in name", `name = "a/b"`, `path separator`},
		{"dot dot", `name = ".."`, `isn't a file name`},
		{"leading space", `name = " a"`, `trims whitespace`},
		{"long name", `name = "` + strings.Repeat("a", 256) + `"`, `256 characters`},
		{"long label", `label = "` + strings.Repeat("a", 256) + `"`, `(?i)length`},
		{"label space", `label = "x "`, `whitespace`},
		{"content type", `content_type = "nonsense"`, `type/subtype`},
		{"content type parameters", `content_type = "text/plain; charset=utf-8"`, `type/subtype`},
		{"bad base64", "content = null\n  content_base64 = \"not base64!\"", `(?i)base64`},
		{"empty content", `content = ""`, `(?i)length`},
		{"bad repository", `repository = "a/b/c"`, `must be "owner/slug"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      testAccReleaseAssetUnitConfig(tt.config),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// TestReleaseAssetConfigValidationLimits checks values at the limits pass
// validation: planning then fails only because the provider has no token.
func TestReleaseAssetConfigValidationLimits(t *testing.T) {
	t.Setenv(envToken, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccReleaseAssetUnitConfig(`name = "` + strings.Repeat("é", 255) + `"
  label = "` + strings.Repeat("l", 255) + `"
  content_type = "Text/Plain"`),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?i)token`),
		}},
	})
}

// testAccReleaseAssetUnitConfig returns a ghost_release_asset with valid
// arguments, with override replacing some of them or adding more.
func testAccReleaseAssetUnitConfig(override string) string {
	set := map[string]bool{}
	for _, line := range strings.Split(override, "\n") {
		if k, _, ok := strings.Cut(line, "="); ok {
			set[strings.TrimSpace(k)] = true
		}
	}
	var b strings.Builder
	for _, kv := range [][2]string{{"repository", `"alice/repo"`}, {"release_id", "1"}, {"name", `"a.txt"`}, {"content", `"x"`}} {
		if !set[kv[0]] {
			fmt.Fprintf(&b, "  %s = %s\n", kv[0], kv[1])
		}
	}

	return fmt.Sprintf(`
resource "ghost_release_asset" "test" {
%s  %s
}
`, b.String(), override)
}

// assetStub is a Ghost stub for the stub-server tests: one repository,
// alice/repo (ID 2), with release 1.
type assetStub struct {
	t       *testing.T
	upload  func(w http.ResponseWriter, r *http.Request)
	asset   string // the GET asset response; "" for 404
	patches []string
	uploads atomic.Int32
}

func (s *assetStub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v3/repos/alice/repo":
		_, _ = io.WriteString(w, `{"id":2,"full_name":"alice/repo"}`)
	case strings.HasPrefix(r.URL.Path, "/api/uploads/"):
		s.uploads.Add(1)
		s.upload(w, r)
	case r.URL.Path == "/api/v3/repos/alice/repo/releases/assets/5" && r.Method == http.MethodPatch:
		b, _ := io.ReadAll(r.Body)
		s.patches = append(s.patches, string(b))
		_, _ = io.WriteString(w, testAssetJSON(`null`))
		s.asset = testAssetJSON(`null`)
	case r.URL.Path == "/api/v3/repos/alice/repo/releases/assets/5" && r.Method == http.MethodGet && s.asset != "":
		_, _ = io.WriteString(w, s.asset)
	case r.URL.Path == "/api/v3/repos/alice/repo/releases/assets/5" && r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/v3/repos/alice/repo/releases/1/assets":
		if r.URL.Query().Get("page") == "1" {
			_, _ = io.WriteString(w, `[{"id":5,"name":"a.txt"}]`)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	}
}

func testAssetJSON(label string) string {
	return `{"id":5,"node_id":"RA_5","url":"u","browser_download_url":"b","name":"a.txt","label":` + label +
		`,"content_type":"application/octet-stream","size":1,"download_count":0,"created_at":"c","updated_at":"u"}`
}

func (s *assetStub) config(srvURL, extra string) string {
	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

resource "ghost_release_asset" "test" {
  repository = "alice/repo"
  release_id = 1
  name       = "a.txt"
  content    = "x"
  %s
}
`, srvURL, extra)
}

func newAssetStub(t *testing.T, upload func(w http.ResponseWriter, r *http.Request)) (*assetStub, string) {
	t.Helper()
	s := &assetStub{t: t, upload: upload}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)

	return s, srv.URL
}

func TestReleaseAssetUploadErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		expect string
	}{
		{"too large", 413, `{"message":"Asset exceeds the 2 GiB limit"}`, `(?s)Release asset too large.*storage quota.*2 GiB limit`},
		{"quota", 413, `{"message":"Release asset storage quota exceeded"}`, `(?s)too large.*quota exceeded`},
		{"duplicate", 422, `{"message":"Validation Failed","errors":[{"field":"name","code":"already_exists"}]}`, `(?s)already\s+has\s+an\s+asset\s+named\s+"a.txt".*terraform\s+import\s+<resource\s+address>\s+"alice/repo:5"`},
		{"invalid name", 422, `{"message":"Validation Failed","errors":[{"field":"name","code":"invalid"}]}`, `(?s)Invalid asset name.*"a.txt"`},
		{"invalid content type", 422, `{"message":"Validation Failed","errors":[{"field":"content_type","code":"invalid"}]}`, `(?s)Invalid content type.*application/octet-stream`},
		{"release not found", 404, `{"message":"Not Found"}`, `(?s)Release not found.*Release 1 not found in alice/repo`},
		{"empty", 411, `{"message":"An upload must declare its Content-Length"}`, `(?s)Empty asset`},
		{"forbidden", 403, `{"message":"Forbidden"}`, `(?s)Not allowed to upload.*write access`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, u := newAssetStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      s.config(u, ""),
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

// A file that changes between plan and apply isn't uploaded.
func TestReleaseAssetFileChangedAfterPlan(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &ReleaseAssetResource{}
	s, u := newAssetStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, testAssetJSON(`null`))
	})
	c, err := client.New(u, "x")
	if err != nil {
		t.Fatal(err)
	}
	r.client = c

	ctx := context.Background()
	var schemaResp fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)
	plan := assetModel(sourceModel("", "", file), sha256Hex("a"))
	if err := os.WriteFile(file, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := fwresource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema}}
	if diags := req.Plan.Set(ctx, &plan); diags.HasError() {
		t.Fatal(diags)
	}
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}}
	r.Create(ctx, req, resp)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), sha256Hex("b")) {
		t.Errorf("diags = %v", resp.Diagnostics)
	}
	if s.uploads.Load() != 0 {
		t.Errorf("%d uploads, want none", s.uploads.Load())
	}
}

// Create, clear the label (sent as "", never null) and refresh after the
// asset is deleted outside Terraform.
func TestReleaseAssetStubLifecycle(t *testing.T) {
	var s *assetStub
	s, u := newAssetStub(t, func(w http.ResponseWriter, r *http.Request) {
		if b, _ := io.ReadAll(r.Body); string(b) != "x" || r.URL.Query().Get("label") != "L" {
			t.Errorf("upload %q with %s", b, r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusCreated)
		s.asset = testAssetJSON(`"L"`)
		_, _ = io.WriteString(w, s.asset)
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: s.config(u, `label = "L"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("ghost_release_asset.test", "id", "alice/repo:5"),
					resource.TestCheckResourceAttr("ghost_release_asset.test", "content_sha256", sha256Hex("x")),
					resource.TestCheckResourceAttr("ghost_release_asset.test", "repository_id", "2"),
				),
			},
			{
				Config: s.config(u, ""),
				Check: func(*terraform.State) error {
					if len(s.patches) != 1 || s.patches[0] != `{"label":""}` {
						return fmt.Errorf("patches = %q", s.patches)
					}
					return nil
				},
			},
			{
				PreConfig:          func() { s.asset = "" },
				Config:             s.config(u, ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
