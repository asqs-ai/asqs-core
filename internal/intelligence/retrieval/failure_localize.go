package retrieval

import (
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator/errloc"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// Constants for failure-grounded retrieval (see docs/DOCUMENTATION.md — Lost in the Middle / RAG grounding).
const (
	// failureLocalizedMinChunkRunes: only splice large chunks so small snippets stay intact.
	failureLocalizedMinChunkRunes = 6000
	// failureLocalizedMaxChunkRunes passed to errloc.FormatFileForPrompt per mutated chunk.
	failureLocalizedMaxChunkRunes = 10000
	// failureHintExcerptRunes caps the stderr block prepended in BuildLLMContext.
	failureHintExcerptRunes = 6000
)

// applyFailureLocalizedRetrieval trims large dependency/reference chunks using file:line citations from
// FailureHint (stderr / compiler output). Target method/class chunks are left intact so the primary gap symbol stays fully visible.
func applyFailureLocalizedRetrieval(out *RetrievalContext, failureHint string) {
	if out == nil {
		return
	}
	failureHint = strings.TrimSpace(failureHint)
	out.FailureHint = failureHint
	if failureHint == "" {
		return
	}
	paths := collectChunkPathsForFailureMap(out)
	if len(paths) == 0 {
		return
	}
	lineByPath := errloc.LinesByCanonicalPaths(failureHint, paths)
	if len(lineByPath) == 0 {
		return
	}
	opts := errloc.DefaultPromptOpts()
	opts.MaxRunes = failureLocalizedMaxChunkRunes
	opts.IsArtifact = false
	opts.PreambleLines = 0

	mutateChunk := func(c *embeddings.Chunk) {
		if c == nil || strings.TrimSpace(c.File) == "" {
			return
		}
		if c.StartLine < 1 || c.EndLine < c.StartLine {
			return
		}
		canon := errloc.NormalizePath(c.File)
		globalHits := lineByPath[canon]
		if len(globalHits) == 0 {
			return
		}
		rel := globalLinesToChunkRelative(globalHits, c.StartLine, c.EndLine)
		if len(rel) == 0 {
			return
		}
		if len([]rune(c.Content)) < failureLocalizedMinChunkRunes {
			return
		}
		newContent := errloc.FormatFileForPrompt(c.Content, rel, opts)
		if newContent != "" && newContent != c.Content {
			c.Content = newContent
		}
	}

	for _, d := range out.Dependencies {
		if d != nil {
			mutateChunk(d.Chunk)
		}
	}
	for _, dm := range out.DomainModels {
		if dm != nil {
			mutateChunk(dm.Chunk)
		}
	}
	for _, c := range out.SimilarTests {
		mutateChunk(c)
	}
	for _, c := range out.RelatedChunks {
		mutateChunk(c)
	}
	for _, c := range out.Fixtures {
		mutateChunk(c)
	}
	for _, c := range out.Config {
		mutateChunk(c)
	}
}

func collectChunkPathsForFailureMap(out *RetrievalContext) []string {
	if out == nil {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	add := func(file string) {
		file = strings.TrimSpace(file)
		if file == "" {
			return
		}
		n := errloc.NormalizePath(file)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		paths = append(paths, n)
	}
	addChunk := func(c *embeddings.Chunk) {
		if c != nil {
			add(c.File)
		}
	}
	if out.TargetMethod != nil {
		addChunk(out.TargetMethod.Chunk)
	}
	if out.TargetClass != nil {
		addChunk(out.TargetClass.Chunk)
	}
	for _, d := range out.Dependencies {
		if d != nil {
			addChunk(d.Chunk)
		}
	}
	for _, dm := range out.DomainModels {
		if dm != nil {
			addChunk(dm.Chunk)
		}
	}
	for _, c := range out.SimilarTests {
		addChunk(c)
	}
	for _, c := range out.RelatedChunks {
		addChunk(c)
	}
	for _, c := range out.Fixtures {
		addChunk(c)
	}
	for _, c := range out.Config {
		addChunk(c)
	}
	return paths
}

func globalLinesToChunkRelative(global []int, chunkStart, chunkEnd int) []int {
	var out []int
	seen := make(map[int]bool)
	for _, ln := range global {
		if ln < chunkStart || ln > chunkEnd {
			continue
		}
		rel := ln - chunkStart + 1
		if rel < 1 || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	return out
}
