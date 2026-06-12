package overview

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

func TestDefaultOverviewPath(t *testing.T) {
	if DefaultOverviewPath != "docs/documentation.md" {
		t.Errorf("DefaultOverviewPath = %q; want docs/documentation.md", DefaultOverviewPath)
	}
}

// TestIsOverviewIgnoredPath verifies that output/build/cache and other irrelevant folders
// are ignored when building overview documentation and dependency diagrams.
func TestIsOverviewIgnoredPath(t *testing.T) {
	tests := []struct {
		path   string
		ignore bool
	}{
		// Ignored: top-level output/build/cache dirs
		{"dist/foo.js", true},
		{"dist", true},
		{"target/classes/App.class", true},
		{"target", true},
		{"out/index.js", true},
		{"build/main.js", true},
		{"node_modules/lodash/index.js", true},
		{".git/config", true},
		{".next/server/page.js", true},
		{".nuxt/router.js", true},
		{".output/public/entry.js", true},
		{".nx/cache/some-hash", true},
		{".angular/cache/foo", true},
		{".vite/deps/_metadata.json", true},
		{".turbo/cache", true},
		{".parcel-cache", true},
		{".cache/bar", true},
		{".serverless/cache", true},
		{"coverage/lcov.info", true},
		{".svelte-kit/output", true},
		{".astro/build", true},
		// Ignored: nested under source
		{"src/dist/bundle.js", true},
		{"packages/app/target/classes", true},
		{"frontend/node_modules/react", true},
		{"lib/build/out", true},
		// Not ignored: source paths
		{"", false},
		{"src/main/java/com/example/App.java", false},
		{"src/main/java/App.java", false},
		{"packages/foo/src/index.ts", false},
		{"frontend/src/App.tsx", false},
		{"lib/utils.js", false},
		{"docs/readme.md", false},
		{"distribute.go", false}, // "dist" is not a path segment alone
		{"targeting/foo.js", false},
	}
	for _, tt := range tests {
		got := isOverviewIgnoredPath(tt.path)
		if got != tt.ignore {
			t.Errorf("isOverviewIgnoredPath(%q) = %v; want %v", tt.path, got, tt.ignore)
		}
	}
}

func TestBuildOverviewContext_nilMeta(t *testing.T) {
	_, err := BuildOverviewContext(context.Background(), nil, "java")
	if err == nil {
		t.Fatal("BuildOverviewContext(nil meta): want error")
	}
}

func TestApplyOverviewContextSizeLimit(t *testing.T) {
	s := strings.Repeat("a", 100)
	if got := applyOverviewContextSizeLimit(s, -1); got != s {
		t.Fatalf("no cap: got len %d", len(got))
	}
	short := "hello"
	if got := applyOverviewContextSizeLimit(short, 50); got != short {
		t.Fatalf("under limit: %q", got)
	}
	long := strings.Repeat("x", 500)
	// Limit must exceed trailer length so the marker is included (tiny limits return a raw prefix only).
	got := applyOverviewContextSizeLimit(long, 400)
	if len([]rune(got)) > 500 {
		t.Fatalf("truncated should be bounded, got runes %d", len([]rune(got)))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("missing marker: %q", got)
	}
}

func TestBuildFileDependencyGraphMermaid_nilOrEmpty(t *testing.T) {
	if got := BuildFileDependencyGraphMermaid(context.Background(), nil, "java"); got != "" {
		t.Errorf("BuildFileDependencyGraphMermaid(nil meta): got %q; want empty", got)
	}
	if got := BuildFileDependencyGraphMermaid(context.Background(), nil, ""); got != "" {
		t.Errorf("BuildFileDependencyGraphMermaid(empty lang): got %q; want empty", got)
	}
}

func TestBuildOverviewVisualSections_nilOrEmpty(t *testing.T) {
	if got := BuildOverviewVisualSections(context.Background(), nil, "java", "", ""); got != "" {
		t.Errorf("BuildOverviewVisualSections(nil meta): got %q; want empty", got)
	}
	if got := BuildOverviewVisualSections(context.Background(), nil, "", "", ""); got != "" {
		t.Errorf("BuildOverviewVisualSections(empty lang): got %q; want empty", got)
	}
}

func TestEscapeMermaidLabel(t *testing.T) {
	if got := escapeMermaidLabel(`a\b"c`); got != `a\\b#quot;c` {
		t.Errorf("escapeMermaidLabel: got %q; want a\\\\b#quot;c", got)
	}
}

func TestSplitOverviewNarrativeAndVisuals_markerAndLegacy(t *testing.T) {
	narr := "# Title\n\nBody.\n"
	visual := "\n\n## Module and file structure\n\n```mermaid\ngraph TD\n```\n"
	withMarker := narr + OverviewVisualAppendixMarker + visual
	gotN, gotV := SplitOverviewNarrativeAndVisuals(withMarker)
	if gotN != strings.TrimSpace(narr) || !gotV {
		t.Fatalf("with marker: narrative=%q hadVisual=%v", gotN, gotV)
	}
	legacy := narr + visual
	gotN2, gotV2 := SplitOverviewNarrativeAndVisuals(legacy)
	if gotN2 != strings.TrimSpace(narr) || !gotV2 {
		t.Fatalf("legacy: narrative=%q hadVisual=%v", gotN2, gotV2)
	}
	only := "# Only narrative"
	gotN3, gotV3 := SplitOverviewNarrativeAndVisuals(only)
	if gotN3 != only || gotV3 {
		t.Fatalf("no appendix: narrative=%q hadVisual=%v", gotN3, gotV3)
	}
}

func TestCanonicalOverviewRepoPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"./src/foo.ts", "src/foo.ts"},
		{"src/foo.ts", "src/foo.ts"},
		{"src//bar/../baz/x.ts", "src/baz/x.ts"},
		{"  ./a/b  ", "a/b"},
	}
	for _, tt := range tests {
		if got := canonicalOverviewRepoPath(tt.in); got != tt.want {
			t.Errorf("canonicalOverviewRepoPath(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestEmptyFileDependencyGraphExplanation_byWorkflowLang(t *testing.T) {
	js := emptyFileDependencyGraphExplanation("typescript")
	if strings.Contains(js, "advanced_jar_path") || strings.Contains(js, "Java **call**") {
		t.Errorf("TS empty explanation should not focus on Java JAR; got:\n%s", js)
	}
	if !strings.Contains(js, "JavaScript/TypeScript") || !strings.Contains(js, "imports") {
		t.Errorf("TS explanation should mention JS/TS indexer behavior; got:\n%s", js)
	}
	java := emptyFileDependencyGraphExplanation("java")
	if !strings.Contains(java, "advanced") || !strings.Contains(java, "advanced_jar_path") {
		t.Errorf("Java explanation should mention advanced indexer; got:\n%s", java)
	}
	gen := emptyFileDependencyGraphExplanation("go")
	if !strings.Contains(gen, "Java") || !strings.Contains(gen, "JavaScript") {
		t.Errorf("generic explanation should mention Java and JS/TS; got:\n%s", gen)
	}
	if strings.Contains(gen, "advanced_jar_path") {
		t.Errorf("generic explanation should not name advanced_jar_path; got:\n%s", gen)
	}
}

func TestFileDependencyMermaidFromEdges_dedupesPathAndPairs(t *testing.T) {
	edges := []*metadata.EdgeFile{
		{CallerFile: "./app/lifecycles.ts", CalleeFile: "app/createRedirect.ts", EdgeType: "CALLS"},
		{CallerFile: "app/lifecycles.ts", CalleeFile: "./app/createRedirect.ts", EdgeType: "IMPORTS"},
		{CallerFile: "app/lifecycles.ts", CalleeFile: "app/webhook.ts", EdgeType: "CALLS"},
	}
	diagram, ok := fileDependencyMermaidFromEdges(edges)
	if !ok {
		t.Fatal("expected diagram")
	}
	if strings.Count(diagram, `["app/lifecycles.ts"]`) != 1 {
		t.Errorf("lifecycles.ts should appear once as full path label; diagram:\n%s", diagram)
	}
	if strings.Count(diagram, "-->") != 2 {
		t.Errorf("want 2 arcs (deduped parallel same pair); got:\n%s", diagram)
	}
}

func TestAppendOverviewUserOutputContract(t *testing.T) {
	full := appendOverviewUserOutputContract("SNAPSHOT", false)
	if !strings.Contains(full, "SNAPSHOT") || !strings.Contains(full, "## OUTPUT CONTRACT") {
		t.Fatalf("full: %q", full)
	}
	if !strings.Contains(full, "top-level") && !strings.Contains(full, "Markdown") {
		t.Error("full contract should mention Markdown / heading")
	}
	if !strings.Contains(full, "batched generation") || !strings.Contains(full, "likely") {
		t.Error("full contract should forbid meta-generation and hedging")
	}
	if strings.Contains(full, "NO_UPDATES") {
		t.Error("full mode should not emphasize NO_UPDATES")
	}
	delta := appendOverviewUserOutputContract("body", true)
	if !strings.Contains(delta, "NO_UPDATES") || !strings.Contains(delta, "Overview updates") {
		t.Fatalf("delta: %q", delta)
	}
	if appendOverviewUserOutputContract("", false) == "" {
		t.Fatal("empty body should still return contract")
	}
}

type stubOverviewChatCompleter struct{ content string }

func (s stubOverviewChatCompleter) Complete(ctx context.Context, messages []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	return &model.CompleteResult{Content: s.content}, nil
}

func TestLLMOverviewDocGenerator_fullNarrativePreservesBodyWithFencedExamples(t *testing.T) {
	want := "# Overview\n\nIntro.\n\n```java\nclass A {}\n```\n\nFooter."
	g := &LLMOverviewDocGenerator{LLM: stubOverviewChatCompleter{content: want}}
	got, _, err := g.GenerateOverview(context.Background(), "snapshot", OverviewGenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q; want full markdown body (not first fence only)", got)
	}
}

func TestLLMOverviewDocGenerator_fullNarrativeKeepsTextAfterEmptyLeadingFence(t *testing.T) {
	// extractCodeBlockContent used to return "" here (first ```…``` pair has no body).
	want := "```markdown\n\n```\n\n# Real title\n\nBody."
	g := &LLMOverviewDocGenerator{LLM: stubOverviewChatCompleter{content: want}}
	got, _, err := g.GenerateOverview(context.Background(), "snapshot", OverviewGenerateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "# Real title") || !strings.Contains(got, "Body.") {
		t.Fatalf("got %q; want narrative after empty fence", got)
	}
}
