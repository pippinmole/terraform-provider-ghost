package client

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
)

const releaseSHA = "d90fe1d27c8299025bf3425daeb03a16ee65f7a4"

func releaseJSON(id int64, tag, commitish string, draft bool) string {
	return fmt.Sprintf(`{"url":"https://ghost.test/api/v3/repos/octo/r/releases/%[1]d",`+
		`"html_url":"https://web.ghost.test/octo/r/releases/tag/%[2]s","assets_url":"a","upload_url":"u{?name,label}",`+
		`"tarball_url":"t","zipball_url":"z","id":%[1]d,"node_id":"RE_%[1]d","tag_name":%[2]q,`+
		`"target_commitish":%[3]q,"name":"First","body":null,"draft":%[4]t,"prerelease":false,"immutable":false,`+
		`"created_at":"2026-10-28T04:57:37.605Z","published_at":null,"updated_at":"2026-10-28T04:57:37.605Z",`+
		`"author":{"login":"octo","id":10},"assets":[{"id":3,"name":"a.zip","label":null,"content_type":"application/zip",`+
		`"size":12,"download_count":1,"browser_download_url":"b","url":"c","node_id":"n","created_at":"x","updated_at":"y"}]}`,
		id, tag, commitish, draft)
}

func TestCreateRelease(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/repos/octo/r/releases" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body := readBody(t, r)
		want := map[string]any{"tag_name": "v1", "target_commitish": "feat/x", "name": "First", "draft": true, "prerelease": false}
		if len(body) != len(want) {
			t.Errorf("unexpected body: %v", body)
		}
		for k, v := range want {
			if body[k] != v {
				t.Errorf("body[%s] = %v, want %v", k, body[k], v)
			}
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(releaseJSON(6, "v1", releaseSHA, true)))
	})

	rel, err := c.CreateRelease(context.Background(), "octo", "r", CreateReleaseRequest{
		TagName: "v1", TargetCommitish: "feat/x", Name: "First", Draft: true,
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	if rel.ID != 6 || rel.NodeID != "RE_6" || rel.TagName != "v1" || rel.Name == nil || *rel.Name != "First" ||
		rel.Body != nil || !rel.Draft || rel.PublishedAt != nil || rel.Author == nil || rel.Author.Login != "octo" ||
		len(rel.Assets) != 1 || rel.Assets[0].Name != "a.zip" || rel.HTMLURL == "" || rel.UploadURL == "" {
		t.Errorf("unexpected release: %+v", rel)
	}
	if rel.CommitSHA() != releaseSHA {
		t.Errorf("CommitSHA = %q", rel.CommitSHA())
	}
}

func TestCreateReleaseOmitsEmptyOptionalFields(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(t, r)
		for _, k := range []string{"target_commitish", "name", "body"} {
			if _, ok := body[k]; ok {
				t.Errorf("%s should be omitted: %v", k, body)
			}
		}
		if body["draft"] != false || body["prerelease"] != false {
			t.Errorf("flags should always be sent: %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(releaseJSON(6, "v1", releaseSHA, false)))
	})

	if _, err := c.CreateRelease(context.Background(), "octo", "r", CreateReleaseRequest{TagName: "v1"}); err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
}

func TestUpdateReleaseSendsNulls(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v3/repos/octo/r/releases/6" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body = readBody(t, r)
		_, _ = w.Write([]byte(releaseJSON(6, "v1", releaseSHA, false)))
	})

	if _, err := c.UpdateRelease(context.Background(), "octo", "r", 6, UpdateReleaseRequest{Prerelease: true}); err != nil {
		t.Fatalf("UpdateRelease: %v", err)
	}
	for _, k := range []string{"name", "body"} {
		if v, ok := body[k]; !ok || v != nil {
			t.Errorf("%s should be sent as null: %v", k, body)
		}
	}
	if body["draft"] != false || body["prerelease"] != true || len(body) != 4 {
		t.Errorf("unexpected body: %v", body)
	}

	name, notes := "n", "b"
	if _, err := c.UpdateRelease(context.Background(), "octo", "r", 6, UpdateReleaseRequest{Name: &name, Body: &notes, Draft: true}); err != nil {
		t.Fatalf("UpdateRelease: %v", err)
	}
	if body["name"] != "n" || body["body"] != "b" || body["draft"] != true {
		t.Errorf("unexpected body: %v", body)
	}
}

func TestReleasePaths(t *testing.T) {
	tests := []struct {
		name   string
		method string
		want   string
		call   func(c *Client) error
	}{
		{"get", http.MethodGet, "/api/v3/repos/octo/r/releases/42", func(c *Client) error {
			_, err := c.GetRelease(context.Background(), "octo", "r", 42)
			return err
		}},
		{"tag with slash", http.MethodGet, "/api/v3/repos/octo/r/releases/tags/release%2F1.0", func(c *Client) error {
			_, err := c.GetReleaseByTag(context.Background(), "octo", "r", "release/1.0")
			return err
		}},
		{"latest", http.MethodGet, "/api/v3/repos/octo/r/releases/latest", func(c *Client) error {
			_, err := c.GetLatestRelease(context.Background(), "octo", "r")
			return err
		}},
		{"delete", http.MethodDelete, "/api/v3/repos/octo/r/releases/42", func(c *Client) error {
			return c.DeleteRelease(context.Background(), "octo", "r", 42)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.EscapedPath() != tt.want {
					t.Errorf("got %s %s, want %s %s", r.Method, r.URL.EscapedPath(), tt.method, tt.want)
				}
				if tt.method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_, _ = w.Write([]byte(releaseJSON(42, "v1", releaseSHA, false)))
			})
			if err := tt.call(c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})

	_, err := c.GetRelease(context.Background(), "octo", "r", 1)
	if !IsNotFound(err) {
		t.Errorf("GetRelease: want a 404, got %v", err)
	}
	if err := c.DeleteRelease(context.Background(), "octo", "r", 1); !IsNotFound(err) {
		t.Errorf("DeleteRelease: want a 404, got %v", err)
	}
}

func TestCreateReleaseValidationError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"Release","field":"tag_name","code":"already_exists"}]}`))
	})

	_, err := c.CreateRelease(context.Background(), "octo", "r", CreateReleaseRequest{TagName: "v1"})
	if !IsFieldError(err, "tag_name", "already_exists") {
		t.Errorf("want tag_name already_exists, got %v", err)
	}
	if IsFieldError(err, "target_commitish", "invalid") {
		t.Errorf("matched the wrong field: %v", err)
	}
}

func TestListReleasesPages(t *testing.T) {
	old := ReleasePageSize
	ReleasePageSize = 2
	t.Cleanup(func() { ReleasePageSize = old })

	pages := map[string][]int64{"1": {5, 4}, "2": {3, 2}, "3": {1}, "4": {}}
	var requests int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v3/repos/octo/r/releases" || r.URL.Query().Get("per_page") != "2" {
			t.Errorf("got %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		ids := pages[r.URL.Query().Get("page")]
		_, _ = w.Write([]byte("["))
		for i, id := range ids {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(releaseJSON(id, "v"+strconv.FormatInt(id, 10), releaseSHA, id == 3)))
		}
		_, _ = w.Write([]byte("]"))
	})

	releases, err := c.ListReleases(context.Background(), "octo", "r")
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(releases) != 5 || releases[0].ID != 5 || releases[4].ID != 1 || requests != 4 {
		t.Errorf("got %d releases in %d requests", len(releases), requests)
	}

	draft, err := c.FindReleaseByTag(context.Background(), "octo", "r", "v3")
	if err != nil || draft == nil || draft.ID != 3 || !draft.Draft {
		t.Errorf("FindReleaseByTag(v3) = %+v, %v", draft, err)
	}
	missing, err := c.FindReleaseByTag(context.Background(), "octo", "r", "V3")
	if err != nil || missing != nil {
		t.Errorf("FindReleaseByTag is case-sensitive: got %+v, %v", missing, err)
	}
}

func TestListReleasesStopsWhenPageIsIgnored(t *testing.T) {
	var requests int
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		// A release whose tag was deleted reports the default branch.
		_, _ = w.Write([]byte("[" + releaseJSON(1, "v1", "main", false) + "]"))
	})

	releases, err := c.ListReleases(context.Background(), "octo", "r")
	if err != nil || len(releases) != 1 || requests != 2 {
		t.Fatalf("got %d releases in %d requests, err %v", len(releases), requests, err)
	}
	if releases[0].CommitSHA() != "" {
		t.Errorf("CommitSHA of a deleted tag = %q", releases[0].CommitSHA())
	}
}

func TestReleaseCommitSHA(t *testing.T) {
	for commitish, want := range map[string]string{
		releaseSHA:     releaseSHA,
		"main":         "",
		"feature/x":    "",
		releaseSHA[:7]: "",
		"":             "",
		"D90FE1D27C8299025BF3425DAEB03A16EE65F7A4": "",
	} {
		r := Release{TargetCommitish: commitish}
		if got := r.CommitSHA(); got != want {
			t.Errorf("CommitSHA(%q) = %q, want %q", commitish, got, want)
		}
	}
}
