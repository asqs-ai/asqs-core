package retrieval

import (
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// ContextRequest specifies for which symbol to gather context (symbol-aware retrieval).
type ContextRequest struct {
	SymbolID string // target symbol (e.g. method) to generate tests for
	Lang     string
	RepoID   string
	// Profile selects graph expansion and similar-chunk strategy (empty = java_unit). See RetrievalProfile constants.
	Profile RetrievalProfile
	// Limits for each section (0 = default)
	MaxDependencyChunks int
	MaxSimilarTests     int
	MaxFixtures         int
	// MaxConfigChunks caps configuration/context helper chunks included in RetrievalContext.Config (0 = default).
	MaxConfigChunks int
	// MaxContextChunks is an optional global cap across retrieval sections used by the section budget allocator.
	// 0 means "no global cap" (section-specific caps apply independently).
	MaxContextChunks int
	// DependencyMaxDepth controls transitive dependency traversal depth from the target symbol.
	// 1 = direct neighbors only, 2 = include neighbors-of-neighbors, etc. 0 = default.
	DependencyMaxDepth int
	// SimilarMMRLambda is the λ in Maximal Marginal Relevance (Carbonell & Goldstein, SIGIR 1998) when selecting
	// similar reference chunks from vector search. 0 or out of range (0,1] uses default 0.5; 1.0 = relevance-only.
	SimilarMMRLambda float64
	// FailureHint is optional recent compiler/test stderr. When set, Retrieve maps file:line citations to large
	// non-target chunks and replaces them with error-localized windows; BuildLLMContext prepends a short excerpt.
	FailureHint string
	// DisableHybridModuleFilter when true, skips SQL module matching (chunk_metadata.module) on similar-chunk vector search and list fallbacks. Default false = hybrid filter + adaptive widening.
	DisableHybridModuleFilter bool
}

// SymbolChunk is a symbol plus its chunk content (for inclusion in context).
type SymbolChunk struct {
	Symbol *metadata.Symbol
	Chunk  *embeddings.Chunk
}

// DependencyEdge is a dependency (callee) with the edge type (calls, extends, implements).
type DependencyEdge struct {
	SymbolChunk
	EdgeType string // e.g. "calls", "extends", "implements"
	Depth    int
	// GraphPath carries inclusion provenance as a compact edge-chain (e.g. "CALLS -> INJECTS").
	GraphPath string
}

// RetrievalContext is the symbol-aware context bundle for test/docs generation.
type RetrievalContext struct {
	// Target: the method + its enclosing class (symbols + chunks).
	TargetMethod *SymbolChunk
	TargetClass  *SymbolChunk // enclosing class/component/module container when found

	// Dependencies: symbols this method calls or uses (with edge type), with chunks.
	Dependencies []*DependencyEdge

	// DomainModels: types used in inputs/outputs (from signature or heuristics), with chunks.
	DomainModels []*SymbolChunk

	// RelatedChunks: extra embedding rows without a driving symbol (e.g. api_contract snippets for http_api profile).
	RelatedChunks []*embeddings.Chunk

	// SimilarTests: existing tests in same module/lang for reference.
	SimilarTests []*embeddings.Chunk

	// Fixtures and build helpers (test utilities, builders).
	Fixtures []*embeddings.Chunk

	// Config: DI/Spring/test runner config chunks.
	Config []*embeddings.Chunk

	// FailureHint echoes ContextRequest.FailureHint when non-empty for BuildLLMContext (execution-feedback grounding).
	FailureHint string

	// ExistingTestCoverage summarizes branch-intent hints when tests already exist for the target source file.
	// Used to steer generation toward missing branches instead of duplicating covered happy paths.
	ExistingTestCoverage *ExistingTestCoverageHint

	// ExistingTestPaths is the sorted list of on-disk test files that already cover the target
	// symbol's source file (repo-relative). Populated from PlanOptions.ExistingTestPathsBySource.
	// Downstream the generator prefers the first entry over SuggestedTestPath so a repo using
	// XTests.java / x.spec.ts gets extended instead of ending up with duplicate XTest.java / x.test.ts.
	ExistingTestPaths []string
}

// ExistingTestCoverageHint carries heuristic branch-gap signals derived from target chunk + similar tests.
type ExistingTestCoverageHint struct {
	HasExistingTests bool
	CoveredIntents   []string
	MissingIntents   []string
}
