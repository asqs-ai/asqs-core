package retrieval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// --- Mocks for Retrieve tests ---

type mockMetaReaderForRetrieve struct {
	symbols      map[string]*metadata.Symbol
	byFile       map[string][]*metadata.Symbol
	edgesFrom    map[string][]*metadata.Edge
	edgesTo      map[string][]*metadata.Edge
	files        map[string]*metadata.File
	byFQName     map[string][]*metadata.Symbol
	bySimpleName map[string][]*metadata.Symbol // exercises the optional typeSimpleNameResolver path
}

// ListSymbolsByTypeSimpleName makes the mock satisfy typeSimpleNameResolver so the cross-package
// resolution fallback can be unit-tested.
func (m *mockMetaReaderForRetrieve) ListSymbolsByTypeSimpleName(ctx context.Context, repoID, simpleName string, limit int) ([]*metadata.Symbol, error) {
	if m.bySimpleName == nil {
		return nil, nil
	}
	return m.bySimpleName[simpleName], nil
}

func (m *mockMetaReaderForRetrieve) GetSymbolByID(ctx context.Context, repoID, id string) (*metadata.Symbol, error) {
	return m.symbols[id], nil
}

func (m *mockMetaReaderForRetrieve) ListSymbolsByFile(ctx context.Context, repoID, file string) ([]*metadata.Symbol, error) {
	return m.byFile[file], nil
}

func (m *mockMetaReaderForRetrieve) GetEdgesFrom(ctx context.Context, repoID, callerSymbolID string) ([]*metadata.Edge, error) {
	return m.edgesFrom[callerSymbolID], nil
}

func (m *mockMetaReaderForRetrieve) GetEdgesTo(ctx context.Context, repoID, calleeSymbolID string) ([]*metadata.Edge, error) {
	if m.edgesTo == nil {
		return nil, nil
	}
	return m.edgesTo[calleeSymbolID], nil
}

func (m *mockMetaReaderForRetrieve) GetFile(ctx context.Context, repoID, file string) (*metadata.File, error) {
	if m.files == nil {
		return nil, nil
	}
	return m.files[file], nil
}

func (m *mockMetaReaderForRetrieve) ListSymbolsByFQName(ctx context.Context, repoID, fqName string) ([]*metadata.Symbol, error) {
	if m.byFQName == nil {
		return nil, nil
	}
	return m.byFQName[fqName], nil
}

// TestResolveTypeNameToSymbols_CrossPackageFallback locks the core fix for the "domain_models=0 /
// deps=0" quality gap: a referenced type in a DIFFERENT package than the target must still resolve
// (the old code only tried <fileModule>.<name>, missing cross-package types entirely).
func TestResolveTypeNameToSymbols_CrossPackageFallback(t *testing.T) {
	order := &metadata.Symbol{ID: "s_order", Kind: "class", FQName: "com.example.model.Order", File: "model/Order.java"}
	m := &mockMetaReaderForRetrieve{
		byFQName:     map[string][]*metadata.Symbol{"com.example.model.Order": {order}},
		bySimpleName: map[string][]*metadata.Symbol{"Order": {order}},
	}
	// Caller lives in com.example.api → same-package guess (com.example.api.Order) misses; the
	// repo-wide simple-name fallback resolves it.
	got := resolveTypeNameToSymbols(context.Background(), m, "", "Order", "com.example.api")
	if len(got) != 1 || got[0].ID != "s_order" {
		t.Fatalf("cross-package resolve: want [s_order], got %+v", got)
	}
	// Fully-qualified name resolves directly.
	if got := resolveTypeNameToSymbols(context.Background(), m, "", "com.example.model.Order", "com.example.api"); len(got) != 1 {
		t.Fatalf("qualified resolve failed: %+v", got)
	}
	// Unknown type resolves to nothing (not a false positive).
	if got := resolveTypeNameToSymbols(context.Background(), m, "", "Nonexistent", "com.example.api"); got != nil {
		t.Fatalf("unknown type should be nil, got %+v", got)
	}
}

// TestResolveTypeNameToSymbols_NoFallbackWhenUnsupported: a MetaReader that does NOT implement the
// optional resolver must not panic and simply returns nothing for an unresolved name.
func TestResolveTypeNameToSymbols_NoFallbackWhenUnsupported(t *testing.T) {
	var meta MetaReader = &minimalMetaReader{}
	if got := resolveTypeNameToSymbols(context.Background(), meta, "", "Order", "com.example.api"); got != nil {
		t.Fatalf("want nil without resolver capability, got %+v", got)
	}
}

// TestReferencedTypeNames_GathersAllSourcesInPriorityOrder verifies signature → fields → body order
// and de-duplication, so the per-gap budget fills with the highest-signal types first.
func TestReferencedTypeNames_GathersAllSourcesInPriorityOrder(t *testing.T) {
	target := &metadata.Symbol{
		ID: "t", Kind: "method", FQName: "com.example.api.OrderResponse#from", File: "api/OrderResponse.java",
		StartLine: 20, EndLine: 25, SignatureJSON: []byte(`{"signature":"OrderResponse from(Order order)"}`),
	}
	cls := &metadata.Symbol{ID: "c", Kind: "class", FQName: "com.example.api.OrderResponse", File: "api/OrderResponse.java", StartLine: 1, EndLine: 40}
	field := &metadata.Symbol{
		ID: "f", Kind: "field", FQName: "com.example.api.OrderResponse#clock", File: "api/OrderResponse.java",
		StartLine: 3, EndLine: 3, SignatureJSON: []byte(`{"signature":"private final Clock clock"}`),
	}
	m := &mockMetaReaderForRetrieve{byFile: map[string][]*metadata.Symbol{"api/OrderResponse.java": {cls, field, target}}}
	body := &embeddings.Chunk{Content: "return new OrderResponse(order.getId(), Status.OK);"}
	got := referencedTypeNames(context.Background(), m, "", target, &SymbolChunk{Symbol: cls}, body)

	idx := func(s string) int {
		for i, v := range got {
			if v == s {
				return i
			}
		}
		return -1
	}
	for _, want := range []string{"Order", "Clock", "Status"} {
		if idx(want) < 0 {
			t.Fatalf("referencedTypeNames missing %q: %v", want, got)
		}
	}
	// Signature types precede field collaborators, which precede body-only types.
	if !(idx("Order") < idx("Clock") && idx("Clock") < idx("Status")) {
		t.Fatalf("priority order violated (sig<field<body): %v", got)
	}
	// "String"-style stdlib noise from the body must not leak in.
	if idx("String") >= 0 {
		t.Fatalf("stdlib noise leaked: %v", got)
	}
}

// TestFieldDeclaredTypeNames_ReadsTypeKey locks the precise root cause from the audit: Java field
// symbols carry the declared type under the "type" key ({"type":"OrderService"}), which the old
// "signature"-only reader missed. Also covers generics and the "signature" fallback shape.
func TestFieldDeclaredTypeNames_ReadsTypeKey(t *testing.T) {
	if got := fieldDeclaredTypeNames(&metadata.Symbol{Kind: "field", SignatureJSON: []byte(`{"type":"OrderService"}`)}); len(got) != 1 || got[0] != "OrderService" {
		t.Fatalf(`"type" key: got %v, want [OrderService]`, got)
	}
	// Generic field type → both the container and inner type are extracted.
	got := fieldDeclaredTypeNames(&metadata.Symbol{Kind: "field", SignatureJSON: []byte(`{"type":"Optional<Order>"}`)})
	has := func(s string) bool {
		for _, v := range got {
			if v == s {
				return true
			}
		}
		return false
	}
	if !has("Optional") || !has("Order") {
		t.Fatalf("generic field type: got %v, want Optional and Order", got)
	}
	// "signature"-shape fallback still works.
	if got := fieldDeclaredTypeNames(&metadata.Symbol{Kind: "field", SignatureJSON: []byte(`{"signature":"private final Clock clock"}`)}); len(got) != 1 || got[0] != "Clock" {
		t.Fatalf(`"signature" fallback: got %v, want [Clock]`, got)
	}
}

// TestReferencedTypeNames_CollaboratorFromClassChunk locks the fix for the audit finding that
// constructor-injected collaborators (e.g. OrderController's OrderService) never surfaced: field
// symbols lack a type-bearing signature, so the collaborator must be recovered from the enclosing
// class CHUNK (field declaration + constructor params).
func TestReferencedTypeNames_CollaboratorFromClassChunk(t *testing.T) {
	target := &metadata.Symbol{
		ID: "t", Kind: "method", FQName: "com.example.api.OrderController#getById", File: "api/OrderController.java",
		StartLine: 20, EndLine: 25, SignatureJSON: []byte(`{"signature":"ResponseEntity getById(Long id)"}`),
	}
	cls := &metadata.Symbol{ID: "c", Kind: "class", FQName: "com.example.api.OrderController", File: "api/OrderController.java", StartLine: 1, EndLine: 40}
	// Field symbol with NO type-bearing signature (mirrors the real Java index) → field path yields
	// nothing; the class chunk is what recovers the collaborator.
	field := &metadata.Symbol{ID: "f", Kind: "field", FQName: "com.example.api.OrderController#orderService", File: "api/OrderController.java", StartLine: 3, EndLine: 3}
	m := &mockMetaReaderForRetrieve{byFile: map[string][]*metadata.Symbol{"api/OrderController.java": {cls, field, target}}}
	classChunk := &embeddings.Chunk{Content: "public class OrderController {\n  private final OrderService orderService;\n  public OrderController(OrderService orderService) { this.orderService = orderService; }\n}"}
	got := referencedTypeNames(context.Background(), m, "", target, &SymbolChunk{Symbol: cls, Chunk: classChunk}, nil)
	found := false
	for _, n := range got {
		if n == "OrderService" {
			found = true
		}
	}
	if !found {
		t.Fatalf("OrderService (constructor-injected collaborator) not recovered from class chunk: %v", got)
	}
}

// TestIsLikelyCollaborator pins the collaborator-vs-value classification used to split the rendered
// sections (mock these vs construct/assert).
func TestIsLikelyCollaborator(t *testing.T) {
	if !isLikelyCollaborator(&metadata.Symbol{Kind: "class", FQName: "com.example.service.OrderService"}) {
		t.Error("Service-suffixed class should classify as collaborator")
	}
	if !isLikelyCollaborator(&metadata.Symbol{Kind: "interface", FQName: "com.example.Repo"}) {
		t.Error("interface should classify as collaborator")
	}
	if isLikelyCollaborator(&metadata.Symbol{Kind: "class", FQName: "com.example.model.Order"}) {
		t.Error("plain model class should classify as value/domain type, not collaborator")
	}
}

// minimalMetaReader implements MetaReader but NOT typeSimpleNameResolver.
type minimalMetaReader struct{}

func (minimalMetaReader) GetSymbolByID(context.Context, string, string) (*metadata.Symbol, error) {
	return nil, nil
}
func (minimalMetaReader) ListSymbolsByFile(context.Context, string, string) ([]*metadata.Symbol, error) {
	return nil, nil
}
func (minimalMetaReader) GetEdgesFrom(context.Context, string, string) ([]*metadata.Edge, error) {
	return nil, nil
}
func (minimalMetaReader) GetEdgesTo(context.Context, string, string) ([]*metadata.Edge, error) {
	return nil, nil
}
func (minimalMetaReader) GetFile(context.Context, string, string) (*metadata.File, error) {
	return nil, nil
}
func (minimalMetaReader) ListSymbolsByFQName(context.Context, string, string) ([]*metadata.Symbol, error) {
	return nil, nil
}

// --- Chunk-reader mock (shared by the similar-reference and omit-embedding tests) ---

type mockChunkReaderForRetrieve struct {
	listBySymbol map[string][]embeddings.Chunk
	listByParent map[string][]embeddings.Chunk
	listByType   map[string][]embeddings.Chunk
	listAll      []embeddings.Chunk
	searchResult []embeddings.SearchResult
	// searchByType, when non-nil, is used by Search keyed by opts.ChunkType (overrides searchResult).
	searchByType map[string][]embeddings.SearchResult
}

func (m *mockChunkReaderForRetrieve) List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
	if opts.ParentSymbolID != "" && m.listByParent != nil {
		out := m.listByParent[opts.ParentSymbolID]
		if opts.Limit > 0 && len(out) > opts.Limit {
			return append([]embeddings.Chunk(nil), out[:opts.Limit]...), nil
		}
		return append([]embeddings.Chunk(nil), out...), nil
	}
	if opts.SymbolID != "" && m.listBySymbol != nil {
		return m.listBySymbol[opts.SymbolID], nil
	}
	if opts.ChunkType != "" && m.listByType != nil {
		return m.listByType[opts.ChunkType], nil
	}
	if m.listAll != nil {
		return m.listAll, nil
	}
	return nil, nil
}

func (m *mockChunkReaderForRetrieve) Search(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	if m.searchByType != nil {
		return m.searchByType[opts.ChunkType], nil
	}
	return m.searchResult, nil
}

func TestGatherSimilarReferenceChunks_segmentedPerFileCapPreservesBreadth(t *testing.T) {
	emb := make([]float32, 8)
	emb[0] = 1
	metaSeg := func(i, n int) []byte {
		b, _ := json.Marshal(map[string]interface{}{
			"embedding_segment_index": i,
			"embedding_segment_count": n,
		})
		return b
	}
	search := []embeddings.SearchResult{
		{Chunk: embeddings.Chunk{ID: "a0", File: "HugeA.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "a0", Embedding: emb, MetadataJSON: metaSeg(0, 6)}},
		{Chunk: embeddings.Chunk{ID: "a1", File: "HugeA.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "a1", Embedding: emb, MetadataJSON: metaSeg(1, 6)}},
		{Chunk: embeddings.Chunk{ID: "a2", File: "HugeA.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "a2", Embedding: emb, MetadataJSON: metaSeg(2, 6)}},
		{Chunk: embeddings.Chunk{ID: "a3", File: "HugeA.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "a3", Embedding: emb, MetadataJSON: metaSeg(3, 6)}},
		{Chunk: embeddings.Chunk{ID: "a4", File: "HugeA.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "a4", Embedding: emb, MetadataJSON: metaSeg(4, 6)}},
		{Chunk: embeddings.Chunk{ID: "a5", File: "HugeA.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "a5", Embedding: emb, MetadataJSON: metaSeg(5, 6)}},
		{Chunk: embeddings.Chunk{ID: "b", File: "OtherB.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "b", Embedding: emb}},
		{Chunk: embeddings.Chunk{ID: "c", File: "OtherC.test.ts", ChunkType: "test", RepoID: "r", Lang: "typescript", Content: "c", Embedding: emb}},
	}
	mock := &mockChunkReaderForRetrieve{searchByType: map[string][]embeddings.SearchResult{"test": search}}
	target := &embeddings.Chunk{File: "app.ts", Lang: "typescript", RepoID: "r", Embedding: emb}
	got := gatherSimilarReferenceChunks(context.Background(), mock, target, ContextRequest{
		Lang: "typescript", RepoID: "r", Profile: ProfileJavaUnit, MaxSimilarTests: 4,
	}, "")
	if len(got) != 4 {
		t.Fatalf("len similar = %d; want 4", len(got))
	}
	perFile := map[string]int{}
	for _, ch := range got {
		if ch != nil {
			perFile[ch.File]++
		}
	}
	if perFile["HugeA.test.ts"] > 2 {
		t.Fatalf("huge file dominates results: %#v", perFile)
	}
	if perFile["OtherB.test.ts"] == 0 || perFile["OtherC.test.ts"] == 0 {
		t.Fatalf("expected cross-file breadth preserved; got %#v", perFile)
	}
}

// The abstention gate cosines SimilarTests against the target, so the chunks feeding it must carry
// embeddings. OmitEmbedding belongs on the fixture/config path ONLY; if it ever leaks onto the
// SimilarTests path (listChunksByType / gatherSimilarReferenceChunks), every cosine reads zero and
// generation abstains spuriously. This pins which call sets it and which must not.
func TestOmitEmbedding_staysOffTheSimilarTestsPath(t *testing.T) {
	var typeListOpts, patternListOpts []embeddings.ListOptions
	rec := &recordingChunkReader{onList: func(o embeddings.ListOptions) {
		if o.ChunkType != "" {
			typeListOpts = append(typeListOpts, o)
		} else {
			patternListOpts = append(patternListOpts, o)
		}
	}}

	_ = listChunksByType(context.Background(), rec, "r", "java", "test", 5, "")
	for _, o := range typeListOpts {
		if o.OmitEmbedding {
			t.Fatal("listChunksByType set OmitEmbedding; SimilarTests reach the abstention gate, which cosines them — every cosine would read zero and generation would abstain spuriously")
		}
	}

	_ = listChunksByPathPattern(context.Background(), rec, "r", "java", []string{"fixture"}, 5)
	if len(patternListOpts) == 0 {
		t.Fatal("listChunksByPathPattern issued no List call")
	}
	for _, o := range patternListOpts {
		if !o.OmitEmbedding {
			t.Fatal("listChunksByPathPattern must omit embeddings: fixtures/config are rendered as text and never scored")
		}
	}
}

type recordingChunkReader struct {
	onList func(embeddings.ListOptions)
}

func (r *recordingChunkReader) List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error) {
	if r.onList != nil {
		r.onList(opts)
	}
	return nil, nil
}

func (r *recordingChunkReader) Search(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error) {
	return nil, nil
}
