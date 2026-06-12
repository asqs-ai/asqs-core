package overview

import (
	"context"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// Generate builds the overview/workflows document from the metadata store: a batched LLM pass over the
// indexed source files (Plan B slicing, with per-slice file/rune caps) plus metadata-driven visual
// sections. It returns the markdown content and the resolved output path (DefaultOverviewPath unless
// g.Path is set). Best-effort: panics are recovered into an error so a generation failure never
// crashes the run. The metadata store is read-only here, so this is safe to run concurrently with
// per-symbol test/doc generation.
func (g *LLMOverviewDocGenerator) Generate(ctx context.Context, meta *metadata.Store, lang, repoRoot string, maxFilesPerSlice, maxIndexRunesPerSlice int) (content, path string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("overview generation panic: %v", r)
		}
	}()
	if g == nil || g.LLM == nil || meta == nil {
		return "", "", fmt.Errorf("overview: generator or metadata store not configured")
	}
	content, path, _, err = g.GenerateOverviewWithMeta(ctx, meta, lang, maxFilesPerSlice, maxIndexRunesPerSlice, OverviewGenerateOpts{RepoRoot: repoRoot})
	if err != nil {
		return content, path, err
	}
	if strings.TrimSpace(content) != "" {
		func() {
			defer func() { _ = recover() }() // visual sections are best-effort; keep the narrative if they fail
			if visual := BuildOverviewVisualSections(ctx, meta, lang, repoRoot, path); visual != "" {
				content += visual
			}
		}()
	}
	return content, path, nil
}
