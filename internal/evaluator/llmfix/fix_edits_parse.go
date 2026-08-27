package llmfix

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
)

// repairRawControlCharsInJSONStrings escapes raw control characters (newline, carriage return,
// tab, and other C0 bytes) that appear INSIDE JSON string literals.
//
// This is the dominant malformation when a model returns whole source files as JSON string values:
// it writes the file's real line breaks instead of the two-character escape \n, and encoding/json
// rejects the result with `invalid character '\n' in string literal`. The output is otherwise
// perfect — braces balanced, keys correct, string closed — so classifyFixParseFailure reports
// not_json rather than truncated_json, and the whole repair chain gives up on a response that is
// one mechanical transformation away from usable.
//
// Safe by construction. Valid JSON can never contain a raw C0 byte inside a string, so this is a
// no-op on well-formed input; and it only ever rewrites bytes while the scanner is inside a string
// literal, so structural characters (braces, colons, commas) are untouched. Escape sequences are
// tracked so an already-escaped `\"` does not flip the in-string state.
//
// It does NOT attempt to repair unescaped double quotes inside values — that is ambiguous (a `"`
// inside a string is indistinguishable from the string's terminator without parsing the payload
// language), and guessing there could silently corrupt the file that gets written to disk.
func repairRawControlCharsInJSONStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/16)
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
			b.WriteByte(c)
			continue
		case c == '\\' && inStr:
			esc = true
			b.WriteByte(c)
			continue
		case c == '"':
			inStr = !inStr
			b.WriteByte(c)
			continue
		}
		if inStr && c < 0x20 {
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// parseFixEdits extracts the targeted-edit response shape:
//
//	{"edits": {"path/to/File.java": [{"find": "…", "replace": "…"}]}}
//
// Returns nil when the response is not in that shape, so the caller falls through to the
// whole-file path→content parser unchanged. Deliberately additive: the whole-file chain carries
// several hard-won repair steps (raw control characters, fenced extraction, truncation recovery)
// and this must not disturb any of them.
func parseFixEdits(content string) map[string][]evaluator.FixEdit {
	content = strings.TrimSpace(content)
	if content == "" || !strings.Contains(content, "\"edits\"") {
		return nil
	}
	type wire struct {
		Edits map[string][]evaluator.FixEdit `json:"edits"`
	}
	try := func(s string) map[string][]evaluator.FixEdit {
		var w wire
		if err := json.Unmarshal([]byte(s), &w); err != nil {
			// Same control-character repair the whole-file path needs: a model quoting source into
			// a JSON string routinely emits real newlines.
			repaired := repairRawControlCharsInJSONStrings(s)
			if repaired == s {
				return nil
			}
			if err := json.Unmarshal([]byte(repaired), &w); err != nil {
				return nil
			}
		}
		out := map[string][]evaluator.FixEdit{}
		for path, edits := range w.Edits {
			p := strings.TrimSpace(path)
			if p == "" {
				continue
			}
			var kept []evaluator.FixEdit
			for _, e := range edits {
				if strings.TrimSpace(e.Find) == "" {
					continue
				}
				kept = append(kept, e)
			}
			if len(kept) > 0 {
				out[p] = kept
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if got := try(content); got != nil {
		return got
	}
	// The model may have wrapped the object in a fence or prose.
	if obj := extractJSONObject(content); obj != "" && obj != content {
		return try(obj)
	}
	return nil
}

// flatFixEdit is one item of the FLAT edits shape the contract does not ask for but models return
// anyway: {"edits": [{"find": …, "replace": …}]} — the path level dropped, sometimes with a
// per-item path field instead. Run api-eb300211385b9616dc6cf81bd513369b ended on two consecutive
// rounds of exactly this reply being classified not_json: valid JSON, obvious intent, zero
// tolerance in the parser.
type flatFixEdit struct {
	Path     string `json:"path"`
	File     string `json:"file"`
	FilePath string `json:"file_path"`
	Filename string `json:"filename"`
	Find     string `json:"find"`
	Replace  string `json:"replace"`
}

// parseFlatFixEdits extracts the flat-array edits shape, or nil when the response is not in it.
// Same extraction ladder as parseFixEdits: raw, control-char-repaired, then the first JSON object
// in prose or a fence. The two shapes cannot be confused: "edits" is an object in one and an array
// in the other, and encoding/json rejects the mismatch.
func parseFlatFixEdits(content string) []flatFixEdit {
	content = strings.TrimSpace(content)
	if content == "" || !strings.Contains(content, "\"edits\"") {
		return nil
	}
	type wire struct {
		Edits []flatFixEdit `json:"edits"`
	}
	try := func(s string) []flatFixEdit {
		var w wire
		if err := json.Unmarshal([]byte(s), &w); err != nil {
			repaired := repairRawControlCharsInJSONStrings(s)
			if repaired == s {
				return nil
			}
			if err := json.Unmarshal([]byte(repaired), &w); err != nil {
				return nil
			}
		}
		var kept []flatFixEdit
		for _, e := range w.Edits {
			if strings.TrimSpace(e.Find) == "" {
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			return nil
		}
		return kept
	}
	if got := try(content); got != nil {
		return got
	}
	if obj := extractJSONObject(content); obj != "" && obj != content {
		return try(obj)
	}
	return nil
}

// resolveFlatEditTarget names the one artifact a pathless edit can apply to, or "" when no single
// answer exists. Only writable artifacts participate: an anchor that matches a dependency must not
// pull a write toward a file the evaluator would refuse anyway.
func resolveFlatEditTarget(req evaluator.FixRequest, e flatFixEdit) string {
	if hint := e.pathHint(); hint != "" {
		if p := matchArtifactPath(req.ArtifactPaths, hint); p != "" {
			return p
		}
		// A path the artifact set cannot explain is kept as claimed: downstream gating decides
		// writability, and rewriting an explicit claim would be the guess this function refuses.
		return filepath.ToSlash(strings.TrimPrefix(hint, "./"))
	}
	if len(req.ArtifactPaths) == 1 {
		return strings.TrimSpace(req.ArtifactPaths[0])
	}
	// Anchor search over the prompt copies the model quoted from.
	match := ""
	for _, p := range req.ArtifactPaths {
		body, ok := req.Files[p]
		if !ok {
			continue
		}
		if strings.Contains(body, e.Find) {
			if match != "" {
				return ""
			}
			match = p
		}
	}
	if match != "" {
		return match
	}
	// Whitespace-tolerant pass, mirroring ApplyFixEdits' relaxed retry: models reflow indentation
	// when quoting an anchor, and that alone should not orphan an otherwise-resolvable edit.
	target := normalizeAnchorWhitespace(e.Find)
	if strings.TrimSpace(target) == "" {
		return ""
	}
	for _, p := range req.ArtifactPaths {
		body, ok := req.Files[p]
		if !ok {
			continue
		}
		if strings.Contains(normalizeAnchorWhitespace(body), target) {
			if match != "" {
				return ""
			}
			match = p
		}
	}
	return match
}

// pathHint returns the item's own path claim, or "" when it carries none.
func (e flatFixEdit) pathHint() string {
	for _, p := range []string{e.Path, e.File, e.FilePath, e.Filename} {
		if strings.TrimSpace(p) != "" {
			return strings.TrimSpace(p)
		}
	}
	return ""
}

// normalizeAnchorWhitespace joins per-line-trimmed lines, the same normalisation
// evaluator.ApplyFixEdits retries an unmatched anchor with.
func normalizeAnchorWhitespace(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimSpace(ln)
	}
	return strings.Join(lines, "\n")
}

// matchArtifactPath resolves a model-claimed path against the artifact set: exact (slash- and
// ./-normalised) first, then unique basename — models routinely emit "OwnerTests.java" for
// "src/test/java/…/OwnerTests.java". Returns "" when the claim matches nothing or is ambiguous.
func matchArtifactPath(artifacts []string, claim string) string {
	c := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(claim), "./"))
	if c == "" {
		return ""
	}
	for _, a := range artifacts {
		if filepath.ToSlash(strings.TrimSpace(a)) == c {
			return strings.TrimSpace(a)
		}
	}
	base := c
	if i := strings.LastIndex(c, "/"); i >= 0 {
		base = c[i+1:]
	}
	match := ""
	for _, a := range artifacts {
		if strings.HasSuffix(filepath.ToSlash(a), "/"+base) || filepath.ToSlash(a) == base {
			if match != "" {
				return ""
			}
			match = strings.TrimSpace(a)
		}
	}
	return match
}

// parseFixEditsAnyShape accepts both edits shapes: the contract's path-keyed map, and the flat
// array recovered by resolving each edit to the artifact it can only belong to. Returns nil when
// neither shape yields at least one edit with a resolvable target, so the caller falls through to
// the whole-file chain exactly as before.
//
// Resolution is recovery, not guessing — an edit is assigned only when the target is unambiguous:
//
//  1. the item names a path itself (normalised against the artifact set);
//  2. exactly one artifact is in scope, so there is nothing to choose (the same reasoning that
//     makes singleFilePlainFallback safe);
//  3. the find anchor occurs in exactly one in-scope artifact's prompt copy — first verbatim, then
//     under the same per-line whitespace normalisation ApplyFixEdits itself retries with.
//
// Anything still ambiguous is dropped and counted; ApplyFixEdits later re-judges every kept edit
// against the bytes on disk, so a wrong assignment cannot silently corrupt a file — the anchor
// simply fails to match and the refusal is reported per edit.
func (f *Fixer) parseFixEditsAnyShape(ctx context.Context, req evaluator.FixRequest, content string) map[string][]evaluator.FixEdit {
	if edits := parseFixEdits(content); edits != nil {
		return edits
	}
	flat := parseFlatFixEdits(content)
	if flat == nil {
		return nil
	}
	resolved := make(map[string][]evaluator.FixEdit)
	dropped := 0
	for _, e := range flat {
		target := resolveFlatEditTarget(req, e)
		if target == "" {
			dropped++
			continue
		}
		resolved[target] = append(resolved[target], evaluator.FixEdit{Find: e.Find, Replace: e.Replace})
	}
	if len(resolved) == 0 {
		return nil
	}
	if f.Audit != nil {
		files := make([]string, 0, len(resolved))
		for p := range resolved {
			files = append(files, p)
		}
		sort.Strings(files)
		f.Audit.Log(ctx, "llmfix.edits_array_recovered", map[string]interface{}{
			"message": fmt.Sprintf("Recovered %d targeted edit(s) from a flat {\"edits\": [...]} array by resolving each to its only possible artifact (%d dropped as unresolvable).",
				countFlatEdits(resolved), dropped),
			"files":   files,
			"dropped": dropped,
		})
	}
	return resolved
}

// plainFallbackTarget names the one artifact a plain-source recovery may ask for, and how the
// choice was made, or ("", "") when no single answer exists. Resolution is recovery, not guessing:
// every rung requires exactly ONE artifact to qualify, and zero or several refuse.
//
//  1. single_artifact_scope — one artifact in scope; nothing to choose (the original rule).
//  2. reply_named_artifact  — the model's own failed replies mention exactly one in-scope artifact
//     by repo-relative path or file basename: the strongest evidence of which file it was fixing.
//  3. error_named_artifact  — the failure output names exactly one in-scope artifact; javac
//     diagnostics and surefire stack frames both carry File.java positions.
//
// Mentions match the full path or the ".java"-style basename only — never the bare class name,
// which is a substring of too many method and helper names to be evidence.
func plainFallbackTarget(req evaluator.FixRequest, assistant1, assistant2 string) (path, how string) {
	if len(req.ArtifactPaths) == 1 {
		if p := strings.TrimSpace(req.ArtifactPaths[0]); p != "" {
			return p, "single_artifact_scope"
		}
		return "", ""
	}
	if p := uniqueArtifactMentioned(req.ArtifactPaths, assistant1+"\n"+assistant2); p != "" {
		return p, "reply_named_artifact"
	}
	if p := uniqueArtifactMentioned(req.ArtifactPaths, req.ErrorOutput); p != "" {
		return p, "error_named_artifact"
	}
	return "", ""
}

// classifyFixParseFailure labels *why* a fixer response could not be parsed, so a post-mortem can
// distinguish a provider output cap (truncated_json — retry with a bigger budget or fewer files)
// from a model that ignored the format entirely (not_json — a prompt/compliance problem). The two
// were previously indistinguishable in the audit trail, which is why the C# run's failure read as
// "invalid JSON" with no indication of what to change.
func classifyFixParseFailure(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return "empty_response"
	}
	if !strings.HasPrefix(t, "{") && !strings.Contains(t, "{") {
		return "not_json"
	}
	// Valid JSON in the flat edits shape whose targets could not be resolved is neither not_json
	// nor truncated: the model followed the edits idea and dropped the path level. Reporting it as
	// not_json cost run api-eb300211385b9616dc6cf81bd513369b its diagnosis — the audit said "not
	// JSON" about a reply that was nothing but JSON.
	if parseFlatFixEdits(t) != nil {
		return "edits_array_unresolved"
	}
	depth := 0
	inStr := false
	esc := false
	for _, r := range t {
		switch {
		case esc:
			esc = false
		case r == '\\' && inStr:
			esc = true
		case r == '"':
			inStr = !inStr
		case inStr:
			// no-op: braces inside strings do not affect depth
		case r == '{':
			depth++
		case r == '}':
			depth--
		}
	}
	if inStr || depth > 0 {
		return "truncated_json"
	}
	return "not_json"
}

// partitionArtifactsBySize splits req.ArtifactPaths into those that can be emitted in FULL within
// maxArtifactRunes and those that cannot. Callers must drop the oversized set from the round: it is
// never valid to show a windowed artifact while asking for full file content back.
//
// Paths with no matching entry in req.Files are kept — there is nothing to measure, and the
// existing emit loop already skips them.
func partitionArtifactsBySize(req evaluator.FixRequest, maxArtifactRunes int) (keep, oversized []string) {
	if maxArtifactRunes <= 0 {
		maxArtifactRunes = maxArtifactFileRunes
	}
	type artifact struct {
		path  string
		size  int
		cited bool
	}
	arts := make([]artifact, 0, len(req.ArtifactPaths))
	for _, ap := range req.ArtifactPaths {
		apKey := evaluator.NormalizeRepoRelPath(ap)
		size := -1
		for path, content := range req.Files {
			if evaluator.NormalizeRepoRelPath(path) == apKey {
				size = len([]rune(content))
				break
			}
		}
		if size > maxArtifactRunes {
			oversized = append(oversized, apKey)
			continue
		}
		arts = append(arts, artifact{path: ap, size: size, cited: strings.Contains(req.ErrorOutput, apKey)})
	}

	// The SUM is bounded too, not only each artifact. Selection order mirrors rankDependencyPaths'
	// reasoning: artifacts the error log actually blames go first — they are what this round is
	// FOR — then smaller before larger so the round keeps as many as fit. Withheld ones get the
	// standard note and write-guard, and their errors recur next round against a smaller set.
	sort.SliceStable(arts, func(i, j int) bool {
		if arts[i].cited != arts[j].cited {
			return arts[i].cited
		}
		return arts[i].size < arts[j].size
	})
	total := 0
	for _, a := range arts {
		if len(keep) > 0 && a.size > 0 && total+a.size > maxArtifactTotalRunes {
			oversized = append(oversized, evaluator.NormalizeRepoRelPath(a.path))
			continue
		}
		if a.size > 0 {
			total += a.size
		}
		keep = append(keep, a.path)
	}
	// Restore the request's artifact order for the kept set: prompt assembly and response matching
	// both index into it, and a reordered keep list would silently change which artifact is
	// "first" for the single-artifact fallback.
	orderIdx := make(map[string]int, len(req.ArtifactPaths))
	for i, ap := range req.ArtifactPaths {
		orderIdx[ap] = i
	}
	sort.SliceStable(keep, func(i, j int) bool { return orderIdx[keep[i]] < orderIdx[keep[j]] })
	sort.Strings(oversized)
	return keep, oversized
}

// writeWithheldArtifactNote tells the model, in the prompt, that a file it may see referenced in
// the error log is deliberately absent and must not be emitted. Silence here invites the model to
// reconstruct the file from the error log alone.
func writeWithheldArtifactNote(b *strings.Builder, oversized []string) {
	if len(oversized) == 0 {
		return
	}
	b.WriteString("=== WITHHELD ARTIFACTS (do NOT output these paths) ===\n")
	b.WriteString("The following generated test file(s) are too large to include in full, and this response format requires full file content. They are NOT shown below and are NOT yours to fix in this round. Do not guess at their contents and do not include them in your JSON output:\n")
	for _, p := range oversized {
		b.WriteString("  - " + p + "\n")
	}
	b.WriteString("\n")
}

func countFlatEdits(m map[string][]evaluator.FixEdit) int {
	n := 0
	for _, l := range m {
		n += len(l)
	}
	return n
}

// uniqueArtifactMentioned returns the one artifact the text mentions — by slash-normalised
// repo-relative path or by file basename — or "" when zero or several are mentioned. Two artifacts
// sharing a basename that the text names only by basename count as several.
func uniqueArtifactMentioned(artifacts []string, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	match := ""
	for _, a := range artifacts {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		slashed := filepath.ToSlash(a)
		base := slashed
		if i := strings.LastIndex(slashed, "/"); i >= 0 {
			base = slashed[i+1:]
		}
		if !strings.Contains(text, slashed) && (base == "" || !strings.Contains(text, base)) {
			continue
		}
		if match != "" {
			return ""
		}
		match = a
	}
	return match
}

// maxArtifactFileRunes is the ceiling for an ARTIFACT emitted in full. Artifacts are never

// maxArtifactFileRunes is the ceiling for an ARTIFACT emitted in full. Artifacts are never
// error-localized: targeted edits need exact anchors copied from the real file, and the
// whole-file FALLBACK asks for the full corrected content — either way, showing the model a
// window means asking it to work from lines it was never given. For the fallback it reconstructs
// the remainder from memory and silently truncates or invents the tail — which lands on disk as a
// syntactically broken file ("class, interface, enum, or record expected" at the last line).
//
// Past this ceiling an artifact cannot participate in a full-file contract at all. It is WITHHELD
// from the prompt and from the writable set for the round rather than windowed; see
// partitionArtifactsBySize. Anchored-edit responses (which scale with the edit, not the file) are
// the real fix for those; this constant only bounds what full-file mode will attempt.
const maxArtifactFileRunes = 120000

// maxArtifactTotalRunes bounds the SUM of artifacts shown in one fix round. The per-artifact
// ceiling above assumed one artifact; a project-level fix round carries several, and three
// mid-sized artifacts assembled a 136k-rune prompt into a 32768-token window (run api-55c3eafd…).
// Ollama then silently dropped the FRONT of the prompt — the system prompt, the output contract
// and the tool definitions — so the round could neither call tools nor honour the contract, and
// presented as "fixer_response_unusable" rather than as what it was.
//
// Artifacts past the ceiling are WITHHELD for the round with the existing semantics (named in the
// prompt, excluded from the writable set), not windowed — the full-file contract still holds for
// what remains, and the withheld artifacts get their own later round against whatever errors
// survive. 60000 runes ≈ 15-18k tokens, leaving the default section budgets and the tool
// definitions comfortable room inside a 32k window.
const maxArtifactTotalRunes = 60000

// singleFilePlainFallback asks for one file's raw content when JSON parsing has failed twice and
// exactly one artifact can be the target, then synthesises the path→content map.
//
// "Can be the target" is resolved by plainFallbackTarget: a single artifact in scope is the
// original, trivially safe case; with a wider scope, the one artifact the failed replies or the
// failure output unambiguously name serves the same role. The generalisation exists because the
// fallback saved two compile rounds of run api-eb300211385b9616dc6cf81bd513369b and then sat
// disabled while the run died in a four-artifact test scope — the proven recovery gated on a
// stricter condition than recovery actually needs.
//
// The synthesised content is gated on evaluator.SyntacticShellReason so a prose or fenced reply
// cannot be laundered into a write by this path — the whole point of the fallback is to recover a
// real repair, not to lower the bar for accepting one.
// budget is this Fix call's shared tool allowance (see CP52): the fallback is another completion
// for the SAME question, so its lookups draw from the same budget rather than starting fresh.
func (f *Fixer) singleFilePlainFallback(ctx context.Context, req evaluator.FixRequest, repairBase []model.Message, assistant1, assistant2 string, budget *tools.RunBudget) (map[string]string, bool) {
	path, how := plainFallbackTarget(req, assistant1, assistant2)
	if path == "" {
		return nil, false
	}
	msgs := make([]model.Message, 0, len(repairBase)+3)
	msgs = append(msgs, repairBase...)
	msgs = append(msgs,
		model.Message{Role: "assistant", Content: assistant2},
		model.Message{Role: "user", Content: "Forget the JSON format. Reply with ONLY the complete corrected contents of " + path +
			" as plain source code. No JSON, no markdown fences, no explanation — the first character of your reply must be the first character of the file."},
	)
	res, err := f.completeWithRetry(ctx, msgs, f.fixCompleteOpts(false, req), budget)
	if err != nil || res == nil {
		return nil, false
	}
	// Stripped again here, not only at the provider boundary. This is the system's ONLY plain-text
	// contract and the gate below rejects a <think> prefix by name, so a provider, proxy or stub
	// that does not strip would silently make the whole recovery unreachable — which is precisely
	// the failure this path was found in. StripReasoningBlock is idempotent, so the second call
	// costs nothing on content the client already cleaned.
	stripped, thought := model.StripReasoningBlock(res.Content)
	content := strings.TrimSpace(stripped)
	if content == "" {
		return nil, false
	}
	if reason := evaluator.SyntacticShellReason(path, content); reason != "" {
		if f.Audit != nil {
			f.Audit.Log(ctx, "llmfix.single_file_fallback_rejected", map[string]interface{}{
				"message": fmt.Sprintf("Plain-source recovery for %s was refused: %s. The reply was not a usable file body.", path, reason),
				"path":    path,
				"reason":  reason,
			})
		}
		return nil, false
	}
	if f.Audit != nil {
		f.Audit.Log(ctx, "llmfix.single_file_fallback_used", map[string]interface{}{
			"message":         fmt.Sprintf("Recovered a fix from a plain-source reply after two unparseable JSON attempts; target %s resolved by %s.", path, describeFallbackHow(how)),
			"path":            path,
			"how":             how,
			"reasoning_runes": thought,
		})
	}
	return map[string]string{path: content}, true
}

func describeFallbackHow(how string) string {
	switch how {
	case "single_artifact_scope":
		return "the only artifact in scope"
	case "reply_named_artifact":
		return "the one in-scope artifact the failed replies name"
	case "error_named_artifact":
		return "the one in-scope artifact the failure output names"
	}
	return how
}

// writeAbsentSymbolsBlock states the negative the classpath scan proved: names it looked up and did
// not find.
//
// Separate block from the API surface because it is a different claim. The surface says "here is
// where this type lives"; this says "this name is on no classpath entry, so no import can make it
// resolve." Without it the prompt simply omitted the misses, and an omission reads as an oversight
// rather than as evidence — which is why MockBean survived ten rounds of repair in run
// api-0c344e6bc0658e0db06506efb9d964f5 with its own diagnostic in the prompt every time.
func writeAbsentSymbolsBlock(b *strings.Builder, req evaluator.FixRequest) {
	if len(req.AbsentSymbols) == 0 {
		return
	}
	b.WriteString("=== VERIFIED ABSENT (looked up on the compile classpath and NOT found) ===\n")
	b.WriteString("No import makes these resolve — they are not on any classpath entry of this project. " +
		"Do not import, reference or re-introduce them; delete the code that uses them, or express it with a type that this prompt shows is declared.\n")
	for _, n := range req.AbsentSymbols {
		b.WriteString("- " + n + "\n")
	}
	b.WriteString("\n")
}

// writeTestFailureFactsBlock renders the runtime-verified facts for test-step failures — today,
// Mockito misuse proved from the failure's stack frames plus the generated test's own source
// (evaluator/fix_mockito_facts.go). A separate block from the compiler-verified one above so
// neither header overclaims its provenance. It exists because the motivating run repaired around
// `when()` on a non-mock for six rounds with the exception text in the prompt every time: the
// model needed the misuse NAMED, not merely shown.
func writeTestFailureFactsBlock(b *strings.Builder, req evaluator.FixRequest) {
	if len(req.TestFailureFacts) == 0 {
		return
	}
	b.WriteString("=== TEST FAILURE FACTS (verified from the failing run and this test class's source; treat as ground truth) ===\n")
	for _, f := range req.TestFailureFacts {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString("\n")
}
