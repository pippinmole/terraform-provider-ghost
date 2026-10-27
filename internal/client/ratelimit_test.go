package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock replaces the client's clock and sleep so rate-limit waits finish
// instantly and can be inspected.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.t
}

func (f *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	f.t = f.t.Add(d)

	return nil
}

func (f *fakeClock) slept() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]time.Duration(nil), f.sleeps...)
}

func newRateLimitedClient(t *testing.T, handler http.HandlerFunc) (*Client, *fakeClock) {
	t.Helper()

	c := newTestClient(t, handler)
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	c.now = clock.now
	c.sleep = clock.sleep

	return c, clock
}

func writeRateLimited(w http.ResponseWriter, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("X-RateLimit-Remaining", "0")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func TestDoRetriesAfterRetryAfterSeconds(t *testing.T) {
	var calls atomic.Int32
	c, clock := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeRateLimited(w, "7")
			return
		}
		_, _ = w.Write([]byte(`{"login":"pippinmole","id":10}`))
	})

	u, err := c.GetAuthenticatedUser(context.Background())
	if err != nil {
		t.Fatalf("GetAuthenticatedUser: %v", err)
	}
	if u.Login != "pippinmole" {
		t.Errorf("login = %q", u.Login)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
	if got, want := clock.slept(), []time.Duration{7 * time.Second}; !equalDurations(got, want) {
		t.Errorf("slept %v, want %v", got, want)
	}
}

func TestDoRetriesAfterRetryAfterDate(t *testing.T) {
	var calls atomic.Int32
	var c *Client
	var clock *fakeClock
	c, clock = newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeRateLimited(w, clock.now().Add(15*time.Second).UTC().Format(http.TimeFormat))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got, want := clock.slept(), []time.Duration{15 * time.Second}; !equalDurations(got, want) {
		t.Errorf("slept %v, want %v", got, want)
	}
}

func TestDoFallsBackToRateLimitReset(t *testing.T) {
	var calls atomic.Int32
	var c *Client
	var clock *fakeClock
	c, clock = newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(clock.now().Add(12*time.Second).Unix(), 10))
			writeRateLimited(w, "")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if got, want := clock.slept(), []time.Duration{12 * time.Second}; !equalDurations(got, want) {
		t.Errorf("slept %v, want %v", got, want)
	}
}

func TestDoBacksOffWithoutRateLimitHeaders(t *testing.T) {
	var calls atomic.Int32
	c, clock := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if got := clock.slept(); !equalDurations(got, want) {
		t.Errorf("slept %v, want %v", got, want)
	}
}

func TestDoResendsBodyOnRetry(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	c, _ := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			writeRateLimited(w, "1")
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"bug","color":"ededed"}`))
	})

	if _, err := c.CreateLabel(context.Background(), "o", "r", CreateLabelRequest{Name: "bug", Color: "ededed"}); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] {
		t.Errorf("bodies = %q, want the same non-empty body twice", bodies)
	}
}

func TestDoGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	c, clock := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeRateLimited(w, "2")
	})

	err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected a 429 APIError, got %v", err)
	}
	if got, want := int(calls.Load()), defaultMaxRetries+1; got != want {
		t.Errorf("requests = %d, want %d", got, want)
	}
	if got := len(clock.slept()); got != defaultMaxRetries {
		t.Errorf("waits = %d, want %d", got, defaultMaxRetries)
	}
}

func TestWithMaxRetriesZeroDisablesRetries(t *testing.T) {
	var calls atomic.Int32
	c, _ := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeRateLimited(w, "1")
	})
	WithMaxRetries(0)(c)

	if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestDoDoesNotWaitLongerThanMaxWait(t *testing.T) {
	var calls atomic.Int32
	c, clock := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeRateLimited(w, "3600")
	})

	err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected a 429 APIError, got %v", err)
	}
	if want := "ghost API GET /api/v3/user: 429 API rate limit exceeded (retry after 1h0m0s)"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	if got := clock.slept(); len(got) != 0 {
		t.Errorf("slept %v, want no waits", got)
	}
}

func TestDoStopsWaitingWhenContextIsCancelled(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeRateLimited(w, "30")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := c.Do(ctx, http.MethodGet, V3Path("user"), nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Do took %v, want it to return when the context ends", elapsed)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestDoPausesWhenRemainingIsZero(t *testing.T) {
	var calls atomic.Int32
	var c *Client
	var clock *fakeClock
	var sentAt []time.Time
	c, clock = newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		sentAt = append(sentAt, clock.now())
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(clock.now().Add(30*time.Second).Unix(), 10))
		w.WriteHeader(http.StatusNoContent)
	})

	for range 2 {
		if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (no 429s, so no retries)", got)
	}
	if gap := sentAt[1].Sub(sentAt[0]); gap != 30*time.Second {
		t.Errorf("second request sent %v after the first, want 30s", gap)
	}
}

func TestDoDoesNotPauseWithRemainingRequests(t *testing.T) {
	var c *Client
	var clock *fakeClock
	c, clock = newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "1")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(clock.now().Add(30*time.Second).Unix(), 10))
		w.WriteHeader(http.StatusNoContent)
	})

	for range 3 {
		if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err != nil {
			t.Fatalf("Do: %v", err)
		}
	}
	if got := clock.slept(); len(got) != 0 {
		t.Errorf("slept %v, want no waits", got)
	}
}

func TestDoIgnoresStaleOrFarOffReset(t *testing.T) {
	for _, offset := range []time.Duration{-10 * time.Second, time.Hour} {
		t.Run(offset.String(), func(t *testing.T) {
			var c *Client
			var clock *fakeClock
			c, clock = newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(clock.now().Add(offset).Unix(), 10))
				w.WriteHeader(http.StatusNoContent)
			})

			for range 2 {
				if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err != nil {
					t.Fatalf("Do: %v", err)
				}
			}
			if got := clock.slept(); len(got) != 0 {
				t.Errorf("slept %v, want no waits", got)
			}
		})
	}
}

func TestDoDoesNotRetryOtherErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var calls atomic.Int32
			c, clock := newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(status)
			})

			err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
				t.Fatalf("expected a %d APIError, got %v", status, err)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("requests = %d, want 1", got)
			}
			if got := clock.slept(); len(got) != 0 {
				t.Errorf("slept %v, want no waits", got)
			}
		})
	}
}

// TestDoConcurrentRequestsShareThePause runs many requests against a server
// with a small fixed-window budget, the way Terraform runs resources in
// parallel. Run with -race.
func TestDoConcurrentRequestsShareThePause(t *testing.T) {
	const budget, workers = 5, 20

	var c *Client
	var clock *fakeClock
	var mu sync.Mutex
	var windowEnd time.Time
	var used int
	var served, limited int
	c, clock = newRateLimitedClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		now := clock.now()
		if !now.Before(windowEnd) {
			windowEnd, used = now.Add(time.Minute), 0
		}
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(windowEnd.Unix(), 10))
		if used >= budget {
			limited++
			writeRateLimited(w, strconv.Itoa(int(windowEnd.Sub(now).Seconds())))
			return
		}
		used++
		served++
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(budget-used))
		w.WriteHeader(http.StatusNoContent)
	})
	WithMaxRetries(workers)(c)

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Do(context.Background(), http.MethodGet, V3Path("user"), nil, nil); err != nil {
				errs <- fmt.Errorf("worker %d: %w", i, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if served != workers {
		t.Errorf("served = %d, want %d", served, workers)
	}
	t.Logf("served %d, limited %d, waits %d", served, limited, len(clock.slept()))
}
