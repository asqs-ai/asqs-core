package indexer

import (
	"context"
	"path/filepath"
	"strings"
)

// CompositeLangIndexer returns a LangIndexer that serves paths present in bulk from the map (fresh source each call)
// and delegates all other paths to fallback (e.g. minimal Java indexer when the repo mixes Java + C#).
func CompositeLangIndexer(bulk map[string]*ParsedFile, fallback LangIndexer) LangIndexer {
	if fallback == nil {
		fallback = StubLangIndexer
	}
	if len(bulk) == 0 {
		return fallback
	}
	return func(ctx context.Context, path string, lang string, source []byte) (*ParsedFile, error) {
		path = filepath.ToSlash(path)
		for _, k := range []string{path, strings.TrimPrefix(path, "/"), filepath.ToSlash(filepath.Clean(path))} {
			if pf, ok := bulk[k]; ok && pf != nil {
				out := *pf
				out.Source = string(source)
				return &out, nil
			}
		}
		return fallback(ctx, path, lang, source)
	}
}
