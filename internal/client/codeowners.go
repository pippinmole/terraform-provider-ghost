package client

import (
	"context"
	"net/http"
	"net/url"
)

// CodeownersError is one problem Ghost found in a CODEOWNERS file.
type CodeownersError struct {
	// Line and Column are 1-based. Column counts characters, so a tab is 1.
	Line   int64 `json:"line"`
	Column int64 `json:"column"`
	// Kind is e.g. "Unknown owner", "Owner without write access",
	// "Unknown team", "Team without write access", "Invalid owner" or
	// "Invalid pattern".
	Kind string `json:"kind"`
	// Source is the whole line as written.
	Source     string  `json:"source"`
	Suggestion *string `json:"suggestion"`
	Message    string  `json:"message"`
	// Path is the file the error is in.
	Path string `json:"path"`
}

// Codeowners is the CODEOWNERS file in effect at a ref and the errors in it.
type Codeowners struct {
	// Path is the file in effect (.github/CODEOWNERS, CODEOWNERS or
	// docs/CODEOWNERS, in that order of precedence), or nil when the ref has
	// none or it is over 3 MB.
	Path   *string           `json:"path"`
	Errors []CodeownersError `json:"errors"`
}

// GetCodeowners checks the CODEOWNERS file of owner/repo at ref (a branch,
// tag or commit SHA; "" for the default branch) through the native API. Unlike
// v3 /codeowners/errors, which is a bare 404 when there is no file, it returns
// a nil Path then, and a 404 "Branch not found: <ref>" for an unknown ref.
func (c *Client) GetCodeowners(ctx context.Context, owner, repo, ref string) (*Codeowners, error) {
	p := NativePath("repositories", owner, repo, "codeowners")
	if ref != "" {
		p += "?" + url.Values{"ref": {ref}}.Encode()
	}

	var co Codeowners
	if err := c.Do(ctx, http.MethodGet, p, nil, &co); err != nil {
		return nil, err
	}
	if co.Errors == nil {
		co.Errors = []CodeownersError{}
	}

	return &co, nil
}
