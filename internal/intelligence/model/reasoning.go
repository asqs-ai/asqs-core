package model

import "strings"

// Reasoning models emit their chain of thought inline, ahead of the answer, wrapped in a tag the
// API does not separate out: DeepSeek R1 uses <think>, several others <thinking>. Providers that
// return reasoning in a dedicated field are unaffected — there is no tag in Content to find.
//
// Nothing in ASQS handled this, and one contract cannot survive it. A JSON reply does: the fixer's
// extraction ladder walks past a preamble and finds the object, which is why the defect stayed
// invisible for as long as it did. A PLAIN-TEXT reply does not, and the fixer has exactly one —
// llmfix.singleFilePlainFallback, the last-resort recovery after two failed JSON turns, which asks
// for "the complete corrected contents … the first character of your reply must be the first
// character of the file" and then gates the answer on evaluator.SyntacticShellReason. That gate
// rejects a <think> prefix by name ("stray token \"<think>\" at line 1, before the first type
// declaration"), so on a reasoning model the fallback could not succeed for ANY reply, however good.
// In run api-0c344e6bc0658e0db06506efb9d964f5 it had an unambiguous target, ran, and produced
// nothing; the round died at fixer_response_unusable and no llmfix.single_file_fallback_used row
// appears anywhere in the log.
//
// Stripping happens at the provider boundary rather than at that one call site, because the hazard
// belongs to every plain-text contract in the system — present and future — and the reasoning block
// is never wanted content in any of them.

// reasoningTags are the opening tags a leading reasoning block may use, each with its closer.
var reasoningTags = [][2]string{
	{"<think>", "</think>"},
	{"<thinking>", "</thinking>"},
}

// StripReasoningBlock removes a LEADING reasoning block from a completion, returning the remaining
// content and how many runes were dropped.
//
// Leading only, and that bound is what makes this safe rather than merely useful. A generated test
// may legitimately contain the literal text "<think>" — asserting on markup, say — and a rule that
// hunted for the tag anywhere would corrupt it. A reply that OPENS with the tag is reasoning by
// construction: no source file, no JSON document and no prose answer begins that way.
//
// An unclosed block means the reply was cut off mid-thought and contains no answer at all. Returning
// empty is the honest result: the caller then reports an empty completion instead of handing a
// parser a document that was never written.
func StripReasoningBlock(content string) (string, int) {
	trimmed := strings.TrimLeft(content, " \t\r\n")
	for _, tag := range reasoningTags {
		open, close := tag[0], tag[1]
		if !strings.HasPrefix(strings.ToLower(trimmed), open) {
			continue
		}
		before := len([]rune(content))
		rest := trimmed[len(open):]
		idx := indexFold(rest, close)
		if idx < 0 {
			return "", before // truncated mid-thought: there is no answer in here.
		}
		out := strings.TrimLeft(rest[idx+len(close):], " \t\r\n")
		return out, before - len([]rune(out))
	}
	return content, 0
}

// indexFold is strings.Index over a case-insensitive needle. The needle is ASCII here, so lowering
// the haystack cannot shift byte offsets.
func indexFold(haystack, needle string) int {
	return strings.Index(strings.ToLower(haystack), needle)
}
