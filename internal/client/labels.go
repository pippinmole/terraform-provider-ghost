package client

import (
	"context"
	"net/http"
)

// Label is an issue label as returned by the v3 API. Ghost stores colors as
// six lower-case hex digits without a leading "#".
type Label struct {
	ID          int64   `json:"id"`
	NodeID      string  `json:"node_id"`
	URL         string  `json:"url"`
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	Description *string `json:"description"`
	Default     bool    `json:"default"`
}

// CreateLabelRequest is the body for creating a label. Unset fields are
// omitted, so Ghost applies its defaults.
type CreateLabelRequest struct {
	Name        string  `json:"name"`
	Color       string  `json:"color,omitempty"`
	Description *string `json:"description,omitempty"`
}

// UpdateLabelRequest is the body for updating a label. It replaces the color
// and description: a nil Description is sent as null and clears it. NewName
// renames the label when set.
type UpdateLabelRequest struct {
	NewName     *string `json:"new_name,omitempty"`
	Color       string  `json:"color"`
	Description *string `json:"description"`
}

func labelPath(owner, repo string, name ...string) string {
	return V3Path(append([]string{"repos", owner, repo, "labels"}, name...)...)
}

// CreateLabel creates a label in the repository owner/repo.
func (c *Client) CreateLabel(ctx context.Context, owner, repo string, req CreateLabelRequest) (*Label, error) {
	var l Label
	if err := c.Do(ctx, http.MethodPost, labelPath(owner, repo), req, &l); err != nil {
		return nil, err
	}

	return &l, nil
}

// GetLabel returns the label with the given name. Ghost matches label names
// case-sensitively.
func (c *Client) GetLabel(ctx context.Context, owner, repo, name string) (*Label, error) {
	var l Label
	if err := c.Do(ctx, http.MethodGet, labelPath(owner, repo, name), nil, &l); err != nil {
		return nil, err
	}

	return &l, nil
}

// UpdateLabel updates the label with the given name.
func (c *Client) UpdateLabel(ctx context.Context, owner, repo, name string, req UpdateLabelRequest) (*Label, error) {
	var l Label
	if err := c.Do(ctx, http.MethodPatch, labelPath(owner, repo, name), req, &l); err != nil {
		return nil, err
	}

	return &l, nil
}

// DeleteLabel deletes the label with the given name.
func (c *Client) DeleteLabel(ctx context.Context, owner, repo, name string) error {
	return c.Do(ctx, http.MethodDelete, labelPath(owner, repo, name), nil, nil)
}
