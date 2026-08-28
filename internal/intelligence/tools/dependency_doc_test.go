package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// ---- search_code dilution guard (B55) --------------------------------------
//
// Dependency docs share the vector space with repository chunks. The guard under test: default
// search_code never returns them; an explicit chunk_type:"dependency_doc" opts in.

func TestSearchCode_ExcludesDependencyDocsByDefault(t *testing.T) {
	r, _, c := testRegistry(t)
	c.lexical = []embeddings.SearchResult{{Chunk: embeddings.Chunk{File: "a.java", ChunkType: "definition", Content: "x"}}}

	invoke(t, r, ToolSearchCode, `{"query":"pageable"}`)
	if len(c.lexOpts) != 1 {
		t.Fatalf("lexical called %d times; want 1", len(c.lexOpts))
	}
	got := c.lexOpts[0]
	if got.ExcludeChunkType != embeddings.ChunkTypeDependencyDoc || got.ChunkType != "" {
		t.Errorf("default search opts = ChunkType %q ExcludeChunkType %q; want \"\", %q",
			got.ChunkType, got.ExcludeChunkType, embeddings.ChunkTypeDependencyDoc)
	}
}

func TestSearchCode_ExplicitDependencyDocOptsIn(t *testing.T) {
	r, _, c := testRegistry(t)
	c.lexical = []embeddings.SearchResult{{Chunk: embeddings.Chunk{File: "dep://maven/g:a:1", ChunkType: embeddings.ChunkTypeDependencyDoc, Content: "docs"}}}

	invoke(t, r, ToolSearchCode, `{"query":"pageable","chunk_type":"dependency_doc"}`)
	got := c.lexOpts[len(c.lexOpts)-1]
	if got.ChunkType != embeddings.ChunkTypeDependencyDoc || got.ExcludeChunkType != "" {
		t.Errorf("explicit opts = ChunkType %q ExcludeChunkType %q; want %q, \"\"",
			got.ChunkType, got.ExcludeChunkType, embeddings.ChunkTypeDependencyDoc)
	}
}

// ---- get_symbol fallback (B55) ---------------------------------------------

// depDocChunkFixture returns a listFn serving one Splitter dependency chunk, honoring the
// MetadataContains filter the way the store's @> containment does.
func depDocChunkFixture(t *testing.T) (func(embeddings.ListOptions) ([]embeddings.Chunk, error), embeddings.Chunk) {
	t.Helper()
	meta := map[string]string{
		"coordinate":        "org.example:util:1.0.0",
		"dependency_source": "maven",
		"fq_name":           "org.example.util.Splitter",
		"simple_name":       "Splitter",
	}
	mj, _ := json.Marshal(meta)
	chunk := embeddings.Chunk{
		ID: "dep-1", File: "dep://maven/org.example:util:1.0.0",
		ChunkType: embeddings.ChunkTypeDependencyDoc,
		Content:   "org.example.util.Splitter\nUtility for splitting strings.\n\nPublic API:\n  public static List<String> split(String input)",
		RepoID:    "org/repo", MetadataJSON: mj,
	}
	fn := func(opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
		if opts.ChunkType != embeddings.ChunkTypeDependencyDoc {
			return nil, nil
		}
		var filter map[string]string
		if err := json.Unmarshal(opts.MetadataContains, &filter); err != nil {
			return nil, nil
		}
		for k, v := range filter {
			if meta[k] != v {
				return nil, nil
			}
		}
		return []embeddings.Chunk{chunk}, nil
	}
	return fn, chunk
}

func TestGetSymbol_FallsBackToDependencyDoc(t *testing.T) {
	for _, tc := range []struct {
		name, fq string
	}{
		{"exact fq_name", "org.example.util.Splitter"},
		{"member suffix stripped", "org.example.util.Splitter#split"},
		{"simple name", "Splitter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, c := testRegistry(t)
			fn, _ := depDocChunkFixture(t)
			c.listFn = fn

			out := invoke(t, r, ToolGetSymbol, `{"fq_name":"`+tc.fq+`"}`)
			for _, want := range []string{
				"not defined in this repository",
				"org.example:util:1.0.0",
				"maven",
				"public static List<String> split(String input)",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestGetSymbol_RepoSymbolStillWinsOverDependencyDoc(t *testing.T) {
	// The indexed repo symbol from testRegistry resolves; the fallback must not run at all.
	r, _, c := testRegistry(t)
	fn, _ := depDocChunkFixture(t)
	c.listFn = fn

	out := invoke(t, r, ToolGetSymbol, `{"fq_name":"com.acme.PricingEngine#quote"}`)
	if strings.Contains(out, "not defined in this repository") {
		t.Errorf("repo symbol answered with dependency fallback:\n%s", out)
	}
	if !strings.Contains(out, "com.acme.PricingEngine#quote") {
		t.Errorf("repo symbol content missing:\n%s", out)
	}
}

func TestGetSymbol_MissWithoutDependencyDocStaysError(t *testing.T) {
	r, _, c := testRegistry(t)
	c.listFn = func(embeddings.ListOptions) ([]embeddings.Chunk, error) { return nil, nil }

	_, err := r.Invoke(context.Background(), ToolGetSymbol, json.RawMessage(`{"fq_name":"com.nowhere.Missing"}`))
	if err == nil || !strings.Contains(err.Error(), `no symbol named "com.nowhere.Missing" is indexed`) {
		t.Fatalf("err = %v; want the standard not-indexed message", err)
	}
}

// Overload ambiguity keeps its behavior: multiple repo symbols under one FQName resolve to the
// deterministic first — never the dependency fallback.
func TestGetSymbol_FallbackNotUsedOnStoreError(t *testing.T) {
	r, m, c := testRegistry(t)
	m.listErr = errListBoom
	fn, _ := depDocChunkFixture(t)
	c.listFn = fn

	_, err := r.Invoke(context.Background(), ToolGetSymbol, json.RawMessage(`{"fq_name":"org.example.util.Splitter"}`))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v; want the store error, not a dependency-doc answer", err)
	}
}

var errListBoom = &storeErr{"boom"}

type storeErr struct{ msg string }

func (e *storeErr) Error() string { return e.msg }

var _ = metadata.Symbol{} // keep the import honest if fixtures change

// ---- B25 bare-FQ fallback ----------------------------------------------------

// bareMeta opts the fake into the optional bareFQLookup capability.
type bareMeta struct {
	*fakeMeta
	byBare map[string][]*metadata.Symbol
}

func (b *bareMeta) ListSymbolsByBareFQName(_ context.Context, _, bare string) ([]*metadata.Symbol, error) {
	return b.byBare[bare], nil
}

// A model asking with the pre-B25 bare form must still resolve, and overloads keep the existing
// deterministic-first ambiguity handling.
func TestGetSymbol_bareFQNameFallbackResolvesOverloads(t *testing.T) {
	r, m, _ := testRegistry(t)
	intAdd := &metadata.Symbol{ID: "s-int", FQName: "Fixture.Lib.Calc#Add(int,int)", File: "src/Calc.cs", Lang: "csharp", Kind: "method", StartLine: 3, EndLine: 3}
	dblAdd := &metadata.Symbol{ID: "s-dbl", FQName: "Fixture.Lib.Calc#Add(double,double)", File: "src/Calc.cs", Lang: "csharp", Kind: "method", StartLine: 4, EndLine: 4}
	r.Meta = &bareMeta{fakeMeta: m, byBare: map[string][]*metadata.Symbol{
		"Fixture.Lib.Calc#Add": {intAdd, dblAdd},
	}}

	out := invoke(t, r, ToolGetSymbol, `{"fq_name":"Fixture.Lib.Calc#Add"}`)
	if !strings.Contains(out, "Fixture.Lib.Calc#Add(") {
		t.Errorf("bare lookup did not resolve a parameterized overload:\n%s", out)
	}

	// Exact parameterized names still resolve through the primary path untouched.
	m.byFQ["Fixture.Lib.Calc#Add(double,double)"] = []*metadata.Symbol{dblAdd}
	out = invoke(t, r, ToolGetSymbol, `{"fq_name":"Fixture.Lib.Calc#Add(double,double)"}`)
	if !strings.Contains(out, "Add(double,double)") {
		t.Errorf("exact parameterized lookup failed:\n%s", out)
	}
}
