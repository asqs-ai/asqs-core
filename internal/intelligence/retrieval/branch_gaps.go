package retrieval

import (
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

var (
	reIf         = regexp.MustCompile(`\bif\s*\(`)
	reElse       = regexp.MustCompile(`\belse\b`)
	reSwitch     = regexp.MustCompile(`\bswitch\s*\(`)
	reCase       = regexp.MustCompile(`\bcase\b`)
	reDefault    = regexp.MustCompile(`\bdefault\s*:`)
	reTryCatch   = regexp.MustCompile(`\b(catch|throws?|throw)\b`)
	reNullCheck  = regexp.MustCompile(`(?i)(==\s*null|!=\s*null|\bis\s+null\b|\bnull\b)`)
	reBoundary   = regexp.MustCompile(`(?i)(<=|>=|<|>|==)\s*(0|1|-1|max|min|length|count|size|limit|timeout)`)
	reEmpty      = regexp.MustCompile(`(?i)(empty|null|blank|whitespace|zero)`)
	reErrorWords = regexp.MustCompile(`(?i)(throws?|exception|error|invalid|fail|timeout|retry)`)
)

func buildExistingTestCoverageHint(rc *RetrievalContext, hasExistingTests bool) *ExistingTestCoverageHint {
	if rc == nil {
		return nil
	}
	if !hasExistingTests && len(rc.SimilarTests) == 0 {
		return nil
	}
	targetIntents := inferTargetBranchIntents(rc)
	coveredIntents := inferCoveredBranchIntents(rc.SimilarTests)
	missing := diffIntents(targetIntents, coveredIntents)
	if len(targetIntents) == 0 && len(coveredIntents) == 0 {
		return &ExistingTestCoverageHint{HasExistingTests: hasExistingTests}
	}
	return &ExistingTestCoverageHint{
		HasExistingTests: hasExistingTests || len(rc.SimilarTests) > 0,
		CoveredIntents:   coveredIntents,
		MissingIntents:   missing,
	}
}

func inferTargetBranchIntents(rc *RetrievalContext) []string {
	if rc == nil || rc.TargetMethod == nil || rc.TargetMethod.Chunk == nil {
		return nil
	}
	return inferBranchIntentsFromContent(rc.TargetMethod.Chunk.Content)
}

func inferCoveredBranchIntents(similar []*embeddings.Chunk) []string {
	seen := make(map[string]bool)
	for _, ch := range similar {
		if ch == nil {
			continue
		}
		for _, k := range inferBranchIntentsFromContent(ch.Content) {
			seen[k] = true
		}
		lc := strings.ToLower(ch.Content)
		if reErrorWords.MatchString(lc) {
			seen["error_path"] = true
		}
		if reEmpty.MatchString(lc) {
			seen["empty_or_null_input"] = true
		}
	}
	return sortedIntentKeys(seen)
}

func inferBranchIntentsFromContent(content string) []string {
	s := strings.TrimSpace(content)
	if s == "" {
		return nil
	}
	seen := make(map[string]bool)
	if reIf.MatchString(s) {
		seen["if_true_path"] = true
		if reElse.MatchString(s) {
			seen["if_false_path"] = true
		}
	}
	if reSwitch.MatchString(s) {
		seen["switch_case_paths"] = true
		if reDefault.MatchString(s) || reCase.MatchString(s) {
			seen["switch_default_path"] = true
		}
	}
	if reTryCatch.MatchString(s) {
		seen["exception_path"] = true
	}
	if reNullCheck.MatchString(s) {
		seen["null_handling"] = true
	}
	if reBoundary.MatchString(s) {
		seen["boundary_conditions"] = true
	}
	return sortedIntentKeys(seen)
}

func diffIntents(target, covered []string) []string {
	if len(target) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(covered))
	for _, s := range covered {
		seen[s] = true
	}
	var out []string
	for _, t := range target {
		if !seen[t] {
			out = append(out, t)
		}
	}
	return out
}

func sortedIntentKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
