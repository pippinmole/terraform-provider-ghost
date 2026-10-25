// Package client is a hand-written, typed HTTP client for the Ghost API.
//
// Ghost exposes two API surfaces:
//   - a GitHub-compatible API under /api/v3, which is preferred where it covers
//     the functionality we need, and
//   - the native API under /api, used as a fallback for everything else.
//
// Request paths passed to the client include their prefix (see V3Path and
// NativePath) so callers are explicit about which surface they target.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the public Ghost API endpoint.
const DefaultBaseURL = "https://api.ghost.procrastinator.fyi"

const (
	v3Prefix     = "/api/v3"
	nativePrefix = "/api"
)

// Client talks to the Ghost API.
type Client struct {
	baseURL    *url.URL
	token      string
	userAgent  string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the underlying HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithUserAgent sets the User-Agent header sent with every request.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// New returns a Client for the given base URL (e.g. DefaultBaseURL) and
// personal access token.
func New(baseURL, token string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("base URL %q must be absolute, e.g. %s", baseURL, DefaultBaseURL)
	}

	c := &Client{
		baseURL:    u,
		token:      token,
		userAgent:  "terraform-provider-ghost",
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// V3Path builds a path on the GitHub-compatible API, escaping each segment.
func V3Path(segments ...string) string {
	return buildPath(v3Prefix, segments)
}

// NativePath builds a path on the native Ghost API, escaping each segment.
func NativePath(segments ...string) string {
	return buildPath(nativePrefix, segments)
}

func buildPath(prefix string, segments []string) string {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}

	return prefix + "/" + strings.Join(escaped, "/")
}

// APIError is returned for any non-2xx response.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ghost API %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// IsNotFound reports whether err is an APIError with a 404 status.
func IsNotFound(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// Do sends a request to path (which must include its API prefix), JSON-encoding
// body when non-nil and decoding the response into out when non-nil.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("parsing request path: %w", err)
	}
	u := c.baseURL.JoinPath(ref.EscapedPath())
	u.RawQuery = ref.RawQuery

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ghost API %s %s: %w", method, ref.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       ref.Path,
			Message:    errorMessage(respBody, resp.Status),
		}
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding response from %s %s: %w", method, ref.Path, err)
	}

	return nil
}

// errorMessage extracts the "message" field both API surfaces use for errors,
// falling back to the HTTP status text.
func errorMessage(body []byte, status string) string {
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Message != "" {
		return payload.Message
	}

	return status
}
