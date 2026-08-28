package model

import (
	"errors"
	"fmt"
	"strings"
)

// TruncatedCompletionError reports that a provider ended a completion because the output token cap
// was reached, not because the model finished. The HTTP status is 200 and the body parses, so
// without this error the caller consumes a partial artifact: a half-written test class, or a JSON
// object cut mid-string that the defensive parsers may still extract a plausible-looking fragment
// from. That fragment is then written to disk and fails to compile.
//
// Providers return this instead of a *CompleteResult. Callers that can afford a larger cap should
// retry once at a higher MaxTokens (see IsLengthStopReason); callers that cannot must surface it
// rather than treating it as a normal response.
type TruncatedCompletionError struct {
	Provider  string // "openai", "anthropic", "ollama"
	Reason    string // provider-native stop reason, e.g. "length" or "max_tokens"
	MaxTokens int    // the cap that was hit (0 when the request did not set one)
	GotTokens int    // completion tokens produced, when the provider reported usage
	// Content is the partial text the provider did return. Retained for audit and debugging only —
	// callers must not treat it as a usable artifact.
	Content string
}

func (e *TruncatedCompletionError) Error() string {
	if e == nil {
		return "<nil truncated completion>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: completion truncated at the output cap (stop reason %q", e.Provider, e.Reason)
	if e.MaxTokens > 0 {
		fmt.Fprintf(&b, ", max_tokens=%d", e.MaxTokens)
	}
	if e.GotTokens > 0 {
		fmt.Fprintf(&b, ", produced=%d", e.GotTokens)
	}
	b.WriteString("); the response is incomplete and must not be used as an artifact")
	return b.String()
}

// IsTruncatedCompletion reports whether err is or wraps a *TruncatedCompletionError, and returns it.
func IsTruncatedCompletion(err error) (*TruncatedCompletionError, bool) {
	var t *TruncatedCompletionError
	if errors.As(err, &t) {
		return t, true
	}
	return nil, false
}

// IsLengthStopReason reports whether a provider-native stop reason means "hit the output cap".
// Covers OpenAI ("length"), Anthropic ("max_tokens") and Ollama ("length"). Comparison is
// case-insensitive because providers are not consistent about casing.
func IsLengthStopReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}
