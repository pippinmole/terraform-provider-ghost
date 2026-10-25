package client

import (
	"context"
	"net/http"
)

// User is a user or organization as returned by the v3 API.
type User struct {
	Login     string `json:"login"`
	ID        int64  `json:"id"`
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	SiteAdmin bool   `json:"site_admin"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// GetAuthenticatedUser returns the user that owns the configured token.
func (c *Client) GetAuthenticatedUser(ctx context.Context) (*User, error) {
	var u User
	if err := c.Do(ctx, http.MethodGet, V3Path("user"), nil, &u); err != nil {
		return nil, err
	}

	return &u, nil
}

// GetUser returns the user or organization with the given login. Ghost
// matches logins case-sensitively.
func (c *Client) GetUser(ctx context.Context, login string) (*User, error) {
	var u User
	if err := c.Do(ctx, http.MethodGet, V3Path("users", login), nil, &u); err != nil {
		return nil, err
	}

	return &u, nil
}
