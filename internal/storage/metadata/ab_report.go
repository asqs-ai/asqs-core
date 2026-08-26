package metadata

import (
	"context"
	"fmt"
	"strings"
)

// ABRow aggregates outcome metrics for one config revision.
type ABRow struct {
	ConfigRevisionID string
	Version          int
	Runs             int
	// PassWithoutFixRate is the mean of first_wave_metrics.test_ok_without_fix — the pass@1 proxy
	// and the primary quality metric.
	PassWithoutFixRate float64
	// FirstCompileRate is the mean of compile_ok_after_generate.
	FirstCompileRate float64
	// AvgTokensToStable pairs with PassWithoutFixRate: a change that improves quality while
	// tripling cost is not obviously an improvement, and reading either number alone hides that.
	AvgTokensToStable float64
	AvgIterations     float64
	AvgTotalTokens    float64
}

// ABReport is the full comparison, newest revision last.
type ABReport struct {
	RepoID string
	Rows   []ABRow
}

// ABReportForRepo groups completed runs by config revision and reports outcome metrics.
//
// The data has been there all along: index_runs.first_wave_metrics records the outcome, and
// index_runs.config_revision_id joins a run to the exact configuration that produced it. ASQS could
// A/B its own retrieval changes with one query and did not — every retrieval change was unmeasured,
// and so is every proposed one.
//
// Two caveats the caller must surface rather than bury:
//
//   - **Run counts matter.** An average over 3 runs and one over 30 are not comparable, so Runs is
//     part of the row rather than a footnote.
//   - **Gap selection must be deterministic**, or two revisions are scored against different gap
//     sets and the comparison means nothing. That is now guaranteed by the FQName/File/StartLine
//     tie-break in gap ordering.
func (s *Store) ABReportForRepo(ctx context.Context, repoID string) (*ABReport, error) {
	const q = `
SELECT r.config_revision_id,
       COALESCE(cr.version, 0)                                                  AS version,
       count(*)                                                                 AS runs,
       avg(CASE WHEN (r.first_wave_metrics->>'test_ok_without_fix')::boolean THEN 1.0 ELSE 0.0 END)      AS pass_without_fix,
       avg(CASE WHEN (r.first_wave_metrics->>'compile_ok_after_generate')::boolean THEN 1.0 ELSE 0.0 END) AS first_compile,
       avg(COALESCE(NULLIF(r.first_wave_metrics->>'tokens_to_stable','')::numeric, 0))                    AS avg_tokens_to_stable,
       avg(COALESCE(NULLIF(r.first_wave_metrics->>'eval_iterations','')::numeric, 0))                     AS avg_iterations,
       avg(COALESCE(NULLIF(r.first_wave_metrics->>'llm_total_tokens','')::numeric, 0))                    AS avg_total_tokens
FROM index_runs r
LEFT JOIN config_revisions cr ON cr.id = r.config_revision_id
WHERE r.first_wave_metrics IS NOT NULL
  AND r.config_revision_id IS NOT NULL
  AND ($1 = '' OR r.repo_id = $1)
GROUP BY r.config_revision_id, cr.version
ORDER BY cr.version NULLS FIRST, r.config_revision_id`

	rows, err := s.db.Query(ctx, q, strings.TrimSpace(repoID))
	if err != nil {
		return nil, fmt.Errorf("ab report: %w", err)
	}
	defer rows.Close()

	out := &ABReport{RepoID: strings.TrimSpace(repoID)}
	for rows.Next() {
		var r ABRow
		if err := rows.Scan(&r.ConfigRevisionID, &r.Version, &r.Runs,
			&r.PassWithoutFixRate, &r.FirstCompileRate,
			&r.AvgTokensToStable, &r.AvgIterations, &r.AvgTotalTokens); err != nil {
			return nil, fmt.Errorf("ab report scan: %w", err)
		}
		out.Rows = append(out.Rows, r)
	}
	return out, rows.Err()
}
