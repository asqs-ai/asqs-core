package model

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blockingCompleter blocks inside Complete until released, recording peak concurrency so the test
// can assert the limiter never lets more than cap calls run at once.
type blockingCompleter struct {
	inFlight atomic.Int32
	peak     atomic.Int32
	release  chan struct{}
	started  chan struct{}
}

func (b *blockingCompleter) Complete(ctx context.Context, _ []Message, _ CompleteOptions) (*CompleteResult, error) {
	n := b.inFlight.Add(1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if b.started != nil {
		b.started <- struct{}{}
	}
	<-b.release
	b.inFlight.Add(-1)
	return &CompleteResult{Content: "ok"}, nil
}

func TestLLMLimiter_CapsConcurrency(t *testing.T) {
	const cap = 3
	const callers = cap * 3
	inner := &blockingCompleter{release: make(chan struct{}), started: make(chan struct{}, callers)}
	c := NewConcurrencyLimitedCompleter(inner, NewLLMLimiter(cap))

	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Complete(context.Background(), nil, CompleteOptions{})
		}()
	}

	// Wait until exactly `cap` calls are inside Complete, then confirm no more get in while blocked.
	for i := 0; i < cap; i++ {
		<-inner.started
	}
	// Give any (incorrectly) unblocked extra callers a chance to enter before asserting.
	time.Sleep(20 * time.Millisecond)
	if got := inner.inFlight.Load(); got != cap {
		t.Fatalf("in-flight while blocked = %d, want %d", got, cap)
	}

	close(inner.release)
	wg.Wait()
	if got := inner.peak.Load(); got > cap {
		t.Fatalf("peak concurrency = %d, want <= %d", got, cap)
	}
}

func TestLLMLimiter_DefaultWhenUnset(t *testing.T) {
	for _, max := range []int{0, -1} {
		if got := NewLLMLimiter(max).Cap(); got != DefaultLLMMaxConcurrent {
			t.Fatalf("NewLLMLimiter(%d).Cap() = %d, want %d", max, got, DefaultLLMMaxConcurrent)
		}
	}
	if got := NewLLMLimiter(5).Cap(); got != 5 {
		t.Fatalf("NewLLMLimiter(5).Cap() = %d, want 5", got)
	}
}

func TestConcurrencyLimitedCompleter_CtxCancelDuringAcquire(t *testing.T) {
	inner := &blockingCompleter{release: make(chan struct{})}
	defer close(inner.release)
	lim := NewLLMLimiter(1)
	c := NewConcurrencyLimitedCompleter(inner, lim)

	// Saturate the single slot with a blocked call.
	go func() { _, _ = c.Complete(context.Background(), nil, CompleteOptions{}) }()
	// Spin until the slot is taken.
	for inner.inFlight.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Complete(ctx, nil, CompleteOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled when cancelled during acquire, got %v", err)
	}
}

func TestNewConcurrencyLimitedCompleter_NilPassthrough(t *testing.T) {
	inner := &blockingCompleter{release: make(chan struct{})}
	if got := NewConcurrencyLimitedCompleter(nil, NewLLMLimiter(1)); got != nil {
		t.Fatalf("nil inner must pass through as nil, got %T", got)
	}
	if got := NewConcurrencyLimitedCompleter(inner, nil); got != ChatCompleter(inner) {
		t.Fatalf("nil limiter must return inner unchanged")
	}
}

// Core-authored addition: the global-ceiling claim itself. Upstream's cap test wraps ONE completer;
// what BuildStepCompleters relies on is that DIFFERENT completers wrapped with the SAME limiter
// share one in-flight cap — a per-step limiter would multiply the ceiling by the number of steps.
func TestLLMLimiter_isSharedAcrossCompleters(t *testing.T) {
	release := make(chan struct{})
	var inFlight, peak atomic.Int32
	track := func() func() {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		return func() { inFlight.Add(-1) }
	}
	mk := func() ChatCompleter {
		return completerFunc(func(ctx context.Context) (*CompleteResult, error) {
			defer track()()
			<-release
			return &CompleteResult{Content: "ok"}, nil
		})
	}
	lim := NewLLMLimiter(1)
	a := NewConcurrencyLimitedCompleter(mk(), lim)
	b := NewConcurrencyLimitedCompleter(mk(), lim)

	var wg sync.WaitGroup
	for _, c := range []ChatCompleter{a, b, a, b} {
		wg.Add(1)
		go func(c ChatCompleter) {
			defer wg.Done()
			_, _ = c.Complete(context.Background(), nil, CompleteOptions{})
		}(c)
	}
	// Let one call take the single slot, then give any incorrectly-admitted extras time to enter.
	for inFlight.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := inFlight.Load(); got != 1 {
		t.Fatalf("in-flight across two wrapped completers = %d, want 1 (one shared slot)", got)
	}
	close(release)
	wg.Wait()
	if got := peak.Load(); got > 1 {
		t.Fatalf("peak concurrency across completers = %d, want <= 1 at max_concurrent 1", got)
	}
}

// completerFunc adapts a func to ChatCompleter for the shared-limiter test.
type completerFunc func(ctx context.Context) (*CompleteResult, error)

func (f completerFunc) Complete(ctx context.Context, _ []Message, _ CompleteOptions) (*CompleteResult, error) {
	return f(ctx)
}
