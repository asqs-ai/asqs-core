package indexer

import (
	"fmt"
	"sort"
)

// WorstUnresolvedFiles names the files with the most unresolved invocations, most first.
//
// The repository-wide total has been on the audit trail since the per-project compilation work, and
// on its own it is not actionable: "19 unresolved" names no file. A file whose calls do not bind is
// a file whose project references are incomplete, so the list is the form somebody can act on.
//
// Files reporting nothing are skipped rather than counted as clean — the indexer omits the field
// when it does not measure, and reading that as zero would put a silent file at the bottom of a
// list it does not belong on.
func WorstUnresolvedFiles(parsed map[string]*ParsedFile, limit int) []string {
	if limit <= 0 || len(parsed) == 0 {
		return nil
	}
	type row struct {
		path string
		n    int
	}
	var rows []row
	for path, pf := range parsed {
		if pf == nil || pf.UnresolvedInvocations == nil || *pf.UnresolvedInvocations <= 0 {
			continue
		}
		rows = append(rows, row{path: path, n: *pf.UnresolvedInvocations})
	}
	if len(rows) == 0 {
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].path < rows[j].path // stable across runs
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s (%d)", r.path, r.n))
	}
	return out
}
