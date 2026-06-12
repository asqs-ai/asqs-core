package overview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

func TestIsRetriableOverviewLLMError_postUnexpectedEOF(t *testing.T) {
	err := errors.New(`Post "https://api.openai.com/v1/chat/completions": unexpected EOF`)
	if !isRetriableOverviewLLMError(err) {
		t.Fatal("want retriable")
	}
}

func TestIsRetriableOverviewLLMError_wrappedChain(t *testing.T) {
	inner := errors.New(`Post "https://api.openai.com/v1/chat/completions": unexpected EOF`)
	wrapped := fmt.Errorf("openai chat: %w", inner)
	if !isRetriableOverviewLLMError(wrapped) {
		t.Fatal("wrapped: want retriable")
	}
}

func TestIsRetriableOverviewLLMError_notAuth(t *testing.T) {
	if isRetriableOverviewLLMError(errors.New("incorrect api key")) {
		t.Fatal("auth string should not be retriable")
	}
}

func TestIsRetriableOverviewLLMError_canceled(t *testing.T) {
	if isRetriableOverviewLLMError(context.Canceled) {
		t.Fatal("canceled not retriable")
	}
}

func TestIsRetriableOverviewLLMError_ioUnexpectedEOF(t *testing.T) {
	if !isRetriableOverviewLLMError(io.ErrUnexpectedEOF) {
		t.Fatal("io.ErrUnexpectedEOF retriable")
	}
}

type failNThenOK struct {
	left int
}

func (f *failNThenOK) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	if f.left > 0 {
		f.left--
		return nil, errors.New(`Post "https://api.openai.com/v1/chat/completions": unexpected EOF`)
	}
	return &model.CompleteResult{Content: "ok"}, nil
}

func TestOverviewLLMCompleteWithRetry_eventuallySucceeds(t *testing.T) {
	llm := &failNThenOK{left: 2}
	res, err := overviewLLMCompleteWithRetry(context.Background(), llm, "test", []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{MaxTokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" {
		t.Fatalf("got %q", res.Content)
	}
}

type eofEveryCall struct{ calls int }

func (e *eofEveryCall) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	e.calls++
	return nil, errors.New(`Post "https://api.openai.com/v1/chat/completions": unexpected EOF`)
}

func TestOverviewLLMCompleteBatchedSlice_eofNoSamePayloadRetries(t *testing.T) {
	llm := &eofEveryCall{}
	_, err := overviewLLMCompleteBatchedSlice(context.Background(), llm, "test", []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("got %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("Complete calls=%d want 1 (unexpected EOF returns immediately for batched overview resplit)", llm.calls)
	}
}

type fail503ThenOK struct{ left int }

func (f *fail503ThenOK) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	if f.left > 0 {
		f.left--
		return nil, errors.New("503 Service Unavailable")
	}
	return &model.CompleteResult{Content: "ok"}, nil
}

func TestOverviewLLMCompleteBatchedSlice_retries503(t *testing.T) {
	llm := &fail503ThenOK{left: 1}
	res, err := overviewLLMCompleteBatchedSlice(context.Background(), llm, "test", []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "ok" {
		t.Fatalf("got %q", res.Content)
	}
}

func TestOverviewLLMCompleteWithRetry_nonRetriableStops(t *testing.T) {
	llm := &stubRetryAlwaysErr{err: errors.New("invalid model: xyz")}
	_, err := overviewLLMCompleteWithRetry(context.Background(), llm, "test", []model.Message{{Role: "user", Content: "x"}}, model.CompleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "invalid model") {
		t.Fatalf("got %v", err)
	}
	if llm.calls != 1 {
		t.Fatalf("calls=%d want 1", llm.calls)
	}
}

type stubRetryAlwaysErr struct {
	err   error
	calls int
}

func (s *stubRetryAlwaysErr) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	s.calls++
	return nil, s.err
}
