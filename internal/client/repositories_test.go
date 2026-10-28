package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

const repoJSON = `{
  "id": 42, "node_id": "R_1", "name": "My Repo", "full_name": "octo/my-repo",
  "private": true, "visibility": "private",
  "owner": {"login": "octo", "html_url": "https://ghost.test/octo"},
  "html_url": "https://ghost.test/octo/my-repo", "description": null, "fork": false,
  "clone_url": "https://ghost.test/octo/my-repo.git", "ssh_url": "git@ghost.test:octo/my-repo.git",
  "default_branch": "main",
  "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "pushed_at": "2026-01-03T00:00:00Z"
}`

func readBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()

	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decoding body %q: %v", b, err)
	}

	return m
}

func TestCreateRepositoryUser(t *testing.T) {
	autoInit := true
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/user/repos" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body := readBody(t, r)
		if body["name"] != "my-repo" || body["visibility"] != "private" || body["auto_init"] != true {
			t.Errorf("unexpected body: %v", body)
		}
		for _, k := range []string{"description", "gitignore_template", "license_template", "private"} {
			if _, ok := body[k]; ok {
				t.Errorf("key %q should be omitted: %v", k, body)
			}
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(repoJSON))
	})

	repo, err := c.CreateRepository(context.Background(), "", CreateRepositoryRequest{
		Name: "my-repo", Visibility: "private", AutoInit: &autoInit,
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if repo.ID != 42 || repo.FullName != "octo/my-repo" || repo.Owner.Login != "octo" ||
		repo.Description != nil || repo.DefaultBranch != "main" || !repo.Private ||
		repo.PushedAt != "2026-01-03T00:00:00Z" || repo.CloneURL == "" || repo.SSHURL == "" {
		t.Errorf("unexpected repo: %+v", repo)
	}
}

func TestCreateRepositoryOrg(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v3/orgs/my%20org/repos" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(repoJSON))
	})

	if _, err := c.CreateRepository(context.Background(), "my org", CreateRepositoryRequest{Name: "x"}); err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
}

func TestGetRepository(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/v3/repos/octo/my%20repo" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(repoJSON))
	})

	repo, err := c.GetRepository(context.Background(), "octo", "my repo")
	if err != nil {
		t.Fatalf("GetRepository: %v", err)
	}
	if repo.Name != "My Repo" || repo.Visibility != "private" {
		t.Errorf("unexpected repo: %+v", repo)
	}
}

func TestGetRepositoryNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})

	if _, err := c.GetRepository(context.Background(), "a", "b"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestUpdateRepository(t *testing.T) {
	name, branch := "renamed", "dev"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.EscapedPath() != "/api/repositories/octo/my%20slug" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		body := readBody(t, r)
		if len(body) != 2 || body["name"] != "renamed" || body["defaultBranch"] != "dev" {
			t.Errorf("unexpected body: %v", body)
		}
		_, _ = w.Write([]byte(`{"id":"abc","slug":"renamed"}`))
	})

	resp, err := c.UpdateRepository(context.Background(), "octo", "my slug", UpdateRepositoryRequest{
		Name: &name, DefaultBranch: &branch,
	})
	if err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}
	if resp.ID != "abc" || resp.Slug != "renamed" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestDeleteRepository(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.EscapedPath() != "/api/repositories/octo/my%20slug" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.DeleteRepository(context.Background(), "octo", "my slug"); err != nil {
		t.Fatalf("DeleteRepository: %v", err)
	}
}

func TestDeleteRepositoryNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if err := c.DeleteRepository(context.Background(), "a", "b"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestDoNoContentWithOut(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	var out struct{ ID string }
	if err := c.Do(context.Background(), http.MethodDelete, NativePath("x"), nil, &out); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestSplitFullName(t *testing.T) {
	owner, slug, err := SplitFullName("octo/my-repo")
	if err != nil || owner != "octo" || slug != "my-repo" {
		t.Errorf("got %q %q %v", owner, slug, err)
	}
	for _, bad := range []string{"", "octo", "octo/", "/repo", "a/b/c"} {
		if _, _, err := SplitFullName(bad); err == nil {
			t.Errorf("SplitFullName(%q): expected error", bad)
		}
	}
}

func ownerRepoJSON(id, slug string) string {
	return fmt.Sprintf(`{"id":%q,"numericId":7,"name":%q,"slug":%q,"description":null,"organizationId":null,`+
		`"ownerId":"u1","visibility":"private","defaultBranch":null,"lastPushedAt":null,"parentRepositoryId":"repo_p",`+
		`"createdAt":"2026-10-28T04:31:57.000Z","updatedAt":"2026-10-28T04:31:58.000Z"}`, id, strings.ToUpper(slug), slug)
}

func TestListOwnerRepositoriesPaginates(t *testing.T) {
	var queries []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/repositories/my%20org" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		queries = append(queries, r.URL.RawQuery)
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"repositories":[` + ownerRepoJSON("repo_a", "a") + `,` + ownerRepoJSON("repo_b", "b") + `],"nextCursor":"c1","hasMore":true}`))
		case "c1":
			// repo_b again, as if it was updated mid-listing.
			_, _ = w.Write([]byte(`{"repositories":[` + ownerRepoJSON("repo_b", "b") + `,` + ownerRepoJSON("repo_c", "c") + `],"nextCursor":"c2","hasMore":true}`))
		case "c2":
			_, _ = w.Write([]byte(`{"repositories":[],"nextCursor":null,"hasMore":false}`))
		default:
			t.Errorf("unexpected cursor in %q", r.URL.RawQuery)
		}
	})

	old := OwnerRepositoryPageSize
	OwnerRepositoryPageSize = 2
	t.Cleanup(func() { OwnerRepositoryPageSize = old })

	repos, err := c.ListOwnerRepositories(context.Background(), "my org", "Web App")
	if err != nil {
		t.Fatalf("ListOwnerRepositories: %v", err)
	}
	var ids []string
	for _, r := range repos {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "repo_a,repo_b,repo_c" {
		t.Errorf("ids = %v", ids)
	}
	want := []string{"limit=2&q=Web+App", "cursor=c1&limit=2&q=Web+App", "cursor=c2&limit=2&q=Web+App"}
	if strings.Join(queries, " ") != strings.Join(want, " ") {
		t.Errorf("queries = %q, want %q", queries, want)
	}
	r := repos[0]
	if r.NumericID != 7 || r.Name != "A" || r.Slug != "a" || r.Description != nil || r.OrganizationID != nil ||
		r.Visibility != "private" || r.ParentRepositoryID == nil || *r.ParentRepositoryID != "repo_p" ||
		r.LastPushedAt != nil || r.CreatedAt != "2026-10-28T04:31:57.000Z" || r.UpdatedAt != "2026-10-28T04:31:58.000Z" {
		t.Errorf("unexpected repository: %+v", r)
	}
}

func TestListOwnerRepositoriesNoQuery(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "limit=100" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"repositories":[],"nextCursor":null,"hasMore":false}`))
	})

	repos, err := c.ListOwnerRepositories(context.Background(), "alice", "")
	if err != nil || len(repos) != 0 {
		t.Errorf("ListOwnerRepositories = %v, %v", repos, err)
	}
}

func TestListOwnerRepositoriesRepeatedCursor(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"repositories":[` + ownerRepoJSON("repo_a", "a") + `],"nextCursor":"same","hasMore":true}`))
	})

	_, err := c.ListOwnerRepositories(context.Background(), "alice", "")
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Errorf("error = %v, want a repeated cursor error", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestListOwnerRepositoriesNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"User nope not found"}`))
	})

	_, err := c.ListOwnerRepositories(context.Background(), "nope", "")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "User nope not found") {
		t.Errorf("error = %v, want a 404", err)
	}
}

func TestListOwnerRepositoriesBadRequest(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":["limit must not be greater than 100"],"error":"Bad Request","statusCode":400}`))
	})

	_, err := c.ListOwnerRepositories(context.Background(), "alice", "")
	if err == nil || !strings.Contains(err.Error(), "400 limit must not be greater than 100") {
		t.Errorf("error = %v", err)
	}
}
