package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsLengthStopReason(t *testing.T) {
	// Each provider spells "hit the output cap" differently; all three must be recognized, and
	// nothing else may be.
	truthy := []string{"length", "max_tokens", "max_output_tokens", "LENGTH", "  Max_Tokens  "}
	for _, s := range truthy {
		if !IsLengthStopReason(s) {
			t.Errorf("IsLengthStopReason(%q) = false, want true", s)
		}
	}
	falsy := []string{"", "stop", "end_turn", "stop_sequence", "content_filter", "tool_use", "lengthy"}
	for _, s := range falsy {
		if IsLengthStopReason(s) {
			t.Errorf("IsLengthStopReason(%q) = true, want false", s)
		}
	}
}

func TestIsTruncatedCompletion_unwrapsWrappedError(t *testing.T) {
	base := &TruncatedCompletionError{Provider: "openai", Reason: "length", MaxTokens: 4096, GotTokens: 4096}
	wrapped := fmt.Errorf("generate gap %s: %w", "OrderService#place", base)

	got, ok := IsTruncatedCompletion(wrapped)
	if !ok {
		t.Fatal("IsTruncatedCompletion did not match through fmt.Errorf %w wrapping")
	}
	if got != base {
		t.Fatalf("returned a different error instance: %#v", got)
	}
	if !errors.Is(wrapped, error(base)) {
		t.Error("errors.Is should match the wrapped truncation error")
	}
}

func TestIsTruncatedCompletion_otherErrors(t *testing.T) {
	if _, ok := IsTruncatedCompletion(nil); ok {
		t.Error("nil error must not report truncation")
	}
	if _, ok := IsTruncatedCompletion(errors.New("connection reset by peer")); ok {
		t.Error("transport error must not report truncation")
	}
}

func TestTruncatedCompletionError_Message(t *testing.T) {
	e := &TruncatedCompletionError{Provider: "anthropic", Reason: "max_tokens", MaxTokens: 8192, GotTokens: 8192}
	msg := e.Error()
	for _, want := range []string{"anthropic", "max_tokens", "8192", "must not be used"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}
