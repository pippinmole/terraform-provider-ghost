package client

import (
	"context"
	"net/http"
	"testing"
)

func TestListGitignoreTemplates(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/gitignore/templates" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`["C","C++","Go","JENKINS_HOME","bun","ecu.test"]`))
	})

	names, err := c.ListGitignoreTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListGitignoreTemplates: %v", err)
	}
	want := []string{"C", "C++", "Go", "JENKINS_HOME", "bun", "ecu.test"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q (order must be kept)", i, names[i], want[i])
		}
	}
}

func TestGetGitignoreTemplate(t *testing.T) {
	for _, tc := range []struct{ name, path, canonical string }{
		{"go", "/api/v3/gitignore/templates/go", "Go"},
		{"C++", "/api/v3/gitignore/templates/C++", "C++"},
		{"ecu.test", "/api/v3/gitignore/templates/ecu.test", "ecu.test"},
		{"a b/c?", "/api/v3/gitignore/templates/a%20b%2Fc%3F", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.EscapedPath() != tc.path {
					t.Errorf("got %s %s, want %s", r.Method, r.URL.EscapedPath(), tc.path)
				}
				_, _ = w.Write([]byte(`{"name":"` + tc.canonical + `","source":"*.exe\n# end\n"}`))
			})

			tmpl, err := c.GetGitignoreTemplate(context.Background(), tc.name)
			if err != nil {
				t.Fatalf("GetGitignoreTemplate: %v", err)
			}
			if tmpl.Name != tc.canonical || tmpl.Source != "*.exe\n# end\n" {
				t.Errorf("unexpected template: %+v", tmpl)
			}
		})
	}
}

func TestGetGitignoreTemplateNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`))
	})

	_, err := c.GetGitignoreTemplate(context.Background(), "Nope")
	if !IsNotFound(err) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
}
