package evaluator

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Per-file progress, which is the measurement the fix loop was missing.
//
// The loop has three breakers and none of them can see a file that was rewritten to no effect:
//
//   - the primary-site guard measures ONE file, the one the first diagnostic blames;
//   - the reachability breaker asks only whether ANY file named in the output was written;
//   - the repeat-fingerprint breaker compares the WHOLE output, and only after the next compile,
//     at which point it is terminal.
//
// Run api-e0982497f502f5daf4aa64b4555c7ffa fell straight through the middle of that. Two rounds
// each rewrote VetControllerE2EIT.java and VetsTests.java; their diagnostics came back byte for
// byte, same lines (33/57/95/118 and 53), and both rounds were recorded as applied successes. The
// only thing that ever noticed was the whole-output fingerprint on the third compile, which ended
// the run at iteration 2 of a 20-round budget.
//
// This reports, and it tells the model. It deliberately blocks nothing: the primary-site guard
// already owns the one case where reverting is justified, and a second gate that discards writes
// is how a loop starts losing correct repairs.

// FileDiagnostics fingerprints the diagnostics attributed to each file in a compiler output.
//
// Attribution follows the same rule ParsePrimaryFailureSite uses: a diagnostic owns the text from
// its own `path:[line,col]` location up to the next one, which is what keeps javac's indented
// `symbol:` / `location:` detail lines with the diagnostic they belong to. Fingerprints rather than
// text so a caller cannot accidentally grow the prompt with them, and so comparison is exact.
func FileDiagnostics(errorOutput string) map[string]string {
	if strings.TrimSpace(errorOutput) == "" {
		return nil
	}
	buckets := map[string][]string{}
	rest := errorOutput
	offset := 0
	for {
		m := primaryDiagnosticRE.FindStringSubmatchIndex(rest[offset:])
		if m == nil {
			break
		}
		start := offset + m[0]
		path := normalizePathForFix(rest[offset+m[2] : offset+m[3]])
		end := len(rest)
		if next := primaryDiagnosticRE.FindStringIndex(rest[offset+m[1]:]); next != nil {
			end = offset + m[1] + next[0]
		}
		if path != "" {
			buckets[path] = append(buckets[path], collapseWhitespace(rest[start:end]))
		}
		offset += m[1]
	}
	if len(buckets) == 0 {
		return nil
	}
	out := make(map[string]string, len(buckets))
	for path, diags := range buckets {
		// Sorted so a reordering of otherwise identical diagnostics is not mistaken for progress.
		sort.Strings(diags)
		sum := sha256.Sum256([]byte(strings.Join(diags, "\n")))
		out[path] = hex.EncodeToString(sum[:8])
	}
	return out
}

// stalledFiles returns the paths a round wrote whose diagnostics are unchanged since the round
// before it — the file was rewritten and the compiler said exactly the same thing about it.
//
// A file that disappeared from the output made progress (its errors are gone) and a file that
// appeared did too, in the sense that something changed; only "present before, present after,
// identical" is evidence of a wasted write. `written` is the set that actually landed on disk,
// never what the model returned, for the reason recordFixAttempt's doc gives.
func stalledFiles(before, after map[string]string, written []string) []string {
	if len(before) == 0 || len(after) == 0 || len(written) == 0 {
		return nil
	}
	var out []string
	for _, p := range written {
		prev, hadPrev := lookupDiagnostic(before, p)
		now, hasNow := lookupDiagnostic(after, p)
		if !hadPrev || !hasNow || prev != now {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// lookupDiagnostic finds a repo-relative path's entry in a map keyed by whatever path the compiler
// printed.
//
// Equality cannot do it. A containerised build reports "/workspace/src/test/…", which shares no
// prefix with the repo-relative path the fixer writes, so a map lookup answers "this file has no
// diagnostics" for every file in a Docker run — the same trap sameDiagnosticFile was written for,
// and the reason it is reused here rather than reimplemented.
func lookupDiagnostic(diagnostics map[string]string, repoRel string) (string, bool) {
	if v, ok := diagnostics[normalizePathForFix(repoRel)]; ok {
		return v, true
	}
	for diagPath, v := range diagnostics {
		if sameDiagnosticFile(diagPath, repoRel) {
			return v, true
		}
	}
	return "", false
}

// noteFileNoProgress annotates the most recent attempt record so RenderFixAttemptMemory can tell
// the model which of its last edits achieved nothing.
//
// The annotation lands on the PREVIOUS round's record because that is the round being judged: the
// evidence only exists after the next compile has run.
func noteFileNoProgress(loopState *FixLoopState, paths []string) {
	if loopState == nil || len(paths) == 0 || len(loopState.attempts) == 0 {
		return
	}
	rec := &loopState.attempts[len(loopState.attempts)-1]
	if rec.NoProgress == nil {
		rec.NoProgress = map[string]bool{}
	}
	for _, p := range paths {
		rec.NoProgress[p] = true
	}
}
