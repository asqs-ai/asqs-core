package vcs

import "github.com/asqs/asqs-core/internal/langid"

// LanguageSupported reports whether a repo's detected language is in the operator's
// serve.gating.supported_languages list.
//
// Both sides are canonicalised. The comparison was `l == lang` against whatever the repo inspector
// answered, so a config naming `csharp` rejected a repo the inspector called `cs` — and the reason
// the operator saw was "language/framework not supported", which reads like a deliberate policy
// rather than a spelling mismatch.
//
// An empty list means the gate is not configured, and an empty detection means the inspector could
// not tell — neither is grounds for rejection.
func LanguageSupported(supported []string, detected string) bool {
	if len(supported) == 0 || detected == "" {
		return true
	}
	for _, l := range supported {
		if langid.Is(l, detected) {
			return true
		}
	}
	return false
}
