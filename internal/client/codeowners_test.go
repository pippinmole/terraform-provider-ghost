package client

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestGetCodeowners(t *testing.T) {
	const errorsBody = `{"path":".github/CODEOWNERS","errors":[{"line":4,"column":7,"kind":"Unknown owner",` +
		`"source":"docs/ @alcie","suggestion":"@alice","message":"Unknown owner on line 4","path":".github/CODEOWNERS"},` +
		`{"line":6,"column":1,"kind":"Invalid pattern","source":"[bad @alice","suggestion":null,` +
		`"message":"Invalid pattern on line 6","path":".github/CODEOWNERS"}]}`
	file := ".github/CODEOWNERS"
	alice := "@alice"

	for _, tc := range []struct {
		name, owner, repo, ref, path, query, body string
		want                                      Codeowners
	}{
		{
			name: "errors", owner: "alice", repo: "app", ref: "main",
			path: "/api/repositories/alice/app/codeowners", query: "ref=main", body: errorsBody,
			want: Codeowners{Path: &file, Errors: []CodeownersError{
				{Line: 4, Column: 7, Kind: "Unknown owner", Source: "docs/ @alcie", Suggestion: &alice,
					Message: "Unknown owner on line 4", Path: file},
				{Line: 6, Column: 1, Kind: "Invalid pattern", Source: "[bad @alice",
					Message: "Invalid pattern on line 6", Path: file},
			}},
		},
		{
			name: "default branch", owner: "alice", repo: "app",
			path: "/api/repositories/alice/app/codeowners", body: `{"path":null,"errors":[]}`,
			want: Codeowners{Errors: []CodeownersError{}},
		},
		{
			name: "escaped", owner: "a b", repo: "c/d", ref: "feat/x y&z",
			path: "/api/repositories/a%20b/c%2Fd/codeowners", query: "ref=feat%2Fx+y%26z", body: `{"path":null,"errors":null}`,
			want: Codeowners{Errors: []CodeownersError{}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != tc.path || r.URL.RawQuery != tc.query {
					t.Errorf("got %s %s?%s, want GET %s?%s", r.Method, r.URL.EscapedPath(), r.URL.RawQuery, tc.path, tc.query)
				}
				_, _ = w.Write([]byte(tc.body))
			})

			got, err := c.GetCodeowners(context.Background(), tc.owner, tc.repo, tc.ref)
			if err != nil {
				t.Fatalf("GetCodeowners: %v", err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Errorf("got %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestGetCodeownersErrors(t *testing.T) {
	for _, tc := range []struct {
		status              int
		message             string
		notFound, forbidden bool
	}{
		{http.StatusNotFound, "Branch not found: nope", true, false},
		{http.StatusNotFound, "Repository not found", true, false},
		{http.StatusForbidden, "Forbidden", false, true},
		{http.StatusUnauthorized, "Unauthorized", false, false},
		{http.StatusInternalServerError, "boom", false, false},
	} {
		t.Run(tc.message, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"statusCode":%d,"message":%q}`, tc.status, tc.message)
			})

			got, err := c.GetCodeowners(context.Background(), "alice", "app", "nope")
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("got %v, %v, want an error with %q", got, err, tc.message)
			}
			if IsNotFound(err) != tc.notFound || IsForbidden(err) != tc.forbidden {
				t.Errorf("IsNotFound = %v, IsForbidden = %v for %v", IsNotFound(err), IsForbidden(err), err)
			}
			if apiErr, ok := err.(*APIError); !ok || apiErr.Message != tc.message {
				t.Errorf("err = %#v, want the API message", err)
			}
		})
	}
}
