package client

import (
	"context"
	"errors"
	"net/http"
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
