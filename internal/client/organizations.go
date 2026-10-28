package client

import (
	"context"
	"encoding/json"
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

// OrganizationSettings are an organization's settings, which only its
// admins can read and change.
type OrganizationSettings struct {
	// BasePermission is every member's role on every repository: read,
	// triage, write, maintain or admin. Nil grants members nothing beyond
	// their teams and collaborations.
	BasePermission                      *string `json:"basePermission"`
	MembersCanCreatePublicRepositories  bool    `json:"membersCanCreatePublicRepositories"`
	MembersCanCreatePrivateRepositories bool    `json:"membersCanCreatePrivateRepositories"`
	// AllowPrivateForks reports whether private repositories may be forked.
	AllowPrivateForks bool `json:"allowPrivateForks"`
	// OAuthAppRestrictions limits OAuth apps to the ones an admin approved.
	OAuthAppRestrictions bool `json:"oauthAppRestrictions"`
	// DefaultBranch is recorded as the default branch of each new
	// repository. Ghost doesn't validate it.
	DefaultBranch *string `json:"defaultBranch"`
	Description   *string `json:"description"`
	Website       *string `json:"website"`
	Location      *string `json:"location"`
	Email         *string `json:"email"`
}

// Nullable is a field of a partial update: unset fields are left out of the
// request, and a set field with a nil Value is sent as null, which clears it.
type Nullable[T any] struct {
	Set   bool
	Value *T
}

// Null returns a set Nullable that clears its field.
func Null[T any]() Nullable[T] { return Nullable[T]{Set: true} }

// Value returns a set Nullable holding v.
func Value[T any](v T) Nullable[T] { return Nullable[T]{Set: true, Value: &v} }

// UpdateOrganizationSettingsRequest is a partial update of an
// organization's settings. Only the set fields are sent.
type UpdateOrganizationSettingsRequest struct {
	BasePermission                      Nullable[string]
	MembersCanCreatePublicRepositories  Nullable[bool]
	MembersCanCreatePrivateRepositories Nullable[bool]
	AllowPrivateForks                   Nullable[bool]
	OAuthAppRestrictions                Nullable[bool]
	DefaultBranch                       Nullable[string]
	Description                         Nullable[string]
	Website                             Nullable[string]
	Location                            Nullable[string]
	Email                               Nullable[string]
}

func (r UpdateOrganizationSettingsRequest) fields() map[string]any {
	m := map[string]any{}
	addNullable(m, "basePermission", r.BasePermission)
	addNullable(m, "membersCanCreatePublicRepositories", r.MembersCanCreatePublicRepositories)
	addNullable(m, "membersCanCreatePrivateRepositories", r.MembersCanCreatePrivateRepositories)
	addNullable(m, "allowPrivateForks", r.AllowPrivateForks)
	addNullable(m, "oauthAppRestrictions", r.OAuthAppRestrictions)
	addNullable(m, "defaultBranch", r.DefaultBranch)
	addNullable(m, "description", r.Description)
	addNullable(m, "website", r.Website)
	addNullable(m, "location", r.Location)
	addNullable(m, "email", r.Email)

	return m
}

func addNullable[T any](m map[string]any, key string, f Nullable[T]) {
	if f.Set {
		m[key] = f.Value
	}
}

// IsEmpty reports whether the request changes nothing. Ghost answers an
// empty update with a 500, so callers skip it.
func (r UpdateOrganizationSettingsRequest) IsEmpty() bool {
	return len(r.fields()) == 0
}

// MarshalJSON encodes only the set fields, with null for cleared ones.
func (r UpdateOrganizationSettingsRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.fields())
}

// GetOrganizationSettings returns the settings of the organization with
// the given slug. Only admins can read them: Ghost answers 404
// "Organization not found" outside the organization (and its spec lists a
// 403 for members who aren't admins).
func (c *Client) GetOrganizationSettings(ctx context.Context, slug string) (*OrganizationSettings, error) {
	var s OrganizationSettings
	if err := c.Do(ctx, http.MethodGet, NativePath("organizations", slug, "settings"), nil, &s); err != nil {
		return nil, err
	}

	return &s, nil
}

// UpdateOrganizationSettings changes the set fields of the organization's
// settings and returns all of them. An empty request returns the current
// settings without sending an update.
func (c *Client) UpdateOrganizationSettings(ctx context.Context, slug string, req UpdateOrganizationSettingsRequest) (*OrganizationSettings, error) {
	if req.IsEmpty() {
		return c.GetOrganizationSettings(ctx, slug)
	}

	var s OrganizationSettings
	if err := c.Do(ctx, http.MethodPatch, NativePath("organizations", slug, "settings"), req, &s); err != nil {
		return nil, err
	}

	return &s, nil
}
