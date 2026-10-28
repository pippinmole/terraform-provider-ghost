package client

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestListStatusContexts(t *testing.T) {
	for _, tc := range []struct {
		name, owner, repo, path, body string
		want                          []string
	}{
		{"order kept", "alice", "app", "/api/repositories/alice/app/status-contexts",
			`{"contexts":["CI/Build","ci/build","default"]}`, []string{"CI/Build", "ci/build", "default"}},
		{"escaped", "a b", "c/d", "/api/repositories/a%20b/c%2Fd/status-contexts",
			`{"contexts":[]}`, []string{}},
		{"null is empty", "alice", "app", "/api/repositories/alice/app/status-contexts",
			`{"contexts":null}`, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != tc.path {
					t.Errorf("got %s %s, want GET %s", r.Method, r.URL.EscapedPath(), tc.path)
				}
				_, _ = w.Write([]byte(tc.body))
			})

			got, err := c.ListStatusContexts(context.Background(), tc.owner, tc.repo)
			if err != nil {
				t.Fatalf("ListStatusContexts: %v", err)
			}
			if got == nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("contexts = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestListStatusContextsErrors(t *testing.T) {
	for _, tc := range []struct {
		status    int
		notFound  bool
		forbidden bool
	}{
		{http.StatusNotFound, true, false},
		{http.StatusForbidden, false, true},
		{http.StatusInternalServerError, false, false},
	} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"statusCode":0,"message":"nope"}`))
		})

		_, err := c.ListStatusContexts(context.Background(), "alice", "app")
		if err == nil {
			t.Fatalf("%d: no error", tc.status)
		}
		if IsNotFound(err) != tc.notFound || IsForbidden(err) != tc.forbidden {
			t.Errorf("%d: IsNotFound = %v, IsForbidden = %v", tc.status, IsNotFound(err), IsForbidden(err))
		}
	}
}

func TestCreateCommitStatus(t *testing.T) {
	const sha = "43b5a608cc804a9b40c3f3cf028114e4356b8f6d"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if want := "/api/v3/repos/alice/app/statuses/" + sha; r.Method != http.MethodPost || r.URL.Path != want {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, want)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"state": "success", "context": "ci/build"}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("body = %v, want %v (empty fields left out)", body, want)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":62,"state":"success","context":"ci/build","description":null,"target_url":null}`))
	})

	s, err := c.CreateCommitStatus(context.Background(), "alice", "app", sha, CreateCommitStatusRequest{State: "success", Context: "ci/build"})
	if err != nil {
		t.Fatalf("CreateCommitStatus: %v", err)
	}
	if want := (&CreatedCommitStatus{ID: 62, State: "success", Context: "ci/build"}); !reflect.DeepEqual(s, want) {
		t.Errorf("status = %+v, want %+v", s, want)
	}
}
