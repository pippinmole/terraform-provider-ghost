package client

import (
	"context"
	"net/http"
	"testing"
)

func TestListBranches(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/repositories/octo/r/branches" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"defaultBranch":"main","branches":["feat/x","main"]}`))
	})

	b, err := c.ListBranches(context.Background(), "octo", "r")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if b.DefaultBranch == nil || *b.DefaultBranch != "main" || !b.Has("feat/x") || !b.Has("main") || b.Has("Main") {
		t.Errorf("unexpected branches: %+v", b)
	}
}

func TestListBranchesEmptyRepository(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"defaultBranch":null,"branches":[]}`))
	})

	b, err := c.ListBranches(context.Background(), "octo", "r")
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if b.DefaultBranch != nil || b.Has("main") {
		t.Errorf("unexpected branches: %+v", b)
	}
}

func TestCreateBranch(t *testing.T) {
	var body map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/repositories/octo/r/branches" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		body = readBody(t, r)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"feat/x","sha":"abc123"}`))
	})

	b, err := c.CreateBranch(context.Background(), "octo", "r", CreateBranchRequest{Name: "feat/x"})
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if b.Name != "feat/x" || b.SHA != "abc123" {
		t.Errorf("unexpected branch: %+v", b)
	}
	if _, ok := body["from"]; ok || len(body) != 1 {
		t.Errorf("from should be omitted when empty: %v", body)
	}

	if _, err := c.CreateBranch(context.Background(), "octo", "r", CreateBranchRequest{Name: "feat/x", From: "v1.0.0"}); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if body["from"] != "v1.0.0" {
		t.Errorf("from not sent: %v", body)
	}
}

func TestDeleteBranchEscapesName(t *testing.T) {
	status := http.StatusNoContent
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/api/repositories/octo/r/branches/feat%2Fx"; r.Method != http.MethodDelete || got != want {
			t.Errorf("got %s %s, want DELETE %s", r.Method, got, want)
		}
		w.WriteHeader(status)
	})

	if err := c.DeleteBranch(context.Background(), "octo", "r", "feat/x"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	status = http.StatusConflict
	if err := c.DeleteBranch(context.Background(), "octo", "r", "feat/x"); !IsConflict(err) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestGetCommitStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/api/v3/repos/octo/r/commits/refs%2Fheads%2Ffeat%2Fx/status"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"state":"pending","sha":"abc123","total_count":0,"statuses":[]}`))
	})

	s, err := c.GetCommitStatus(context.Background(), "octo", "r", "refs/heads/feat/x")
	if err != nil {
		t.Fatalf("GetCommitStatus: %v", err)
	}
	if s.SHA != "abc123" {
		t.Errorf("sha = %q", s.SHA)
	}
}

// TestBranchErrors pins the status codes the provider relies on: Ghost
// returns 404 when there is nothing to branch from (including an empty
// repository) and 409 when the branch already exists.
func TestBranchErrors(t *testing.T) {
	status := http.StatusNotFound
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"statusCode":404,"message":"Nothing to branch from at: nope"}`))
	})
	ctx := context.Background()

	if _, err := c.CreateBranch(ctx, "octo", "r", CreateBranchRequest{Name: "x", From: "nope"}); !IsNotFound(err) {
		t.Errorf("CreateBranch: expected not found, got %v", err)
	}
	if _, err := c.ListBranches(ctx, "octo", "r"); !IsNotFound(err) {
		t.Errorf("ListBranches: expected not found, got %v", err)
	}
	if _, err := c.GetCommitStatus(ctx, "octo", "r", "nope"); !IsNotFound(err) {
		t.Errorf("GetCommitStatus: expected not found, got %v", err)
	}

	status = http.StatusConflict
	if _, err := c.CreateBranch(ctx, "octo", "r", CreateBranchRequest{Name: "x"}); !IsConflict(err) {
		t.Errorf("CreateBranch: expected conflict, got %v", err)
	}
}

func TestListProtectedBranches(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/repositories/octo/r/protected-refs" ||
			r.URL.Query().Get("target") != "branch" {
			t.Errorf("got %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"refs":{
			"refs/heads/main":{"rules":["deletion","pull_request"],"bypass":"never"},
			"refs/heads/feat/x":{"rules":["non_fast_forward"],"bypass":"always"},
			"refs/tags/v1":{"rules":["deletion"],"bypass":"never"}}}`))
	})

	p, err := c.ListProtectedBranches(context.Background(), "octo", "r")
	if err != nil {
		t.Fatalf("ListProtectedBranches: %v", err)
	}
	if len(p) != 2 || len(p["main"].Rules) != 2 || p["feat/x"].Bypass != "always" {
		t.Errorf("unexpected protected branches: %+v", p)
	}
}

func TestListProtectedBranchesNone(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"refs":{}}`))
	})

	p, err := c.ListProtectedBranches(context.Background(), "octo", "r")
	if err != nil || len(p) != 0 {
		t.Errorf("got %v, %v", p, err)
	}
}
