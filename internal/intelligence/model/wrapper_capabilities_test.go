package model

import (
	"context"
	"testing"
)

type declaresTools struct{}

func (declaresTools) Complete(context.Context, []Message, CompleteOptions) (*CompleteResult, error) {
	return &CompleteResult{}, nil
}
func (declaresTools) Capabilities() Capabilities { return Capabilities{ToolCalling: true} }

type declaresNothing struct{}

func (declaresNothing) Complete(context.Context, []Message, CompleteOptions) (*CompleteResult, error) {
	return &CompleteResult{}, nil
}

// Wrappers must forward Capabilities, or every provider looks undeclared to callers that gate on it.
//
// This is not cosmetic. Completers reach the generator through the concurrency limiter and the usage
// tracker, so a wrapper that swallows Capabilities() makes DeclaredCapabilitiesOf report "undeclared"
// for OpenAI and Anthropic too — and the tool-mode resolver would then put every provider on the
// prompted fallback, never the native path, with nothing failing to show it.
func TestWrappers_forwardCapabilities(t *testing.T) {
	inner := declaresTools{}
	for _, tc := range []struct {
		name    string
		wrapped ChatCompleter
	}{
		{"concurrency limiter", NewConcurrencyLimitedCompleter(inner, NewLLMLimiter(4))},
		{"usage tracker", NewUsageTrackingChatCompleter(inner, &UsageAccumulator{})},
		{"both, limiter outermost", NewConcurrencyLimitedCompleter(NewUsageTrackingChatCompleter(inner, &UsageAccumulator{}), NewLLMLimiter(4))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps, declared := DeclaredCapabilitiesOf(tc.wrapped)
			if !declared {
				t.Fatal("wrapper hides the provider's declaration; every provider looks undeclared")
			}
			if !caps.ToolCalling {
				t.Errorf("capabilities not forwarded: %+v", caps)
			}
		})
	}
}

// The inverse must hold too: wrapping a provider that declares NOTHING must not make it look like it
// declared all-false. "Undeclared" and "declared incapable" resolve to different tiers — prompted
// versus one-shot — so conflating them silently removes tool access from unknown providers.
func TestWrappers_doNotFabricateADeclaration(t *testing.T) {
	inner := declaresNothing{}
	for _, tc := range []struct {
		name    string
		wrapped ChatCompleter
	}{
		{"concurrency limiter", NewConcurrencyLimitedCompleter(inner, NewLLMLimiter(4))},
		{"usage tracker", NewUsageTrackingChatCompleter(inner, &UsageAccumulator{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, declared := DeclaredCapabilitiesOf(tc.wrapped); declared {
				t.Error("wrapper claims a declaration the provider never made")
			}
		})
	}
}
