package overview

import "context"

// OverviewDocResult is the result of generating the overview/workflows document.
type OverviewDocResult struct {
	Content    string // markdown content
	Path       string // e.g. docs/documentation.md (configurable via indexer.overview_doc_path)
	Err        error
	DurationMs int64 // wall-clock cost of overview generation in milliseconds (0 when skipped)
}

// OverviewGenerateOpts configures a single overview generation call (e.g. repo root for reading an
// existing doc so the generator can append index deltas instead of rewriting the full narrative).
type OverviewGenerateOpts struct {
	RepoRoot string
}

// OverviewDocGenerator produces the big-picture document (workflows and dependencies) from a prebuilt
// context string.
type OverviewDocGenerator interface {
	GenerateOverview(ctx context.Context, overviewContext string, opts OverviewGenerateOpts) (content string, path string, err error)
}
