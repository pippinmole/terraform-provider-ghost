package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// RepositoryOwner is the owner of a repository as returned by the v3 API.
type RepositoryOwner struct {
	Login   string `json:"login"`
	HTMLURL string `json:"html_url"`
}

// Repository is a repository as returned by the v3 API.
type Repository struct {
	ID            int64           `json:"id"`
	NodeID        string          `json:"node_id"`
	Name          string          `json:"name"`
	FullName      string          `json:"full_name"`
	Private       bool            `json:"private"`
	Visibility    string          `json:"visibility"`
	Owner         RepositoryOwner `json:"owner"`
	HTMLURL       string          `json:"html_url"`
	Description   *string         `json:"description"`
	Fork          bool            `json:"fork"`
	CloneURL      string          `json:"clone_url"`
	SSHURL        string          `json:"ssh_url"`
	DefaultBranch string          `json:"default_branch"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	PushedAt      string          `json:"pushed_at"`
}

// CreateRepositoryRequest is the body for creating a repository. Unset fields
// are omitted.
type CreateRepositoryRequest struct {
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	Visibility        string  `json:"visibility,omitempty"`
	AutoInit          *bool   `json:"auto_init,omitempty"`
	GitignoreTemplate string  `json:"gitignore_template,omitempty"`
	LicenseTemplate   string  `json:"license_template,omitempty"`
}

// UpdateRepositoryRequest is the body for updating a repository through the
// native API. Nil fields are left unchanged.
type UpdateRepositoryRequest struct {
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	Visibility    *string `json:"visibility,omitempty"`
	DefaultBranch *string `json:"defaultBranch,omitempty"`
}

// UpdateRepositoryResponse is returned by the native update endpoint. Renaming
// a repository changes its slug.
type UpdateRepositoryResponse struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
}

// CreateRepository creates a repository owned by org, or by the authenticated
// user when org is empty.
func (c *Client) CreateRepository(ctx context.Context, org string, req CreateRepositoryRequest) (*Repository, error) {
	path := V3Path("user", "repos")
	if org != "" {
		path = V3Path("orgs", org, "repos")
	}

	var repo Repository
	if err := c.Do(ctx, http.MethodPost, path, req, &repo); err != nil {
		return nil, err
	}

	return &repo, nil
}

// GetRepository returns the repository owner/repo.
func (c *Client) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	var r Repository
	if err := c.Do(ctx, http.MethodGet, V3Path("repos", owner, repo), nil, &r); err != nil {
		return nil, err
	}

	return &r, nil
}

// UpdateRepository updates the repository owner/slug via the native API.
func (c *Client) UpdateRepository(ctx context.Context, owner, slug string, req UpdateRepositoryRequest) (*UpdateRepositoryResponse, error) {
	var resp UpdateRepositoryResponse
	if err := c.Do(ctx, http.MethodPatch, NativePath("repositories", owner, slug), req, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

// DeleteRepository deletes the repository owner/slug via the native API.
func (c *Client) DeleteRepository(ctx context.Context, owner, slug string) error {
	return c.Do(ctx, http.MethodDelete, NativePath("repositories", owner, slug), nil, nil)
}

// SplitFullName splits an "owner/slug" full name into its parts. It returns an
// error unless there are exactly two non-empty segments.
func SplitFullName(fullName string) (owner, slug string, err error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repository full name %q, expected \"owner/slug\"", fullName)
	}

	return parts[0], parts[1], nil
}

// OwnerRepositoryPageSize is how many repositories ListOwnerRepositories
// asks for per page. Ghost allows 1 to 100. Tests lower it to exercise
// pagination without creating many repositories.
var OwnerRepositoryPageSize = 100

// OwnerRepository is one repository in the native API's list of an owner's
// repositories.
type OwnerRepository struct {
	ID          string  `json:"id"`
	NumericID   int64   `json:"numericId"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	// OrganizationID is set when an organization owns the repository.
	OrganizationID *string `json:"organizationId"`
	Visibility     string  `json:"visibility"`
	// ParentRepositoryID is set when the repository is a fork.
	ParentRepositoryID *string `json:"parentRepositoryId"`
	LastPushedAt       *string `json:"lastPushedAt"`
	CreatedAt          string  `json:"createdAt"`
	UpdatedAt          string  `json:"updatedAt"`
}

type ownerRepositoriesPage struct {
	Repositories []OwnerRepository `json:"repositories"`
	NextCursor   *string           `json:"nextCursor"`
	HasMore      bool              `json:"hasMore"`
}

// ListOwnerRepositories returns every repository of the user or
// organization owner that the token's user can read, following Ghost's
// cursor pagination. Ghost matches owner case-insensitively and returns the
// most recently updated first. A non-empty query keeps only repositories
// whose name or description contains it, ignoring case.
func (c *Client) ListOwnerRepositories(ctx context.Context, owner, query string) ([]OwnerRepository, error) {
	base := NativePath("repositories", owner)
	seenRepos := map[string]bool{}
	seenCursors := map[string]bool{}
	var repos []OwnerRepository
	cursor := ""

	for {
		params := url.Values{}
		params.Set("limit", strconv.Itoa(OwnerRepositoryPageSize))
		if query != "" {
			params.Set("q", query)
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		var page ownerRepositoriesPage
		if err := c.Do(ctx, http.MethodGet, base+"?"+params.Encode(), nil, &page); err != nil {
			return nil, err
		}

		// The cursor is (updatedAt, id). Pages don't overlap while
		// nothing changes, but de-duplicate in case a repository is
		// updated mid-listing.
		for _, r := range page.Repositories {
			if !seenRepos[r.ID] {
				seenRepos[r.ID] = true
				repos = append(repos, r)
			}
		}

		if !page.HasMore || page.NextCursor == nil || *page.NextCursor == "" {
			return repos, nil
		}
		if seenCursors[*page.NextCursor] {
			return nil, fmt.Errorf("ghost API GET %s: pagination cursor %q repeated", base, *page.NextCursor)
		}
		seenCursors[*page.NextCursor] = true
		cursor = *page.NextCursor
	}
}
