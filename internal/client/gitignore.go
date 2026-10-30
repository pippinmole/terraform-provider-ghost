package client

import (
	"context"
	"net/http"
)

// GitignoreTemplate is a .gitignore template as returned by the v3 API.
type GitignoreTemplate struct {
	// Name is Ghost's spelling of the template name, which may differ in
	// case from the name that was looked up.
	Name string `json:"name"`
	// Source is the template's content, exactly as a repository created
	// with it gets it as its .gitignore.
	Source string `json:"source"`
}

// ListGitignoreTemplates returns the name of every .gitignore template, the
// values a repository's gitignore_template takes, in Ghost's (byte) order.
func (c *Client) ListGitignoreTemplates(ctx context.Context) ([]string, error) {
	var names []string
	if err := c.Do(ctx, http.MethodGet, V3Path("gitignore", "templates"), nil, &names); err != nil {
		return nil, err
	}

	return names, nil
}

// GetGitignoreTemplate returns one .gitignore template. Ghost matches the
// name case-insensitively and returns its own spelling in Name.
func (c *Client) GetGitignoreTemplate(ctx context.Context, name string) (*GitignoreTemplate, error) {
	var t GitignoreTemplate
	if err := c.Do(ctx, http.MethodGet, V3Path("gitignore", "templates", name), nil, &t); err != nil {
		return nil, err
	}

	return &t, nil
}
