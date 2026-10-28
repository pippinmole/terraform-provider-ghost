package client

import (
	"context"
	"net/http"
)

// LicenseSummary is a license as listed by the v3 API.
type LicenseSummary struct {
	// Key is the lower-case key a repository's license_template takes,
	// e.g. "mit".
	Key    string `json:"key"`
	Name   string `json:"name"`
	SPDXID string `json:"spdx_id"`
}

// License is one license with its full text, as returned by the v3 API.
type License struct {
	LicenseSummary

	HTMLURL        string   `json:"html_url"`
	Description    string   `json:"description"`
	Implementation string   `json:"implementation"`
	Permissions    []string `json:"permissions"`
	Conditions     []string `json:"conditions"`
	Limitations    []string `json:"limitations"`
	// Body is the license text. A repository created with this
	// license_template gets it as its LICENSE, with [year] and [fullname]
	// filled in.
	Body     string `json:"body"`
	Featured bool   `json:"featured"`
}

// ListLicenses returns every license a repository's license_template takes,
// in Ghost's order (by key). The list isn't paginated.
func (c *Client) ListLicenses(ctx context.Context) ([]LicenseSummary, error) {
	var licenses []LicenseSummary
	if err := c.Do(ctx, http.MethodGet, V3Path("licenses"), nil, &licenses); err != nil {
		return nil, err
	}

	return licenses, nil
}

// GetLicense returns one license. Ghost matches the key case-insensitively
// (MIT finds mit) and returns its own lower-case key.
func (c *Client) GetLicense(ctx context.Context, key string) (*License, error) {
	var l License
	if err := c.Do(ctx, http.MethodGet, V3Path("licenses", key), nil, &l); err != nil {
		return nil, err
	}

	return &l, nil
}
