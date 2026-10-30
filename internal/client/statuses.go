package client

import (
	"context"
	"net/http"
)

// statusContexts is the native status-contexts response.
type statusContexts struct {
	Contexts []string `json:"contexts"`
}

// CreateCommitStatusRequest is the v3 body for reporting a commit status.
// State is pending, success, failure or error. An empty Context is stored as
// "default".
type CreateCommitStatusRequest struct {
	State       string `json:"state"`
	Context     string `json:"context,omitempty"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

// CreatedCommitStatus is a commit status as returned when it is reported.
type CreatedCommitStatus struct {
	ID      int64  `json:"id"`
	State   string `json:"state"`
	Context string `json:"context"`
}

// ListStatusContexts returns the distinct contexts the repository
// owner/repo's commit statuses have named, most recently posted first. Ghost
// returns at most 50 and has no pagination. Contexts are case-sensitive. It
// needs write access: a read-only caller gets a 403.
func (c *Client) ListStatusContexts(ctx context.Context, owner, repo string) ([]string, error) {
	var s statusContexts
	if err := c.Do(ctx, http.MethodGet, NativePath("repositories", owner, repo, "status-contexts"), nil, &s); err != nil {
		return nil, err
	}
	if s.Contexts == nil {
		s.Contexts = []string{}
	}

	return s.Contexts, nil
}

// CreateCommitStatus reports a status on the commit sha, which must be a
// full 40-character SHA of a commit in owner/repo (422 otherwise).
func (c *Client) CreateCommitStatus(ctx context.Context, owner, repo, sha string, req CreateCommitStatusRequest) (*CreatedCommitStatus, error) {
	var s CreatedCommitStatus
	if err := c.Do(ctx, http.MethodPost, V3Path("repos", owner, repo, "statuses", sha), req, &s); err != nil {
		return nil, err
	}

	return &s, nil
}
