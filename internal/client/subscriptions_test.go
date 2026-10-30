package client

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGetRepositorySubscription(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/repositories/my%20org/re%2Fpo/subscription" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"level":"participating"}`))
	})

	got, err := c.GetRepositorySubscription(context.Background(), "my org", "re/po")
	if err != nil {
		t.Fatalf("GetRepositorySubscription: %v", err)
	}
	if got.Level != WatchParticipating {
		t.Errorf("level = %q", got.Level)
	}
}

func TestGetRepositorySubscriptionNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"Repository not found"}`))
	})

	_, err := c.GetRepositorySubscription(context.Background(), "acme", "missing")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "Repository not found") {
		t.Errorf("got %v, want a 404 Repository not found", err)
	}
}

func TestSetRepositorySubscription(t *testing.T) {
	for _, level := range []string{WatchAll, WatchIgnore, WatchParticipating} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/repositories/acme/repo/subscription" {
				t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Errorf("Authorization = %q", got)
			}
			body, _ := io.ReadAll(r.Body)
			if strings.TrimSpace(string(body)) != `{"level":"`+level+`"}` {
				t.Errorf("body = %s", body)
			}
			_, _ = w.Write([]byte(`{"level":"` + level + `"}`))
		})

		got, err := c.SetRepositorySubscription(context.Background(), "acme", "repo", level)
		if err != nil {
			t.Fatalf("SetRepositorySubscription(%s): %v", level, err)
		}
		if got.Level != level {
			t.Errorf("level = %q, want %q", got.Level, level)
		}
	}
}

func TestSetRepositorySubscriptionInvalidLevel(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":["level must be one of the following values: participating, all, ignore"],"error":"Bad Request","statusCode":400}`))
	})

	_, err := c.SetRepositorySubscription(context.Background(), "acme", "repo", "watching")
	if err == nil || !strings.Contains(err.Error(), "level must be one of") {
		t.Errorf("got %v, want the validation message", err)
	}
}
