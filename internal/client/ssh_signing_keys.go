package client

import (
	"context"
	"net/http"
	"strconv"
)

// SSHSigningKey is an SSH key the authenticated user signs commits with, as
// returned by the v3 API. Ghost stores Key as "<type> <base64>", without the
// comment.
type SSHSigningKey struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

// CreateSSHSigningKeyRequest is the body for adding an SSH signing key. Ghost
// trims the title, and falls back to the key's comment (or its type) when the
// title is empty.
type CreateSSHSigningKeyRequest struct {
	Title string `json:"title"`
	Key   string `json:"key"`
}

func sshSigningKeyPath(id ...int64) string {
	segments := []string{"user", "ssh_signing_keys"}
	for _, i := range id {
		segments = append(segments, strconv.FormatInt(i, 10))
	}

	return V3Path(segments...)
}

// CreateSSHSigningKey adds an SSH signing key to the authenticated user.
func (c *Client) CreateSSHSigningKey(ctx context.Context, req CreateSSHSigningKeyRequest) (*SSHSigningKey, error) {
	var k SSHSigningKey
	if err := c.Do(ctx, http.MethodPost, sshSigningKeyPath(), req, &k); err != nil {
		return nil, err
	}

	return &k, nil
}

// GetSSHSigningKey returns one of the authenticated user's SSH signing keys.
func (c *Client) GetSSHSigningKey(ctx context.Context, id int64) (*SSHSigningKey, error) {
	var k SSHSigningKey
	if err := c.Do(ctx, http.MethodGet, sshSigningKeyPath(id), nil, &k); err != nil {
		return nil, err
	}

	return &k, nil
}

// ListSSHSigningKeys returns the authenticated user's SSH signing keys.
func (c *Client) ListSSHSigningKeys(ctx context.Context) ([]SSHSigningKey, error) {
	var keys []SSHSigningKey
	if err := c.Do(ctx, http.MethodGet, sshSigningKeyPath(), nil, &keys); err != nil {
		return nil, err
	}

	return keys, nil
}

// DeleteSSHSigningKey removes one of the authenticated user's SSH signing
// keys.
func (c *Client) DeleteSSHSigningKey(ctx context.Context, id int64) error {
	return c.Do(ctx, http.MethodDelete, sshSigningKeyPath(id), nil, nil)
}
