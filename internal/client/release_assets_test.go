package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const releaseAssetFixture = `{
	"id": 9007199254740993,
	"node_id": "RA_1",
	"url": "https://api.example/repos/octo/r/releases/assets/9007199254740993",
	"browser_download_url": "https://web.example/octo/r/releases/download/v1/a%20b%3F.txt",
	"name": "a b?.txt",
	"label": null,
	"state": "uploaded",
	"content_type": "text/plain",
	"size": 5,
	"download_count": 0,
	"created_at": "2026-10-28T10:00:00Z",
	"updated_at": "2026-10-28T10:00:00Z",
	"uploader": {"login": "octo", "id": 1}
}`

func TestUploadReleaseAsset(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/uploads/repos/octo/r/releases/42/assets" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.RawQuery; got != "label=L&name=a+b%3F.txt" {
			t.Errorf("query = %q", got)
		}
		if got := r.URL.Query().Get("name"); got != "a b?.txt" {
			t.Errorf("name = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "text/plain" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header not sent as a bearer token")
		}
		if r.ContentLength != 5 || len(r.TransferEncoding) != 0 {
			t.Errorf("ContentLength = %d, TransferEncoding = %v; want 5, not chunked", r.ContentLength, r.TransferEncoding)
		}
		if b, _ := io.ReadAll(r.Body); string(b) != "hello" {
			t.Errorf("body = %q", b)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, releaseAssetFixture)
	})

	asset, err := c.UploadReleaseAsset(context.Background(), "octo", "r", 42, UploadReleaseAssetRequest{
		Name: "a b?.txt", Label: "L", ContentType: "text/plain", Content: strings.NewReader("hello"), Size: 5,
	})
	if err != nil {
		t.Fatalf("UploadReleaseAsset: %v", err)
	}
	if asset.ID != 9007199254740993 || asset.NodeID != "RA_1" || asset.Name != "a b?.txt" || asset.Label != nil ||
		asset.State != "uploaded" || asset.Size != 5 || asset.Uploader == nil || asset.Uploader.Login != "octo" ||
		!strings.HasSuffix(asset.BrowserDownloadURL, "/a%20b%3F.txt") {
		t.Errorf("asset = %+v", asset)
	}
}

func TestUploadReleaseAssetDefaults(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["label"]; ok {
			t.Errorf("label sent: %q", r.URL.RawQuery)
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("Content-Type = %q", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, releaseAssetFixture)
	})

	if _, err := c.UploadReleaseAsset(context.Background(), "octo", "r", 42, UploadReleaseAssetRequest{
		Name: "x", Content: strings.NewReader("x"), Size: 1,
	}); err != nil {
		t.Fatalf("UploadReleaseAsset: %v", err)
	}
}

func TestUploadReleaseAssetUnicodeName(t *testing.T) {
	long := strings.Repeat("é", 255)
	for _, name := range []string{"ü 名.bin", long, "100%2F.txt"} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("name"); got != name {
				t.Errorf("name = %q, want %q", got, name)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, releaseAssetFixture)
		})
		if _, err := c.UploadReleaseAsset(context.Background(), "octo", "r", 42, UploadReleaseAssetRequest{
			Name: name, Content: strings.NewReader("x"), Size: 1,
		}); err != nil {
			t.Fatalf("UploadReleaseAsset(%q): %v", name, err)
		}
	}
}

func TestUploadReleaseAssetRetriesFromStart(t *testing.T) {
	var calls atomic.Int32
	c, _ := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if b, _ := io.ReadAll(r.Body); string(b) != "payload" {
			t.Errorf("attempt %d body = %q", calls.Load()+1, b)
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, releaseAssetFixture)
	})

	if _, err := c.UploadReleaseAsset(context.Background(), "octo", "r", 42, UploadReleaseAssetRequest{
		Name: "x", Content: strings.NewReader("payload"), Size: 7,
	}); err != nil {
		t.Fatalf("UploadReleaseAsset: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

// slowClient returns a client whose HTTP client times out after 50ms, against
// a server that answers after 200ms.
func slowClient(t *testing.T, body string) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "test-token", WithHTTPClient(&http.Client{Timeout: 50 * time.Millisecond}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return c
}

func TestUploadAndDownloadIgnoreClientTimeout(t *testing.T) {
	c := slowClient(t, releaseAssetFixture)
	ctx := context.Background()

	if _, err := c.UploadReleaseAsset(ctx, "octo", "r", 42, UploadReleaseAssetRequest{
		Name: "x", Content: strings.NewReader("x"), Size: 1,
	}); err != nil {
		t.Errorf("UploadReleaseAsset: %v", err)
	}
	var buf bytes.Buffer
	if err := c.DownloadReleaseAsset(ctx, "octo", "r", 7, &buf); err != nil {
		t.Errorf("DownloadReleaseAsset: %v", err)
	}
	if _, err := c.GetReleaseAsset(ctx, "octo", "r", 7); err == nil {
		t.Error("GetReleaseAsset succeeded; want the client timeout to apply to JSON requests")
	}
}

func TestUploadReleaseAssetContextCancel(t *testing.T) {
	c := slowClient(t, releaseAssetFixture)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := c.UploadReleaseAsset(ctx, "octo", "r", 42, UploadReleaseAssetRequest{
		Name: "x", Content: strings.NewReader("x"), Size: 1,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestUploadReleaseAssetErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"not found", 404, `{"message":"Not Found"}`, IsNotFound},
		{"too large", 413, `{"message":"Asset exceeds the 2 GiB limit"}`, func(err error) bool {
			return HasStatus(err, http.StatusRequestEntityTooLarge) && strings.Contains(err.Error(), "2 GiB")
		}},
		{"length required", 411, `{"message":"An upload must declare its Content-Length"}`, func(err error) bool {
			return HasStatus(err, http.StatusLengthRequired) && strings.Contains(err.Error(), "Content-Length")
		}},
		{"duplicate", 422, `{"message":"Validation Failed","errors":[{"resource":"ReleaseAsset","field":"name","code":"already_exists"}]}`, func(err error) bool {
			return IsFieldError(err, "name", "already_exists")
		}},
		{"invalid name", 422, `{"message":"Validation Failed","errors":[{"field":"name","code":"invalid"}]}`, func(err error) bool {
			return IsFieldError(err, "name", "invalid")
		}},
		{"invalid content type", 422, `{"message":"Validation Failed","errors":[{"field":"content_type","code":"invalid"}]}`, func(err error) bool {
			return IsFieldError(err, "content_type", "invalid")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			_, err := c.UploadReleaseAsset(context.Background(), "octo", "r", 42, UploadReleaseAssetRequest{
				Name: "x", Content: strings.NewReader("x"), Size: 1,
			})
			if err == nil || !tt.check(err) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestGetReleaseAsset(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/repos/octo/r/releases/assets/7" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		_, _ = io.WriteString(w, releaseAssetFixture)
	})

	asset, err := c.GetReleaseAsset(context.Background(), "octo", "r", 7)
	if err != nil || asset.ContentType != "text/plain" {
		t.Fatalf("GetReleaseAsset = %+v, %v", asset, err)
	}
}

func TestDownloadReleaseAsset(t *testing.T) {
	want := bytes.Repeat([]byte{0, 1, 0xff, '\n'}, 768)
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/octo/r/releases/assets/7" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/octet-stream" {
			t.Errorf("Accept = %q", got)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment")
		_, _ = w.Write(want)
	})

	var buf bytes.Buffer
	if err := c.DownloadReleaseAsset(context.Background(), "octo", "r", 7, &buf); err != nil {
		t.Fatalf("DownloadReleaseAsset: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("got %d bytes, want %d", buf.Len(), len(want))
	}
}

func TestDownloadReleaseAssetError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	})

	var buf bytes.Buffer
	err := c.DownloadReleaseAsset(context.Background(), "octo", "r", 7, &buf)
	if !IsNotFound(err) {
		t.Errorf("err = %v, want 404", err)
	}
	if buf.Len() != 0 {
		t.Errorf("writer got %q, want nothing", buf.String())
	}
}

func TestDownloadReleaseAssetRetry(t *testing.T) {
	var calls atomic.Int32
	c, _ := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"slow down"}`)
			return
		}
		_, _ = io.WriteString(w, "bytes")
	})

	var buf bytes.Buffer
	if err := c.DownloadReleaseAsset(context.Background(), "octo", "r", 7, &buf); err != nil {
		t.Fatalf("DownloadReleaseAsset: %v", err)
	}
	if buf.String() != "bytes" {
		t.Errorf("writer got %q, want %q", buf.String(), "bytes")
	}
}

func TestUpdateReleaseAsset(t *testing.T) {
	name, label, empty := "n", "l", ""
	tests := []struct {
		req  UpdateReleaseAssetRequest
		want string
	}{
		{UpdateReleaseAssetRequest{Name: &name}, `{"name":"n"}`},
		{UpdateReleaseAssetRequest{Label: &empty}, `{"label":""}`},
		{UpdateReleaseAssetRequest{Name: &name, Label: &label}, `{"name":"n","label":"l"}`},
		{UpdateReleaseAssetRequest{}, `{}`},
	}
	for _, tt := range tests {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPatch || r.URL.Path != "/api/v3/repos/octo/r/releases/assets/7" {
				t.Errorf("got %s %s", r.Method, r.URL.Path)
			}
			if b, _ := io.ReadAll(r.Body); string(b) != tt.want {
				t.Errorf("body = %s, want %s", b, tt.want)
			}
			_, _ = io.WriteString(w, releaseAssetFixture)
		})
		if _, err := c.UpdateReleaseAsset(context.Background(), "octo", "r", 7, tt.req); err != nil {
			t.Fatalf("UpdateReleaseAsset: %v", err)
		}
	}
}

func TestDeleteReleaseAsset(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v3/repos/octo/r/releases/assets/7" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	if err := c.DeleteReleaseAsset(context.Background(), "octo", "r", 7); err != nil {
		t.Fatalf("DeleteReleaseAsset: %v", err)
	}
	if err := c.DeleteReleaseAsset(context.Background(), "octo", "r", 7); !IsNotFound(err) {
		t.Errorf("second delete err = %v, want 404", err)
	}
}

func TestListReleaseAssetsPaging(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/repos/octo/r/releases/42/assets" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("got %s", r.URL)
		}
		var assets []ReleaseAsset
		switch r.URL.Query().Get("page") {
		case "1":
			for i := range 100 {
				assets = append(assets, ReleaseAsset{ID: int64(i + 1)})
			}
		case "2":
			assets = []ReleaseAsset{{ID: 101}, {ID: 102}, {ID: 103}}
		}
		_ = json.NewEncoder(w).Encode(assets)
	})

	assets, err := c.ListReleaseAssets(context.Background(), "octo", "r", 42)
	if err != nil {
		t.Fatalf("ListReleaseAssets: %v", err)
	}
	if len(assets) != 103 || assets[0].ID != 1 || assets[102].ID != 103 {
		t.Errorf("got %d assets", len(assets))
	}
}

func TestReleaseAssetPathEscaping(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := map[string]string{
			http.MethodPost: "/api/uploads/repos/o%20w/r.x/releases/1/assets",
			http.MethodGet:  "/api/v3/repos/o%20w/r.x/releases/assets/7",
		}[r.Method]
		if r.URL.EscapedPath() != want {
			t.Errorf("%s path = %s, want %s", r.Method, r.URL.EscapedPath(), want)
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = io.WriteString(w, releaseAssetFixture)
	})

	ctx := context.Background()
	if _, err := c.UploadReleaseAsset(ctx, "o w", "r.x", 1, UploadReleaseAssetRequest{Name: "a", Content: strings.NewReader("x"), Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetReleaseAsset(ctx, "o w", "r.x", 7); err != nil {
		t.Fatal(err)
	}
}
