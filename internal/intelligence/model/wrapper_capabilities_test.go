package model

import (
	"context"
	"testing"
)

type wrapDeclaringCompleter struct{ caps Capabilities }

func (d wrapDeclaringCompleter) Complete(context.Context, []Message, CompleteOptions) (*CompleteResult, error) {
	return &CompleteResult{}, nil
}
func (d wrapDeclaringCompleter) Capabilities() Capabilities { return d.caps }

type wrapPlainCompleter struct{}

func (wrapPlainCompleter) Complete(context.Context, []Message, CompleteOptions) (*CompleteResult, error) {
	return &CompleteResult{}, nil
}

// A wrapper must forward its inner completer's declaration: losing it would make every wrapped
// provider read as "unknown" and re-enable the degradations the declaration exists to prevent.
func TestWrappers_forwardCapabilities(t *testing.T) {
	inner := wrapDeclaringCompleter{caps: Capabilities{StructuredOutput: true, UsageReporting: true}}
	wrapped := NewUsageTrackingChatCompleter(inner, &UsageAccumulator{})
	caps, declared := DeclaredCapabilitiesOf(wrapped)
	if !declared {
		t.Fatal("usage wrapper dropped the inner declaration")
	}
	if !caps.StructuredOutput || !caps.UsageReporting {
		t.Fatalf("forwarded capabilities wrong: %+v", caps)
	}
}

// The inverse matters just as much: a wrapper must NOT manufacture a declaration for an inner
// completer that never made one — "undeclared" means unknown, not incapable, and fabricating the
// zero value would flip that to "declared incapable".
func TestWrappers_doNotFabricateADeclaration(t *testing.T) {
	wrapped := NewUsageTrackingChatCompleter(wrapPlainCompleter{}, &UsageAccumulator{})
	if _, declared := DeclaredCapabilitiesOf(wrapped); declared {
		t.Fatal("usage wrapper fabricated a capability declaration for an undeclared inner completer")
	}
}

// The concurrency-limiter wrapper's forwarding tests return with the bundle that brings
// NewConcurrencyLimitedCompleter/NewLLMLimiter (the llm.max_concurrent machinery, CP60's call).
