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

// RepositoryTeam is a team of a repository's organization with its role on
// the repository. Role is nil when the team has no access.
type RepositoryTeam struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	MemberCount int64   `json:"memberCount"`
	Role        *string `json:"role"`
}

// ListRepositoryTeams returns every team in the organization that owns
// owner/repo, each with its role on the repository. Only repository admins
// may list them, and Ghost returns 400 for a repository a user owns.
func (c *Client) ListRepositoryTeams(ctx context.Context, owner, repo string) ([]RepositoryTeam, error) {
	var out struct {
		Teams []RepositoryTeam `json:"teams"`
	}
	if err := c.Do(ctx, http.MethodGet, NativePath("repositories", owner, repo, "teams"), nil, &out); err != nil {
		return nil, err
	}

	return out.Teams, nil
}

// SetRepositoryTeamRole gives the team with the given ID role on owner/repo,
// or changes the role it has. Ghost takes the team's ID, not its slug, and
// returns 404 "Team not found in this organization" for anything else.
func (c *Client) SetRepositoryTeamRole(ctx context.Context, owner, repo, teamID, role string) error {
	body := struct {
		Role string `json:"role"`
	}{Role: role}

	return c.Do(ctx, http.MethodPut, NativePath("repositories", owner, repo, "teams", teamID), body, nil)
}

// RemoveRepositoryTeam removes the team's access to owner/repo. Ghost returns
// 404 when the team has no access.
func (c *Client) RemoveRepositoryTeam(ctx context.Context, owner, repo, teamID string) error {
	return c.Do(ctx, http.MethodDelete, NativePath("repositories", owner, repo, "teams", teamID), nil, nil)
}

// AddTeamMember adds the user to the team with the given slug, or does
// nothing when they're already in it. Ghost only adds members of the
// organization: anyone else gets 400 "<user> is not a member here". It takes
// the team's slug, not its ID, and keeps a maintainer a maintainer.
func (c *Client) AddTeamMember(ctx context.Context, org, teamSlug, username string) error {
	return c.Do(ctx, http.MethodPut, NativePath("organizations", org, "teams", teamSlug, "members", username), nil, nil)
}

// RemoveTeamMember removes the user from the team with the given slug, which
// also ends their role as a maintainer. Ghost returns 204 when they weren't
// in the team, and 404 when the user or team doesn't exist.
func (c *Client) RemoveTeamMember(ctx context.Context, org, teamSlug, username string) error {
	return c.Do(ctx, http.MethodDelete, NativePath("organizations", org, "teams", teamSlug, "members", username), nil, nil)
}

// AppointTeamMaintainer makes a member of the team with the given slug one
// of its maintainers. Ghost returns 400 "<user> is not a member here" for
// someone who isn't in the team.
func (c *Client) AppointTeamMaintainer(ctx context.Context, org, teamSlug, username string) error {
	return c.Do(ctx, http.MethodPut, NativePath("organizations", org, "teams", teamSlug, "maintainers", username), nil, nil)
}

// DismissTeamMaintainer makes a maintainer of the team with the given slug
// an ordinary member again. Only organization admins may, and Ghost returns
// 204 when the user wasn't a maintainer.
func (c *Client) DismissTeamMaintainer(ctx context.Context, org, teamSlug, username string) error {
	return c.Do(ctx, http.MethodDelete, NativePath("organizations", org, "teams", teamSlug, "maintainers", username), nil, nil)
}
