package retrieval

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func inventoryFixture() *TestPlanItem {
	return &TestPlanItem{
		Gap: &TestGap{Symbol: &metadata.Symbol{
			FQName: "com.acme.OrderService#place", File: "src/main/java/com/acme/OrderService.java",
			Kind: "method", Lang: "java", StartLine: 41, EndLine: 58,
		}},
		Context: &RetrievalContext{
			TargetMethod: &SymbolChunk{
				Symbol: &metadata.Symbol{FQName: "com.acme.OrderService#place", File: "src/main/java/com/acme/OrderService.java"},
				Chunk: &embeddings.Chunk{
					File: "src/main/java/com/acme/OrderService.java", Lang: "java", StartLine: 41, EndLine: 58,
					Content: "public Order place(Order o) { return repository.save(o); }",
				},
			},
			Dependencies: []*DependencyEdge{
				{
					SymbolChunk: SymbolChunk{
						Symbol: &metadata.Symbol{FQName: "com.acme.OrderRepository", File: "src/main/java/com/acme/OrderRepository.java", StartLine: 1, EndLine: 8},
						Chunk: &embeddings.Chunk{
							File: "src/main/java/com/acme/OrderRepository.java", StartLine: 1, EndLine: 8,
							Content: "public interface OrderRepository { Order save(Order o); }",
						},
					},
					EdgeType: "CALLS", Depth: 1,
				},
				{
					SymbolChunk: SymbolChunk{
						Symbol: &metadata.Symbol{FQName: "com.acme.Order", File: "src/main/java/com/acme/Order.java", StartLine: 3, EndLine: 40},
						Chunk: &embeddings.Chunk{
							File: "src/main/java/com/acme/Order.java", StartLine: 3, EndLine: 40,
							Content: strings.Repeat("// a large domain type\n", 60),
						},
					},
					EdgeType: "REFERENCES", Depth: 2,
				},
			},
			SimilarTests: []*embeddings.Chunk{
				{File: "src/test/java/com/acme/PaymentServiceTest.java", StartLine: 1, EndLine: 30, Content: "@Mock PaymentRepository repo;"},
				{File: "src/test/java/com/acme/OrderServiceTest.java", StartLine: 1, EndLine: 20, Content: "@Test void places() {}"},
			},
		},
	}
}

// The core stays inlined; dependency BODIES do not. A table row costs ~15 tokens against 300-1500
// for a body — that arithmetic is the whole point of the restructure.
func TestAvailableContext_listsDependenciesWithoutTheirBodies(t *testing.T) {
	opts := DefaultFormatOptions()
	opts.ToolsAvailable = true
	out := BuildLLMContextForGap(inventoryFixture(), opts)

	// The target is the high-precision core and must still be inlined.
	if !strings.Contains(out, "public Order place(Order o)") {
		t.Error("the target body must stay in the context")
	}
	// Dependencies are named, so the model can fetch them.
	for _, want := range []string{"com.acme.OrderRepository", "com.acme.Order", "get_symbol"} {
		if !strings.Contains(out, want) {
			t.Errorf("inventory missing %q", want)
		}
	}
	// Their bodies are not.
	if strings.Contains(out, "Order save(Order o);") {
		t.Error("a dependency body was inlined despite tools being available")
	}
	if strings.Contains(out, "a large domain type") {
		t.Error("a large domain type body was inlined despite tools being available")
	}
}

// Without tools the previous shape must be unchanged — a run that fell back to one-shot has no way
// to fetch what an inventory would merely name.
func TestAvailableContext_inlinesBodiesWhenToolsAreUnavailable(t *testing.T) {
	opts := DefaultFormatOptions()
	opts.ToolsAvailable = false
	out := BuildLLMContextForGap(inventoryFixture(), opts)

	if !strings.Contains(out, "Order save(Order o);") {
		t.Error("dependency bodies must be inlined when the model cannot fetch them")
	}
	if strings.Contains(out, "Available context (use tools to retrieve)") {
		t.Error("the inventory section appeared without tools")
	}
}

// The review focus: listing a symbol the tools cannot fetch is worse than not listing it. get_symbol
// resolves on fully-qualified name, so an entry without one is a broken promise and must be dropped.
func TestAvailableContext_omitsEntriesThatCannotBeFetched(t *testing.T) {
	item := inventoryFixture()
	item.Context.Dependencies = append(item.Context.Dependencies, &DependencyEdge{
		SymbolChunk: SymbolChunk{Symbol: &metadata.Symbol{FQName: "   ", File: "src/main/java/com/acme/Anon.java"}},
		EdgeType:    "CALLS", Depth: 1,
	})
	opts := DefaultFormatOptions()
	opts.ToolsAvailable = true
	out := BuildLLMContextForGap(item, opts)

	if strings.Contains(out, "Anon.java") {
		t.Error("an entry with no fully-qualified name was listed; get_symbol cannot resolve it")
	}
	// Every listed backticked entry must carry a name get_symbol could take.
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "- `") {
			continue
		}
		fq := line[3:]
		if i := strings.Index(fq, "`"); i > 0 {
			fq = fq[:i]
		}
		if strings.TrimSpace(fq) == "" {
			t.Errorf("inventory row with an empty name: %q", line)
		}
	}
}

// Duplicates waste rows and make the inventory look unreliable.
func TestAvailableContext_deduplicates(t *testing.T) {
	item := inventoryFixture()
	item.Context.Dependencies = append(item.Context.Dependencies, item.Context.Dependencies[0])
	opts := DefaultFormatOptions()
	opts.ToolsAvailable = true
	out := BuildLLMContextForGap(item, opts)

	if n := strings.Count(out, "- `com.acme.OrderRepository`"); n != 1 {
		t.Errorf("OrderRepository listed %d times", n)
	}
}

// Counts are actionable where bodies are not, and the tool call is named so the model does not have
// to infer the argument shape.
func TestAvailableContext_reportsTestCountsWithTheToolToCallThem(t *testing.T) {
	opts := DefaultFormatOptions()
	opts.ToolsAvailable = true
	out := BuildLLMContextForGap(inventoryFixture(), opts)

	if !strings.Contains(out, "Existing tests touching this target: 2") {
		t.Errorf("test count missing:\n%s", out)
	}
	if !strings.Contains(out, `find_tests_for("com.acme.OrderService#place")`) {
		t.Error("the tool call should name the target so the argument shape is unambiguous")
	}
}

// Edge type and depth are what let the model judge whether a lookup is worth a turn.
func TestAvailableContext_carriesEdgeTypeAndDepth(t *testing.T) {
	opts := DefaultFormatOptions()
	opts.ToolsAvailable = true
	out := BuildLLMContextForGap(inventoryFixture(), opts)

	if !strings.Contains(out, "CALLS, depth 1") {
		t.Errorf("edge/depth missing for the direct dependency:\n%s", out)
	}
	if !strings.Contains(out, "REFERENCES, depth 2") {
		t.Errorf("edge/depth missing for the transitive dependency:\n%s", out)
	}
}

// The inventory must be materially smaller than the inlined shape, or the restructure buys nothing.
func TestAvailableContext_isSubstantiallySmaller(t *testing.T) {
	inlined := DefaultFormatOptions()
	inlined.ToolsAvailable = false
	withTools := DefaultFormatOptions()
	withTools.ToolsAvailable = true

	big := len(BuildLLMContextForGap(inventoryFixture(), inlined))
	small := len(BuildLLMContextForGap(inventoryFixture(), withTools))
	if small >= big {
		t.Errorf("inventory (%d chars) is not smaller than the inlined context (%d)", small, big)
	}
	t.Logf("inlined %d chars -> inventory %d chars (%.0f%% smaller)", big, small, 100*float64(big-small)/float64(big))
}
