package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const testTeamJSON = `{"id":"T1","name":"Core Team","slug":"core-team","members":[{"username":"alice","name":"Alice","image":null,"maintainer":true},{"username":"bob","name":"Bob","image":"https://img.test/b.png","maintainer":false}],"repositories":[{"slug":"api","name":"API","visibility":"private","role":"maintain"}],"viewerCanManage":true}`

func TestGetTeam(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/organizations/my%20org/teams/core%2Fteam" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(testTeamJSON))
	})

	team, err := c.GetTeam(context.Background(), "my org", "core/team")
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if team.ID != "T1" || team.Name != "Core Team" || team.Slug != "core-team" || !team.ViewerCanManage {
		t.Errorf("unexpected team: %+v", team)
	}
	if len(team.Members) != 2 || team.Members[0].Image != nil || !team.Members[0].Maintainer ||
		team.Members[1].Image == nil || *team.Members[1].Image != "https://img.test/b.png" || team.Members[1].Maintainer {
		t.Errorf("unexpected members: %+v", team.Members)
	}
	if len(team.Repositories) != 1 || team.Repositories[0] != (TeamRepository{Slug: "api", Name: "API", Visibility: "private", Role: "maintain"}) {
		t.Errorf("unexpected repositories: %+v", team.Repositories)
	}
}

func TestGetTeamNotFound(t *testing.T) {
	for _, msg := range []string{"Team not found", "Organization not found: nope"} {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"` + msg + `"}`))
		})

		_, err := c.GetTeam(context.Background(), "nope", "x")
		var apiErr *APIError
		if !IsNotFound(err) || !errors.As(err, &apiErr) || apiErr.Message != msg {
			t.Errorf("GetTeam error = %v, want a 404 with message %q", err, msg)
		}
	}
}

func TestListOrganizationTeams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/organizations/acme/teams" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"teams":[{"id":"T1","name":"Core","memberCount":3,"repositoryCount":2,"slug":"core"}]}`))
	})

	teams, err := c.ListOrganizationTeams(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ListOrganizationTeams: %v", err)
	}
	if len(teams) != 1 || teams[0] != (TeamSummary{ID: "T1", Name: "Core", Slug: "core", MemberCount: 3, RepositoryCount: 2}) {
		t.Errorf("unexpected teams: %+v", teams)
	}
}

func TestGetTeamByID(t *testing.T) {
	var paths []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/organizations/acme/teams" {
			_, _ = w.Write([]byte(`{"teams":[{"id":"T0","name":"Other","memberCount":0,"repositoryCount":0,"slug":"other"},{"id":"T1","name":"Core Team","memberCount":2,"repositoryCount":1,"slug":"core-team"}]}`))
			return
		}
		_, _ = w.Write([]byte(testTeamJSON))
	})

	team, err := c.GetTeamByID(context.Background(), "acme", "T1")
	if err != nil || team.ID != "T1" {
		t.Fatalf("GetTeamByID = %+v, %v", team, err)
	}
	if strings.Join(paths, " ") != "/api/organizations/acme/teams /api/organizations/acme/teams/core-team" {
		t.Errorf("requests = %v", paths)
	}

	_, err = c.GetTeamByID(context.Background(), "acme", "t1")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "no team with ID t1") {
		t.Errorf("GetTeamByID of a missing ID: got %v, want a 404", err)
	}
}

func TestListRepositoryTeams(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != "/api/repositories/acme/my%20repo/teams" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		_, _ = w.Write([]byte(`{"teams":[{"id":"T1","name":"Core","memberCount":2,"role":"write"},{"id":"T2","name":"Other","memberCount":0,"role":null}]}`))
	})

	teams, err := c.ListRepositoryTeams(context.Background(), "acme", "my repo")
	if err != nil {
		t.Fatalf("ListRepositoryTeams: %v", err)
	}
	if len(teams) != 2 || teams[0].ID != "T1" || teams[0].Name != "Core" || teams[0].MemberCount != 2 ||
		teams[0].Role == nil || *teams[0].Role != "write" || teams[1].ID != "T2" || teams[1].Role != nil {
		t.Errorf("unexpected teams: %+v", teams)
	}
}

func TestListRepositoryTeamsUserRepository(t *testing.T) {
	const msg = "Only an organization’s repositories can be shared with teams"
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"statusCode":400,"error":"Error","message":"` + msg + `"}`))
	})

	_, err := c.ListRepositoryTeams(context.Background(), "alice", "repo")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != msg {
		t.Errorf("ListRepositoryTeams error = %v, want a 400 with message %q", err, msg)
	}
}

func TestSetRepositoryTeamRole(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.EscapedPath() != "/api/repositories/acme/repo/teams/T%2F1" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(body)) != `{"role":"maintain"}` {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.SetRepositoryTeamRole(context.Background(), "acme", "repo", "T/1", "maintain"); err != nil {
		t.Fatalf("SetRepositoryTeamRole: %v", err)
	}
}

func TestRemoveRepositoryTeam(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/api/repositories/acme/repo/teams/T1" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"error":"Error","message":"Team not found in this organization"}`))
	})

	if err := c.RemoveRepositoryTeam(context.Background(), "acme", "repo", "T1"); err != nil {
		t.Fatalf("RemoveRepositoryTeam: %v", err)
	}
	if err := c.RemoveRepositoryTeam(context.Background(), "acme", "repo", "T1"); !IsNotFound(err) {
		t.Errorf("second RemoveRepositoryTeam error = %v, want a 404", err)
	}
}

func TestTeamMemberMethods(t *testing.T) {
	type call func(c *Client) error
	tests := []struct {
		name   string
		call   call
		method string
		path   string
	}{
		{"AddTeamMember", func(c *Client) error {
			return c.AddTeamMember(context.Background(), "my org", "core/team", "Al ice")
		}, http.MethodPut, "/api/organizations/my%20org/teams/core%2Fteam/members/Al%20ice"},
		{"RemoveTeamMember", func(c *Client) error {
			return c.RemoveTeamMember(context.Background(), "acme", "core", "alice")
		}, http.MethodDelete, "/api/organizations/acme/teams/core/members/alice"},
		{"AppointTeamMaintainer", func(c *Client) error {
			return c.AppointTeamMaintainer(context.Background(), "acme", "core", "alice")
		}, http.MethodPut, "/api/organizations/acme/teams/core/maintainers/alice"},
		{"DismissTeamMaintainer", func(c *Client) error {
			return c.DismissTeamMaintainer(context.Background(), "acme", "core", "alice")
		}, http.MethodDelete, "/api/organizations/acme/teams/core/maintainers/alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.EscapedPath() != tt.path {
					t.Errorf("got %s %s, want %s %s", r.Method, r.URL.EscapedPath(), tt.method, tt.path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("Authorization = %q", got)
				}
				if body, _ := io.ReadAll(r.Body); len(body) != 0 {
					t.Errorf("body = %s, want none", body)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			if err := tt.call(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
		})
	}
}

func TestAddTeamMemberErrors(t *testing.T) {
	tests := []struct {
		status int
		msg    string
	}{
		{http.StatusBadRequest, "pippinextra is not a member here"},
		{http.StatusNotFound, "User nobody not found"},
		{http.StatusNotFound, "Team not found"},
		{http.StatusForbidden, "Admins and the team maintainers only"},
	}
	for _, tt := range tests {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(`{"statusCode":0,"error":"Error","message":"` + tt.msg + `"}`))
		})

		err := c.AddTeamMember(context.Background(), "acme", "core", "x")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != tt.status || apiErr.Message != tt.msg {
			t.Errorf("AddTeamMember error = %v, want %d %q", err, tt.status, tt.msg)
		}
	}
}
