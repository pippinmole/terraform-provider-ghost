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
	// Fields lists the v3 validation errors of a 422, e.g.
	// {Field: "tag_name", Code: "already_exists"}.
	Fields []FieldError
}

// FieldError is one entry of a v3 validation error's errors list.
type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ghost API %s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// IsNotFound reports whether err is an APIError with a 404 status.
func IsNotFound(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsFieldError reports whether err is a v3 validation error (422) naming
// field with code, e.g. ("tag_name", "already_exists").
func IsFieldError(err error, field, code string) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	for _, f := range apiErr.Fields {
		if f.Field == field && f.Code == code {
			return true
		}
	}

	return false
}

// IsForbidden reports whether err is an APIError with a 403 status.
func IsForbidden(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden
}

// HasStatus reports whether err is an APIError with the given status.
func HasStatus(err error, status int) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

// IsConflict reports whether err is an APIError with a 409 status.
func IsConflict(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict
}

// Do sends a request to path (which must include its API prefix), JSON-encoding
// body when non-nil and decoding the response into out when non-nil. A
// rate-limited request is retried once the limit resets, up to maxRetries
// times.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	r := request{method: method, path: path, accept: "application/json"}
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		r.body = bytes.NewReader(buf)
		r.size = int64(len(buf))
		r.contentType = "application/json"
	}

	return c.doJSON(ctx, r, out)
}

// Upload sends size bytes read from body to path with the given Content-Type,
// and decodes the JSON response into out when non-nil. body is read from the
// start on each attempt, so a rate-limited upload can be retried. Uploads
// aren't bound by the client's overall request timeout, only by ctx.
func (c *Client) Upload(ctx context.Context, method, path, contentType string, body io.ReaderAt, size int64, out any) error {
	return c.doJSON(ctx, request{
		method: method, path: path, accept: "application/json",
		contentType: contentType, body: body, size: size, streaming: true,
	}, out)
}

// Download sends a GET to path asking for application/octet-stream and
// copies the response body to w. Downloads aren't bound by the client's
// overall request timeout, only by ctx.
func (c *Client) Download(ctx context.Context, path string, w io.Writer) error {
	_, err := c.do(ctx, request{
		method: http.MethodGet, path: path, accept: "application/octet-stream",
		sink: w, streaming: true,
	})

	return err
}

// request is one API call.
type request struct {
	method, path        string
	contentType, accept string
	// body, when non-nil, is sent as the request body: its first size
	// bytes, with a Content-Length.
	body io.ReaderAt
	size int64
	// sink, when non-nil, receives a 2xx response's body instead of it
	// being returned.
	sink io.Writer
	// streaming lifts the HTTP client's overall timeout.
	streaming bool
}

func (c *Client) doJSON(ctx context.Context, r request, out any) error {
	respBody, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decoding response from %s %s: %w", r.method, requestPath(r.path), err)
	}

	return nil
}

func requestPath(path string) string {
	if ref, err := url.Parse(path); err == nil {
		return ref.Path
	}

	return path
}

// do sends r, retrying it while rate-limited, and returns the body of a 2xx
// response (nil when r has a sink).
func (c *Client) do(ctx context.Context, r request) ([]byte, error) {
	ref, err := url.Parse(r.path)
	if err != nil {
		return nil, fmt.Errorf("parsing request path: %w", err)
	}
	u := c.baseURL.JoinPath(ref.EscapedPath())
	u.RawQuery = ref.RawQuery

	for attempt := 0; ; attempt++ {
		if err := c.waitForRateLimit(ctx); err != nil {
			return nil, fmt.Errorf("ghost API %s %s: waiting for rate limit: %w", r.method, ref.Path, err)
		}

		resp, respBody, err := c.send(ctx, u.String(), r)
		if err != nil {
			return nil, fmt.Errorf("ghost API %s %s: %w", r.method, ref.Path, err)
		}
		c.observeRateLimit(resp.Header)

		if resp.StatusCode == http.StatusTooManyRequests {
			wait := c.retryDelay(resp.Header, attempt)
			if attempt < c.maxRetries && wait <= maxRateLimitWait {
				tflog.Warn(ctx, "Ghost API rate limit reached, waiting before retrying", map[string]any{
					"method": r.method,
					"path":   ref.Path,
					"wait":   wait.String(),
					"retry":  attempt + 1,
				})
				c.pauseUntil(c.now().Add(wait))

				continue
			}

			return nil, &APIError{
				StatusCode: resp.StatusCode,
				Method:     r.method,
				Path:       ref.Path,
				Message:    fmt.Sprintf("%s (retry after %s)", errorMessage(respBody, resp.Status), wait),
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, &APIError{
				StatusCode: resp.StatusCode,
				Method:     r.method,
				Path:       ref.Path,
				Message:    errorMessage(respBody, resp.Status),
				Fields:     fieldErrors(respBody),
			}
		}

		return respBody, nil
	}
}

// send makes one HTTP request. It reads the whole response body, except for
// a 2xx response to a request with a sink, which is copied to the sink.
func (c *Client) send(ctx context.Context, u string, r request) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, r.method, u, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("building request: %w", err)
	}
	if r.body != nil {
		body, size := r.body, r.size
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(io.NewSectionReader(body, 0, size)), nil
		}
		req.Body, _ = req.GetBody()
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
	}
	req.Header.Set("Accept", r.accept)
	req.Header.Set("User-Agent", c.userAgent)
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	hc := c.httpClient
	if r.streaming {
		unbounded := *c.httpClient
		unbounded.Timeout = 0
		hc = &unbounded
	}

	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if r.sink != nil && resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		if _, err := io.Copy(r.sink, resp.Body); err != nil {
			return nil, nil, fmt.Errorf("reading response body: %w", err)
		}

		return resp, nil, nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response body: %w", err)
	}

	return resp, respBody, nil
}

// fieldErrors decodes the errors list of a v3 validation error.
func fieldErrors(body []byte) []FieldError {
	var payload struct {
		Errors []FieldError `json:"errors"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}

	return payload.Errors
}

// errorMessage extracts the "message" field both API surfaces use for errors,
// falling back to the HTTP status text. Validation errors carry a list of
// messages, which are joined with "; ".
func errorMessage(body []byte, status string) string {
	var payload struct {
		Message json.RawMessage `json:"message"`
		Errors  []FieldError    `json:"errors"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return status
	}

	var msg string
	if json.Unmarshal(payload.Message, &msg) == nil && msg != "" {
		// v3 validation errors say only "Validation Failed" and list the
		// details in errors, e.g. {"field":"key","code":"already_exists"}.
		var details []string
		for _, e := range payload.Errors {
			switch {
			case e.Message != "":
				details = append(details, e.Message)
			case e.Field != "" || e.Code != "":
				details = append(details, strings.TrimSpace(e.Field+" "+e.Code))
			}
		}
		if len(details) > 0 {
			return msg + ": " + strings.Join(details, "; ")
		}

		return msg
	}
	var msgs []string
	if json.Unmarshal(payload.Message, &msgs) == nil && len(msgs) > 0 {
		return strings.Join(msgs, "; ")
	}

	return status
}
