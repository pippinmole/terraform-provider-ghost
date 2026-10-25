package client

import (
	"context"
	"fmt"
	"net/http"
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
