package evaluator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Application of targeted search/replace edits.
//
// The point of this file is that every outcome is explicit. A whole-file rewrite has exactly one
// observable result — "the model returned a file" — which is true whether it fixed the defect,
// ignored it, or broke something else. An edit has three, and the caller can act on each:
//
//   - applied      : the anchor matched and the content changed
//   - not matched  : the anchor is not in the file, so the model was working from a stale or
//                    imagined version; the round produced nothing for that file and must say so
//   - ambiguous    : the anchor appears more than once, so applying it would edit an arbitrary
//                    occurrence — refused rather than guessed
//
// The run that motivated this spent seven rounds with `Set<Visit> initialVisits = pet.getVisits();`
// unchanged at line 32 while the audit recorded a successful fix to that file every round.

// FixEditOutcome records what happened to one edit.
type FixEditOutcome struct {
	Edit    FixEdit
	Applied bool
	// Reason is empty when Applied; otherwise it explains why the edit was refused.
	Reason string
}

// ApplyFixEdits applies edits to content in order, returning the new content and a per-edit
// outcome. An edit that cannot be applied leaves the content untouched and is reported; the
// remaining edits are still attempted, because one bad anchor should not discard a round's other
// correct repairs.
//
// Byte-identical edits are one logical intent, not N accidents. The model expresses "replace each
// occurrence" by repeating the same {find, replace} once per site — run
// api-eb300211385b9616dc6cf81bd513369b sent `nullValue(result);` four times against an anchor
// occurring four times, and refusing each as ambiguous left the file half-fixed for two more
// rounds. When the number of identical edits equals the occurrence count there is nothing
// arbitrary left to refuse: every occurrence was asked for, so every occurrence is replaced. A
// count MISMATCH still refuses (which sites the model meant is genuinely unknowable), as does the
// same anchor arriving with different replacements (which occurrence gets which is arbitrary).
func ApplyFixEdits(content string, edits []FixEdit) (string, []FixEditOutcome) {
	// Group identical pairs and detect conflicting replacements per anchor before applying
	// anything: both judgements are about the SET of edits, not about one edit at a time.
	identical := make(map[FixEdit]int, len(edits))
	replacesForFind := make(map[string]map[string]bool, len(edits))
	for _, e := range edits {
		identical[e]++
		if strings.TrimSpace(e.Find) == "" {
			continue
		}
		if replacesForFind[e.Find] == nil {
			replacesForFind[e.Find] = map[string]bool{}
		}
		replacesForFind[e.Find][e.Replace] = true
	}
	groupApplied := make(map[FixEdit]bool)

	outcomes := make([]FixEditOutcome, 0, len(edits))
	for _, e := range edits {
		find := e.Find
		if groupApplied[e] {
			// A duplicate of an intent already satisfied this round. Counting it as applied keeps
			// the audit truthful (4 edits sent, 4 applied) and keeps "anchor not found" — the
			// stale-version diagnosis — out of the next round's memory for an edit that worked.
			outcomes = append(outcomes, FixEditOutcome{Edit: e, Applied: true})
			continue
		}
		switch {
		case strings.TrimSpace(find) == "":
			outcomes = append(outcomes, FixEditOutcome{Edit: e, Reason: "empty find anchor"})
			continue
		case !strings.Contains(content, find):
			// Retry once ignoring leading/trailing whitespace per line: models routinely reflow
			// indentation when quoting an anchor, and that alone should not lose a correct repair.
			if relaxed, ok := applyWhitespaceTolerant(content, find, e.Replace); ok {
				content = relaxed
				groupApplied[e] = true
				outcomes = append(outcomes, FixEditOutcome{Edit: e, Applied: true})
				continue
			}
			outcomes = append(outcomes, FixEditOutcome{Edit: e,
				Reason: "anchor not found in the file (the model may be working from a version that does not exist)"})
			continue
		case strings.Count(content, find) > 1:
			occurrences := strings.Count(content, find)
			if len(replacesForFind[find]) > 1 {
				outcomes = append(outcomes, FixEditOutcome{Edit: e,
					Reason: fmt.Sprintf("anchor appears %d times with conflicting replacements; which occurrence gets which is arbitrary", occurrences)})
				continue
			}
			if identical[e] == occurrences {
				content = strings.ReplaceAll(content, find, e.Replace)
				groupApplied[e] = true
				outcomes = append(outcomes, FixEditOutcome{Edit: e, Applied: true})
				continue
			}
			outcomes = append(outcomes, FixEditOutcome{Edit: e,
				Reason: fmt.Sprintf("anchor appears %d times but %d identical edit(s) were supplied; refusing to pick occurrences arbitrarily", occurrences, identical[e])})
			continue
		}
		content = strings.Replace(content, find, e.Replace, 1)
		groupApplied[e] = true
		outcomes = append(outcomes, FixEditOutcome{Edit: e, Applied: true})
	}
	return content, outcomes
}

// applyWhitespaceTolerant retries a multi-line anchor with per-line leading/trailing whitespace
// normalised. Returns the updated content and whether a unique match was found.
func applyWhitespaceTolerant(content, find, replace string) (string, bool) {
	norm := func(s string) string {
		lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
		for i, ln := range lines {
			lines[i] = strings.TrimSpace(ln)
		}
		return strings.Join(lines, "\n")
	}
	target := norm(find)
	if strings.TrimSpace(target) == "" {
		return content, false
	}
	contentLines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	findLines := strings.Split(target, "\n")
	matchAt := -1
	for i := 0; i+len(findLines) <= len(contentLines); i++ {
		ok := true
		for j := range findLines {
			if strings.TrimSpace(contentLines[i+j]) != findLines[j] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if matchAt >= 0 {
			return content, false // ambiguous under the relaxed rule too
		}
		matchAt = i
	}
	if matchAt < 0 {
		return content, false
	}
	// Preserve the indentation of the first replaced line so the splice keeps the file's shape.
	indent := contentLines[matchAt][:len(contentLines[matchAt])-len(strings.TrimLeft(contentLines[matchAt], " \t"))]
	var repl []string
	for _, ln := range strings.Split(strings.ReplaceAll(replace, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(ln) == "" {
			repl = append(repl, "")
			continue
		}
		repl = append(repl, indent+strings.TrimSpace(ln))
	}
	out := append([]string{}, contentLines[:matchAt]...)
	out = append(out, repl...)
	out = append(out, contentLines[matchAt+len(findLines):]...)
	return strings.Join(out, "\n"), true
}

// DescribeEditOutcomes renders refused edits for audit and for the next round's memory.
func DescribeEditOutcomes(outcomes []FixEditOutcome) (applied int, refused []string) {
	for _, o := range outcomes {
		if o.Applied {
			applied++
			continue
		}
		anchor := strings.TrimSpace(o.Edit.Find)
		if len(anchor) > 60 {
			anchor = anchor[:60] + "…"
		}
		refused = append(refused, fmt.Sprintf("%q: %s", anchor, o.Reason))
	}
	return applied, refused
}

// resolveFixEdits turns targeted edits into whole-file content by applying them to what is on disk.
//
// Reading from disk rather than from the prompt copy is deliberate: format-after-fix and earlier
// rounds may have moved the file, and an edit must be judged against the bytes it will actually
// modify. A file whose edits all fail contributes nothing and is left out of the result, which is
// what lets the caller distinguish "the model repaired nothing" from "the model returned nothing".
//
// The third return carries WHY such a file was left out, keyed by path. Without it those files were
// invisible to everything downstream: they never reach the write gates, so they land in neither
// appliedChanges nor skippedPaths, and stalledFiles only inspects paths that were WRITTEN — so the
// no-progress annotation cannot cover them either. In run api-0c344e6bc0658e0db06506efb9d964f5 all
// four edits to VetControllerE2EIT.java were refused for anchors that do not exist in the file, the
// model was told none of it, and the next round's diagnostics came back byte-identical.
func resolveFixEdits(opts EvalOptions, edits map[string][]FixEdit) (map[string]string, map[string]interface{}, map[string]string) {
	resolved := make(map[string]string, len(edits))
	auditByFile := make(map[string]interface{}, len(edits))
	refusedByPath := make(map[string]string, len(edits))
	for rel, list := range edits {
		clean := normalizePathForFix(rel)
		full := filepath.Join(opts.RepoPath, filepath.FromSlash(clean))
		before, err := os.ReadFile(full)
		if err != nil {
			auditByFile[clean] = map[string]interface{}{"error": fmt.Sprintf("cannot read: %v", err)}
			refusedByPath[clean] = fmt.Sprintf("could not be read from disk: %v", err)
			continue
		}
		after, outcomes := ApplyFixEdits(string(before), list)
		applied, refused := DescribeEditOutcomes(outcomes)
		entry := map[string]interface{}{"edits": len(list), "applied": applied}
		if len(refused) > 0 {
			entry["refused"] = refused
		}
		auditByFile[clean] = entry
		switch {
		case applied == 0:
			refusedByPath[clean] = describeWhollyRefusedEdits(len(list), refused)
			continue
		case after == string(before):
			// No net change: recording this as a repair is how a stalled loop looked productive.
			refusedByPath[clean] = fmt.Sprintf("%d edit(s) applied but produced a file byte-identical to disk", applied)
			continue
		}
		resolved[clean] = after
	}
	return resolved, auditByFile, refusedByPath
}

// maxRememberedEditRefusals bounds how many refusal reasons one path contributes to the next
// prompt. The anchors are the actionable part — they show the model which text it believed was in
// the file — but a round can refuse a dozen and the memory block has a budget.
const maxRememberedEditRefusals = 2

func describeWhollyRefusedEdits(total int, refused []string) string {
	if len(refused) == 0 {
		return fmt.Sprintf("all %d targeted edit(s) were refused", total)
	}
	shown := refused
	suffix := ""
	if len(shown) > maxRememberedEditRefusals {
		shown = shown[:maxRememberedEditRefusals]
		suffix = fmt.Sprintf(", and %d more", len(refused)-maxRememberedEditRefusals)
	}
	return fmt.Sprintf("all %d targeted edit(s) were refused — %s%s", total, strings.Join(shown, "; "), suffix)
}
