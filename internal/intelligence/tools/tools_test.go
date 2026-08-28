package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// ---- fakes -----------------------------------------------------------------

type fakeMeta struct {
	byFQ    map[string][]*metadata.Symbol
	byID    map[string]*metadata.Symbol
	edgesTo map[string][]*metadata.Edge
	expand  []metadata.ExpandRow
	lastOpt metadata.ExpandGraphOptions
	listErr error // forced ListSymbolsByFQName failure
}

func (f *fakeMeta) ListSymbolsByFQName(_ context.Context, _, fq string) ([]*metadata.Symbol, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.byFQ[fq], nil
}
func (f *fakeMeta) GetSymbolByID(_ context.Context, _, id string) (*metadata.Symbol, error) {
	return f.byID[id], nil
}
func (f *fakeMeta) GetEdgesTo(_ context.Context, _, id string) ([]*metadata.Edge, error) {
	return f.edgesTo[id], nil
}
func (f *fakeMeta) ExpandGraph(_ context.Context, _, _ string, opt metadata.ExpandGraphOptions) ([]metadata.ExpandRow, error) {
	f.lastOpt = opt
	return f.expand, nil
}

type fakeChunks struct {
	list        []embeddings.Chunk
	dense       []embeddings.SearchResult
	lexical     []embeddings.SearchResult
	lexCalls    int
	searchCalls int
	// listFn, when set, replaces the canned list response (filter-aware tests).
	listFn func(embeddings.ListOptions) ([]embeddings.Chunk, error)
	// recorded options, in call order
	listOpts   []embeddings.ListOptions
	searchOpts []embeddings.SearchOptions
	lexOpts    []embeddings.SearchOptions
}

func (f *fakeChunks) List(_ context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
	f.listOpts = append(f.listOpts, opts)
	if f.listFn != nil {
		return f.listFn(opts)
	}
	return f.list, nil
}
func (f *fakeChunks) Search(_ context.Context, _ []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	f.searchCalls++
	f.searchOpts = append(f.searchOpts, opts)
	return f.dense, nil
}
func (f *fakeChunks) SearchLexical(_ context.Context, _ string, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	f.lexCalls++
	f.lexOpts = append(f.lexOpts, opts)
	return f.lexical, nil
}

type fakeEmbedder struct{ err error }

func (f *fakeEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return [][]float32{{0.1, 0.2}}, nil
}

func testRegistry(t *testing.T) (*Registry, *fakeMeta, *fakeChunks) {
	t.Helper()
	sym := &metadata.Symbol{
		ID: "sym-1", FQName: "com.acme.PricingEngine#quote", File: "src/main/java/PricingEngine.java",
		Lang: "java", Kind: "method", StartLine: 40, EndLine: 55, InDegreeNonTest: 4, OutDegree: 2,
	}
	m := &fakeMeta{
		byFQ:    map[string][]*metadata.Symbol{sym.FQName: {sym}},
		byID:    map[string]*metadata.Symbol{sym.ID: sym},
		edgesTo: map[string][]*metadata.Edge{},
	}
	c := &fakeChunks{list: []embeddings.Chunk{{
		ID: "c1", File: sym.File, StartLine: 40, EndLine: 55, ChunkType: "definition",
		Content: "public Quote quote(Order o) { return engine.price(o); }",
	}}}
	return &Registry{Meta: m, Chunks: c, RepoID: "org/repo", Lang: "java"}, m, c
}

func invoke(t *testing.T, r *Registry, name, args string) string {
	t.Helper()
	out, err := r.Invoke(context.Background(), name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

// ---- read_file_range: the security-critical one ----------------------------

// A model-supplied path is untrusted. Every one of these must be refused before the file is opened.
func TestReadFileRange_rejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A real file outside the root, to prove refusal is not merely "file not found".
	outside := filepath.Join(filepath.Dir(root), "outside-secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	r := &Registry{RepoRoot: root}
	for _, p := range []string{
		"../outside-secret.txt",
		"../../etc/passwd",
		"subdir/../../outside-secret.txt",
		outside,
		"/etc/passwd",
		"",
	} {
		args, _ := json.Marshal(map[string]any{"path": p, "start": 1, "end": 5})
		out, err := r.Invoke(context.Background(), ToolReadFileRange, args)
		if err == nil {
			t.Errorf("path %q was accepted; got: %s", p, out)
		}
		if strings.Contains(out, "SECRET") {
			t.Fatalf("path %q leaked content from outside the repository", p)
		}
	}
}

// A symlink passes a textual containment check and then resolves elsewhere, so the file type is
// checked before reading.
func TestReadFileRange_rejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	r := &Registry{RepoRoot: root}
	args, _ := json.Marshal(map[string]any{"path": "link.txt", "start": 1, "end": 5})
	out, err := r.Invoke(context.Background(), ToolReadFileRange, args)
	if err == nil {
		t.Errorf("symlink out of the repository was followed; got: %s", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Fatal("symlink leaked content from outside the repository")
	}
}

func TestReadFileRange_readsAndCapsLines(t *testing.T) {
	root := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 1000; i++ {
		sb.WriteString("line\n")
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Registry{RepoRoot: root}
	out := invoke(t, r, ToolReadFileRange, `{"path":"big.txt","start":1,"end":999}`)
	if got := strings.Count(out, "\n"); got > maxReadLines+3 {
		t.Errorf("returned %d lines; the tool must cap at %d", got, maxReadLines)
	}
	if !strings.HasPrefix(out, "big.txt:1-") {
		t.Errorf("output should state the range actually returned; got %q", out[:40])
	}
}

// Without a repo root the tool must be unavailable, not unbounded.
func TestReadFileRange_unavailableWithoutRoot(t *testing.T) {
	r := &Registry{}
	if _, err := r.Invoke(context.Background(), ToolReadFileRange, json.RawMessage(`{"path":"x","start":1,"end":2}`)); err == nil {
		t.Fatal("expected an error with no repo root configured")
	}
	for _, d := range r.Definitions() {
		if d.Name == ToolReadFileRange {
			t.Error("read_file_range must not be advertised when it cannot work")
		}
	}
}

// ---- output caps -----------------------------------------------------------

// A tool returning a 3000-line class body spends more context than the chunk it replaced. Every
// tool caps and says it truncated — silent truncation would have the model reason about a body
// whose second half it never saw.
func TestTools_capOutputAndSaySo(t *testing.T) {
	r, _, c := testRegistry(t)
	r.MaxChars = 200
	c.list[0].Content = strings.Repeat("x", 5000)

	out := invoke(t, r, ToolGetSymbol, `{"fq_name":"com.acme.PricingEngine#quote"}`)
	if len(out) > 400 {
		t.Errorf("output not capped: %d chars", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Error("truncation must be stated in the output, not silent")
	}
}

// ---- get_symbol ------------------------------------------------------------

func TestGetSymbol_returnsSignatureAndBody(t *testing.T) {
	r, _, _ := testRegistry(t)
	out := invoke(t, r, ToolGetSymbol, `{"fq_name":"com.acme.PricingEngine#quote"}`)
	for _, want := range []string{"com.acme.PricingEngine#quote", "PricingEngine.java:40-55", "public Quote quote"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestGetSymbol_unknownSymbolIsAnError(t *testing.T) {
	r, _, _ := testRegistry(t)
	if _, err := r.Invoke(context.Background(), ToolGetSymbol, json.RawMessage(`{"fq_name":"com.acme.Nope"}`)); err == nil {
		t.Fatal("expected an error for an unindexed symbol")
	}
}

// ---- expand_symbol ---------------------------------------------------------

func TestExpandSymbol_directionAndDepth(t *testing.T) {
	r, m, _ := testRegistry(t)
	m.expand = []metadata.ExpandRow{{
		Symbol: &metadata.Symbol{FQName: "com.acme.Caller#c", File: "C.java", StartLine: 9},
		Depth:  1, EdgeType: "CALLS", Inbound: true,
	}}
	out := invoke(t, r, ToolExpandSymbol, `{"fq_name":"com.acme.PricingEngine#quote","direction":"callers","depth":3}`)
	if !m.lastOpt.Callers || m.lastOpt.Callees {
		t.Errorf("direction callers not honoured: %+v", m.lastOpt)
	}
	if m.lastOpt.MaxDepth != 3 {
		t.Errorf("depth = %d, want 3", m.lastOpt.MaxDepth)
	}
	if !strings.Contains(out, "called by") || !strings.Contains(out, "com.acme.Caller#c") {
		t.Errorf("output does not describe the relation:\n%s", out)
	}
}

// Depth is clamped: this tool is interactive, and a deep walk returns more than a turn can use.
func TestExpandSymbol_clampsDepth(t *testing.T) {
	r, m, _ := testRegistry(t)
	invoke(t, r, ToolExpandSymbol, `{"fq_name":"com.acme.PricingEngine#quote","depth":99}`)
	if m.lastOpt.MaxDepth > 5 {
		t.Errorf("depth not clamped: %d", m.lastOpt.MaxDepth)
	}
}

func TestExpandSymbol_rejectsBadDirection(t *testing.T) {
	r, _, _ := testRegistry(t)
	_, err := r.Invoke(context.Background(), ToolExpandSymbol,
		json.RawMessage(`{"fq_name":"com.acme.PricingEngine#quote","direction":"sideways"}`))
	if err == nil {
		t.Fatal("expected an error for an invalid direction")
	}
}

// ---- search_code -----------------------------------------------------------

func TestSearchCode_usesDenseWhenAnEmbedderIsPresent(t *testing.T) {
	r, _, c := testRegistry(t)
	r.Embedder = &fakeEmbedder{}
	c.dense = []embeddings.SearchResult{{Chunk: embeddings.Chunk{
		File: "T.java", StartLine: 1, EndLine: 3, ChunkType: "test", Content: "@Mock Repo repo;",
	}}}
	out := invoke(t, r, ToolSearchCode, `{"query":"test that mocks a repository","k":3}`)
	if c.searchCalls != 1 {
		t.Errorf("dense search calls = %d, want 1", c.searchCalls)
	}
	if !strings.Contains(out, "@Mock") {
		t.Errorf("result body missing:\n%s", out)
	}
}

// Without an embedder the lexical channel still answers identifier searches. Degrading beats
// refusing — most of what this tool is asked for is a literal name.
func TestSearchCode_fallsBackToLexicalWithoutEmbedder(t *testing.T) {
	r, _, c := testRegistry(t)
	c.lexical = []embeddings.SearchResult{{Chunk: embeddings.Chunk{
		File: "L.java", StartLine: 1, EndLine: 2, ChunkType: "definition", Content: "class L {}",
	}}}
	out := invoke(t, r, ToolSearchCode, `{"query":"PricingEngine"}`)
	if c.searchCalls != 0 {
		t.Errorf("dense search attempted without an embedder")
	}
	if c.lexCalls != 1 {
		t.Errorf("lexical calls = %d, want 1", c.lexCalls)
	}
	if !strings.Contains(out, "class L {}") {
		t.Errorf("lexical result missing:\n%s", out)
	}
}

func TestSearchCode_clampsK(t *testing.T) {
	r, _, c := testRegistry(t)
	c.lexical = []embeddings.SearchResult{}
	if _, err := r.Invoke(context.Background(), ToolSearchCode, json.RawMessage(`{"query":"x","k":500}`)); err != nil {
		t.Fatal(err)
	}
	if c.lexCalls != 1 {
		t.Errorf("lexical calls = %d", c.lexCalls)
	}
	// The clamp has to reach the store, not just survive: the per-result share is the whole
	// budget divided by how many results come back, so an unclamped k shrinks every snippet.
	if got := c.lexOpts[0].Limit; got != maxSearchResults {
		t.Errorf("store Limit = %d, want the clamp %d", got, maxSearchResults)
	}
}

// ---- find_tests_for --------------------------------------------------------

func TestFindTestsFor_listsCoveringTests(t *testing.T) {
	r, m, _ := testRegistry(t)
	m.byID["t1"] = &metadata.Symbol{ID: "t1", FQName: "com.acme.PricingEngineTest#quotes", File: "src/test/PricingEngineTest.java", StartLine: 12}
	// The non-test caller MUST be resolvable, or the edge-type filter is never actually exercised:
	// an unresolvable symbol is skipped for the wrong reason and the test passes either way.
	m.byID["plain-caller"] = &metadata.Symbol{ID: "plain-caller", FQName: "com.acme.Invoicer#bill", File: "src/main/Invoicer.java", StartLine: 3}
	m.edgesTo["sym-1"] = []*metadata.Edge{
		{CallerSymbolID: "t1", CalleeSymbolID: "sym-1", EdgeType: metadata.EdgeTypeTestsSource},
		{CallerSymbolID: "plain-caller", CalleeSymbolID: "sym-1", EdgeType: "CALLS"},
	}
	out := invoke(t, r, ToolFindTestsFor, `{"fq_name":"com.acme.PricingEngine#quote"}`)
	if !strings.Contains(out, "PricingEngineTest#quotes") {
		t.Errorf("covering test not listed:\n%s", out)
	}
	// A plain CALLS edge is not test coverage. Reporting a caller as a covering test would send the
	// model to extend a file that tests nothing.
	if strings.Contains(out, "Invoicer#bill") {
		t.Errorf("non-test edge reported as test coverage:\n%s", out)
	}
}

func TestFindTestsFor_reportsAbsence(t *testing.T) {
	r, _, _ := testRegistry(t)
	out := invoke(t, r, ToolFindTestsFor, `{"fq_name":"com.acme.PricingEngine#quote"}`)
	if !strings.Contains(out, "no existing tests") {
		t.Errorf("absence should be stated plainly:\n%s", out)
	}
}

// ---- registry --------------------------------------------------------------

// Tools whose dependencies are missing must not be advertised: a model told it can read files and
// then erroring every time wastes turns and discredits the whole tool set.
func TestDefinitions_onlyAdvertiseUsableTools(t *testing.T) {
	full, _, _ := testRegistry(t)
	full.RepoRoot = t.TempDir()
	names := map[string]bool{}
	for _, d := range full.Definitions() {
		names[d.Name] = true
		if strings.TrimSpace(d.Description) == "" {
			t.Errorf("%s has no description; the model has only this to go on", d.Name)
		}
		if d.Schema == nil {
			t.Errorf("%s has no schema", d.Name)
			continue
		}
		raw, err := d.Schema.MarshalJSON()
		if err != nil {
			t.Errorf("%s schema does not marshal: %v", d.Name, err)
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Errorf("%s schema is not valid JSON: %v\n%s", d.Name, err, raw)
		}
		if probe["type"] != "object" {
			t.Errorf("%s schema must be an object schema; got %v", d.Name, probe["type"])
		}
	}
	for _, want := range []string{ToolGetSymbol, ToolExpandSymbol, ToolSearchCode, ToolFindTestsFor, ToolReadFileRange} {
		if !names[want] {
			t.Errorf("%s not advertised by a fully-configured registry", want)
		}
	}

	bare := &Registry{}
	if got := len(bare.Definitions()); got != 0 {
		t.Errorf("a registry with no dependencies advertised %d tool(s)", got)
	}
}

func TestInvoke_unknownToolIsAnError(t *testing.T) {
	r, _, _ := testRegistry(t)
	if _, err := r.Invoke(context.Background(), "rm_rf", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error for an unknown tool")
	}
}

// Malformed arguments must be an error, not a silently-empty query.
func TestInvoke_malformedArgsAreAnError(t *testing.T) {
	r, _, _ := testRegistry(t)
	if _, err := r.Invoke(context.Background(), ToolGetSymbol, json.RawMessage(`{not json`)); err == nil {
		t.Fatal("expected an error for malformed arguments")
	}
}

// Every tool must be repo-scoped: one that could read another repository's index is a data-leak
// surface, not merely a bug.
func TestTools_areRepoScoped(t *testing.T) {
	r, _, _ := testRegistry(t)
	if r.RepoID == "" {
		t.Fatal("fixture has no repo id")
	}
	src, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, call := range []string{"embeddings.ListOptions{", "embeddings.SearchOptions{"} {
		i := 0
		for {
			j := strings.Index(body[i:], call)
			if j < 0 {
				break
			}
			start := i + j
			end := strings.Index(body[start:], "}")
			if end < 0 {
				break
			}
			if !strings.Contains(body[start:start+end], "RepoID") {
				t.Errorf("a store call omits RepoID and would read across repositories:\n%s", body[start:start+end])
			}
			i = start + len(call)
		}
	}
}

// Advertising and dispatch are separate gates, and dispatch is the one that faces the model.
//
// The tool NAME is model-chosen: the fixer's system prompt lists the whole suite in prose, so a
// registry built without a chunk store is still asked for search_code. Before this guard that call
// reached SearchLexical on a nil store and crashed the process — observed on a live fixer round.
// An unavailable tool must answer with an error the loop can hand back, exactly like an unknown one.
func TestInvoke_unadvertisedToolIsRefusedNotCrashed(t *testing.T) {
	ctx := context.Background()

	noChunks := &Registry{Meta: &fakeMeta{}, RepoID: "r"}
	for _, d := range noChunks.Definitions() {
		if d.Name == ToolSearchCode {
			t.Fatal("search_code must not be advertised without a chunk store")
		}
	}
	out, err := noChunks.Invoke(ctx, ToolSearchCode, json.RawMessage(`{"query":"anything"}`))
	if err == nil {
		t.Fatalf("search_code without a chunk store returned %q instead of an error", out)
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("the error should tell the model the tool is unavailable: %v", err)
	}

	noMeta := &Registry{Chunks: &fakeChunks{}, RepoID: "r"}
	for _, name := range []string{ToolGetSymbol, ToolExpandSymbol, ToolFindTestsFor} {
		if _, err := noMeta.Invoke(ctx, name, json.RawMessage(`{"fq_name":"a.B"}`)); err == nil {
			t.Errorf("%s without a symbol index must be refused", name)
		}
	}
}
