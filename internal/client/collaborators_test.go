package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestListRepositoryCollaborators(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/repositories/my%20org/re%2Fpo/collaborators" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"collaborators":[` +
			`{"username":"alice","name":"Alice","image":null,"role":"write","status":"pending","invitedAt":"2026-10-27T23:05:30.815Z","expiresAt":"2026-11-03T23:05:30.815Z"},` +
			`{"username":"bob","name":"Bob","image":"https://img.test/b.png","role":"admin","status":"accepted","invitedAt":"2026-10-01T00:00:00.000Z","expiresAt":null}]}`))
	})

	got, err := c.ListRepositoryCollaborators(context.Background(), "my org", "re/po")
	if err != nil {
		t.Fatalf("ListRepositoryCollaborators: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d collaborators, want 2", len(got))
	}
	a, b := got[0], got[1]
	if a.Username != "alice" || a.Name != "Alice" || a.Image != nil || a.Role != "write" || a.Status != CollaboratorPending ||
		a.InvitedAt != "2026-10-27T23:05:30.815Z" || a.ExpiresAt == nil || *a.ExpiresAt != "2026-11-03T23:05:30.815Z" {
		t.Errorf("unexpected pending collaborator: %+v", a)
	}
	if b.Username != "bob" || b.Image == nil || b.Status != CollaboratorAccepted || b.ExpiresAt != nil {
		t.Errorf("unexpected accepted collaborator: %+v", b)
	}
}

func TestSetRepositoryCollaborator(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/repositories/acme/repo/collaborators/Al%2Fice" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(body)) != `{"role":"triage"}` {
			t.Errorf("body = %s", body)
		}
		_, _ = w.Write([]byte(`{"username":"alice","name":"Alice","image":null,"role":"triage","status":"pending","invitedAt":"2026-10-27T23:05:30.815Z","expiresAt":"2026-11-03T23:05:30.815Z"}`))
	})

	got, err := c.SetRepositoryCollaborator(context.Background(), "acme", "repo", "Al/ice", "triage")
	if err != nil {
		t.Fatalf("SetRepositoryCollaborator: %v", err)
	}
	if got.Username != "alice" || got.Role != "triage" || got.Status != CollaboratorPending {
		t.Errorf("unexpected collaborator: %+v", got)
	}
}

func TestSetRepositoryCollaboratorErrors(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusNotFound, `{"statusCode":404,"error":"Error","message":"User nobody not found"}`, "User nobody not found"},
		{http.StatusBadRequest, `{"statusCode":400,"error":"Error","message":"The owner already has every permission on this repository"}`, "The owner already has every permission on this repository"},
		{http.StatusBadRequest, `{"message":["role must be one of the following values: read, triage, write, maintain, admin"],"error":"Bad Request","statusCode":400}`, "role must be one of"},
	}
	for _, tt := range tests {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(tt.body))
		})

		_, err := c.SetRepositoryCollaborator(context.Background(), "acme", "repo", "nobody", "read")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tt.status || !strings.Contains(apiErr.Message, tt.want) {
			t.Errorf("error = %v, want a %d containing %q", err, tt.status, tt.want)
		}
	}
}

func TestRemoveRepositoryCollaborator(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/api/repositories/acme/repo/collaborators/alice" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"alice is not a collaborator on this repository"}`))
	})

	if err := c.RemoveRepositoryCollaborator(context.Background(), "acme", "repo", "alice"); err != nil {
		t.Fatalf("first RemoveRepositoryCollaborator: %v", err)
	}
	if err := c.RemoveRepositoryCollaborator(context.Background(), "acme", "repo", "alice"); !IsNotFound(err) {
		t.Errorf("second RemoveRepositoryCollaborator = %v, want a 404", err)
	}
}
