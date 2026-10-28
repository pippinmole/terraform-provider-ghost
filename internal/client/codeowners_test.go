package client

import (
	"context"
	"net/http"
	"reflect"
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

func TestGetCodeownersNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"Branch not found: nope"}`))
	})

	_, err := c.GetCodeowners(context.Background(), "alice", "app", "nope")
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want a 404", err)
	}
	if apiErr, ok := err.(*APIError); !ok || apiErr.Message != "Branch not found: nope" {
		t.Errorf("err = %#v, want the API message", err)
	}
}
