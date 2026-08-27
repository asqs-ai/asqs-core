package retrieval

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/llm/tokens"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

func TestRenderChunkBlock_languageTagAndAnchor(t *testing.T) {
	c := &embeddings.Chunk{
		File: "src/main/java/com/acme/OrderService.java", Lang: "java",
		StartLine: 41, EndLine: 58,
		Content: "public Order place(Order o) {\n  return repo.save(o);\n}",
	}
	got := renderChunkBlock(c, 0, tokens.For("", ""))

	if !strings.HasPrefix(got, "```java\n") {
		t.Errorf("missing language tag; models use the fence tag to decide which syntax to emit.\n%s", got)
	}
	if !strings.Contains(got, "// src/main/java/com/acme/OrderService.java:41-58") {
		t.Errorf("missing file:line anchor inside the block.\n%s", got)
	}
	if !strings.HasSuffix(got, "\n```") {
		t.Errorf("fence not closed:\n%s", got)
	}
}

func TestRenderChunkBlock_commentSyntaxPerLanguage(t *testing.T) {
	cases := []struct {
		lang, file, wantFence, wantComment string
	}{
		{"java", "A.java", "java", "// "},
		{"csharp", "A.cs", "csharp", "// "},
		{"typescript", "a.ts", "typescript", "// "},
		{"", "application-test.yml", "yaml", "# "},
		{"", "pom.xml", "xml", ""},    // XML has no line comment: anchor omitted, not emitted as junk
		{"", "data.json", "json", ""}, // JSON has no comments at all
	}
	for _, c := range cases {
		ch := &embeddings.Chunk{File: c.file, Lang: c.lang, StartLine: 1, EndLine: 2, Content: "x: 1"}
		got := renderChunkBlock(ch, 0, tokens.For("", ""))
		if !strings.HasPrefix(got, "```"+c.wantFence+"\n") {
			t.Errorf("%s: fence = %q, want %q", c.file, firstLine(got), "```"+c.wantFence)
		}
		if c.wantComment == "" {
			if strings.Contains(got, c.file+":") {
				t.Errorf("%s: anchor emitted in a language with no line comment; that would be invalid syntax", c.file)
			}
		} else if !strings.Contains(got, c.wantComment+c.file) {
			t.Errorf("%s: missing %q-prefixed anchor:\n%s", c.file, c.wantComment, got)
		}
	}
}

// TestRenderChunkBlock_elisionStubTellsTheModel: truncation must be informative. The model needs to
// know content is missing and where the rest lives — otherwise it silently reasons over half a
// method, and the fixer (which is explicitly told to fix a cited file:line) gets no anchor.
func TestRenderChunkBlock_elisionStubTellsTheModel(t *testing.T) {
	c := &embeddings.Chunk{
		File: "src/A.java", Lang: "java", StartLine: 10, EndLine: 200,
		Content: strings.Repeat("  int x = compute(y);\n", 200),
	}
	tc := tokens.For("", "")
	got := renderChunkBlock(c, 40, tc)

	if !strings.Contains(got, "line(s) elided") {
		t.Errorf("no elision marker:\n%s", got)
	}
	if !strings.Contains(got, "full body at src/A.java:10-200") {
		t.Errorf("elision marker does not say where the full body is:\n%s", got)
	}
	if len(got) >= len(c.Content) {
		t.Error("content was not actually clamped")
	}
}

// TestRenderChunkBlock_isRuneSafe is the regression test for the UTF-8 half of M-14: the old
// implementation sliced bytes, which splits a multi-byte rune.
func TestRenderChunkBlock_isRuneSafe(t *testing.T) {
	c := &embeddings.Chunk{
		File: "src/Ünicode.java", Lang: "java", StartLine: 1, EndLine: 100,
		Content: strings.Repeat("// çommentaire avec des accents éàü\n", 100),
	}
	tc := tokens.For("", "")
	for _, budget := range []int{1, 3, 7, 25, 100} {
		got := renderChunkBlock(c, budget, tc)
		if !utf8.ValidString(got) {
			t.Fatalf("budget %d produced invalid UTF-8", budget)
		}
	}
}

func TestRenderChunkBlock_emptyInputs(t *testing.T) {
	tc := tokens.For("", "")
	if got := renderChunkBlock(nil, 100, tc); got != "" {
		t.Errorf("nil chunk should render nothing, got %q", got)
	}
	if got := renderChunkBlock(&embeddings.Chunk{File: "a.java"}, 100, tc); got != "" {
		t.Errorf("empty content should render nothing, got %q", got)
	}
	// A budget so small nothing survives should produce nothing rather than an empty fence.
	c := &embeddings.Chunk{File: "a.java", Lang: "java", Content: "some content here"}
	if got := renderChunkBlock(c, 0, tc); !strings.Contains(got, "some content") {
		t.Errorf("maxTokens 0 means unbounded, content should survive:\n%s", got)
	}
}

func TestChunkAnchor(t *testing.T) {
	if got := chunkAnchor(&embeddings.Chunk{File: "a.java", StartLine: 3, EndLine: 9}); got != "a.java:3-9" {
		t.Errorf("got %q", got)
	}
	// Unknown line span: file only, rather than a misleading 0-0.
	if got := chunkAnchor(&embeddings.Chunk{File: "a.java"}); got != "a.java" {
		t.Errorf("got %q", got)
	}
	if got := chunkAnchor(&embeddings.Chunk{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := chunkAnchor(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
