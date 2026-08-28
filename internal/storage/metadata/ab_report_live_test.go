package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestABReportForRepo_groupsByRevision seeds two config revisions with completed runs and asserts
// the report's grouping, run counts and averages (core-own; upstream verified the query against
// its live corpus). Live because the report is one SQL aggregate over JSONB — a fake proves
// nothing about the casts.
func TestABReportForRepo_groupsByRevision(t *testing.T) {
	url, why := ScratchDBForTests()
	if url == "" {
		t.Skip(why)
	}
	ctx := context.Background()
	s, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	repo := fmt.Sprintf("abreport/repo-%d", stamp)
	cfgName := fmt.Sprintf("abreport-%d", stamp)

	cfgID, rev1, _, err := s.CreateConfigWithInitialRevision(ctx, cfgName, "t", "a: 1\n", "test")
	if err != nil {
		t.Fatal(err)
	}
	rev2, _, err := s.AppendConfigRevision(ctx, cfgID, "a: 2\n", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = s.db.Exec(bg, `DELETE FROM index_runs WHERE repo_id = $1`, repo)
		_, _ = s.DeleteConfig(bg, cfgID)
	})

	seedRun := func(i int, revID string, testOK bool, tokens int64) {
		runID := fmt.Sprintf("abrun-%d-%d-%s", stamp, i, revID[:8])
		if err := s.InsertIndexRun(ctx, runID, repo, "sha", time.Now().UnixMilli(), 3, &IndexRunStartExtras{
			TriggerSource: "test", ConfigRevisionID: revID,
		}); err != nil {
			t.Fatal(err)
		}
		st := true
		iters := 1
		if err := s.SetRunCompleted(ctx, runID, &st, &iters); err != nil {
			t.Fatal(err)
		}
		ts := tokens
		if err := s.SetIndexRunFirstWaveMetrics(ctx, runID, &FirstWaveRunMetrics{
			CompileOKAfterGenerate: true,
			TestOKWithoutFix:       testOK,
			EvalStable:             true,
			EvalIterations:         1,
			LlmTotalTokens:         tokens,
			TokensToStable:         &ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// rev1: two runs, one of which passed without fix. rev2: one run, passed without fix.
	seedRun(0, rev1, true, 100)
	seedRun(1, rev1, false, 300)
	seedRun(2, rev2, true, 50)
	// A run with NULL metrics (evaluation skipped) must not appear in any average.
	if err := s.InsertIndexRun(ctx, fmt.Sprintf("abrun-%d-null", stamp), repo, "sha", time.Now().UnixMilli(), 3, &IndexRunStartExtras{
		TriggerSource: "test", ConfigRevisionID: rev1,
	}); err != nil {
		t.Fatal(err)
	}

	rep, err := s.ABReportForRepo(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per revision): %+v", len(rep.Rows), rep.Rows)
	}
	byRev := map[string]ABRow{}
	for _, r := range rep.Rows {
		byRev[r.ConfigRevisionID] = r
	}
	r1 := byRev[rev1]
	if r1.Runs != 2 {
		t.Errorf("rev1 runs = %d, want 2 (the NULL-metrics run must be excluded)", r1.Runs)
	}
	if r1.PassWithoutFixRate < 0.49 || r1.PassWithoutFixRate > 0.51 {
		t.Errorf("rev1 pass-without-fix = %v, want 0.5", r1.PassWithoutFixRate)
	}
	if r1.AvgTokensToStable < 199 || r1.AvgTokensToStable > 201 {
		t.Errorf("rev1 avg tokens = %v, want 200", r1.AvgTokensToStable)
	}
	r2 := byRev[rev2]
	if r2.Runs != 1 || r2.PassWithoutFixRate != 1.0 {
		t.Errorf("rev2 = %+v, want 1 run at 100%%", r2)
	}
	if r1.Version >= r2.Version {
		t.Errorf("versions not increasing: rev1 v%d, rev2 v%d", r1.Version, r2.Version)
	}
}
