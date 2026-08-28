package retrieval

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/asqs/asqs-core/internal/llm/tokens"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// fenceLang maps a chunk's language to a markdown fence tag.
//
// Models use the fence tag as a strong signal for which syntax to emit. An untagged block of Java,
// in a prompt that also carries YAML config and TypeScript examples, is genuinely ambiguous — and
// every block was untagged.
func fenceLang(lang, file string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "java":
		return "java"
	case "csharp", "cs", "c#":
		return "csharp"
	case "typescript", "ts":
		return "typescript"
	case "javascript", "js":
		return "javascript"
	case "kotlin":
		return "kotlin"
	case "go":
		return "go"
	case "python", "py":
		return "python"
	}
	// Fall back to the file extension; config and fixture chunks often carry no lang.
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(file))) {
	case ".java":
		return "java"
	case ".cs":
		return "csharp"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".kt", ".kts":
		return "kotlin"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".yml", ".yaml":
		return "yaml"
	case ".json":
		return "json"
	case ".xml", ".csproj", ".props", ".targets":
		return "xml"
	case ".properties":
		return "properties"
	case ".sql":
		return "sql"
	}
	return ""
}

// lineComment returns the line-comment prefix for a fence language, so the provenance comment does
// not break syntax highlighting or confuse a model that copies the block.
func lineComment(fence string) string {
	switch fence {
	case "yaml", "properties", "python":
		return "# "
	case "xml":
		return "" // XML has no line comment; the anchor is omitted rather than emitted as invalid markup
	case "sql":
		return "-- "
	case "json":
		return "" // JSON has no comments at all
	default:
		return "// "
	}
}

// chunkAnchor renders `file:start-end` for a chunk, or "" when the location is unknown.
func chunkAnchor(c *embeddings.Chunk) string {
	if c == nil || strings.TrimSpace(c.File) == "" {
		return ""
	}
	if c.StartLine > 0 && c.EndLine >= c.StartLine {
		return fmt.Sprintf("%s:%d-%d", c.File, c.StartLine, c.EndLine)
	}
	return c.File
}

// renderChunkBlock emits a fenced code block that is language-tagged, anchored to its source
// location, and clamped to maxTokens on a line boundary.
//
// Three defects it replaces, all in one four-line function:
//
//  1. **No language tag** — the fence was a bare ``` regardless of content.
//  2. **No file:line inside the block** — provenance lived on a preceding markdown line, so any
//     truncation or reordering separated the code from its origin. The fixer prompt explicitly
//     asks the model to cite a location; it could not.
//  3. **Byte slicing** — `content[:maxChars]` splits a multi-byte rune and emits invalid UTF-8.
//     It was unreachable only because MaxChunkChars was always 0; token budgeting makes it
//     reachable, so it had to be fixed as part of this work. Every other truncator in the repo
//     already used []rune.
//
// The elision stub matters as much as the clamp: the model is told that content is missing and
// where to find it, instead of silently reasoning over half a method.
func renderChunkBlock(c *embeddings.Chunk, maxTokens int, tc tokens.Counter) string {
	if c == nil || c.Content == "" {
		return ""
	}
	body := c.Content
	elided := 0
	if maxTokens > 0 && tc != nil {
		body, elided = tokens.ClampToTokens(body, maxTokens, tc)
		if strings.TrimSpace(body) == "" {
			return ""
		}
	}

	fence := fenceLang(c.Lang, c.File)
	comment := lineComment(fence)
	anchor := chunkAnchor(c)

	var b strings.Builder
	b.WriteString("```")
	b.WriteString(fence)
	b.WriteByte('\n')
	if anchor != "" && comment != "" {
		b.WriteString(comment)
		b.WriteString(anchor)
		b.WriteByte('\n')
	}
	b.WriteString(body)
	if elided > 0 {
		b.WriteByte('\n')
		if comment != "" {
			b.WriteString(comment)
		}
		if anchor != "" {
			fmt.Fprintf(&b, "… %d line(s) elided; full body at %s", elided, anchor)
		} else {
			fmt.Fprintf(&b, "… %d line(s) elided", elided)
		}
	}
	b.WriteString("\n```")
	return b.String()
}
