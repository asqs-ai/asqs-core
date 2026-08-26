package indexer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkFromParsedFile_emptySymbols(t *testing.T) {
	parsed := &ParsedFile{
		Path:    "pkg/Foo.java",
		Lang:    "java",
		Source:  "public class Foo {}",
		Symbols: nil,
		Edges:   nil,
	}
	cfg := DefaultChunkConfig()
	sanitize := DefaultSanitizeOptions()
	plans := ChunkFromParsedFile(parsed, "org/repo", "", cfg, sanitize)
	if len(plans) != 0 {
		t.Errorf("empty symbols: got %d plans; want 0", len(plans))
	}
}

func TestChunkFromParsedFile_singleSymbol(t *testing.T) {
	source := "line1\nline2\nline3\nline4\nline5\n"
	parsed := &ParsedFile{
		Path:   "pkg/Foo.java",
		Lang:   "java",
		Source: source,
		Symbols: []ParsedSymbol{
			{Kind: "method", FQName: "com.Foo.run", StartLine: 2, EndLine: 4},
		},
		Edges: nil,
	}
	cfg := DefaultChunkConfig()
	cfg.EnrichChunkContent = false
	sanitize := DefaultSanitizeOptions()
	plans := ChunkFromParsedFile(parsed, "org/repo", "", cfg, sanitize)
	if len(plans) != 1 {
		t.Fatalf("single symbol: got %d plans; want 1", len(plans))
	}
	p := plans[0]
	if p.Content != "line2\nline3\nline4" {
		t.Errorf("Content = %q; want line2\\nline3\\nline4", p.Content)
	}
	if p.File != "pkg/Foo.java" || p.Lang != "java" || p.RepoID != "org/repo" {
		t.Errorf("File=%q Lang=%q RepoID=%q", p.File, p.Lang, p.RepoID)
	}
	if p.ChunkType != "definition" {
		t.Errorf("ChunkType = %q; want definition", p.ChunkType)
	}
	if p.StartLine != 2 || p.EndLine != 4 {
		t.Errorf("StartLine=%d EndLine=%d; want 2, 4", p.StartLine, p.EndLine)
	}
}

func TestChunkFromParsedFile_multipleSymbols(t *testing.T) {
	source := "a\nb\nc\nd\ne\nf\n"
	parsed := &ParsedFile{
		Path:   "X.java",
		Lang:   "java",
		Source: source,
		Symbols: []ParsedSymbol{
			{Kind: "class", FQName: "X", StartLine: 1, EndLine: 6},
			{Kind: "method", FQName: "X.m1", StartLine: 2, EndLine: 3},
			{Kind: "method", FQName: "X.m2", StartLine: 5, EndLine: 5},
		},
		Edges: nil,
	}
	cfg := DefaultChunkConfig()
	cfg.EnrichChunkContent = false
	sanitize := DefaultSanitizeOptions()
	plans := ChunkFromParsedFile(parsed, "r", "", cfg, sanitize)
	if len(plans) != 3 {
		t.Fatalf("got %d plans; want 3", len(plans))
	}
	if plans[0].Content != "a\nb\nc\nd\ne\nf" {
		t.Errorf("first plan content = %q", plans[0].Content)
	}
	if plans[1].Content != "b\nc" {
		t.Errorf("second plan content = %q", plans[1].Content)
	}
	if plans[2].Content != "e" {
		t.Errorf("third plan content = %q", plans[2].Content)
	}
}

func TestChunkFromParsedFile_largeSymbolSplit(t *testing.T) {
	// Build source with many lines so one symbol exceeds MaxTokens
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = "public void doSomething() { return; }"
	}
	source := ""
	for i, l := range lines {
		if i > 0 {
			source += "\n"
		}
		source += l
	}
	parsed := &ParsedFile{
		Path:   "Big.java",
		Lang:   "java",
		Source: source,
		Symbols: []ParsedSymbol{
			{Kind: "method", FQName: "Big.huge", StartLine: 1, EndLine: 500},
		},
		Edges: nil,
	}
	cfg := DefaultChunkConfig()
	cfg.MaxTokens = 50
	cfg.EnrichChunkContent = false
	sanitize := DefaultSanitizeOptions()
	plans := ChunkFromParsedFile(parsed, "repo", "", cfg, sanitize)
	if len(plans) < 2 {
		t.Fatalf("large symbol should be split: got %d plans; want >= 2", len(plans))
	}
	for i, p := range plans {
		if p.StartLine >= p.EndLine && len(plans) > 1 {
			t.Errorf("plan %d: StartLine %d >= EndLine %d", i, p.StartLine, p.EndLine)
		}
		if p.File != "Big.java" || p.RepoID != "repo" {
			t.Errorf("plan %d: File or RepoID wrong", i)
		}
	}
}

func TestSymbolKindToChunkType_e2eKinds(t *testing.T) {
	tests := []struct {
		kind string
		want string
	}{
		{"API_ROUTE", "route"},
		{"PAGE_ROUTE", "route"},
		{"USER_FLOW", "flow"},
		{"E2E_SPEC", "e2e_pattern"},
		{"PAGE_OBJECT", "e2e_pattern"},
		{"FORM", "page"},
		{"TEST_SELECTOR", "page"},
		{"UI_TEST_HOOK", "page"},
		{"STATIC_TEMPLATE", "page"},
		{"API_CLIENT_REQUEST", "api_contract"},
		{"method", "definition"},
		{"NEST_CONTROLLER", "definition"},
		{"NEST_MODULE", "definition"},
		{"NEST_PROVIDER", "definition"},
		{"NEST_GUARD", "definition"},
		{"DTO", "definition"},
		{"ANGULAR_TEMPLATE_BINDING", "definition"},
		{"ENTRYPOINT", "definition"},
		{"CLI_COMMAND", "definition"},
		{"BUILTIN_MODULE_USE", "definition"},
		{"REACT_HOOK", "definition"},
		{"ANGULAR_COMPONENT", "definition"},
		{"ANGULARJS_MODULE", "definition"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			// symbolKindToChunkType is exercised via ChunkFromParsedFile
			parsed := &ParsedFile{
				Path:   "x.ts",
				Lang:   "typescript",
				Source: "l1\nl2\nl3\n",
				Symbols: []ParsedSymbol{
					{Kind: tt.kind, FQName: "x", StartLine: 1, EndLine: 3},
				},
			}
			cfg := DefaultChunkConfig()
			cfg.EnrichChunkContent = false
			plans := ChunkFromParsedFile(parsed, "r", "", cfg, DefaultSanitizeOptions())
			if len(plans) != 1 {
				t.Fatalf("plans: %d", len(plans))
			}
			if plans[0].ChunkType != tt.want {
				t.Errorf("ChunkType = %q; want %q", plans[0].ChunkType, tt.want)
			}
		})
	}
}

func TestChunkFromParsedFile_zeroMinTokensUsesDefault(t *testing.T) {
	parsed := &ParsedFile{
		Path:    "x.java",
		Lang:    "java",
		Source:  "x",
		Symbols: []ParsedSymbol{{Kind: "method", FQName: "X.m", StartLine: 1, EndLine: 1}},
	}
	cfg := ChunkConfig{MinTokens: 0, MaxTokens: 0}
	sanitize := DefaultSanitizeOptions()
	plans := ChunkFromParsedFile(parsed, "r", "", cfg, sanitize)
	if len(plans) != 1 {
		t.Errorf("zero config should use DefaultChunkConfig: got %d plans", len(plans))
	}
}

func TestChunkFromParsedFile_enrichedHeaderAndSignatureHints(t *testing.T) {
	sig, err := json.Marshal(map[string]string{"path_pattern": "/api/items", "http_method": "GET"})
	if err != nil {
		t.Fatal(err)
	}
	parsed := &ParsedFile{
		Path:   "src/h.ts",
		Lang:   "typescript",
		Source: "export const x = 1\n",
		Symbols: []ParsedSymbol{
			{Kind: "API_ROUTE", FQName: "pkg.Handler.getItems", StartLine: 1, EndLine: 1, SignatureJSON: sig},
		},
	}
	cfg := DefaultChunkConfig()
	cfg.EnrichChunkContent = true
	plans := ChunkFromParsedFile(parsed, "org/r", "", cfg, DefaultSanitizeOptions())
	if len(plans) != 1 {
		t.Fatalf("plans: %d", len(plans))
	}
	c := plans[0].Content
	if !strings.HasPrefix(c, "[symbol_kind=") {
		t.Fatalf("expected header prefix, got %q", c)
	}
	if !strings.Contains(c, "fq_name=pkg.Handler.getItems") {
		t.Errorf("missing fq in header: %q", c)
	}
	if !strings.Contains(c, "path_pattern=") || !strings.Contains(c, "http_method=") {
		t.Errorf("expected signature hints in content: %q", c)
	}
}

func TestChunkFromParsedFile_moduleInMetadataJSON(t *testing.T) {
	parsed := &ParsedFile{
		Path:   "src/T.java",
		Lang:   "java",
		Module: "com.acme.core",
		Source: "class T {}\n",
		Symbols: []ParsedSymbol{
			{Kind: "class", FQName: "com.acme.core.T", StartLine: 1, EndLine: 1},
		},
	}
	cfg := DefaultChunkConfig()
	cfg.EnrichChunkContent = false
	cfg.MergeSmallSymbols = false
	plans := ChunkFromParsedFile(parsed, "r", "", cfg, DefaultSanitizeOptions())
	if len(plans) != 1 {
		t.Fatalf("plans: %d", len(plans))
	}
	var m map[string]interface{}
	if err := json.Unmarshal(plans[0].MetadataJSON, &m); err != nil {
		t.Fatal(err)
	}
	if m["module"] != "com.acme.core" {
		t.Errorf("module = %v; want com.acme.core", m["module"])
	}
}

func TestChunkFromParsedFile_parentFQInMetadataJSON(t *testing.T) {
	parsed := &ParsedFile{
		Path:   "mod.ts",
		Lang:   "typescript",
		Source: "child\n",
		Symbols: []ParsedSymbol{
			{Kind: "nest_guard", FQName: "App.AuthGuard", StartLine: 1, EndLine: 1},
		},
		Edges: []ParsedEdge{
			{CallerFQName: "AppModule", CalleeFQName: "App.AuthGuard", EdgeType: "CONTAINS"},
		},
	}
	cfg := DefaultChunkConfig()
	cfg.EnrichChunkContent = false
	cfg.MergeSmallSymbols = false
	plans := ChunkFromParsedFile(parsed, "r", "", cfg, DefaultSanitizeOptions())
	if len(plans) != 1 {
		t.Fatalf("plans: %d", len(plans))
	}
	var m map[string]interface{}
	if err := json.Unmarshal(plans[0].MetadataJSON, &m); err != nil {
		t.Fatal(err)
	}
	if m["parent_fq"] != "AppModule" {
		t.Errorf("parent_fq = %v; want AppModule", m["parent_fq"])
	}
	cfg.EnrichChunkContent = true
	plans2 := ChunkFromParsedFile(parsed, "r", "", cfg, DefaultSanitizeOptions())
	if len(plans2) != 1 {
		t.Fatalf("enrich on: plans %d", len(plans2))
	}
	if !strings.Contains(plans2[0].Content, "[parent_fq=AppModule]") {
		t.Errorf("enriched content should include parent_fq: %q", plans2[0].Content)
	}
}

func TestChunkFromParsedFile_mergeAdjacentNestGuards(t *testing.T) {
	parsed := &ParsedFile{
		Path:   "g.ts",
		Lang:   "typescript",
		Source: "a\nb\n",
		Symbols: []ParsedSymbol{
			{Kind: "nest_guard", FQName: "G1", StartLine: 1, EndLine: 1},
			{Kind: "nest_guard", FQName: "G2", StartLine: 2, EndLine: 2},
		},
	}
	cfg := DefaultChunkConfig()
	cfg.EnrichChunkContent = false
	cfg.MergeSmallSymbols = true
	plans := ChunkFromParsedFile(parsed, "r", "", cfg, DefaultSanitizeOptions())
	if len(plans) != 1 {
		t.Fatalf("merged: want 1 plan, got %d", len(plans))
	}
	if !strings.Contains(plans[0].Content, "a") || !strings.Contains(plans[0].Content, "b") {
		t.Errorf("content: %q", plans[0].Content)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(plans[0].MetadataJSON, &m); err != nil {
		t.Fatal(err)
	}
	merged, ok := m["merged_symbols"].([]interface{})
	if !ok || len(merged) != 2 {
		t.Fatalf("merged_symbols: %+v", m)
	}
}

func TestChunkFromParsedFile_secondaryRouteManifest(t *testing.T) {
	dir := t.TempDir()
	parsed := &ParsedFile{
		Path:   "src/api.ts",
		Lang:   "typescript",
		Source: "routes\n",
		Symbols: []ParsedSymbol{
			{Kind: "API_ROUTE", FQName: "r.z", StartLine: 1, EndLine: 1},
			{Kind: "API_ROUTE", FQName: "r.a", StartLine: 1, EndLine: 1},
		},
	}
	cfg := DefaultChunkConfig()
	cfg.EnableSecondaryChunks = true
	cfg.EnrichChunkContent = false
	plans := ChunkFromParsedFile(parsed, "repo", dir, cfg, DefaultSanitizeOptions())
	// SecondaryRole was deleted from ChunkPlan (L-1): nothing read it, and the role reaches
	// chunk_metadata via chunk_header instead. Identify the manifest by the chunk_role that
	// actually ships.
	var manifest *ChunkPlan
	for i := range plans {
		if strings.Contains(string(plans[i].MetadataJSON), `"chunk_role":"route_manifest"`) ||
			plans[i].ChunkType == "route_manifest" {
			manifest = &plans[i]
			break
		}
	}
	if manifest == nil {
		t.Fatalf("no route_manifest in %d plans", len(plans))
	}
	if !strings.Contains(manifest.Content, "r.a") || !strings.Contains(manifest.Content, "r.z") {
		t.Errorf("manifest body: %q", manifest.Content)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(manifest.MetadataJSON, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["chunk_role"] != "route_manifest" {
		t.Errorf("meta: %+v", meta)
	}
}

func TestChunkFromParsedFile_secondaryAngularTemplate(t *testing.T) {
	dir := t.TempDir()
	appDir := filepath.Join(dir, "app")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	tplPath := filepath.Join(appDir, "hello.component.html")
	if err := os.WriteFile(tplPath, []byte("<p>hello</p>\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sig, err := json.Marshal(map[string]string{"path": "hello.component.html", "component_sym": "app.HelloCmp"})
	if err != nil {
		t.Fatal(err)
	}
	parsed := &ParsedFile{
		Path:   "app/hello.component.ts",
		Lang:   "typescript",
		Source: "export class Hello {}\n",
		Symbols: []ParsedSymbol{
			{Kind: "ANGULAR_TEMPLATE", FQName: "app.HelloCmp.template", StartLine: 1, EndLine: 1, SignatureJSON: sig},
		},
	}
	cfg := DefaultChunkConfig()
	cfg.EnableSecondaryChunks = true
	cfg.EnrichChunkContent = false
	plans := ChunkFromParsedFile(parsed, "r", dir, cfg, DefaultSanitizeOptions())
	var tmpl *ChunkPlan
	for i := range plans {
		if strings.Contains(string(plans[i].MetadataJSON), `"chunk_role":"angular_template_file"`) ||
			plans[i].ChunkType == "angular_template_file" {
			tmpl = &plans[i]
			break
		}
	}
	if tmpl == nil {
		t.Fatalf("no angular_template_file in %d plans", len(plans))
	}
	if !strings.Contains(tmpl.Content, "<p>hello</p>") {
		t.Errorf("template chunk: %q", tmpl.Content)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(tmpl.MetadataJSON, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["chunk_role"] != "angular_template_file" {
		t.Errorf("meta: %+v", meta)
	}
}

func TestBuildRouteManifestBody_truncates(t *testing.T) {
	routes := make([]string, 40)
	for i := range routes {
		routes[i] = strings.Repeat("z", 30)
	}
	body, omitted := buildRouteManifestBody(routes, 120)
	if omitted == 0 {
		t.Fatalf("want omission for huge manifest, got runes=%d", utf8.RuneCountInString(body))
	}
	if !strings.Contains(body, "omitted") {
		t.Fatalf("expected omission footer in %q", body)
	}
	t.Run("fits", func(t *testing.T) {
		b, om := buildRouteManifestBody([]string{"x", "y"}, 100)
		if om != 0 || b != "x\ny" {
			t.Fatalf("got %q omitted=%d", b, om)
		}
	})
}
