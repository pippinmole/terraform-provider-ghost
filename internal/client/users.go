package client

import (
	"context"
	"net/http"
)

// User is the authenticated user as returned by the v3 API.
type User struct {
	Login   string `json:"login"`
	ID      int64  `json:"id"`
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
}

// GetAuthenticatedUser returns the user that owns the configured token.
func (c *Client) GetAuthenticatedUser(ctx context.Context) (*User, error) {
	var u User
	if err := c.Do(ctx, http.MethodGet, V3Path("user"), nil, &u); err != nil {
		return nil, err
	}

	return &u, nil
}
