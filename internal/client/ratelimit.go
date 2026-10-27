package client

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Ghost limits each token to a fixed window of requests (120 per minute),
// shared by both API surfaces. Every response carries GitHub-style
// X-RateLimit-* headers, and a request over the limit gets a 429 with
// Retry-After. A 429 means the request was not processed, so retrying it is
// safe whatever the method.
const (
	// defaultMaxRetries is how many times a rate-limited request is retried.
	defaultMaxRetries = 5
	// maxRateLimitWait is the longest the client waits before one retry.
	// Longer waits fail straight away rather than stall Terraform.
	maxRateLimitWait = 5 * time.Minute
)

// WithMaxRetries sets how many times a rate-limited (429) request is retried
// before its error is returned. Zero disables retries.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = max(n, 0) }
}

// retryDelay returns how long to wait before retrying a 429 response: the
// Retry-After header (seconds or an HTTP date), then X-RateLimit-Reset, then
// exponential backoff.
func (c *Client) retryDelay(h http.Header, attempt int) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(t.Sub(c.now()), 0)
		}
	}
	if reset, ok := rateLimitReset(h); ok {
		return max(reset.Sub(c.now()), 0)
	}

	return min(time.Second<<attempt, maxRateLimitWait)
}

// rateLimitReset parses X-RateLimit-Reset, the start of the next window in
// Unix seconds.
func rateLimitReset(h http.Header) (time.Time, bool) {
	secs, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	if err != nil {
		return time.Time{}, false
	}

	return time.Unix(secs, 0), true
}

// observeRateLimit holds back further requests until the window resets once a
// response reports none remaining, rather than spend them on certain 429s.
// Resets in the past, or further off than maxRateLimitWait (clock skew), are
// ignored; the server's 429 then decides.
func (c *Client) observeRateLimit(h http.Header) {
	if h.Get("X-RateLimit-Remaining") != "0" {
		return
	}
	reset, ok := rateLimitReset(h)
	if !ok || reset.Sub(c.now()) > maxRateLimitWait {
		return
	}
	c.pauseUntil(reset)
}

// pauseUntil stops every request on this client from being sent before t.
func (c *Client) pauseUntil(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.resumeAt) {
		c.resumeAt = t
	}
}

// waitForRateLimit blocks until any pause has passed or ctx ends. The pause
// can be extended while waiting, so it checks again after each sleep.
func (c *Client) waitForRateLimit(ctx context.Context) error {
	for {
		c.mu.Lock()
		d := c.resumeAt.Sub(c.now())
		c.mu.Unlock()
		if d <= 0 {
			return nil
		}
		if err := c.sleep(ctx, d); err != nil {
			return err
		}
	}
}

// sleepContext waits for d or until ctx ends, returning ctx's error then.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
