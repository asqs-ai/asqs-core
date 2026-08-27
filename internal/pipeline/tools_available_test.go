package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/generator"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/intelligence/retrieval"
	"github.com/asqs/asqs-core/internal/intelligence/tools"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

type stubInvoker struct{}

func (stubInvoker) Definitions() []model.ToolDefinition { return nil }
func (stubInvoker) Invoke(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}

// The inventory must follow the RESOLVED loop mode, never the config flag. A run that asked for
// tools and fell back to one-shot still needs the inlined dependency bodies: nothing can fetch what
// an inventory merely names, so getting this backwards produces a context promising lookups nobody
// can perform.
func TestGeneratorHasTools_followsTheResolvedMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		gen  *generator.LLMGenerator
		want bool
	}{
		{"nil generator", nil, false},
		{"no registry, native mode", &generator.LLMGenerator{ToolLoop: tools.LoopOptions{Mode: tools.ModeNative}}, false},
		{"registry, zero mode", &generator.LLMGenerator{Tools: stubInvoker{}}, false},
		{"registry, resolved one-shot", &generator.LLMGenerator{Tools: stubInvoker{}, ToolLoop: tools.LoopOptions{Mode: tools.ModeOneShot}}, false},
		{"registry, native", &generator.LLMGenerator{Tools: stubInvoker{}, ToolLoop: tools.LoopOptions{Mode: tools.ModeNative}}, true},
		{"registry, prompted", &generator.LLMGenerator{Tools: stubInvoker{}, ToolLoop: tools.LoopOptions{Mode: tools.ModePrompted}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := generatorHasTools(tc.gen); got != tc.want {
				t.Fatalf("generatorHasTools = %v, want %v", got, tc.want)
			}
		})
	}
}

// The restructure ships behind generation.tools_enabled, which defaults off. With it off the
// resolution is one-shot, so the rendered context keeps every dependency body inline exactly as it
// did before this bundle — that default is what makes the change safe to land unmeasured.
func TestToolsAvailable_defaultConfigLeavesTheContextUnchanged(t *testing.T) {
	rc := &retrieval.RetrievalContext{
		TargetMethod: &retrieval.SymbolChunk{
			Symbol: &metadata.Symbol{FQName: "com.acme.OrderService#place", File: "OrderService.java"},
			Chunk:  &embeddings.Chunk{Content: "void place(Order o) {}", File: "OrderService.java"},
		},
		Dependencies: []*retrieval.DependencyEdge{{
			SymbolChunk: retrieval.SymbolChunk{
				Symbol: &metadata.Symbol{FQName: "com.acme.OrderRepository", File: "OrderRepository.java"},
				Chunk:  &embeddings.Chunk{Content: "interface OrderRepository { void save(Order o); }", File: "OrderRepository.java"},
			},
			EdgeType: "calls", Depth: 1,
		}},
	}

	// A default config resolves to one-shot, so the pipeline's gate reports no tools.
	loop, _ := toolLoopFromConfig(&config.Config{}, nil)
	gen := &generator.LLMGenerator{Tools: stubInvoker{}, ToolLoop: loop}
	if generatorHasTools(gen) {
		t.Fatal("a default configuration must not switch the context to the inventory shape")
	}

	fo := retrieval.DefaultFormatOptions()
	fo.ToolsAvailable = generatorHasTools(gen)
	out := retrieval.BuildLLMContext(rc, fo)

	if !strings.Contains(out, "interface OrderRepository") {
		t.Error("the dependency body must still be inlined when tools are off")
	}
	if strings.Contains(out, "Available context") {
		t.Error("the inventory must not appear when tools are off")
	}
}
