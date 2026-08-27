package generator

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/storage/metadata"
	"github.com/asqs/asqs-core/internal/teststack"
)

// systemCapturingCompleter records the system message of every completion it is asked for.
type systemCapturingCompleter struct{ systems []string }

func (c *systemCapturingCompleter) Complete(_ context.Context, msgs []model.Message, _ model.CompleteOptions) (*model.CompleteResult, error) {
	for _, m := range msgs {
		if m.Role == "system" {
			c.systems = append(c.systems, m.Content)
		}
	}
	return &model.CompleteResult{Content: "class T { @Test void t() {} }", StopReason: "stop"}, nil
}

func contractPlanItem() *retrieval.TestPlanItem {
	return &retrieval.TestPlanItem{
		Layer: "unit",
		Gap: &retrieval.TestGap{Symbol: &metadata.Symbol{
			Lang: "java", Kind: "method", FQName: "com.example.Svc#run", File: "src/main/java/com/example/Svc.java",
		}},
	}
}

// The contract has to reach EVERY generation path, and it is easy for it not to: two-phase builds
// its own system messages, so a block appended only in Generate silently vanishes for any gap that
// takes that route. That is not hypothetical — the framework API-surface block was missing from
// two-phase exactly this way until this bundle, so the gaps routed through two-phase were generated
// against a strictly weaker prompt than the single-pass ones, with nothing to indicate it.
func TestTestStackBlock_reachesBothGenerationPaths(t *testing.T) {
	for _, twoPhase := range []bool{false, true} {
		name := "single-pass"
		if twoPhase {
			name = "two-phase"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := teststack.Write(root, teststack.Contract{
				Version:          teststack.SchemaVersion,
				Language:         "java",
				Framework:        "spring-boot",
				Runner:           "junit5",
				AvailableImports: []string{"org.junit.jupiter", "org.mockito"},
			}); err != nil {
				t.Fatal(err)
			}
			c := &systemCapturingCompleter{}
			g := &LLMGenerator{
				LLM:                             c,
				RepoPath:                        root,
				TestFramework:                   "junit5",
				TwoPhaseTestGeneration:          twoPhase,
				DisableStructuredGenerateOutput: true,
			}
			if _, _, err := g.Generate(context.Background(), contractPlanItem(), "some context"); err != nil {
				t.Fatalf("generate: %v", err)
			}
			if len(c.systems) == 0 {
				t.Fatal("no completion was made, so the prompt was never exercised")
			}
			for i, sys := range c.systems {
				if !strings.Contains(sys, "Test stack in this repository") {
					t.Errorf("system message %d of %d carries no test-stack contract:\n%s", i+1, len(c.systems), sys)
				}
				if !strings.Contains(sys, "org.mockito") {
					t.Errorf("system message %d does not name the allowed imports", i+1)
				}
			}
			if twoPhase && len(c.systems) < 2 {
				t.Errorf("expected both two-phase system messages, saw %d", len(c.systems))
			}
		})
	}
}

// Bootstrap is off by default, so this is the path virtually every run takes: with no contract the
// system message must be exactly what it would have been before this bundle existed.
func TestTestStackBlock_absentContractLeavesSystemMessageUnchanged(t *testing.T) {
	build := func(repoPath string) string {
		c := &systemCapturingCompleter{}
		g := &LLMGenerator{LLM: c, RepoPath: repoPath, TestFramework: "junit5", DisableStructuredGenerateOutput: true}
		if _, _, err := g.Generate(context.Background(), contractPlanItem(), "some context"); err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(c.systems) != 1 {
			t.Fatalf("expected one system message, got %d", len(c.systems))
		}
		return c.systems[0]
	}
	// Two different empty repositories must produce the identical system message: nothing about the
	// workspace may leak into the prompt when there is no contract to read.
	a, b := build(t.TempDir()), build(t.TempDir())
	if a != b {
		t.Errorf("system message varies without a contract:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
	if strings.Contains(a, "Test stack in this repository") {
		t.Error("a contract block rendered with no contract present")
	}
}
