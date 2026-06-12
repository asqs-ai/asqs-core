package overview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// overviewCompleteMaxAttempts is orchestrator-side retries for overview LLM calls only (does not change the OpenAI client).
const overviewCompleteMaxAttempts = 8

func overviewCompleteRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	d := time.Duration(1<<uint(attempt-1)) * 300 * time.Millisecond
	if d > 25*time.Second {
		return 25 * time.Second
	}
	return d
}

// isRetriableOverviewLLMError classifies errors worth retrying for overview generation (transport / gateway flakes).
// It walks errors.Unwrap so wrapped url.Error / fmt.Errorf chains still match.
func isRetriableOverviewLLMError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			return false
		}
		var netErr net.Error
		if errors.As(e, &netErr) && netErr.Timeout() {
			return true
		}
		if errors.Is(e, io.ErrUnexpectedEOF) || errors.Is(e, io.EOF) {
			return true
		}
		if overviewLLMRetriableMessage(strings.ToLower(e.Error())) {
			return true
		}
	}
	return false
}

func isUnexpectedEOFChatError(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if errors.Is(e, io.ErrUnexpectedEOF) {
			return true
		}
		if strings.Contains(strings.ToLower(e.Error()), "unexpected eof") {
			return true
		}
	}
	return false
}

func overviewLLMRetriableMessage(s string) bool {
	if strings.Contains(s, "unexpected eof") {
		return true
	}
	if strings.Contains(s, "eof") && (strings.Contains(s, "read") || strings.Contains(s, "post") || strings.Contains(s, "http")) {
		return true
	}
	if strings.Contains(s, "connection reset") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "server closed") ||
		strings.Contains(s, "read tcp") ||
		strings.Contains(s, "write tcp") ||
		strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "tls handshake") {
		return true
	}
	if strings.Contains(s, "429") || strings.Contains(s, "502") || strings.Contains(s, "503") || strings.Contains(s, "504") {
		return true
	}
	return false
}

func sleepOverviewLLMRetry(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	base := overviewCompleteRetryBackoff(attempt)
	jitter := time.Duration(rand.Int64N(int64(500 * time.Millisecond)))
	d := base + jitter
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// overviewLLMCompleteWithRetry calls llm.Complete with orchestrator-side retries on transient errors.
// label is used only for stderr logging (e.g. "full_narrative", "batched_full_slice_2").
func overviewLLMCompleteWithRetry(ctx context.Context, llm model.ChatCompleter, label string, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("overview: ChatCompleter is nil")
	}
	var lastErr error
	for attempt := 0; attempt < overviewCompleteMaxAttempts; attempt++ {
		if attempt > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "[asqs-overview] %s Complete retry attempt %d/%d after %v last_err=%v\n",
				label, attempt+1, overviewCompleteMaxAttempts, overviewCompleteRetryBackoff(attempt), lastErr)
			if err := sleepOverviewLLMRetry(ctx, attempt); err != nil {
				return nil, err
			}
		}
		res, err := llm.Complete(ctx, messages, opts)
		if err == nil {
			if attempt > 0 {
				fmt.Fprintf(os.Stderr, "[asqs-overview] %s Complete ok after %d attempt(s)\n", label, attempt+1)
			}
			return res, nil
		}
		lastErr = err
		if !isRetriableOverviewLLMError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// overviewLLMCompleteBatchedSlice calls llm.Complete with retries on transient errors for Plan B batched overview
// slices only. unexpected EOF returns immediately (no retry with the same payload) so the caller can split the
// file batch and delegate the remainder to a follow-up request.
func overviewLLMCompleteBatchedSlice(ctx context.Context, llm model.ChatCompleter, label string, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	if llm == nil {
		return nil, fmt.Errorf("overview: ChatCompleter is nil")
	}
	var lastErr error
	for attempt := 0; attempt < overviewCompleteMaxAttempts; attempt++ {
		if attempt > 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "[asqs-overview] %s Complete retry attempt %d/%d after %v last_err=%v\n",
				label, attempt+1, overviewCompleteMaxAttempts, overviewCompleteRetryBackoff(attempt), lastErr)
			if err := sleepOverviewLLMRetry(ctx, attempt); err != nil {
				return nil, err
			}
		}
		res, err := llm.Complete(ctx, messages, opts)
		if err == nil {
			if attempt > 0 {
				fmt.Fprintf(os.Stderr, "[asqs-overview] %s Complete ok after %d attempt(s)\n", label, attempt+1)
			}
			return res, nil
		}
		lastErr = err
		if isUnexpectedEOFChatError(err) {
			return nil, err
		}
		if !isRetriableOverviewLLMError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}
