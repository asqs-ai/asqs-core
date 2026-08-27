package metadata

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestFirstWaveMetrics_promptTokensRoundTrip pins the wire format for prompt_tokens: the value must
// appear under that key in the stored JSONB (that spelling is what remote SQL and the A/B report
// query against), a pointer field must survive the round trip, and a row written without usage must
// omit the key entirely — absent and zero are different claims. Live because the contract is what
// Postgres stores, not what encoding/json would do in memory.
func TestFirstWaveMetrics_promptTokensRoundTrip(t *testing.T) {
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
	repo := fmt.Sprintf("fwm/repo-%d", stamp)
	runID := fmt.Sprintf("fwm-run-%d", stamp)
	if err := s.InsertIndexRun(ctx, runID, repo, "sha", time.Now().UnixMilli(), 1, &IndexRunStartExtras{TriggerSource: "test"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec(context.Background(), `DELETE FROM index_runs WHERE repo_id = $1`, repo)
	})

	pt := int64(4321)
	if err := s.SetIndexRunFirstWaveMetrics(ctx, runID, &FirstWaveRunMetrics{
		LlmTotalTokens: 5000,
		PromptTokens:   &pt,
	}); err != nil {
		t.Fatal(err)
	}

	var raw string
	if err := s.db.QueryRow(ctx, `SELECT first_wave_metrics::text FROM index_runs WHERE run_id = $1`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"prompt_tokens": 4321`) && !strings.Contains(raw, `"prompt_tokens":4321`) {
		t.Fatalf("stored JSONB lacks prompt_tokens: %s", raw)
	}
	got, err := s.GetIndexRunFirstWaveMetrics(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PromptTokens == nil || *got.PromptTokens != 4321 {
		t.Fatalf("read back PromptTokens = %+v; want 4321", got)
	}

	// A run without reported usage omits the key — a reader must be able to tell "not reported"
	// from "zero prompt tokens".
	if err := s.SetIndexRunFirstWaveMetrics(ctx, runID, &FirstWaveRunMetrics{LlmTotalTokens: 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(ctx, `SELECT first_wave_metrics::text FROM index_runs WHERE run_id = $1`, runID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "prompt_tokens") {
		t.Fatalf("unreported usage must omit the prompt_tokens key, got: %s", raw)
	}
}
