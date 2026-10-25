package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	c, err := New(srv.URL, "test-token", WithUserAgent("test-agent"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return c
}

func TestNewRejectsRelativeURL(t *testing.T) {
	if _, err := New("not-a-url", "t"); err == nil {
		t.Fatal("expected error for relative base URL")
	}
}

func TestPaths(t *testing.T) {
	if got, want := V3Path("repos", "owner", "my repo"), "/api/v3/repos/owner/my%20repo"; got != want {
		t.Errorf("V3Path = %q, want %q", got, want)
	}
	if got, want := NativePath("repositories", "owner"), "/api/repositories/owner"; got != want {
		t.Errorf("NativePath = %q, want %q", got, want)
	}
}

func TestGetAuthenticatedUser(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/user" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "test-agent" {
			t.Errorf("User-Agent = %q", got)
		}
		_, _ = w.Write([]byte(`{"login":"pippinmole","id":10}`))
	})

	u, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("GetAuthenticatedUser: %v", err)
	}
	if u.Login != "pippinmole" || u.ID != 10 {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestDoReturnsAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found","statusCode":404}`))
	})

	err := c.Do(context.Background(), http.MethodGet, NativePath("repositories", "x", "y"), nil, nil)
	if !IsNotFound(err) {
		t.Fatalf("expected not found error, got %v", err)
	}
	if want := "ghost API GET /api/repositories/x/y: 404 Not Found"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestGetUser(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/v3/users/some%20one" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"login":"someone","id":11,"node_id":"U_1","name":"Some One","type":"Organization",` +
			`"site_admin":false,"avatar_url":"a","html_url":"h","created_at":"t1","updated_at":"t2"}`))
	})

	u, err := c.GetUser(context.Background(), "some one")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	want := User{Login: "someone", ID: 11, NodeID: "U_1", Name: "Some One", Type: "Organization",
		AvatarURL: "a", HTMLURL: "h", CreatedAt: "t1", UpdatedAt: "t2"}
	if *u != want {
		t.Errorf("got %+v, want %+v", *u, want)
	}
}

func TestGetUserNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})

	if _, err := c.GetUser(context.Background(), "nobody"); !IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}
