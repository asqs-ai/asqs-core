package generator

import (
	"context"
	"errors"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// truncatingCompleter returns a truncation error for the first failCount calls, recording the
// MaxTokens each call was made with.
type truncatingCompleter struct {
	failCount int
	calls     []int
}

func (c *truncatingCompleter) Complete(ctx context.Context, msgs []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	c.calls = append(c.calls, opts.MaxTokens)
	if len(c.calls) <= c.failCount {
		return nil, &model.TruncatedCompletionError{
			Provider: "openai", Reason: "length", MaxTokens: opts.MaxTokens, GotTokens: opts.MaxTokens,
			Content: `{"a.java":"class A {`,
		}
	}
	return &model.CompleteResult{Content: "ok", StopReason: "stop"}, nil
}

type recordingAuditor struct{ steps []string }

func (a *recordingAuditor) Log(ctx context.Context, step string, payload interface{}) {
	a.steps = append(a.steps, step)
}
func (a *recordingAuditor) LogError(ctx context.Context, step string, payload interface{}) {
	a.steps = append(a.steps, step)
}

func TestCompleteGenerateWithRetry_defaultMaxTokensIs8192(t *testing.T) {
	// 4096 routinely truncates a JUnit class with several test methods; the fixer was already 8192.
	llm := &truncatingCompleter{}
	g := &LLMGenerator{LLM: llm}
	if _, err := g.completeGenerateWithRetry(context.Background(), nil, model.CompleteOptions{}); err != nil {
		t.Fatalf("completeGenerateWithRetry: %v", err)
	}
	if len(llm.calls) != 1 || llm.calls[0] != DefaultGenerateMaxTokens {
		t.Fatalf("calls = %v, want a single call at %d", llm.calls, DefaultGenerateMaxTokens)
	}
}

func TestCompleteGenerateWithRetry_retriesOnceAtDoubledCap(t *testing.T) {
	llm := &truncatingCompleter{failCount: 1}
	aud := &recordingAuditor{}
	g := &LLMGenerator{LLM: llm, Audit: aud}

	res, err := g.completeGenerateWithRetry(context.Background(), nil, model.CompleteOptions{MaxTokens: 4096})
	if err != nil {
		t.Fatalf("completeGenerateWithRetry: %v", err)
	}
	if res.Content != "ok" {
		t.Errorf("Content = %q", res.Content)
	}
	if len(llm.calls) != 2 {
		t.Fatalf("calls = %v, want 2", llm.calls)
	}
	if llm.calls[0] != 4096 || llm.calls[1] != 8192 {
		t.Errorf("caps = %v, want [4096 8192]", llm.calls)
	}
	if len(aud.steps) != 1 || aud.steps[0] != "llm.output_truncated_retry" {
		t.Errorf("audit steps = %v, want one llm.output_truncated_retry", aud.steps)
	}
}

// The retry is bounded: a model that keeps truncating must surface the error rather than escalate
// MaxTokens without limit or silently return the partial content.
func TestCompleteGenerateWithRetry_surfacesTruncationAtCeiling(t *testing.T) {
	llm := &truncatingCompleter{failCount: 99}
	aud := &recordingAuditor{}
	g := &LLMGenerator{LLM: llm, Audit: aud}

	res, err := g.completeGenerateWithRetry(context.Background(), nil, model.CompleteOptions{MaxTokens: maxGenerateOutputTokens})
	if err == nil {
		t.Fatalf("expected the truncation error to surface, got %+v", res)
	}
	if res != nil {
		t.Error("must not return partial content alongside the error")
	}
	if _, ok := model.IsTruncatedCompletion(err); !ok {
		t.Fatalf("error = %v, want *model.TruncatedCompletionError", err)
	}
	if len(llm.calls) != 1 {
		t.Errorf("calls = %v, want a single call at the ceiling", llm.calls)
	}
	if len(aud.steps) != 1 || aud.steps[0] != "llm.output_truncated" {
		t.Errorf("audit steps = %v, want one llm.output_truncated", aud.steps)
	}
}

// A truncation is not a transport failure: the transient-network retry must not consume attempts on
// it, because the identical request reproduces it exactly.
func TestCompleteGenerateWithRetry_truncationDoesNotUseTransientRetries(t *testing.T) {
	llm := &truncatingCompleter{failCount: 99}
	g := &LLMGenerator{LLM: llm}
	_, err := g.completeGenerateWithRetry(context.Background(), nil, model.CompleteOptions{MaxTokens: 8192})
	if err == nil {
		t.Fatal("expected an error")
	}
	// 8192 -> 16384 (ceiling) -> surface. Two calls, not the three transient attempts.
	if len(llm.calls) != 2 {
		t.Fatalf("calls = %v, want exactly 2 (one doubling, then surface)", llm.calls)
	}
}

func TestCompleteGenerateWithRetry_nilAuditIsSafe(t *testing.T) {
	llm := &truncatingCompleter{failCount: 1}
	g := &LLMGenerator{LLM: llm} // Audit deliberately nil
	if _, err := g.completeGenerateWithRetry(context.Background(), nil, model.CompleteOptions{MaxTokens: 4096}); err != nil {
		t.Fatalf("completeGenerateWithRetry: %v", err)
	}
}

// A non-truncation error keeps the existing behaviour: no cap escalation.
func TestCompleteGenerateWithRetry_nonTruncationErrorDoesNotBumpCap(t *testing.T) {
	llm := &alwaysFailingCompleter{err: errors.New("invalid api key")}
	g := &LLMGenerator{LLM: llm}
	if _, err := g.completeGenerateWithRetry(context.Background(), nil, model.CompleteOptions{MaxTokens: 4096}); err == nil {
		t.Fatal("expected an error")
	}
	if len(llm.calls) != 1 || llm.calls[0] != 4096 {
		t.Errorf("calls = %v, want a single call at 4096 (auth errors are not retried)", llm.calls)
	}
}

type alwaysFailingCompleter struct {
	err   error
	calls []int
}

func (c *alwaysFailingCompleter) Complete(ctx context.Context, msgs []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	c.calls = append(c.calls, opts.MaxTokens)
	return nil, c.err
}
