package llmfix

import (
	"context"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// recordingCompleter replays canned responses in order and records the CompleteOptions each call
// was made with, so a test can assert whether structured output was requested per turn.
type recordingCompleter struct {
	replies []string
	errs    []error
	opts    []model.CompleteOptions
	msgs    [][]model.Message
}

func (c *recordingCompleter) Complete(_ context.Context, msgs []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	i := len(c.opts)
	c.opts = append(c.opts, opts)
	c.msgs = append(c.msgs, msgs)
	if i < len(c.errs) && c.errs[i] != nil {
		return nil, c.errs[i]
	}
	if i >= len(c.replies) {
		return &model.CompleteResult{Content: "still not json", StopReason: "stop"}, nil
	}
	return &model.CompleteResult{Content: c.replies[i], StopReason: "stop"}, nil
}

func fixReqOneArtifact() evaluator.FixRequest {
	return evaluator.FixRequest{
		Step:          evaluator.StepCompile,
		Lang:          "csharp",
		ErrorOutput:   "error CS1002: ; expected",
		ArtifactPaths: []string{"tests/FooTests.cs"},
		Files:         map[string]string{"tests/FooTests.cs": "public class FooTests { }"},
	}
}

type recordingFixAudit struct{ steps []string }

func (a *recordingFixAudit) Log(_ context.Context, step string, _ interface{}) {
	a.steps = append(a.steps, step)
}

// If structured output produced the unparseable text, asking for it again on the repair turn
// reproduces the failure. The repair turn must always be unstructured.
func TestFix_repairTurnDropsStructuredOutput(t *testing.T) {
	llm := &recordingCompleter{replies: []string{
		"here is your fix, buddy",                                      // main turn: unparseable
		`{"tests/FooTests.cs":"public class FooTests { void A(){} }"}`, // repair turn: good
	}}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: false}

	resp, err := f.Fix(context.Background(), fixReqOneArtifact())
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("Files = %v, want one entry", resp.Files)
	}
	if len(llm.opts) < 2 {
		t.Fatalf("expected at least a main turn and a repair turn, got %d calls", len(llm.opts))
	}
	if llm.opts[len(llm.opts)-1].Structured != nil {
		t.Fatal("repair turn must not request structured output")
	}
}

// (Upstream continues here with the plain-fallback target-resolution tests — reply-named /
// error-named artifact selection via plainFallbackTarget — and classifyFixParseFailure. Both
// functions arrive with the fixer robustness batches in CP53; port those test functions with them.)

// (Upstream additionally tests the single-file plain-source fallback here; the fallback itself
// arrives with CP53 — port those tests with it. Two of them would even pass today, but for the
// wrong reason: the round fails because the fallback does not exist, not because it refused.)
