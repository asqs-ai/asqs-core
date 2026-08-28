package retryhttp

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func buildFor(url string) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	}
}

func TestDo_retriesRetriableStatusThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), srv.Client(), buildFor(srv.URL), Options{MaxAttempts: 4})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("server calls = %d, want 3", got)
	}
}

func TestDo_doesNotRetryNonRetriableStatus(t *testing.T) {
	// A 400 or 401 fails identically on every attempt. Retrying it wastes the caller's time and,
	// on a metered provider, its money.
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
		}))

		resp, err := Do(context.Background(), srv.Client(), buildFor(srv.URL), Options{MaxAttempts: 5})
		if err != nil {
			t.Fatalf("code %d: Do returned error: %v", code, err)
		}
		// A non-retriable status comes back with the response intact so the caller can read the
		// provider's error body.
		if resp.StatusCode != code {
			t.Errorf("code %d: got status %d", code, resp.StatusCode)
		}
		resp.Body.Close()
		if got := calls.Load(); got != 1 {
			t.Errorf("code %d: server calls = %d, want 1", code, got)
		}
		srv.Close()
	}
}

func TestDo_exhaustsAttemptsAndReturnsStatusError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), srv.Client(), buildFor(srv.URL), Options{MaxAttempts: 3})
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected an error after exhausting attempts")
	}
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want *StatusError", err)
	}
	if se.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d", se.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("server calls = %d, want 3", got)
	}
}

func TestDo_rebuildsRequestBodyEachAttempt(t *testing.T) {
	// A request body is consumed by the first attempt; without rebuilding, retry #2 sends an empty
	// body and the provider rejects it as malformed JSON.
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := Do(context.Background(), srv.Client(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"model":"x"}`))
	}, Options{MaxAttempts: 3})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2", len(bodies))
	}
	for i, b := range bodies {
		if b != `{"model":"x"}` {
			t.Errorf("attempt %d body = %q, want the full payload", i+1, b)
		}
	}
}

func TestDo_contextCancellationStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := Do(ctx, srv.Client(), buildFor(srv.URL), Options{MaxAttempts: 10})
	if err == nil {
		t.Fatal("expected an error when the context expires")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want a context error", err)
	}
}

func TestIsRetriableStatus(t *testing.T) {
	retriable := []int{429, 500, 502, 503, 504}
	for _, c := range retriable {
		if !IsRetriableStatus(c) {
			t.Errorf("IsRetriableStatus(%d) = false, want true", c)
		}
	}
	for _, c := range []int{200, 201, 400, 401, 403, 404, 409, 422} {
		if IsRetriableStatus(c) {
			t.Errorf("IsRetriableStatus(%d) = true, want false", c)
		}
	}
}

// TestIsRetriableTransportError_ignoresStatusLikeSubstrings is the regression test for the finding
// this package replaces: the old predicate matched "429"/"502"/"503"/"504" anywhere in err.Error(),
// so an error quoting a line number or a token count was retried as if it were a rate limit.
func TestIsRetriableTransportError_ignoresStatusLikeSubstrings(t *testing.T) {
	misleading := []error{
		errors.New("invalid request: prompt is 502 tokens over the limit"),
		errors.New("parse error at line 429"),
		errors.New(`unmarshal failed: unexpected character at offset 503`),
		errors.New("model gpt-504-preview is not available to this account"),
	}
	for _, err := range misleading {
		if IsRetriableTransportError(err) {
			t.Errorf("IsRetriableTransportError(%q) = true; status-like digits in a message must not trigger a retry", err)
		}
	}
}

func TestIsRetriableTransportError_typedErrors(t *testing.T) {
	if !IsRetriableTransportError(io.ErrUnexpectedEOF) {
		t.Error("io.ErrUnexpectedEOF should be retriable")
	}
	if !IsRetriableTransportError(io.EOF) {
		t.Error("io.EOF should be retriable")
	}
	if !IsRetriableTransportError(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}) {
		t.Error("net.OpError should be retriable")
	}
	// A cancelled caller context is not transient: retrying cannot help and only delays the error
	// the caller is already waiting for.
	if IsRetriableTransportError(context.Canceled) {
		t.Error("context.Canceled must not be retriable")
	}
	if IsRetriableTransportError(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded must not be retriable")
	}
	if IsRetriableTransportError(nil) {
		t.Error("nil must not be retriable")
	}
	// A permanent DNS failure is fatal; a temporary one is not.
	if IsRetriableTransportError(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Error("permanent DNS failure must not be retriable")
	}
}

func TestRetryAfter(t *testing.T) {
	mk := func(v string) *http.Response {
		h := http.Header{}
		if v != "" {
			h.Set("Retry-After", v)
		}
		return &http.Response{Header: h}
	}
	if got := retryAfter(mk("2")); got != 2*time.Second {
		t.Errorf("delta-seconds: got %v, want 2s", got)
	}
	if got := retryAfter(mk("")); got != 0 {
		t.Errorf("absent header: got %v, want 0", got)
	}
	if got := retryAfter(mk("garbage")); got != 0 {
		t.Errorf("unparseable: got %v, want 0", got)
	}
	// A server asking for an unreasonable wait must not park a limiter slot for minutes.
	if got := retryAfter(mk("3600")); got != maxRetryAfter {
		t.Errorf("oversized: got %v, want the %v clamp", got, maxRetryAfter)
	}
	// An HTTP-date in the past yields no wait.
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := retryAfter(mk(past)); got != 0 {
		t.Errorf("past date: got %v, want 0", got)
	}
}

func TestBackoffFor_serverHintWins(t *testing.T) {
	// The server knows its own rate-limit window; a longer Retry-After must not be shortened by
	// the computed backoff.
	if got := backoffFor(1, 5*time.Second); got != 5*time.Second {
		t.Errorf("got %v, want the 5s server hint", got)
	}
	// A shorter hint does not shorten the computed backoff. Attempt 5 computes 2^4 * 200ms = 3.2s.
	if got := backoffFor(5, time.Millisecond); got < 3200*time.Millisecond {
		t.Errorf("got %v, want at least the 3.2s computed backoff", got)
	}
	// Backoff is capped: attempt 8 would compute 25.6s, which must clamp to maxBackoff (+jitter).
	if got := backoffFor(8, 0); got < maxBackoff || got > maxBackoff+jitterWindow {
		t.Errorf("got %v, want maxBackoff (%v) plus at most %v jitter", got, maxBackoff, jitterWindow)
	}
}
