// Package httpx provides an HTTP client that tolerates slow or flaky networks
// (WSL DNS proxies, captive links) by retrying transient failures with
// per-attempt timeouts that grow on each timeout and adapt to observed latency.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	defaultAttempts = 4
	maxBackoff      = 8 * time.Second
)

// New returns a client whose first attempt uses base as its timeout. Timeouts
// double on each retry up to 4x base, and the learned timeout carries over to
// later requests made with the same client.
//
// QABAS_HTTP_TIMEOUT (Go duration, e.g. "90s") overrides base and
// QABAS_HTTP_RETRIES overrides the attempt count.
func New(base time.Duration) *http.Client {
	if v, err := time.ParseDuration(os.Getenv("QABAS_HTTP_TIMEOUT")); err == nil && v > 0 {
		base = v
	}
	attempts := defaultAttempts
	if v, err := strconv.Atoi(os.Getenv("QABAS_HTTP_RETRIES")); err == nil && v >= 0 {
		attempts = v + 1
	}
	return &http.Client{Transport: NewTransport(http.DefaultTransport.(*http.Transport).Clone(), base, 4*base, attempts)}
}

// Transport retries idempotent requests on transient network errors and
// 429/502/503/504 responses.
type Transport struct {
	Next     http.RoundTripper
	Base     time.Duration
	Max      time.Duration
	Attempts int
	Backoff  time.Duration

	mu      sync.Mutex
	current time.Duration
}

func NewTransport(next http.RoundTripper, base, max time.Duration, attempts int) *Transport {
	if max < base {
		max = base
	}
	if attempts < 1 {
		attempts = 1
	}
	return &Transport{Next: next, Base: base, Max: max, Attempts: attempts, Backoff: 500 * time.Millisecond, current: base}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempts := t.Attempts
	if !replayable(req) {
		attempts = 1
	}
	timeout := t.startTimeout()
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := sleep(req.Context(), t.backoff(i, lastErr)); err != nil {
				return nil, err
			}
			timeout = t.clamp(timeout * 2)
		}
		ctx, cancel := context.WithTimeout(req.Context(), timeout)
		attempt := req.Clone(ctx)
		if req.Body != nil && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				cancel()
				return nil, err
			}
			attempt.Body = body
		}
		start := time.Now()
		resp, err := t.Next.RoundTrip(attempt)
		last := i == attempts-1
		if err == nil {
			if !retryableStatus(resp.StatusCode) || last {
				t.observe(time.Since(start))
				resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
				return resp, nil
			}
			lastErr = &statusError{code: resp.StatusCode, retryAfter: retryAfter(resp)}
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			cancel()
			continue
		}
		cancel()
		if req.Context().Err() != nil {
			return nil, err
		}
		if isTimeout(err) {
			t.grow(timeout * 2)
		}
		if !retryableErr(err) {
			return nil, err
		}
		lastErr = err
	}
	if attempts > 1 {
		return nil, fmt.Errorf("%w (gave up after %d attempts; set QABAS_HTTP_TIMEOUT to allow longer)", lastErr, attempts)
	}
	return nil, lastErr
}

func (t *Transport) startTimeout() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current <= 0 {
		t.current = t.Base
	}
	return t.current
}

func (t *Transport) clamp(d time.Duration) time.Duration {
	if d > t.Max {
		return t.Max
	}
	if d < t.Base {
		return t.Base
	}
	return d
}

func (t *Transport) grow(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d = t.clamp(d); d > t.current {
		t.current = d
	}
}

// observe moves the starting timeout toward 4x the latest successful latency,
// decaying gradually so one fast response doesn't erase a slow-network history.
func (t *Transport) observe(latency time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	target := t.clamp(4 * latency)
	if target >= t.current {
		t.current = target
		return
	}
	t.current = t.clamp((t.current + target) / 2)
}

func (t *Transport) backoff(attempt int, lastErr error) time.Duration {
	var se *statusError
	if errors.As(lastErr, &se) && se.retryAfter > 0 {
		return min(se.retryAfter, t.Max)
	}
	d := t.Backoff << (attempt - 1)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func replayable(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, "":
	default:
		return false
	}
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func retryableErr(err error) bool {
	if isTimeout(err) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTemporary || dnsErr.IsTimeout
	}
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.EOF) ||
		platformRetryable(err)
}

func retryAfter(resp *http.Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		return time.Until(at)
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type statusError struct {
	code       int
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	return fmt.Sprintf("HTTP %d %s", e.code, http.StatusText(e.code))
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
