package indexer

import (
	"encoding/json"
	"testing"
)

func TestMergeStructuredSignatureIntoChunkMetadata_routeAndVisibility(t *testing.T) {
	meta := map[string]interface{}{
		"symbol_kind": "API_ROUTE",
		"fq_name":     "GET:/api/x",
	}
	sig := []byte(`{"framework":"spring_web","http_method":"GET","path_pattern":"/api/x","handler_fq":"pkg.C#m","class":"pkg.C","visibility":"public"}`)
	mergeStructuredSignatureIntoChunkMetadata(meta, sig)
	if meta["framework"] != "spring_web" {
		t.Errorf("framework = %v", meta["framework"])
	}
	if meta["http_method"] != "GET" || meta["path_pattern"] != "/api/x" {
		t.Errorf("flat route fields: %#v", meta)
	}
	if meta["class_fq"] != "pkg.C" {
		t.Errorf("class_fq = %v", meta["class_fq"])
	}
	if meta["handler_fq"] != "pkg.C#m" {
		t.Errorf("handler_fq = %v", meta["handler_fq"])
	}
	rh, ok := meta["route_hints"].(map[string]interface{})
	if !ok {
		t.Fatalf("route_hints missing: %#v", meta["route_hints"])
	}
	if rh["http_method"] != "GET" || rh["path_pattern"] != "/api/x" {
		t.Errorf("route_hints = %#v", rh)
	}
	// visibility on route symbol is unusual but should copy if present
	if meta["visibility"] != "public" {
		t.Errorf("visibility = %v", meta["visibility"])
	}
}

func TestMergeStructuredSignatureIntoChunkMetadata_deriveExported(t *testing.T) {
	meta := map[string]interface{}{"symbol_kind": "method"}
	mergeStructuredSignatureIntoChunkMetadata(meta, []byte(`{"visibility":"private"}`))
	if exp, ok := meta["exported"].(bool); !ok || exp {
		t.Errorf("exported = %v; want false", meta["exported"])
	}
	meta2 := map[string]interface{}{"symbol_kind": "method"}
	mergeStructuredSignatureIntoChunkMetadata(meta2, []byte(`{"visibility":"public"}`))
	if exp, ok := meta2["exported"].(bool); !ok || !exp {
		t.Errorf("exported = %v; want true", meta2["exported"])
	}
}

func TestMergeStructuredSignatureIntoChunkMetadata_staticAndExplicitExported(t *testing.T) {
	meta := map[string]interface{}{"a": 1}
	mergeStructuredSignatureIntoChunkMetadata(meta, []byte(`{"exported":true,"is_static":true}`))
	if meta["static"] != true {
		t.Errorf("static = %v", meta["static"])
	}
	if meta["exported"] != true {
		t.Errorf("exported = %v", meta["exported"])
	}
}

func TestMergeStructuredSignatureIntoChunkMetadata_preservesIndexerKeys(t *testing.T) {
	meta := map[string]interface{}{
		"module":  "m1",
		"fq_name": "keep",
	}
	mergeStructuredSignatureIntoChunkMetadata(meta, []byte(`{"fq_name":"from_sig","framework":"nest"}`))
	if meta["fq_name"] != "keep" {
		t.Errorf("fq_name should not be overwritten from signature: %v", meta["fq_name"])
	}
	if meta["framework"] != "nest" {
		t.Errorf("framework = %v", meta["framework"])
	}
}

func TestMergeStructuredSignatureIntoChunkMetadata_invalidJSON(t *testing.T) {
	meta := map[string]interface{}{"x": 1}
	mergeStructuredSignatureIntoChunkMetadata(meta, []byte(`not json`))
	if len(meta) != 1 {
		t.Errorf("meta mutated: %#v", meta)
	}
}

func TestChunkMetadataMap_mergesSignature(t *testing.T) {
	sym := ParsedSymbol{
		Kind:          "method",
		FQName:        "pkg.T#run",
		StartLine:     1,
		EndLine:       1,
		SignatureJSON: []byte(`{"visibility":"public","signature":"void run()","static":true}`),
	}
	meta := chunkMetadataMap(sym, "T.java", "", "definition", 0, "", nil, "pkg")
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["visibility"] != "public" {
		t.Errorf("visibility = %v", m["visibility"])
	}
	if exp, ok := m["exported"].(bool); !ok || !exp {
		t.Errorf("exported = %v", m["exported"])
	}
	if st, ok := m["static"].(bool); !ok || !st {
		t.Errorf("static = %v", m["static"])
	}
}
