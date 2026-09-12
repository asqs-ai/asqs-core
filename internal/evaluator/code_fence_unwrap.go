package evaluator

import "strings"

// mdFence is the markdown code-fence marker. Never valid source in any language this system
// generates, which is why SyntacticShellReason refuses a file containing one.
const mdFence = "```"

// UnwrapSingleCodeFence strips the markdown fence from content that is EXACTLY one fenced block
// and nothing else, returning the body and true. Anything else comes back unchanged with false.
//
// A model that wraps its whole answer in one fence has produced the right file inside the wrong
// envelope. The FIX path has always unwrapped that (llmfix.extractFixResponseCodeBlock); the
// GENERATE path only refused it — and on the generate path a refusal destroys the artifact
// outright rather than costing a round. Run api-8a6ad5508ad43b22f3ff969b2d1da5cd lost
// e2e/routes/checkout.spec.ts to `generated_not_written: contains markdown code fence`, leaving
// the run with no spec for the /checkout page route; api-fd5599a24f84dbe71e0c831b3266e4f4 lost a
// .ts spec the same way before it.
//
// Deliberately narrow, because the cost of the two error directions is not symmetric: accepting a
// wrapper that was really prose would write a file the compiler has to reject, while declining a
// genuine wrapper costs only the refusal we already had. So every one of these must hold:
//
//   - the trimmed content both opens and closes with a fence;
//   - the fence marker appears exactly twice, so nothing survives inside the body;
//   - the opening line's info string is a single token (```ts, ```java) — spaces mean the line is
//     prose that happens to start with backticks;
//   - the body is non-empty.
//
// Unwrapping is an envelope repair, not a waiver: the caller must re-run the content gates on the
// body, which is what refuses an unwrapped file that is empty, a bare shell, or otherwise unusable.
func UnwrapSingleCodeFence(content string) (string, bool) {
	s := strings.TrimSpace(content)
	if !strings.HasPrefix(s, mdFence) || !strings.HasSuffix(s, mdFence) {
		return content, false
	}
	if strings.Count(s, mdFence) != 2 {
		return content, false
	}
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return content, false
	}
	if info := strings.TrimSpace(s[len(mdFence):nl]); strings.ContainsAny(info, " \t") {
		return content, false
	}
	body := strings.TrimSpace(s[nl+1 : len(s)-len(mdFence)])
	if body == "" || strings.Contains(body, mdFence) {
		return content, false
	}
	return body, true
}
