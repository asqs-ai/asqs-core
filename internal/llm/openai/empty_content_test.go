package openai

import "testing"

// An assistant message with neither content nor tool_calls still needs a string.
//
// (Upstream also has two wire-level tests here — a tool message with empty content keeps a
// `content` key, and an assistant tool-call turn keeps its legal absent content. Both need the
// tool message types and arrive with the tool contract in CP41; contentOrPlaceholder's third
// parameter is what they exercise.)
func TestContentOrPlaceholder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		content      string
		role         string
		hasToolCalls bool
		want         string
	}{
		{"passes through", "body", "tool", false, "body"},
		{"empty tool", "", "tool", false, emptyContentPlaceholder},
		{"empty user", "", "user", false, emptyContentPlaceholder},
		{"assistant with calls", "", "assistant", true, ""},
		{"assistant without calls", "", "assistant", false, emptyContentPlaceholder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := contentOrPlaceholder(tc.content, tc.role, tc.hasToolCalls); got != tc.want {
				t.Errorf("contentOrPlaceholder(%q, %q, %v) = %q, want %q", tc.content, tc.role, tc.hasToolCalls, got, tc.want)
			}
		})
	}
}
