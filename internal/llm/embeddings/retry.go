package llembed

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"
)

const embedMaxAttempts = 5

func embedRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	d := time.Duration(1<<uint(attempt-1)) * 200 * time.Millisecond
	if d > 8*time.Second {
		return 8 * time.Second
	}
	return d
}

func sleepEmbedRetry(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	base := embedRetryBackoff(attempt)
	jitter := time.Duration(rand.Int64N(int64(500 * time.Millisecond)))
	d := base + jitter
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func isRetriableGatewayError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "unexpected eof") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "server closed") ||
		strings.Contains(s, "tls handshake") {
		return true
	}
	if strings.Contains(s, "429") || strings.Contains(s, "502") ||
		strings.Contains(s, "503") || strings.Contains(s, "504") {
		return true
	}
	return false
}

// RunEmbedRetries executes fn with exponential backoff on transient failures (aligned with OpenAI embed retries).
func RunEmbedRetries(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < embedMaxAttempts; attempt++ {
		if e := ctx.Err(); e != nil {
			return e
		}
		if attempt > 0 {
			if errSleep := sleepEmbedRetry(ctx, attempt); errSleep != nil {
				return errSleep
			}
		}
		err = fn()
		if err == nil {
			return nil
		}
		if !isRetriableGatewayError(err) {
			return err
		}
	}
	return err
}

// SleepBeforeRetry waits with backoff before retry attempt (attempt >= 1).
func SleepBeforeRetry(ctx context.Context, attempt int) error {
	return sleepEmbedRetry(ctx, attempt)
}

// IsRetriableChatTransport mirrors transient detection used for embedding retries (chat HTTP failures).
func IsRetriableChatTransport(err error) bool {
	return isRetriableGatewayError(err)
}

// IsRetriableHTTPStatus is true for overload/gateway statuses worth retrying.
func IsRetriableHTTPStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}
