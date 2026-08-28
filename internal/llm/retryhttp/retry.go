// Package retryhttp is the single retry implementation shared by the LLM provider clients.
//
// It exists because the three clients had drifted: OpenAI retried 5 times with backoff, Ollama had
// its own loop, and Anthropic had none at all — so the same transient 429 lost a gap on one provider
// and was invisible on another.
//
// Retriability here is decided by HTTP status code and by typed transport errors, never by
// substring-matching an error string. The previous predicate matched "429"/"502"/"503"/"504"
// anywhere in err.Error(), which false-positives on any message that happens to quote a line number
// or a token count — retrying a request that will never succeed, at full cost.
package retryhttp

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxAttempts matches the OpenAI client's historical behaviour (1 try + 4 retries).
const DefaultMaxAttempts = 5

// maxBackoff caps a single wait; with jitter the worst case is maxBackoff+jitterWindow.
const (
	baseBackoff  = 200 * time.Millisecond
	maxBackoff   = 8 * time.Second
	jitterWindow = 500 * time.Millisecond
	// maxRetryAfter bounds how long a server-supplied Retry-After can park a request. Providers
	// occasionally return minutes; blocking a limiter slot that long stalls the whole fan-out.
	maxRetryAfter = 30 * time.Second
)

// Options tunes Do. The zero value is valid and uses DefaultMaxAttempts.
type Options struct {
	MaxAttempts int
}

func (o Options) attempts() int {
	if o.MaxAttempts > 0 {
		return o.MaxAttempts
	}
	return DefaultMaxAttempts
}

// Do issues the request built by build, retrying transient transport failures and retriable HTTP
// statuses with exponential backoff plus jitter, honouring Retry-After when the server sends it.
//
// build is called once per attempt because a request body is not reusable — callers that send a body
// must return a fresh io.Reader over the same bytes each time.
//
// On success the response is returned with its body unread and unclosed; the caller owns it. When
// every attempt fails, the last error (or a synthesized status error) is returned. A non-retriable
// status is returned immediately with its response intact so the caller can read the error body.
func Do(ctx context.Context, c *http.Client, build func() (*http.Request, error), opts Options) (*http.Response, error) {
	if c == nil {
		c = http.DefaultClient
	}
	attempts := opts.attempts()
	var lastErr error
	// wait carries the delay owed before the next attempt, so a Retry-After hint replaces the
	// computed backoff instead of adding to it.
	var wait time.Duration
	for attempt := 0; attempt < attempts; attempt++ {
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			if !IsRetriableTransportError(err) {
				return nil, err
			}
			lastErr = err
			wait = backoffFor(attempt+1, 0)
			continue
		}
		if !IsRetriableStatus(resp.StatusCode) {
			return resp, nil
		}
		// Retriable status: drain a bounded prefix and close so the connection can be reused.
		wait = backoffFor(attempt+1, retryAfter(resp))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		lastErr = &StatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	return nil, lastErr
}

// StatusError reports a retriable HTTP status that survived every attempt.
type StatusError struct {
	StatusCode int
	Status     string
}

func (e *StatusError) Error() string {
	if e.Status != "" {
		return "http " + e.Status
	}
	return "http status " + strconv.Itoa(e.StatusCode)
}

// IsRetriableStatus reports whether an HTTP status is worth retrying: rate limiting and the
// gateway/overload family. Everything else — including 400, 401, 403 and 404 — is a client or
// configuration error that will fail identically on the next attempt.
func IsRetriableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// IsRetriableTransportError reports whether a transport-level error is transient. Detection is by
// error identity (net.Error timeouts, io.EOF / io.ErrUnexpectedEOF, syscall-level resets surfaced as
// net.OpError) rather than by message text.
func IsRetriableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// A cancelled or expired caller context is not transient — retrying cannot help and would
		// only delay the error the caller is already waiting for.
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// Connection resets, broken pipes and abrupt server closes arrive as *net.OpError. The wrapped
	// syscall errno differs per platform, so treat any non-timeout OpError on a read/write as
	// transient — a genuinely fatal dial error (no such host) carries a *net.DNSError instead.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTemporary || dnsErr.IsTimeout
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

// retryAfter parses the Retry-After header. Anthropic returns it on 429 and respecting it is
// strictly better than blind exponential backoff. Both the delta-seconds and HTTP-date forms are
// accepted. Returns 0 when absent, unparseable, in the past, or beyond maxRetryAfter.
func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return clampRetryAfter(time.Duration(secs) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return clampRetryAfter(time.Until(t))
	}
	return 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}

// backoffFor returns the wait before the given attempt (1-based). A server-supplied Retry-After
// wins over the computed backoff when it is longer — the server knows its own rate limit window.
func backoffFor(attempt int, serverHint time.Duration) time.Duration {
	if attempt <= 0 {
		return serverHint
	}
	d := time.Duration(1<<uint(attempt-1)) * baseBackoff
	if d > maxBackoff {
		d = maxBackoff
	}
	d += time.Duration(rand.Int64N(int64(jitterWindow)))
	if serverHint > d {
		return serverHint
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
