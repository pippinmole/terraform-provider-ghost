package client

import (
	"context"
	"net/http"
)

// Organization is an organization's public profile, as returned by the
// native API. Ghost's v3 API has no route for organizations.
type Organization struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Logo        *string `json:"logo"`
	CreatedAt   string  `json:"createdAt"`
	Description *string `json:"description"`
	Website     *string `json:"website"`
	Location    *string `json:"location"`
	Email       *string `json:"email"`
	// RepositoryCount counts public repositories only.
	RepositoryCount int64 `json:"repositoryCount"`
	// Members holds every member when the token's user is one, and only
	// the public members otherwise.
	Members []OrganizationMember `json:"members"`
	// Pinned holds the pinned repositories the token's user can read, in
	// pinned order.
	Pinned []PinnedRepository `json:"pinned"`
	// ViewerRole is the token user's role in the organization: member,
	// admin or owner. It is nil outside the organization, and when the
	// user holds only custom roles.
	ViewerRole *string `json:"viewerRole"`
}

// OrganizationMember is one member of an organization.
type OrganizationMember struct {
	Username string  `json:"username"`
	Name     string  `json:"name"`
	Image    *string `json:"image"`
	// Public reports whether the member shows their membership outside
	// the organization.
	Public bool `json:"public"`
}

// PinnedRepository is a repository pinned to an organization's profile. It
// belongs to the organization.
type PinnedRepository struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Visibility  string  `json:"visibility"`
}

// GetOrganization returns the profile of the organization with the given
// slug, which Ghost matches case-insensitively. The profile is public, so
// it doesn't need the token's user to be a member.
func (c *Client) GetOrganization(ctx context.Context, slug string) (*Organization, error) {
	var o Organization
	if err := c.Do(ctx, http.MethodGet, NativePath("organizations", slug), nil, &o); err != nil {
		return nil, err
	}

	return &o, nil
}
