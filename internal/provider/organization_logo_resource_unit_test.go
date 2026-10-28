package provider

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestLogoContentType(t *testing.T) {
	tests := []struct {
		name, head, want, wantErr string
	}{
		{"png", "\x89PNG\r\n\x1a\nrest", "image/png", ""},
		{"jpeg", "\xff\xd8\xff\xe0rest", "image/jpeg", ""},
		{"gif", "GIF89a\x01\x00", "", "image/gif"},
		{"webp", "RIFF\x00\x00\x00\x00WEBPVP8 ", "", "image/webp"},
		{"svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`, "", "text/plain"},
		{"text", "hello", "", "text/plain"},
		{"truncated png", "\x89PN", "", "PNG or JPEG"},
		{"empty", "", "", "PNG or JPEG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := logoContentType([]byte(tt.head))
			if tt.wantErr != "" {
				if err == nil || !regexp.MustCompile(regexp.QuoteMeta(tt.wantErr)).MatchString(err.Error()) {
					t.Fatalf("err = %v, want one mentioning %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestCheckLogoSize(t *testing.T) {
	head := []byte("\x89PNG\r\n\x1a\n")
	if _, err := checkLogo(contentInfo{size: maxLogoBytes, head: head}); err != nil {
		t.Errorf("2 MiB: %v", err)
	}
	if _, err := checkLogo(contentInfo{size: maxLogoBytes + 1, head: head}); err == nil {
		t.Error("2 MiB + 1: no error")
	}
}

// logoStub is a fake Ghost with one organization, acme.
type logoStub struct {
	mu      sync.Mutex
	srv     *httptest.Server
	logo    []byte // nil for none
	n       int    // uploads so far, which number the URLs
	put     func(w http.ResponseWriter, r *http.Request) bool
	deletes int
}

func newLogoStub(t *testing.T) *logoStub {
	t.Helper()
	s := &logoStub{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)

	return s
}

func (s *logoStub) url() string {
	return fmt.Sprintf("%s/api/users/avatars/org1/%d.png", s.srv.URL, s.n)
}

func (s *logoStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.EqualFold(r.URL.Path, "/api/organizations/acme") && r.Method == http.MethodGet:
		logo := "null"
		if s.logo != nil {
			logo = fmt.Sprintf("%q", s.url())
		}
		_, _ = fmt.Fprintf(w, `{"slug":"acme","name":"Acme","logo":%s,"members":[],"pinned":[]}`, logo)
	case r.URL.Path == "/api/organizations/acme/logo" && r.Method == http.MethodPut:
		if s.put != nil && !s.put(w, r) {
			return
		}
		s.logo, _ = io.ReadAll(r.Body)
		s.n++
		_, _ = fmt.Fprintf(w, `{"url":%q}`, s.url())
	case r.URL.Path == "/api/organizations/acme/logo" && r.Method == http.MethodDelete:
		s.deletes++
		s.logo = nil
		w.WriteHeader(http.StatusNoContent)
	case s.logo != nil && r.URL.Path == fmt.Sprintf("/api/users/avatars/org1/%d.png", s.n):
		_, _ = w.Write(s.logo)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"statusCode":404,"message":"Organization not found: x"}`)
	}
}

func (s *logoStub) config(source string) string {
	return fmt.Sprintf(`
provider "ghost" {
  token    = "x"
  base_url = %q
}

resource "ghost_organization_logo" "test" {
  organization = "acme"
  %s
}
`, s.srv.URL, source)
}

func base64Source(b []byte) string {
	return fmt.Sprintf("content_base64 = %q", base64.StdEncoding.EncodeToString(b))
}

func TestOrganizationLogoPlanValidation(t *testing.T) {
	s := newLogoStub(t)
	tests := []struct {
		name, source, expect string
	}{
		{"gif", base64Source([]byte("GIF89a\x01\x00\x01\x00")), `(?s)Unsupported logo image.*PNG or JPEG.*image/gif`},
		{"empty base64", `content_base64 = ""`, `(?s)content_base64`},
		{"bad base64", `content_base64 = "not base64!"`, `(?s)must be standard base64`},
		{"both", `file = "x.png"` + "\n" + base64Source([]byte("\x89PNG\r\n\x1a\n")), `(?s)file.*content_base64`},
		{"neither", ``, `(?s)Exactly one of these attributes must be configured`},
		{"too large", base64Source(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxLogoBytes)...)), `(?s)2097160\s+bytes.*up\s+to\s+2\s+MiB`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      s.config(tt.source),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestOrganizationLogoUploadErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		expect string
	}{
		{"personal access token", 403, `{"statusCode":403,"message":"Personal access tokens cannot use this route; sign in instead"}`, `(?s)only\s+the\s+organization's\s+admins.*Personal\s+access\s+tokens\s+cannot`},
		{"not found", 404, `{"statusCode":404,"message":"Organization not found: acme"}`, `(?s)Ghost\s+has\s+no\s+organization\s+"acme"\s+that\s+you're\s+an\s+admin\s+of`},
		{"too large", 413, `{"message":"request entity too large"}`, `(?s)too\s+large:\s+logos\s+can\s+be\s+up\s+to\s+2\s+MiB`},
		{"unsupported", 415, `{"message":"Unsupported Media Type"}`, `(?s)valid\s+PNG\s+or\s+JPEG`},
		{"bad image", 400, `{"message":["not an image"]}`, `(?s)valid\s+PNG\s+or\s+JPEG.*not\s+an\s+image`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newLogoStub(t)
			s.put = func(w http.ResponseWriter, _ *http.Request) bool {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
				return false
			}
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      s.config(base64Source(testLogoPNG(t, color.RGBA{1, 1, 1, 255}))),
					ExpectError: regexp.MustCompile(tt.expect),
				}},
			})
		})
	}
}

func TestOrganizationLogoStubLifecycle(t *testing.T) {
	s := newLogoStub(t)
	pngA := testLogoPNG(t, color.RGBA{9, 9, 9, 255})
	jpegB := testLogoJPEG(t, color.RGBA{200, 9, 9, 255})
	var contentType string
	s.put = func(_ http.ResponseWriter, r *http.Request) bool {
		contentType = r.Header.Get("Content-Type")
		return true
	}
	dir := t.TempDir()
	file := testAccWriteFile(t, dir, "logo.png", pngA)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: s.config(fmt.Sprintf("file = %q", file)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(logoAddr, "id", "acme"),
					resource.TestCheckResourceAttr(logoAddr, "content_type", "image/png"),
					resource.TestCheckResourceAttr(logoAddr, "content_sha256", sha256Hex(string(pngA))),
					func(_ *terraform.State) error { return expectString("Content-Type", contentType, "image/png") },
				),
			},
			{
				// The file is gone but the configuration is the same: no change.
				PreConfig: func() { removeFile(t, file) },
				Config:    s.config(fmt.Sprintf("file = %q", file)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan(),
				}},
			},
			{
				Config: s.config(base64Source(jpegB)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(logoAddr, "content_type", "image/jpeg"),
					resource.TestCheckResourceAttr(logoAddr, "url", s.srv.URL+"/api/users/avatars/org1/2.png"),
					func(_ *terraform.State) error { return expectString("Content-Type", contentType, "image/jpeg") },
				),
			},
			{
				// Removed outside Terraform: uploaded again.
				PreConfig: func() { s.mu.Lock(); s.logo = nil; s.mu.Unlock() },
				Config:    s.config(base64Source(jpegB)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(logoAddr, plancheck.ResourceActionCreate),
				}},
			},
			{
				// Replaced outside Terraform with other bytes: uploaded again.
				PreConfig: func() { s.mu.Lock(); s.logo = pngA; s.n++; s.mu.Unlock() },
				Config:    s.config(base64Source(jpegB)),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(logoAddr, plancheck.ResourceActionUpdate),
				}},
				Check: func(_ *terraform.State) error {
					if !bytes.Equal(s.logo, jpegB) {
						return fmt.Errorf("stub logo isn't the configured JPEG")
					}
					return nil
				},
			},
			{
				ResourceName:            logoAddr,
				ImportState:             true,
				ImportStateId:           "ACME",
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"content_base64", "organization"},
			},
		},
	})
	if s.logo != nil || s.deletes != 1 {
		t.Errorf("after destroy: logo %d bytes, %d deletes; want none and 1", len(s.logo), s.deletes)
	}
}

func expectString(what, got, want string) error {
	if got != want {
		return fmt.Errorf("%s = %q, want %q", what, got, want)
	}

	return nil
}

func removeFile(t *testing.T, name string) {
	t.Helper()
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
}
