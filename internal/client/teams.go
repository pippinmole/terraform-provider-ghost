package client

import (
	"context"
	"fmt"
	"net/http"
)

// TeamSummary is a team as listed by the native API.
type TeamSummary struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	MemberCount     int64  `json:"memberCount"`
	RepositoryCount int64  `json:"repositoryCount"`
}

// Team is a team with its members and repositories, as returned by the
// native API.
type Team struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Slug         string           `json:"slug"`
	Members      []TeamMember     `json:"members"`
	Repositories []TeamRepository `json:"repositories"`
	// ViewerCanManage reports whether the token's user may change the team's
	// members: an organization admin or one of the team's maintainers.
	ViewerCanManage bool `json:"viewerCanManage"`
}

// TeamMember is one member of a team.
type TeamMember struct {
	Username   string  `json:"username"`
	Name       string  `json:"name"`
	Image      *string `json:"image"`
	Maintainer bool    `json:"maintainer"`
}

// TeamRepository is a repository a team can reach. The repository belongs to
// the team's organization.
type TeamRepository struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
	Role       string `json:"role"`
}

// ListOrganizationTeams returns the teams of the organization with the given
// slug. Only organization members may list them.
func (c *Client) ListOrganizationTeams(ctx context.Context, org string) ([]TeamSummary, error) {
	var out struct {
		Teams []TeamSummary `json:"teams"`
	}
	if err := c.Do(ctx, http.MethodGet, NativePath("organizations", org, "teams"), nil, &out); err != nil {
		return nil, err
	}

	return out.Teams, nil
}

// GetTeam returns the team with the given slug. Ghost matches organization
// and team slugs case-insensitively, and has no endpoint to get a team by
// its ID: see GetTeamByID.
func (c *Client) GetTeam(ctx context.Context, org, slug string) (*Team, error) {
	var t Team
	if err := c.Do(ctx, http.MethodGet, NativePath("organizations", org, "teams", slug), nil, &t); err != nil {
		return nil, err
	}

	return &t, nil
}

// GetTeamByID finds the team with the given ID in the organization's list,
// then gets it by its slug. It returns a 404 APIError when the organization
// has no such team.
func (c *Client) GetTeamByID(ctx context.Context, org, id string) (*Team, error) {
	teams, err := c.ListOrganizationTeams(ctx, org)
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		if t.ID == id {
			return c.GetTeam(ctx, org, t.Slug)
		}
	}

	return nil, &APIError{
		StatusCode: http.StatusNotFound,
		Method:     http.MethodGet,
		Path:       NativePath("organizations", org, "teams"),
		Message:    fmt.Sprintf("Team not found: no team with ID %s", id),
	}
}
