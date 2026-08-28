package evaluator

import (
	"fmt"
	"strings"
)

// Compacted memory of what the fixer already tried, and what it got for it.
//
// llmfix reports `multi_turn: true` in every audit row, but that field echoes a config flag rather
// than describing behaviour. Its retention drops message PAIRS while the stored conversation
// exceeds maxMultiTurnConvRunes (64k), and a single fix prompt in the run this was built for was
// 141–147k runes — over twice the whole budget. Both messages were therefore dropped after every
// round, including the smallest, so the follow-up branch never executed and every round was a fresh
// stateless prompt. Rounds 3 and 4 produced byte-identical compiler output, which is exactly what
// asking an identical question with no memory should produce.
//
// Raising the conversation budget would ship 145k runes of static context back to the provider on
// every round — the cost multi-turn exists to avoid. Instead each round contributes a few hundred
// runes describing what changed and what happened next, which is the part the model actually needs
// ("you replaced hasURLContaining with X last round and the identical error came back").
//
// The diff recorded is of what was APPLIED, never what the model returned. Those differ after
// path-scope gating, low-value rejection, the coverage gate, and format-after-fix. A record
// claiming an edit that never reached disk would be worse than no record: it would teach the model
// that an approach was tried and failed when it was never tried at all.

// skipReasonReturnedUnchanged is the Skipped-map reason for a candidate write that was
// byte-identical to the file on disk. It is matched in render() to swap the neutral
// "NOT applied" wording for an explicit do-not-repeat instruction.
const skipReasonReturnedUnchanged = "returned the file unchanged"

const (
	// maxAttemptRecordRunes bounds one round's record.
	maxAttemptRecordRunes = 2000
	// maxAttemptMemoryRunes bounds the whole history handed to one prompt. Oldest rounds are
	// dropped first — the most recent attempt is the one the model is about to repeat.
	maxAttemptMemoryRunes = 8000
	// maxDiffLinesPerFile bounds the per-file change excerpt inside a record.
	maxDiffLinesPerFile = 12
)

// FixAttemptRecord is one completed fixer round.
type FixAttemptRecord struct {
	// Iteration is the eval iteration this round belonged to (0-based), for wording only.
	Iteration int
	// FailureSignature identifies the failure this round was trying to repair. Two consecutive
	// records with the same signature mean the round changed nothing that mattered.
	FailureSignature string
	// Changes maps repo-relative path -> compact change excerpt of what actually landed on disk.
	Changes map[string]string
	// Skipped lists paths the model returned that were not written, with the reason.
	Skipped map[string]string
	// Note records a round-level outcome that no per-path entry can express: the model's reply
	// never became a candidate write at all.
	//
	// Two rounds of run api-0c344e6bc0658e0db06506efb9d964f5 ended that way — one on unparseable
	// JSON, one on an edit set whose every anchor missed — and both returned before recordFixAttempt
	// ran, so the memory handed to the next prompt skipped them entirely and prior_attempts stalled
	// (3→3 across attempts 4→5, 6→6 across attempts 8→9). The model was then asked the same question
	// with no indication its previous answer had been unusable.
	Note string
	// NoProgress marks paths this round DID write and whose compiler diagnostics came back
	// unchanged afterwards. Filled in one round late by noteFileNoProgress, because the evidence
	// does not exist until the next compile has run.
	//
	// It is the per-file counterpart of the failure signature above: that one says the whole build
	// did not move, this one says which of the round's edits were the reason. Without it a model
	// re-reading its own change excerpt has no way to tell an edit that helped from an edit that
	// was cosmetic — and in run api-e0982497f502f5daf4aa64b4555c7ffa it rewrote the same two files
	// twice on that basis.
	NoProgress map[string]bool
}

// Render turns the history into the prompt block, newest last so the model reads it as a timeline.
// Returns "" when there is nothing to say.
func RenderFixAttemptMemory(records []FixAttemptRecord) string {
	if len(records) == 0 {
		return ""
	}
	rendered := make([]string, 0, len(records))
	for _, r := range records {
		rendered = append(rendered, r.render())
	}
	// Drop oldest until the whole block fits.
	for len(rendered) > 1 && runeLen(strings.Join(rendered, "")) > maxAttemptMemoryRunes {
		rendered = rendered[1:]
	}
	var b strings.Builder
	b.WriteString("=== PRIOR ATTEMPTS (this step, this run) ===\n\n")
	b.WriteString("You have already tried the following on this failure. The compiler output below is what came back AFTER these edits were applied. Do not repeat an approach that is listed here as having produced the same failure signature.\n\n")
	for _, r := range rendered {
		b.WriteString(r)
		b.WriteString("\n")
	}
	return b.String()
}

func (r FixAttemptRecord) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- attempt %d (failure signature %s) ---\n", r.Iteration+1, shortSignature(r.FailureSignature))
	if strings.TrimSpace(r.Note) != "" {
		fmt.Fprintf(&b, "  %s\n", r.Note)
	}
	if len(r.Changes) == 0 && strings.TrimSpace(r.Note) == "" {
		b.WriteString("  (no file was changed)\n")
	}
	for _, p := range sortedMapKeys(r.Changes) {
		if r.NoProgress[p] {
			fmt.Fprintf(&b, "  %s: NO EFFECT — you rewrote this file and the compiler reported the identical diagnostics for it afterwards. Edit the exact lines the errors name, or leave the file alone.\n", p)
		} else {
			fmt.Fprintf(&b, "  %s:\n", p)
		}
		for _, ln := range strings.Split(strings.TrimRight(r.Changes[p], "\n"), "\n") {
			b.WriteString("    " + ln + "\n")
		}
	}
	for _, p := range sortedMapKeys(r.Skipped) {
		// The unchanged echo gets an explicit instruction, not just a reason. A round whose only
		// candidate was byte-identical to disk counts toward the run-scope unusable-response
		// breaker (two in a row is terminal), so the retry prompt must say outright what not to
		// repeat — otherwise the retry is a near-identical prompt and the echo is deterministic
		// (both test rounds of run api-148358c668670fd95da8c4e65afa445a).
		if r.Skipped[p] == skipReasonReturnedUnchanged {
			fmt.Fprintf(&b, "  %s: REJECTED — you returned this file byte-identical to what is on disk. Echoing a file is never a fix. Either change the content of a failing file (see the error block) or return targeted edits for it; do not return this file unchanged again.\n", p)
			continue
		}
		fmt.Fprintf(&b, "  %s: NOT applied (%s)\n", p, r.Skipped[p])
	}
	out := b.String()
	if runeLen(out) > maxAttemptRecordRunes {
		out = string([]rune(out)[:maxAttemptRecordRunes]) + "\n    … (record truncated)\n"
	}
	return out
}

func shortSignature(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func runeLen(s string) int { return len([]rune(s)) }

// summarizeAppliedChange produces the compact excerpt stored for one applied write.
//
// Not a real unified diff: the point is to remind the model what it altered, and a full diff of a
// 300-line test file would blow the record budget on context the model already has (the current
// file content is in the prompt). Line-level added/removed sets, capped, carry the signal.
func summarizeAppliedChange(before, after string) string {
	if strings.TrimSpace(before) == "" {
		return "(new file)"
	}
	beforeSet := map[string]int{}
	for _, ln := range strings.Split(before, "\n") {
		beforeSet[strings.TrimSpace(ln)]++
	}
	afterSet := map[string]int{}
	for _, ln := range strings.Split(after, "\n") {
		afterSet[strings.TrimSpace(ln)]++
	}
	var added, removed []string
	for _, ln := range strings.Split(after, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if beforeSet[t] < afterSet[t] {
			added = append(added, "+ "+t)
			beforeSet[t]++
		}
	}
	for _, ln := range strings.Split(before, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		if afterSet[t] < beforeSet[t] {
			removed = append(removed, "- "+t)
			afterSet[t]++
		}
	}
	lines := append(removed, added...)
	if len(lines) == 0 {
		return "(rewritten with no net line change)"
	}
	truncated := false
	if len(lines) > maxDiffLinesPerFile {
		lines = lines[:maxDiffLinesPerFile]
		truncated = true
	}
	out := strings.Join(lines, "\n")
	if truncated {
		out += "\n… (change excerpt truncated)"
	}
	return out
}

// priorAttempts returns the memory accumulated for this step, or nil.
func priorAttempts(loopState *FixLoopState) []FixAttemptRecord {
	if loopState == nil || len(loopState.attempts) == 0 {
		return nil
	}
	return append([]FixAttemptRecord(nil), loopState.attempts...)
}

// recordFixAttempt appends one completed round to the step's memory.
//
// Bounded here rather than at render time so a long loop cannot grow the slice without limit; the
// renderer trims again against the prompt budget, which is the tighter of the two.
func recordFixAttempt(loopState *FixLoopState, iteration int, signature string, changes, skipped map[string]string) {
	if loopState == nil {
		return
	}
	if len(changes) == 0 && len(skipped) == 0 {
		return
	}
	rec := FixAttemptRecord{
		Iteration:        iteration,
		FailureSignature: signature,
		Changes:          copyStringMap(changes),
		Skipped:          copyStringMap(skipped),
	}
	loopState.attempts = append(loopState.attempts, rec)
	const maxRememberedAttempts = 8
	if len(loopState.attempts) > maxRememberedAttempts {
		loopState.attempts = loopState.attempts[len(loopState.attempts)-maxRememberedAttempts:]
	}
}

// recordFixRoundFailure banks a round whose reply never produced a candidate write.
//
// Separate from recordFixAttempt because the two carry different evidence and the guard differs: a
// round with no changes and no skipped paths is nothing to remember, while a round that failed as a
// WHOLE is the single most useful thing the next prompt can be told — it is the case most likely to
// repeat, and the one the model has no other way to learn about.
func recordFixRoundFailure(loopState *FixLoopState, iteration int, signature, note string) {
	if loopState == nil || strings.TrimSpace(note) == "" {
		return
	}
	loopState.attempts = append(loopState.attempts, FixAttemptRecord{
		Iteration:        iteration,
		FailureSignature: signature,
		Note:             note,
	})
	const maxRememberedAttempts = 8
	if len(loopState.attempts) > maxRememberedAttempts {
		loopState.attempts = loopState.attempts[len(loopState.attempts)-maxRememberedAttempts:]
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
