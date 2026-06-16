package retrieval

import (
	"context"
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
func (m *mockMetaReaderForRetrieve) ListSymbolsByTypeSimpleName(ctx context.Context, simpleName string, limit int) ([]*metadata.Symbol, error) {
	if m.bySimpleName == nil {
		return nil, nil
	}
	return m.bySimpleName[simpleName], nil
}

func (m *mockMetaReaderForRetrieve) GetSymbolByID(ctx context.Context, id string) (*metadata.Symbol, error) {
	return m.symbols[id], nil
}

func (m *mockMetaReaderForRetrieve) ListSymbolsByFile(ctx context.Context, file string) ([]*metadata.Symbol, error) {
	return m.byFile[file], nil
}

func (m *mockMetaReaderForRetrieve) GetEdgesFrom(ctx context.Context, callerSymbolID string) ([]*metadata.Edge, error) {
	return m.edgesFrom[callerSymbolID], nil
}

func (m *mockMetaReaderForRetrieve) GetEdgesTo(ctx context.Context, calleeSymbolID string) ([]*metadata.Edge, error) {
	if m.edgesTo == nil {
		return nil, nil
	}
	return m.edgesTo[calleeSymbolID], nil
}

func (m *mockMetaReaderForRetrieve) GetFile(ctx context.Context, file string) (*metadata.File, error) {
	if m.files == nil {
		return nil, nil
	}
	return m.files[file], nil
}

func (m *mockMetaReaderForRetrieve) ListSymbolsByFQName(ctx context.Context, fqName string) ([]*metadata.Symbol, error) {
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
	got := resolveTypeNameToSymbols(context.Background(), m, "Order", "com.example.api")
	if len(got) != 1 || got[0].ID != "s_order" {
		t.Fatalf("cross-package resolve: want [s_order], got %+v", got)
	}
	// Fully-qualified name resolves directly.
	if got := resolveTypeNameToSymbols(context.Background(), m, "com.example.model.Order", "com.example.api"); len(got) != 1 {
		t.Fatalf("qualified resolve failed: %+v", got)
	}
	// Unknown type resolves to nothing (not a false positive).
	if got := resolveTypeNameToSymbols(context.Background(), m, "Nonexistent", "com.example.api"); got != nil {
		t.Fatalf("unknown type should be nil, got %+v", got)
	}
}

// TestResolveTypeNameToSymbols_NoFallbackWhenUnsupported: a MetaReader that does NOT implement the
// optional resolver must not panic and simply returns nothing for an unresolved name.
func TestResolveTypeNameToSymbols_NoFallbackWhenUnsupported(t *testing.T) {
	var meta MetaReader = &minimalMetaReader{}
	if got := resolveTypeNameToSymbols(context.Background(), meta, "Order", "com.example.api"); got != nil {
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
	got := referencedTypeNames(context.Background(), m, target, &SymbolChunk{Symbol: cls}, body)

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
	got := referencedTypeNames(context.Background(), m, target, &SymbolChunk{Symbol: cls, Chunk: classChunk}, nil)
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

func (minimalMetaReader) GetSymbolByID(context.Context, string) (*metadata.Symbol, error) {
	return nil, nil
}
func (minimalMetaReader) ListSymbolsByFile(context.Context, string) ([]*metadata.Symbol, error) {
	return nil, nil
}
func (minimalMetaReader) GetEdgesFrom(context.Context, string) ([]*metadata.Edge, error) {
	return nil, nil
}
func (minimalMetaReader) GetEdgesTo(context.Context, string) ([]*metadata.Edge, error) {
	return nil, nil
}
func (minimalMetaReader) GetFile(context.Context, string) (*metadata.File, error) { return nil, nil }
func (minimalMetaReader) ListSymbolsByFQName(context.Context, string) ([]*metadata.Symbol, error) {
	return nil, nil
}
