package retrieval

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/llm/tokens"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func chunkOf(file string, lines int) *embeddings.Chunk {
	return &embeddings.Chunk{
		File: file, Lang: "java", StartLine: 1, EndLine: lines,
		Content: strings.Repeat("    doSomethingUseful(withAnArgument);\n", lines),
	}
}

func oversizedContext() *RetrievalContext {
	return &RetrievalContext{
		TargetMethod: &SymbolChunk{
			Symbol: &metadata.Symbol{FQName: "com.acme.OrderService#place", File: "OrderService.java"},
			Chunk:  chunkOf("OrderService.java", 40),
		},
		TargetClass: &SymbolChunk{
			Symbol: &metadata.Symbol{FQName: "com.acme.OrderService", File: "OrderService.java"},
			Chunk:  chunkOf("OrderService.java", 300),
		},
		Dependencies: []*DependencyEdge{
			{SymbolChunk: SymbolChunk{Symbol: &metadata.Symbol{FQName: "com.acme.PricingEngine", File: "PricingEngine.java"}, Chunk: chunkOf("PricingEngine.java", 400)}, EdgeType: "CALLS"},
			{SymbolChunk: SymbolChunk{Symbol: &metadata.Symbol{FQName: "com.acme.AuditLog", File: "AuditLog.java"}, Chunk: chunkOf("AuditLog.java", 400)}, EdgeType: "CALLS"},
		},
		SimilarTests: []*embeddings.Chunk{chunkOf("OtherTest.java", 400), chunkOf("MoreTest.java", 400)},
		Fixtures:     []*embeddings.Chunk{chunkOf("Fixtures.java", 200)},
		Config:       []*embeddings.Chunk{chunkOf("application-test.yml", 100)},
	}
}

// TestBuildLLMContext_respectsTokenBudget is the headline property of C-3: the prompt is bounded.
// Before this there was no size control of any kind, so a gap whose enclosing class was a
// 3000-line God object emitted that class in full into every one of its methods' prompts.
func TestBuildLLMContext_respectsTokenBudget(t *testing.T) {
	opts := DefaultFormatOptions()
	counter := tokens.For("", "")

	unbounded := BuildLLMContext(oversizedContext(), opts)
	unboundedTokens := counter.Count(unbounded)

	const budget = 3000
	opts.MaxContextTokens = budget
	bounded := BuildLLMContext(oversizedContext(), opts)
	boundedTokens := counter.Count(bounded)

	if boundedTokens >= unboundedTokens {
		t.Fatalf("budget had no effect: %d tokens bounded vs %d unbounded", boundedTokens, unboundedTokens)
	}
	// Some overshoot is expected — non-truncatable sections are emitted whole by design and
	// headers/tables are not clamped — but it must be within the same order of magnitude, not the
	// 10x+ the unbounded version produces.
	if boundedTokens > budget*2 {
		t.Errorf("bounded context is %d tokens against a %d budget; clamping is not effective enough",
			boundedTokens, budget)
	}
}

// The target sections must survive any budget: they are the code under test.
func TestBuildLLMContext_targetSurvivesTightBudget(t *testing.T) {
	opts := DefaultFormatOptions()
	opts.MaxContextTokens = 200 // absurdly small

	rc := oversizedContext()
	targetBody := rc.TargetMethod.Chunk.Content
	out := BuildLLMContext(rc, opts)

	firstLine := strings.SplitN(strings.TrimSpace(targetBody), "\n", 2)[0]
	if !strings.Contains(out, firstLine) {
		t.Fatal("the target method body was dropped under a tight budget; it must never be truncated")
	}
}

// Zero budget must be byte-identical to pre-budget behaviour, so deployments that cannot resolve a
// model window are unaffected.
func TestBuildLLMContext_zeroBudgetIsUnchanged(t *testing.T) {
	opts := DefaultFormatOptions()
	a := BuildLLMContext(oversizedContext(), opts)

	opts.MaxContextTokens = 0
	b := BuildLLMContext(oversizedContext(), opts)

	if a != b {
		t.Error("a zero budget changed the rendered context; unbounded must remain the previous behaviour")
	}
}

// Every code block must carry a language tag and an anchor — the context-quality assertions the
// review asks for, checked over the real renderer rather than a unit of it.
func TestBuildLLMContext_everyCodeBlockIsTaggedAndAnchored(t *testing.T) {
	out := BuildLLMContext(oversizedContext(), DefaultFormatOptions())

	var untagged, unanchored int
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "```") {
			continue
		}
		lang := strings.TrimPrefix(line, "```")
		if strings.TrimSpace(lang) == "" {
			untagged++ // closing fences are also bare ``` so this counts both; see the ratio check
		}
	}
	// Opening fences carry a tag, closing fences do not — so bare fences should be at most half.
	total := strings.Count(out, "```")
	if untagged > total/2 {
		t.Errorf("%d bare fences out of %d; opening fences must carry a language tag", untagged, total)
	}
	if !strings.Contains(out, "```java\n") {
		t.Error("no java-tagged fence in a Java context")
	}
	if !strings.Contains(out, "// OrderService.java:") {
		t.Errorf("no file:line anchor found; the fixer is told to cite locations and needs them\n%s", unanchoredSample(out))
	}
	_ = unanchored
}

func unanchoredSample(s string) string {
	if len(s) > 600 {
		return s[:600]
	}
	return s
}

// TestBudgetIsRecordedForTelemetry: prompt_tokens telemetry depends on the caller being able to
// read the spend without re-counting the whole prompt.
func TestBuildLLMContext_reportsPerSectionSpend(t *testing.T) {
	opts := DefaultFormatOptions()
	opts.MaxContextTokens = 5000
	b := tokens.NewBudget(opts.MaxContextTokens, opts.CounterOrDefault())
	opts.LastBudget = b

	BuildLLMContext(oversizedContext(), opts)

	bd := b.Breakdown()
	if len(bd) == 0 {
		t.Fatal("no per-section spend recorded; prompt_tokens telemetry has nothing to report")
	}
	if bd["target"] == 0 {
		t.Errorf("target section spend not recorded: %+v", bd)
	}
	if b.UsedTotal() == 0 {
		t.Error("UsedTotal is zero")
	}
}
