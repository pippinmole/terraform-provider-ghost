package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGetOrganization(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/organizations/my%20org" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme Inc","logo":"https://img.test/l.png","createdAt":"2026-10-27T18:49:54.738Z",` +
			`"description":"Rockets","website":null,"location":"Earth","email":null,"repositoryCount":2,` +
			`"members":[{"username":"bob","name":"Bob","image":null,"public":false},{"username":"alice","name":"Alice","image":"https://img.test/a.png","public":true}],` +
			`"pinned":[{"slug":"web","name":"Web","description":null,"visibility":"public"}],"viewerRole":"owner"}`))
	})

	o, err := c.GetOrganization(context.Background(), "my org")
	if err != nil {
		t.Fatalf("GetOrganization: %v", err)
	}
	if o.Slug != "acme" || o.Name != "Acme Inc" || o.Logo == nil || *o.Logo != "https://img.test/l.png" ||
		o.CreatedAt != "2026-10-27T18:49:54.738Z" || o.Description == nil || *o.Description != "Rockets" ||
		o.Website != nil || o.Location == nil || *o.Location != "Earth" || o.Email != nil || o.RepositoryCount != 2 ||
		o.ViewerRole == nil || *o.ViewerRole != "owner" {
		t.Errorf("unexpected organization: %+v", o)
	}
	if len(o.Members) != 2 || o.Members[0].Username != "bob" || o.Members[0].Image != nil || o.Members[0].Public ||
		o.Members[1].Image == nil || !o.Members[1].Public {
		t.Errorf("unexpected members: %+v", o.Members)
	}
	if len(o.Pinned) != 1 || o.Pinned[0] != (PinnedRepository{Slug: "web", Name: "Web", Visibility: "public"}) {
		t.Errorf("unexpected pinned: %+v", o.Pinned)
	}
}

func TestGetOrganizationOutsider(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"slug":"acme","name":"Acme","logo":null,"createdAt":"2026-10-27T18:49:54.738Z","description":null,"website":null,"location":null,"email":null,"repositoryCount":0,"members":[],"pinned":[],"viewerRole":null}`))
	})

	o, err := c.GetOrganization(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetOrganization: %v", err)
	}
	if o.ViewerRole != nil || o.Logo != nil || o.Members == nil || len(o.Members) != 0 || len(o.Pinned) != 0 {
		t.Errorf("unexpected organization: %+v", o)
	}
}

func TestGetOrganizationNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"Organization not found: nope"}`))
	})

	_, err := c.GetOrganization(context.Background(), "nope")
	var apiErr *APIError
	if !IsNotFound(err) || !errors.As(err, &apiErr) || apiErr.Message != "Organization not found: nope" {
		t.Errorf("GetOrganization error = %v, want a 404", err)
	}
}

const testOrganizationSettings = `{"basePermission":null,"membersCanCreatePublicRepositories":true,"membersCanCreatePrivateRepositories":false,` +
	`"allowPrivateForks":true,"oauthAppRestrictions":false,"defaultBranch":"trunk","description":"","website":null,"location":"Earth","email":null}`

func TestGetOrganizationSettings(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/organizations/my%20org/settings" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(testOrganizationSettings))
	})

	s, err := c.GetOrganizationSettings(context.Background(), "my org")
	if err != nil {
		t.Fatalf("GetOrganizationSettings: %v", err)
	}
	if s.BasePermission != nil || !s.MembersCanCreatePublicRepositories || s.MembersCanCreatePrivateRepositories ||
		!s.AllowPrivateForks || s.OAuthAppRestrictions || s.DefaultBranch == nil || *s.DefaultBranch != "trunk" ||
		s.Description == nil || *s.Description != "" || s.Website != nil || s.Location == nil || *s.Location != "Earth" || s.Email != nil {
		t.Errorf("unexpected settings: %+v", s)
	}
}

func TestUpdateOrganizationSettings(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPatch || r.URL.EscapedPath() != "/api/organizations/acme/settings" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		// Only the set fields are sent, and cleared ones as null.
		const want = `{"allowPrivateForks":true,"basePermission":null,"defaultBranch":"trunk","description":"","website":null}`
		if string(body) != want {
			t.Errorf("body = %s, want %s", body, want)
		}
		_, _ = w.Write([]byte(testOrganizationSettings))
	})

	s, err := c.UpdateOrganizationSettings(context.Background(), "acme", UpdateOrganizationSettingsRequest{
		BasePermission:    Null[string](),
		AllowPrivateForks: Value(true),
		DefaultBranch:     Value("trunk"),
		Description:       Value(""),
		Website:           Null[string](),
	})
	if err != nil {
		t.Fatalf("UpdateOrganizationSettings: %v", err)
	}
	if s.DefaultBranch == nil || *s.DefaultBranch != "trunk" {
		t.Errorf("unexpected settings: %+v", s)
	}
}

func TestUpdateOrganizationSettingsEmpty(t *testing.T) {
	// Ghost answers an empty update with a 500, so it's a GET instead.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/organizations/acme/settings" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(testOrganizationSettings))
	})

	if !(UpdateOrganizationSettingsRequest{}).IsEmpty() {
		t.Error("an empty request isn't empty")
	}
	if (UpdateOrganizationSettingsRequest{Email: Null[string]()}).IsEmpty() {
		t.Error("a request that clears a field is empty")
	}
	if _, err := c.UpdateOrganizationSettings(context.Background(), "acme", UpdateOrganizationSettingsRequest{}); err != nil {
		t.Fatalf("UpdateOrganizationSettings: %v", err)
	}
}

func TestUpdateOrganizationSettingsValidation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":["website must be a URL address"],"error":"Bad Request","statusCode":400}`))
	})

	_, err := c.UpdateOrganizationSettings(context.Background(), "acme", UpdateOrganizationSettingsRequest{Website: Value("nope")})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "website must be a URL address" {
		t.Errorf("UpdateOrganizationSettings error = %v, want a 400", err)
	}
}

func TestSetOrganizationPins(t *testing.T) {
	for _, tt := range []struct {
		name  string
		slugs []string
		want  string
	}{
		{"ordered", []string{"web", "api"}, `{"repositories":["web","api"]}`},
		{"nil clears", nil, `{"repositories":[]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/organizations/my%20org/pins" || string(body) != tt.want {
					t.Errorf("got %s %s %s", r.Method, r.URL.EscapedPath(), body)
				}
				w.WriteHeader(http.StatusNoContent)
			})

			if err := c.SetOrganizationPins(context.Background(), "my org", tt.slugs); err != nil {
				t.Fatalf("SetOrganizationPins: %v", err)
			}
		})
	}
}

func TestSetOrganizationPinsNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"Repository not found"}`))
	})

	err := c.SetOrganizationPins(context.Background(), "acme", []string{"nope"})
	if !IsNotFound(err) {
		t.Errorf("SetOrganizationPins error = %v, want a 404", err)
	}
}

func TestListOutsideCollaborators(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/organizations/a%2Fb/outside-collaborators" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"collaborators":[{"username":"bob","name":"Bob","image":null,"repositories":[` +
			`{"slug":"web","role":"write","pending":true},{"slug":"api","role":"read","pending":false}]},` +
			`{"username":"alice","name":"Alice","image":"https://img.test/a.png","repositories":[]}]}`))
	})

	got, err := c.ListOutsideCollaborators(context.Background(), "a/b")
	if err != nil {
		t.Fatalf("ListOutsideCollaborators: %v", err)
	}
	if len(got) != 2 || got[0].Username != "bob" || got[0].Name != "Bob" || got[0].Image != nil ||
		got[1].Image == nil || *got[1].Image != "https://img.test/a.png" || len(got[1].Repositories) != 0 {
		t.Fatalf("unexpected collaborators: %+v", got)
	}
	want := []OutsideCollaboratorRepository{{Slug: "web", Role: "write", Pending: true}, {Slug: "api", Role: "read"}}
	if len(got[0].Repositories) != 2 || got[0].Repositories[0] != want[0] || got[0].Repositories[1] != want[1] {
		t.Errorf("unexpected repositories: %+v", got[0].Repositories)
	}
}

func TestListOutsideCollaboratorsEmpty(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"collaborators":[]}`))
	})

	got, err := c.ListOutsideCollaborators(context.Background(), "acme")
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("ListOutsideCollaborators = %+v, %v, want an empty list", got, err)
	}
}

func TestListOutsideCollaboratorsErrors(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"statusCode":0,"message":"nope"}`))
		})

		_, err := c.ListOutsideCollaborators(context.Background(), "acme")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != status || apiErr.Message != "nope" {
			t.Errorf("status %d: error = %v", status, err)
		}
	}
}

func TestSetOrganizationLogo(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/organizations/my%20org/logo" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		if ct := r.Header.Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("Content-Type = %q", ct)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.ContentLength != 4 {
			t.Errorf("Authorization %q, Content-Length %d", r.Header.Get("Authorization"), r.ContentLength)
		}
		if b, _ := io.ReadAll(r.Body); string(b) != "\xff\xd8\xff\xe0" {
			t.Errorf("body = %q", b)
		}
		_, _ = io.WriteString(w, `{"url":"https://img.test/api/users/avatars/u1/a.jpg"}`)
	})

	u, err := c.SetOrganizationLogo(context.Background(), "my org", "image/jpeg", strings.NewReader("\xff\xd8\xff\xe0"), 4)
	if err != nil {
		t.Fatalf("SetOrganizationLogo: %v", err)
	}
	if u != "https://img.test/api/users/avatars/u1/a.jpg" {
		t.Errorf("url = %q", u)
	}
}

func TestSetOrganizationLogoRetriesFromStart(t *testing.T) {
	var calls atomic.Int32
	c, _ := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if b, _ := io.ReadAll(r.Body); string(b) != "\x89PNG" {
			t.Errorf("attempt %d body = %q", calls.Load()+1, b)
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"url":"https://img.test/l.png"}`)
	})

	if _, err := c.SetOrganizationLogo(context.Background(), "acme", "image/png", strings.NewReader("\x89PNG"), 4); err != nil {
		t.Fatalf("SetOrganizationLogo: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestSetOrganizationLogoErrors(t *testing.T) {
	for _, status := range []int{400, 403, 404, 413, 415} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"statusCode":0,"message":"nope"}`)
		})
		_, err := c.SetOrganizationLogo(context.Background(), "acme", "image/png", strings.NewReader("x"), 1)
		if !HasStatus(err, status) || !strings.Contains(err.Error(), "nope") {
			t.Errorf("status %d: err = %v", status, err)
		}
	}
}

func TestRemoveOrganizationLogo(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.EscapedPath() != "/api/organizations/a%2Fb/logo" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.RemoveOrganizationLogo(context.Background(), "a/b"); err != nil {
		t.Fatalf("RemoveOrganizationLogo: %v", err)
	}
}

func TestDownloadLogo(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/users/avatars/u1/a.png" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, "\x89PNG")
	})

	var buf bytes.Buffer
	if err := c.DownloadLogo(context.Background(), c.baseURL.String()+"/api/users/avatars/u1/a.png", &buf); err != nil {
		t.Fatalf("DownloadLogo: %v", err)
	}
	if buf.String() != "\x89PNG" {
		t.Errorf("downloaded %q", buf.String())
	}

	// The token is never sent to another host, or outside the API.
	for _, u := range []string{"https://elsewhere.test/api/users/avatars/u1/a.png", c.baseURL.String() + "/other/a.png", "::"} {
		if err := c.DownloadLogo(context.Background(), u, &buf); err == nil {
			t.Errorf("DownloadLogo(%q) succeeded", u)
		}
	}
}

func TestSetOrganizationMembershipPublic(t *testing.T) {
	for _, tc := range []struct {
		public bool
		method string
	}{{true, http.MethodPut}, {false, http.MethodDelete}} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != tc.method || r.URL.EscapedPath() != "/api/organizations/a%2Fb/public-members" {
				t.Errorf("public=%t: got %s %s", tc.public, r.Method, r.URL.EscapedPath())
			}
			if r.ContentLength > 0 {
				t.Errorf("public=%t: sent a body of %d bytes", tc.public, r.ContentLength)
			}
			w.WriteHeader(http.StatusNoContent)
		})

		if err := c.SetOrganizationMembershipPublic(context.Background(), "a/b", tc.public); err != nil {
			t.Fatalf("SetOrganizationMembershipPublic(%t): %v", tc.public, err)
		}
	}
}

func TestSetOrganizationMembershipPublicNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Organization not found: nope","statusCode":404}`)
	})

	err := c.SetOrganizationMembershipPublic(context.Background(), "nope", true)
	if !IsNotFound(err) || !strings.Contains(err.Error(), "Organization not found: nope") {
		t.Fatalf("got %v", err)
	}
}
