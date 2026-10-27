package client

import (
	"context"
	"errors"
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
