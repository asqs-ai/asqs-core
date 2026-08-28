package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

type recordingAuditor struct {
	steps []string
}

func (r *recordingAuditor) Log(_ context.Context, step string, _ interface{}) {
	r.steps = append(r.steps, step)
}

// The compaction mechanism existed in full — code, tests, YAML plumbing — with zero production
// callers, so retrieval.context_compact did nothing. This pins the wiring: an oversized non-target
// chunk shrinks, and the audit counter fires.
func TestCompactPlanContexts_runsAndCounts(t *testing.T) {
	big := strings.Repeat("x\n", 30000)
	plan := &retrieval.TestPlan{Items: []*retrieval.TestPlanItem{{
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{FQName: "a.B#c"}},
		Context: &retrieval.RetrievalContext{
			Dependencies: []*retrieval.DependencyEdge{
				{SymbolChunk: retrieval.SymbolChunk{Chunk: &embeddings.Chunk{Content: big, File: "Dep.java"}}},
			},
		},
	}}}

	var fo retrieval.FormatOptions
	applyRetrievalContextCompactToFormat(&config.RetrievalConfig{}, &fo)
	if !fo.ContextCompact.Enabled {
		t.Fatal("compaction must default ON when the enabled key is omitted")
	}

	audit := &recordingAuditor{}
	compactPlanContexts(context.Background(), fo, audit, plan)

	got := plan.Items[0].Context.Dependencies[0].Chunk.Content
	if len(got) >= len(big) {
		t.Fatalf("oversized dependency chunk was not compacted (len %d)", len(got))
	}
	found := false
	for _, s := range audit.steps {
		if s == "retrieve.context_compacted_total" {
			found = true
		}
	}
	if !found {
		t.Fatalf("compaction ran but the audit counter did not fire: %v", audit.steps)
	}

	// Explicit false disables — the one switch the config keeps.
	off := false
	var foOff retrieval.FormatOptions
	applyRetrievalContextCompactToFormat(&config.RetrievalConfig{ContextCompact: config.ContextCompactConfig{Enabled: &off}}, &foOff)
	if foOff.ContextCompact.Enabled {
		t.Fatal("enabled: false must disable compaction")
	}
}
