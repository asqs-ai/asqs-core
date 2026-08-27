package model

import "context"

// DefaultLLMMaxConcurrent is the in-flight LLM-request cap used when LLM.MaxConcurrent is unset
// (<= 0). It is high enough to let the test / doc / overview generation workstreams overlap for a
// real speedup, while staying conservative enough for typical provider rate limits.
const DefaultLLMMaxConcurrent = 8

// LLMLimiter bounds the number of concurrent LLM Complete calls. One instance is shared across all
// step completers (generation, docs, overview, fixer) for a run so that parallelizing those
// workstreams never exceeds the provider's safe concurrency, regardless of how many goroutines call
// the LLM. It complements (does not replace) the providers' reactive 429 exponential backoff.
//
// The zero value is not usable; construct with NewLLMLimiter.
type LLMLimiter struct {
	sem chan struct{}
}

// ResolveLLMMaxConcurrent returns the effective in-flight cap for a configured LLM.MaxConcurrent
// value: the value itself when > 0, else DefaultLLMMaxConcurrent. Shared by NewLLMLimiter and by
// callers (e.g. the run-scope doc pass) that size their fan-out to the same cap.
func ResolveLLMMaxConcurrent(max int) int {
	if max <= 0 {
		return DefaultLLMMaxConcurrent
	}
	return max
}

// NewLLMLimiter returns a limiter that allows at most max concurrent Complete calls. max <= 0
// falls back to DefaultLLMMaxConcurrent.
func NewLLMLimiter(max int) *LLMLimiter {
	return &LLMLimiter{sem: make(chan struct{}, ResolveLLMMaxConcurrent(max))}
}

// Cap reports the configured maximum number of concurrent calls.
func (l *LLMLimiter) Cap() int {
	if l == nil {
		return 0
	}
	return cap(l.sem)
}

// acquire blocks until a slot is free or ctx is done. Returns ctx.Err() when cancelled before a
// slot is acquired so callers never proceed to the LLM after cancellation.
func (l *LLMLimiter) acquire(ctx context.Context) error {
	select {
	case l.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *LLMLimiter) release() { <-l.sem }

// concurrencyLimitedChatCompleter wraps a ChatCompleter and gates every Complete on a shared
// LLMLimiter.
type concurrencyLimitedChatCompleter struct {
	inner ChatCompleter
	lim   *LLMLimiter
}

// NewConcurrencyLimitedCompleter returns inner unchanged when inner or lim is nil (matching
// NewUsageTrackingChatCompleter); otherwise it wraps inner so each Complete acquires a slot from
// lim first. Wrapping multiple completers with the SAME lim gives a single global in-flight cap
// shared across all of them.
func NewConcurrencyLimitedCompleter(inner ChatCompleter, lim *LLMLimiter) ChatCompleter {
	if inner == nil || lim == nil {
		return inner
	}
	base := &concurrencyLimitedChatCompleter{inner: inner, lim: lim}
	// Two types, chosen by whether inner declares capabilities.
	//
	// A single wrapper type carrying Capabilities() would satisfy CapabilityReporter ALWAYS, so an
	// undeclared provider would come back "declared, all false". That is the opposite error and just
	// as damaging: undeclared resolves to the prompted tool tier, declared-incapable to one-shot.
	// Returning a reporting type only when there is something to report keeps both answers honest.
	if _, ok := inner.(CapabilityReporter); ok {
		return &concurrencyLimitedReporter{concurrencyLimitedChatCompleter: *base}
	}
	return base
}

// concurrencyLimitedReporter is the limiter wrapper for a provider that declares capabilities.
type concurrencyLimitedReporter struct {
	concurrencyLimitedChatCompleter
}

func (c *concurrencyLimitedReporter) Capabilities() Capabilities {
	return c.inner.(CapabilityReporter).Capabilities()
}

func (c *concurrencyLimitedChatCompleter) Complete(ctx context.Context, messages []Message, opts CompleteOptions) (*CompleteResult, error) {
	if err := c.lim.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.lim.release()
	return c.inner.Complete(ctx, messages, opts)
}
