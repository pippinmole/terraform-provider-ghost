package client

import (
	"context"
	"net/http"
	"slices"
)

// Branches lists a repository's branches as returned by the native API.
type Branches struct {
	// DefaultBranch is nil until something has been pushed.
	DefaultBranch *string `json:"defaultBranch"`
	// Names are without the refs/heads/ prefix, ordered by name.
	Names []string `json:"branches"`
}

// Has reports whether the repository has a branch with the given name.
// Branch names are case-sensitive.
func (b *Branches) Has(name string) bool {
	return slices.Contains(b.Names, name)
}

// Branch is a branch as returned when it is created.
type Branch struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// CreateBranchRequest is the body for creating a branch. From is a branch,
// tag or commit SHA; when empty, Ghost starts the branch at the default branch.
type CreateBranchRequest struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"`
}

// CommitStatus is the combined status of a ref's commit. Only the SHA is
// decoded: it is the cheapest way to resolve a branch to its commit.
type CommitStatus struct {
	SHA string `json:"sha"`
}

func branchPath(owner, repo string, name ...string) string {
	return NativePath(append([]string{"repositories", owner, repo, "branches"}, name...)...)
}

// ListBranches returns the branches of the repository owner/repo.
func (c *Client) ListBranches(ctx context.Context, owner, repo string) (*Branches, error) {
	var b Branches
	if err := c.Do(ctx, http.MethodGet, branchPath(owner, repo), nil, &b); err != nil {
		return nil, err
	}

	return &b, nil
}

// CreateBranch creates a branch in the repository owner/repo. It returns a
// 404 when the repository or the revision to branch from doesn't exist, and a
// 409 when the branch already exists.
func (c *Client) CreateBranch(ctx context.Context, owner, repo string, req CreateBranchRequest) (*Branch, error) {
	var b Branch
	if err := c.Do(ctx, http.MethodPost, branchPath(owner, repo), req, &b); err != nil {
		return nil, err
	}

	return &b, nil
}

// DeleteBranch deletes a branch. It uses the native API, which documents
// percent-encoded branch names and returns a 409 for the default branch or a
// branch an open pull request compares.
func (c *Client) DeleteBranch(ctx context.Context, owner, repo, name string) error {
	return c.Do(ctx, http.MethodDelete, branchPath(owner, repo, name), nil, nil)
}

// GetCommitStatus returns the combined status of the commit ref points at.
func (c *Client) GetCommitStatus(ctx context.Context, owner, repo, ref string) (*CommitStatus, error) {
	var s CommitStatus
	if err := c.Do(ctx, http.MethodGet, V3Path("repos", owner, repo, "commits", ref, "status"), nil, &s); err != nil {
		return nil, err
	}

	return &s, nil
}
