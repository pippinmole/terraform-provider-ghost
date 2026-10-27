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
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

// DefaultBaseURL is the public Ghost API endpoint.
const DefaultBaseURL = "https://api.ghost.procrastinator.fyi"

const (
	v3Prefix     = "/api/v3"
	nativePrefix = "/api"
)

// Client talks to the Ghost API. It is safe for concurrent use, and waits out
// Ghost's rate limit (see ratelimit.go) across all of its requests.
type Client struct {
	baseURL    *url.URL
	token      string
	userAgent  string
	httpClient *http.Client
	maxRetries int

	// now and sleep are replaced in tests.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	resumeAt time.Time // no request is sent before this
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
		maxRetries: defaultMaxRetries,
		now:        time.Now,
		sleep:      sleepContext,
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
// body when non-nil and decoding the response into out when non-nil. A
// rate-limited request is retried once the limit resets, up to maxRetries
// times.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var reqBody []byte
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reqBody = buf
	}

	ref, err := url.Parse(path)
	if err != nil {
		return fmt.Errorf("parsing request path: %w", err)
	}
	u := c.baseURL.JoinPath(ref.EscapedPath())
	u.RawQuery = ref.RawQuery

	for attempt := 0; ; attempt++ {
		if err := c.waitForRateLimit(ctx); err != nil {
			return fmt.Errorf("ghost API %s %s: waiting for rate limit: %w", method, ref.Path, err)
		}

		resp, respBody, err := c.send(ctx, method, u.String(), reqBody)
		if err != nil {
			return fmt.Errorf("ghost API %s %s: %w", method, ref.Path, err)
		}
		c.observeRateLimit(resp.Header)

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := c.retryDelay(resp.Header, attempt)
			if attempt < c.maxRetries && wait <= maxRateLimitWait {
				tflog.Warn(ctx, "Ghost API rate limit reached, waiting before retrying", map[string]any{
					"method": method,
					"path":   ref.Path,
					"wait":   wait.String(),
					"retry":  attempt + 1,
				})
				c.pauseUntil(c.now().Add(wait))

				continue
			}

			return &APIError{
				StatusCode: resp.StatusCode,
				Method:     method,
				Path:       ref.Path,
				Message:    fmt.Sprintf("%s (retry after %s)", errorMessage(respBody, resp.Status), wait),
			}
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
}

// send makes one HTTP request and reads the whole response body.
func (c *Client) send(ctx context.Context, method, u string, body []byte) (*http.Response, []byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("building request: %w", err)
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
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response body: %w", err)
	}

	return resp, respBody, nil
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
