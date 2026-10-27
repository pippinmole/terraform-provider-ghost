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
		if got, want := r.URL.EscapedPath(), "/api/v3/repos/octo/r/commits/feat%2Fx/status"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"state":"pending","sha":"abc123","total_count":0,"statuses":[]}`))
	})

	s, err := c.GetCommitStatus(context.Background(), "octo", "r", "feat/x")
	if err != nil {
		t.Fatalf("GetCommitStatus: %v", err)
	}
	if s.SHA != "abc123" {
		t.Errorf("sha = %q", s.SHA)
	}
}
