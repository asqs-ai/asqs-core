package pipeline

import (
	"context"
	"strings"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
	javaindexer "github.com/asqs/asqs-core/tools/java-indexer"
)

// javaAdvancedLangIndexer uses the advanced JAR map when possible; on map miss (empty stub) falls back to the
// minimal Java indexer. Always augments with line-based E2E_SPEC when the JAR did not emit one — fixes
// ListGapsE2E empty after advanced-only indexing (path skew, older JAR, or parser edge cases).
func javaAdvancedLangIndexer(parsedMap map[string]*indexer.ParsedFile) indexer.LangIndexer {
	base := indexer.LangIndexerFromMap(parsedMap)
	return func(ctx context.Context, path string, lang string, source []byte) (*indexer.ParsedFile, error) {
		pf, err := base(ctx, path, lang, source)
		if err != nil {
			return nil, err
		}
		// Server-rendered templates indexed by the Java JAR (STATIC_TEMPLATE / UI_TEST_HOOK).
		if strings.TrimSpace(lang) == "html" {
			return pf, nil
		}
		if strings.TrimSpace(lang) != "java" {
			return pf, nil
		}
		if len(pf.Symbols) == 0 {
			return javaindexer.Index(ctx, path, lang, source)
		}
		javaindexer.AugmentParsedFileWithE2ESpecHeuristic(pf, path)
		return pf, nil
	}
}
