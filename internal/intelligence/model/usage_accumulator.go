package model

import (
	"context"
	"sync"
)

// UsageAccumulator sums token usage from multiple ChatCompleter.Complete calls (e.g. test generation + evaluator fixes).
// Safe for concurrent use. Nil receiver is a no-op for Add and Totals.
type UsageAccumulator struct {
	mu               sync.Mutex
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// Add merges one completion's usage into the accumulator. Nil usage is ignored.
func (a *UsageAccumulator) Add(u *Usage) {
	if a == nil || u == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.PromptTokens += int64(u.PromptTokens)
	a.CompletionTokens += int64(u.CompletionTokens)
	if u.TotalTokens > 0 {
		a.TotalTokens += int64(u.TotalTokens)
	} else {
		a.TotalTokens += int64(u.PromptTokens + u.CompletionTokens)
	}
}

// Totals returns accumulated prompt, completion, and total token counts.
func (a *UsageAccumulator) Totals() (prompt, completion, total int64) {
	if a == nil {
		return 0, 0, 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.PromptTokens, a.CompletionTokens, a.TotalTokens
}

// usageTrackingChatCompleter wraps a ChatCompleter and records Usage from each successful Complete.
type usageTrackingChatCompleter struct {
	inner ChatCompleter
	acc   *UsageAccumulator
}

// NewUsageTrackingChatCompleter returns inner unchanged if inner or acc is nil; otherwise wraps inner.
func NewUsageTrackingChatCompleter(inner ChatCompleter, acc *UsageAccumulator) ChatCompleter {
	if inner == nil || acc == nil {
		return inner
	}
	base := &usageTrackingChatCompleter{inner: inner, acc: acc}
	// A wrapper must forward a capability declaration when there is one and must not manufacture
	// one when there is not: an undeclared inner completer stays undeclared through the wrapper,
	// so callers keep the "unknown, not incapable" semantics.
	if _, ok := inner.(CapabilityReporter); ok {
		return &usageTrackingReporter{usageTrackingChatCompleter: *base}
	}
	return base
}

func (t *usageTrackingChatCompleter) Complete(ctx context.Context, messages []Message, opts CompleteOptions) (*CompleteResult, error) {
	res, err := t.inner.Complete(ctx, messages, opts)
	if err != nil || res == nil {
		return res, err
	}
	if res.Usage != nil {
		t.acc.Add(res.Usage)
	}
	return res, nil
}

// usageTrackingReporter is the usage wrapper for a provider that declares capabilities.
type usageTrackingReporter struct {
	usageTrackingChatCompleter
}

func (u *usageTrackingReporter) Capabilities() Capabilities {
	return u.inner.(CapabilityReporter).Capabilities()
}
