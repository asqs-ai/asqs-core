package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// RunOptions configure a full index run (incremental: detect changes, then index only changed/added files).
type RunOptions struct {
	// CurrentFiles is the list of files in the repo with their SHAs (and lang, module, is_test).
	CurrentFiles []FileVersion
	// RepoPath is the absolute path to the repo root (to read file contents).
	RepoPath string
	// RepoID is stored with chunks and runs (e.g. org/repo or client id).
	RepoID string
	// CommitSHA is the repo commit at index time (for versioning).
	CommitSHA string
	// RunID is a unique id for this run (e.g. UUID); if empty, one is generated.
	RunID string
	// ChunkConfig controls chunk size (default: 300–800 tokens).
	ChunkConfig ChunkConfig
	// Sanitize options for chunk content (default: strip long comments, block comments).
	Sanitize SanitizeOptions
	// LangIndexer is the language-specific parser (Java/C# helper).
	LangIndexer LangIndexer
	// Embedder produces vectors for chunks; if nil, chunks are produced but not written to embeddings (caller embeds later).
	Embedder Embedder
	// EmbeddingProvider and EmbeddingModel are stored in the embeddings DB when chunks are written.
	EmbeddingProvider  string // e.g. "openai"
	EmbeddingModel     string // e.g. "text-embedding-3-small"
	EmbeddingDimension int    // 0 = infer from first vector length
	// Audit logs each step to the audit store (and optional file) for debugging and improvement. Optional.
	Audit Auditor
	// IndexablePaths when non-nil restricts indexing to these repo-relative paths (forward slashes).
	// Used with JS/TS indexer so only files the indexer emitted are indexed (config/tooling and skip-dir files are excluded).
	IndexablePaths map[string]struct{}
	// StartMaxIteration is the initial current_iteration for new runs (max evaluation fix-iteration budget). 0 = default 3. Used when inserting the index run.
	StartMaxIteration int
	// TriggerSource, RepoURL, RepoLocalPath, ConfigRevisionID, ProjectID are persisted on index_runs when any is non-empty (API / webhook).
	TriggerSource    string
	RepoURL          string
	RepoLocalPath    string
	ConfigRevisionID string
	ProjectID        string
}

// Auditor is the interface for run-scoped audit logging (e.g. implemented by audit.Logger).
type Auditor interface {
	Log(ctx context.Context, step string, payload interface{})
	LogError(ctx context.Context, step string, payload interface{})
}

// RunResult summarizes what was done in the run.
type RunResult struct {
	RunID         string
	Added         int
	Changed       int
	Removed       int
	ChunksStored  int
	SymbolsStored int // total symbols inserted this run
	EdgesStored   int // total edges inserted this run
	DurationMs    int64
	// ChunksTotal/SymbolsTotal/EdgesTotal are the post-run sizes of the respective stores
	// (counted once at the end of Run). They are repo-scoped for chunks (when RepoID is set on
	// RunOptions) and global for symbols/edges (those tables are repo-agnostic — see
	// schema.sql). Zero when the relevant CountX method failed; counting errors are best-effort
	// and do not fail the run. Consumed by orchestrator.IndexPhaseResult and surfaced in the
	// session_feedback "index_delta" payload so cache-hit runs (Added=Changed=Removed=0) carry
	// useful totals instead of an all-zero payload (A.7).
	ChunksTotal  int64
	SymbolsTotal int64
	EdgesTotal   int64
	// EdgesUnresolvedMissingCaller counts edges skipped because the caller symbol id could not
	// be resolved, keyed by canonical edge type (uppercase, length-capped). Same for Callee.
	EdgesUnresolvedMissingCaller map[string]int64 `json:"edges_unresolved_missing_caller,omitempty"`
	EdgesUnresolvedMissingCallee map[string]int64 `json:"edges_unresolved_missing_callee,omitempty"`
}

// Run performs change detection, then incremental index: remove stale data, parse changed/added files,
// write symbol table and dependency graph to metadata, chunk and sanitize, embed and store chunks.
func Run(ctx context.Context, meta MetadataWriter, emb EmbeddingsWriter, opts RunOptions) (*RunResult, error) {
	if meta == nil {
		return nil, fmt.Errorf("indexer: MetadataWriter required")
	}
	chunkCfg := opts.ChunkConfig
	if chunkCfg.MinTokens <= 0 {
		chunkCfg = DefaultChunkConfig()
	}
	sanitize := opts.Sanitize
	if sanitize.MaxCommentRunes == 0 && !sanitize.StripBlockComments {
		sanitize = DefaultSanitizeOptions()
	}

	runID := opts.RunID
	if runID == "" {
		runID = fmt.Sprintf("run-%d", time.Now().UnixMilli())
	}
	started := time.Now().UnixMilli()
	if opts.Audit != nil {
		opts.Audit.Log(ctx, "index.start", map[string]interface{}{
			"message": fmt.Sprintf("Index run started for repo %s.", opts.RepoID),
			"repo_id": opts.RepoID, "commit_sha": opts.CommitSHA, "started_at_ms": started,
		})
	}
	currentIteration := opts.StartMaxIteration
	if currentIteration <= 0 {
		currentIteration = 3
	}
	preFilterCount := len(opts.CurrentFiles)
	currentFiles := opts.CurrentFiles
	if len(opts.IndexablePaths) > 0 {
		currentFiles = filterFileVersionsByIndexablePaths(currentFiles, opts.IndexablePaths)
		// Diagnostic: when the lang-indexer parsed map yielded no overlap with the scanned
		// files we silently produce a 0/0/0 detect_changes report and the run becomes a
		// no-op. This is almost always a path-normalization mismatch (e.g. JS/TS indexer
		// emitted absolute paths while the scanner produced repo-relative ones, or a mono
		// workspace prefix was applied to one side but not the other). Surface a sample of
		// both sides so operators can fix it.
		if opts.Audit != nil && preFilterCount > 0 && len(currentFiles) == 0 {
			scannedSample := samplePaths(opts.CurrentFiles, 5)
			indexableSample := sampleSetKeys(opts.IndexablePaths, 5)
			opts.Audit.LogError(ctx, "index.indexable_paths_dropped_all", map[string]interface{}{
				"message":          fmt.Sprintf("All %d scanned file(s) were filtered out by IndexablePaths (lang indexer parsed %d path(s)); indexing will be a no-op. Likely a path-normalization mismatch.", preFilterCount, len(opts.IndexablePaths)),
				"scanned_count":    preFilterCount,
				"indexable_count":  len(opts.IndexablePaths),
				"scanned_sample":   scannedSample,
				"indexable_sample": indexableSample,
			})
		}
	}
	var extras *metadata.IndexRunStartExtras
	if strings.TrimSpace(opts.TriggerSource) != "" || strings.TrimSpace(opts.RepoURL) != "" || strings.TrimSpace(opts.RepoLocalPath) != "" || strings.TrimSpace(opts.ConfigRevisionID) != "" || strings.TrimSpace(opts.ProjectID) != "" {
		extras = &metadata.IndexRunStartExtras{
			TriggerSource:    strings.TrimSpace(opts.TriggerSource),
			RepoURL:          strings.TrimSpace(opts.RepoURL),
			RepoLocalPath:    strings.TrimSpace(opts.RepoLocalPath),
			ConfigRevisionID: strings.TrimSpace(opts.ConfigRevisionID),
			ProjectID:        strings.TrimSpace(opts.ProjectID),
		}
	}
	if err := meta.InsertIndexRun(ctx, runID, opts.RepoID, opts.CommitSHA, started, currentIteration, extras); err != nil {
		return nil, fmt.Errorf("indexer: persist index run: %w", err)
	}
	defer func() {
		_ = meta.UpdateIndexRunFinished(ctx, runID, time.Now().UnixMilli())
	}()

	changeSet, err := DetectChanges(ctx, currentFiles, meta)
	if err != nil {
		return nil, fmt.Errorf("indexer: detect changes: %w", err)
	}
	// Force-reindex decision (first-run safety):
	//
	// DetectChanges relies on the GLOBAL `files` table to compute SHA-based cache hits.
	// In multi-tenant deployments — where several projects share the same metadata DB —
	// rows written by another project (or a prior aborted run for this same project) can
	// match the scanned files exactly and produce a false-positive cache hit (0/0/0
	// detected changes), even when this repo has never produced symbols/chunks.
	//
	// We use two repo-scoped truths to override the cache verdict:
	//   1. CountIndexRuns(repoID) <= 1 → this is the first index_run for the repo (the
	//      current run was already inserted above, so 1 means "only this run").
	//   2. CountChunksByRepo(repoID) == 0 → the per-repo chunk store is empty (no prior
	//      run finished the embed step for this repo).
	//
	// On any count-call error we log it AND force reindex anyway. Silently skipping the
	// guard (the legacy behaviour, which also required a global symbols == 0 check that
	// can never become true in a multi-tenant DB) was the main reason "first run not
	// indexing" reports were filed.
	//
	// The `index.force_reindex` audit event carries the reason so operators can triage.
	forceReindex := false
	forceReason := ""
	if len(changeSet.Added) == 0 && len(changeSet.Changed) == 0 && len(changeSet.Removed) == 0 && len(currentFiles) > 0 {
		if n, err := meta.CountIndexRuns(ctx, opts.RepoID); err != nil {
			if opts.Audit != nil {
				opts.Audit.LogError(ctx, "index.first_run_check_error", map[string]interface{}{
					"message": fmt.Sprintf("Could not check prior index_runs for repo %s: %v. Forcing reindex.", opts.RepoID, err),
					"error":   err.Error(),
				})
			}
			forceReindex = true
			forceReason = "first_run_check_failed"
		} else if n <= 1 {
			forceReindex = true
			forceReason = "first_index_run_for_repo"
		}
		if !forceReindex && emb != nil {
			if chunks, cerr := emb.CountChunksByRepo(ctx, opts.RepoID); cerr != nil {
				if opts.Audit != nil {
					opts.Audit.LogError(ctx, "index.chunk_count_error", map[string]interface{}{
						"message": fmt.Sprintf("Could not count chunks for repo %s: %v. Forcing reindex.", opts.RepoID, cerr),
						"error":   cerr.Error(),
					})
				}
				forceReindex = true
				forceReason = "chunk_count_failed"
			} else if chunks == 0 {
				forceReindex = true
				forceReason = "chunk_store_empty_for_repo"
			}
		}
	}
	if forceReindex {
		changeSet.Changed = append(changeSet.Changed, currentFiles...)
		if opts.Audit != nil {
			opts.Audit.Log(ctx, "index.force_reindex", map[string]interface{}{
				"message":       fmt.Sprintf("Forcing reindex of %d current file(s) for repo %s (reason: %s).", len(currentFiles), opts.RepoID, forceReason),
				"current_files": len(currentFiles),
				"reason":        forceReason,
			})
		}
	}
	if opts.Audit != nil {
		opts.Audit.Log(ctx, "index.detect_changes", map[string]interface{}{
			"message": fmt.Sprintf("Change detection: %d files to add, %d to change, %d to remove.", len(changeSet.Added), len(changeSet.Changed), len(changeSet.Removed)),
			"added":   len(changeSet.Added), "changed": len(changeSet.Changed), "removed": len(changeSet.Removed),
		})
	}

	// Remove stale: embeddings first, then symbols (cascade deletes edges), then file row
	for _, path := range changeSet.Removed {
		if emb != nil {
			_, _ = emb.DeleteByFile(ctx, path)
		}
		_, _ = meta.DeleteSymbolsByFile(ctx, path)
		_ = meta.DeleteFile(ctx, path)
	}
	if opts.Audit != nil && len(changeSet.Removed) > 0 {
		opts.Audit.Log(ctx, "index.removed", map[string]interface{}{
			"message": fmt.Sprintf("Removed %d stale file(s) from index.", len(changeSet.Removed)),
			"files":   changeSet.Removed,
		})
	}

	// Build FQName -> symbol ID for edges (symbols we insert this run; others we resolve from meta)
	fqNameToID := make(map[string]string)

	chunksStored := 0
	symbolsStored := 0
	edgesStored := 0
	unresolvedCallerByType := make(map[string]int64)
	unresolvedCalleeByType := make(map[string]int64)
	filesToIndex := append(changeSet.Added, changeSet.Changed...)
	for _, fv := range filesToIndex {
		source, err := os.ReadFile(filepath.Join(opts.RepoPath, fv.Path))
		if err != nil {
			if opts.Audit != nil {
				opts.Audit.LogError(ctx, "index.error", map[string]interface{}{
					"message": fmt.Sprintf("Failed to read file %s: %s", fv.Path, err.Error()),
					"step":    "read_file", "file": fv.Path, "error": err.Error(),
				})
			}
			return nil, fmt.Errorf("indexer: read file %s: %w", fv.Path, err)
		}
		var parsed *ParsedFile
		if strings.EqualFold(strings.TrimSpace(fv.Lang), "openapi") {
			parsed = ParsedFileFromOpenAPISource(fv.Path, source)
		} else {
			var parseErr error
			parsed, parseErr = opts.LangIndexer(ctx, fv.Path, fv.Lang, source)
			if parseErr != nil {
				if opts.Audit != nil {
					opts.Audit.LogError(ctx, "index.error", map[string]interface{}{
						"message": fmt.Sprintf("Failed to parse file %s: %s", fv.Path, parseErr.Error()),
						"step":    "parse", "file": fv.Path, "error": parseErr.Error(),
					})
				}
				return nil, fmt.Errorf("indexer: parse %s: %w", fv.Path, parseErr)
			}
		}
		parsed.Source = string(source)
		// Always use scan path as DB file key so symbols join ListSymbolsInTestFiles (JAR/map paths may differ in case or shape).
		parsed.Path = filepath.ToSlash(fv.Path)
		parsed.Module = fv.Module
		indexerMarkedTest := parsed.IsTest
		parsed.IsTest = fv.IsTest
		parsed.Lang = strings.ToLower(strings.TrimSpace(parsed.Lang))
		// JS/TS indexer uses broader is_test (e2e/, playwright/, cypress/) than directory scan alone; ListGapsE2E
		// requires files.is_test = true. Keep indexer flag or any emitted E2E_SPEC.
		if parsed.Lang == "javascript" || parsed.Lang == "typescript" {
			if indexerMarkedTest {
				parsed.IsTest = true
			}
			if !parsed.IsTest {
				for _, sym := range parsed.Symbols {
					if strings.EqualFold(sym.Kind, "E2E_SPEC") {
						parsed.IsTest = true
						break
					}
				}
			}
		}
		// When JS/TS indexer returns no symbols (e.g. map miss, or React/TSX not fully parsed), add a synthetic MODULE so we get at least one chunk per file.
		if len(parsed.Symbols) == 0 && (parsed.Lang == "javascript" || parsed.Lang == "typescript" || parsed.Lang == "html") {
			endLine := 1
			if len(parsed.Source) > 0 {
				endLine = strings.Count(parsed.Source, "\n") + 1
			}
			moduleFQ := pathToModuleFQ(parsed.Path)
			parsed.Symbols = []ParsedSymbol{
				{Kind: "MODULE", FQName: moduleFQ, StartLine: 1, EndLine: endLine},
			}
		}
		// Razor/Blazor markup: csharp-indexer only enumerates *.cs; scanned .cshtml/.razor still need a MODULE for chunks/metadata.
		if len(parsed.Symbols) == 0 && parsed.Lang == "csharp" {
			pl := strings.ToLower(parsed.Path)
			if strings.HasSuffix(pl, ".cshtml") || strings.HasSuffix(pl, ".razor") {
				endLine := 1
				if len(parsed.Source) > 0 {
					endLine = strings.Count(parsed.Source, "\n") + 1
				}
				moduleFQ := pathToModuleFQ(parsed.Path)
				parsed.Symbols = []ParsedSymbol{
					{Kind: "MODULE", FQName: moduleFQ, StartLine: 1, EndLine: endLine},
				}
			}
		}
		// Skip persisting JS/TS files that are stub-only (path not in indexer map: config/tooling); avoid cluttering DB.
		// Same for openapi specs with no operations (MODULE-only).
		if parsed.Lang == "javascript" || parsed.Lang == "typescript" || parsed.Lang == "openapi" {
			if len(parsed.Symbols) == 1 && strings.ToLower(parsed.Symbols[0].Kind) == "module" {
				continue
			}
		}
		if opts.Audit != nil {
			opts.Audit.Log(ctx, "index.file_parsed", map[string]interface{}{
				"message": fmt.Sprintf("Parsed %s: %d symbols, %d edges.", fv.Path, len(parsed.Symbols), len(parsed.Edges)),
				"file":    fv.Path, "symbols": len(parsed.Symbols), "edges": len(parsed.Edges),
			})
		}

		// Delete existing symbols/chunks for this file (reindex)
		if emb != nil {
			_, _ = emb.DeleteByFile(ctx, fv.Path)
		}
		_, _ = meta.DeleteSymbolsByFile(ctx, fv.Path)

		// Insert symbols and collect IDs
		symbolIDByFQName := make(map[string]string)
		for _, sym := range parsed.Symbols {
			metaSym := &metadata.Symbol{
				Lang:          parsed.Lang,
				Kind:          sym.Kind,
				FQName:        sym.FQName,
				File:          parsed.Path,
				StartLine:     sym.StartLine,
				EndLine:       sym.EndLine,
				SignatureJSON: sym.SignatureJSON,
			}
			if sym.StartColumn != nil {
				v := *sym.StartColumn
				metaSym.StartColumn = &v
			}
			if sym.EndColumn != nil {
				v := *sym.EndColumn
				metaSym.EndColumn = &v
			}
			id, err := meta.InsertSymbol(ctx, metaSym)
			if err != nil {
				return nil, fmt.Errorf("indexer: insert symbol %s: %w", sym.FQName, err)
			}
			symbolIDByFQName[sym.FQName] = id
			fqNameToID[sym.FQName] = id // global for cross-file edge resolution
			symbolsStored++
		}
		// Synthetic CONTAINS for chunk parent_fq / parent_symbol_id (same relations we persist below).
		var chunkExtraEdges []ParsedEdge
		importHintPaths := collectImportTargetPathsFromParsed(parsed)
		edgeHintFiles := append([]string{parsed.Path}, importHintPaths...)
		ambiguousEdgeKeys := make(map[string]struct{})
		logAmbiguous := func(role, fq string) {
			if opts.Audit == nil || len(ambiguousEdgeKeys) >= 24 {
				return
			}
			k := role + ":" + fq + "@" + parsed.Path
			if _, ok := ambiguousEdgeKeys[k]; ok {
				return
			}
			ambiguousEdgeKeys[k] = struct{}{}
			opts.Audit.Log(ctx, "index.edge_resolve_ambiguous", map[string]interface{}{
				"message": fmt.Sprintf("Ambiguous %s resolution for fq_name %q in %s (picked best match using import/file hints).", role, fq, parsed.Path),
				"file":    parsed.Path, "role": role, "fq_name": fq,
			})
		}
		// Insert edges (resolve caller/callee ID from this file, other files in run, or metadata)
		for _, e := range parsed.Edges {
			edgeType := CanonicalEdgeType(e.EdgeType)
			if edgeType == "" {
				continue
			}
			metricKey := edgeTypeMetricsKey(edgeType)
			callerID := symbolIDByFQName[e.CallerFQName]
			if callerID == "" {
				callerID = fqNameToID[e.CallerFQName]
			}
			if callerID == "" {
				var amb bool
				callerID, amb = resolveSymbolIDForFQName(ctx, meta, e.CallerFQName, edgeHintFiles, parsed.Lang)
				if amb {
					logAmbiguous("caller", e.CallerFQName)
				}
			}
			if callerID == "" {
				unresolvedCallerByType[metricKey]++
				continue
			}
			calleeID := symbolIDByFQName[e.CalleeFQName]
			if calleeID == "" {
				calleeID = fqNameToID[e.CalleeFQName]
			}
			if calleeID == "" {
				var amb bool
				calleeID, amb = resolveSymbolIDForFQName(ctx, meta, e.CalleeFQName, edgeHintFiles, parsed.Lang)
				if amb {
					logAmbiguous("callee", e.CalleeFQName)
				}
			}
			// Advanced Java indexer uses JavaParser getQualifiedSignature() e.g. "pkg.Class.method(params)".
			// Try class#method form so we resolve cross-file callees.
			if calleeID == "" {
				normalized := qualifiedSignatureToFQName(e.CalleeFQName)
				if normalized != e.CalleeFQName {
					if id := symbolIDByFQName[normalized]; id != "" {
						calleeID = id
					} else if id := fqNameToID[normalized]; id != "" {
						calleeID = id
					} else {
						var amb bool
						calleeID, amb = resolveSymbolIDForFQName(ctx, meta, normalized, edgeHintFiles, parsed.Lang)
						if amb {
							logAmbiguous("callee", normalized)
						}
					}
				}
			}
			// IMPORTS: JavaParser emits full declaration text ("import pkg.T;"); normalize and strip segments for static members.
			if calleeID == "" && strings.EqualFold(edgeType, "IMPORTS") {
				calleeID = resolveJavaImportCalleeID(ctx, meta, symbolIDByFQName, fqNameToID, e.CalleeFQName)
			}
			// C#: using directives reference namespaces; map to an indexed symbol by trimming suffixes.
			if calleeID == "" && strings.EqualFold(edgeType, "IMPORTS") && parsed.Lang == "csharp" {
				calleeID = resolveCSharpImportCalleeID(ctx, meta, symbolIDByFQName, fqNameToID, e.CalleeFQName)
			}
			if calleeID == "" {
				unresolvedCalleeByType[metricKey]++
				continue
			}
			if meta.InsertEdge(ctx, &metadata.Edge{CallerSymbolID: callerID, CalleeSymbolID: calleeID, EdgeType: edgeType}) == nil {
				edgesStored++
			}
		}
		// Java / C#: add type -> method "contains" for graph structure (method FQName uses Type#Member; see csharp-indexer).
		if parsed.Lang == "java" || parsed.Lang == "csharp" {
			for _, sym := range parsed.Symbols {
				k := strings.ToLower(sym.Kind)
				if k != "method" && k != "constructor" {
					continue
				}
				classFQName, _ := methodFQNameToClassFQName(sym.FQName)
				if classFQName == "" {
					continue
				}
				classID := symbolIDByFQName[classFQName]
				if classID == "" {
					classID = fqNameToID[classFQName]
				}
				if classID == "" {
					var amb bool
					classID, amb = resolveSymbolIDForFQName(ctx, meta, classFQName, edgeHintFiles, parsed.Lang)
					if amb {
						logAmbiguous("class", classFQName)
					}
				}
				methodID := symbolIDByFQName[sym.FQName]
				if classID != "" && methodID != "" {
					chunkExtraEdges = append(chunkExtraEdges, ParsedEdge{CallerFQName: classFQName, CalleeFQName: sym.FQName, EdgeType: "CONTAINS"})
					if meta.InsertEdge(ctx, &metadata.Edge{CallerSymbolID: classID, CalleeSymbolID: methodID, EdgeType: "CONTAINS"}) == nil {
						edgesStored++
					}
				}
			}
		}
		// JS/TS: MODULE -> children only when the indexer emitted no edges (Nest/React enrichers add CONTAINS / other edges).
		if len(parsed.Edges) == 0 && (parsed.Lang == "javascript" || parsed.Lang == "typescript") {
			var moduleID, moduleFQ string
			for _, sym := range parsed.Symbols {
				if strings.ToLower(sym.Kind) == "module" {
					moduleID = symbolIDByFQName[sym.FQName]
					moduleFQ = sym.FQName
					break
				}
			}
			if moduleID != "" {
				for _, sym := range parsed.Symbols {
					if strings.ToLower(sym.Kind) == "module" {
						continue
					}
					childID := symbolIDByFQName[sym.FQName]
					if childID != "" {
						chunkExtraEdges = append(chunkExtraEdges, ParsedEdge{CallerFQName: moduleFQ, CalleeFQName: sym.FQName, EdgeType: "CONTAINS"})
						if meta.InsertEdge(ctx, &metadata.Edge{CallerSymbolID: moduleID, CalleeSymbolID: childID, EdgeType: "CONTAINS"}) == nil {
							edgesStored++
						}
					}
				}
			}
		}
		// Upsert file
		_ = meta.UpsertFile(ctx, &metadata.File{File: parsed.Path, SHA: fv.SHA, Lang: parsed.Lang, Module: parsed.Module, IsTest: parsed.IsTest})

		// Chunk and optionally embed + store (include synthetic CONTAINS so parent_fq matches persisted graph).
		forChunk := *parsed
		forChunk.Edges = append(append([]ParsedEdge{}, parsed.Edges...), chunkExtraEdges...)
		plans := ChunkFromParsedFile(&forChunk, opts.RepoID, opts.RepoPath, chunkCfg, sanitize)
		// Build ChunkToEmbed with SymbolID from plan.SymbolFQ (merged / split / secondary chunks).
		var toEmbed []*ChunkToEmbed
		for _, p := range plans {
			symbolID := ""
			if p.SymbolFQ != "" {
				symbolID = symbolIDByFQName[p.SymbolFQ]
			}
			if symbolID == "" {
				for _, sym := range parsed.Symbols {
					if p.StartLine >= sym.StartLine && p.EndLine <= sym.EndLine {
						symbolID = symbolIDByFQName[sym.FQName]
						break
					}
				}
			}
			chunkMeta := p.MetadataJSON
			if len(chunkMeta) == 0 {
				chunkMeta = nil
			}
			parentSymID := ""
			if p.ParentFQ != "" {
				parentSymID = symbolIDByFQName[p.ParentFQ]
			}
			toEmbed = append(toEmbed, &ChunkToEmbed{
				Content: p.Content, SymbolID: symbolID, File: p.File, Lang: p.Lang,
				ChunkType: p.ChunkType, StartLine: p.StartLine, EndLine: p.EndLine, RepoID: p.RepoID,
				MetadataJSON: chunkMeta, ParentSymbolID: parentSymID,
			})
		}
		if emb != nil && opts.Embedder != nil && len(toEmbed) > 0 {
			stats := &embedFallbackStats{}
			chunks, dim, err := embedChunksWithFallback(ctx, opts.Embedder, toEmbed, chunkCfg, stats)
			if err != nil {
				if errors.Is(err, errEmbedSkipFile) {
					if opts.Audit != nil {
						opts.Audit.LogError(ctx, "index.embed_skipped", map[string]interface{}{
							"message": fmt.Sprintf("Embedding skipped for %s (%s), continuing with other files", fv.Path, embedSkipReason(err)),
							"file":    fv.Path, "error": err.Error(), "recoverable": true, "skip_reason": embedSkipReason(err),
						})
						opts.Audit.Log(ctx, "index.file_indexed", map[string]interface{}{
							"message": fmt.Sprintf("Indexed file %s: 0 chunks stored (embed skipped).", fv.Path),
							"file":    fv.Path, "chunks_stored": 0,
						})
					}
					continue
				}
				if opts.Audit != nil {
					opts.Audit.LogError(ctx, "index.error", map[string]interface{}{
						"message": fmt.Sprintf("Embedding failed for %s: %s", fv.Path, err.Error()),
						"step":    "embed", "file": fv.Path, "error": err.Error(),
					})
				}
				return nil, fmt.Errorf("indexer: embed: %w", err)
			}
			if len(chunks) == 0 {
				if opts.Audit != nil {
					opts.Audit.LogError(ctx, "index.embed_skipped", map[string]interface{}{
						"message": fmt.Sprintf("Embedding skipped for %s (no embeddable segments after provider limits), continuing with other files", fv.Path),
						"file":    fv.Path, "skip_reason": "embedding_segments_dropped",
					})
				}
				continue
			}
			_, err = emb.InsertChunks(ctx, chunks)
			if err != nil {
				if opts.Audit != nil {
					opts.Audit.LogError(ctx, "index.error", map[string]interface{}{
						"message": fmt.Sprintf("Insert chunks failed for %s: %s", fv.Path, err.Error()),
						"step":    "insert_chunks", "file": fv.Path, "error": err.Error(),
					})
				}
				return nil, fmt.Errorf("indexer: insert chunks: %w", err)
			}
			chunksStored += len(chunks)
			if opts.EmbeddingDimension > 0 {
				dim = opts.EmbeddingDimension
			}
			if opts.EmbeddingProvider != "" && opts.EmbeddingModel != "" {
				_ = emb.SetEmbeddingProvider(ctx, opts.EmbeddingProvider, opts.EmbeddingModel, dim)
			}
			if opts.Audit != nil && stats.FileTooLarge {
				opts.Audit.Log(ctx, "index.embed_large_file_fallback", map[string]interface{}{
					"message":          fmt.Sprintf("Applied large-file embedding fallback for %s.", fv.Path),
					"file":             fv.Path,
					"segment_retry":    stats.SegmentRetries,
					"segments_created": stats.SegmentsCreated,
					"segments_dropped": stats.SegmentsDropped,
				})
			}
		}
		if opts.Audit != nil {
			opts.Audit.Log(ctx, "index.file_indexed", map[string]interface{}{
				"message": fmt.Sprintf("Indexed file %s: %d chunks stored.", fv.Path, len(toEmbed)),
				"file":    fv.Path, "chunks_stored": len(toEmbed),
			})
		}
	}
	// Match HTTP client symbols to backend routes by method + path (same run + fqNameToID).
	if n := LinkAPIClientRequestsToRoutes(ctx, meta, fqNameToID); n > 0 {
		edgesStored += n
		if opts.Audit != nil {
			opts.Audit.Log(ctx, "index.api_client_route_links", map[string]interface{}{
				"message": fmt.Sprintf("Linked %d API_CLIENT_REQUEST → API_ROUTE edge(s).", n),
				"edges":   n,
			})
		}
	}

	if n, err := meta.MaterializeTestsSourceEdges(ctx); err != nil {
		if opts.Audit != nil {
			opts.Audit.LogError(ctx, "index.tests_source_materialize", map[string]interface{}{
				"message": fmt.Sprintf("TESTS_SOURCE materialization failed: %v", err),
				"error":   err.Error(),
			})
		}
	} else if n > 0 && opts.Audit != nil {
		opts.Audit.Log(ctx, "index.tests_source_materialize", map[string]interface{}{
			"message": fmt.Sprintf("Materialized %d TESTS_SOURCE trace edge(s) (test→production heuristics).", n),
			"edges":   n,
		})
	}

	// Resolve edges for cross-file: symbols not in this run - we need ListSymbolsByFQName on MetadataWriter. For MVP we only stored same-file edges above. Leave as is.

	finished := time.Now().UnixMilli()
	if opts.Audit != nil {
		finishPayload := map[string]interface{}{
			"message": fmt.Sprintf("Index run finished: %d added, %d changed, %d removed; %d symbols, %d edges, %d chunks; %d ms.", len(changeSet.Added), len(changeSet.Changed), len(changeSet.Removed), symbolsStored, edgesStored, chunksStored, finished-started),
			"run_id":  runID, "added": len(changeSet.Added), "changed": len(changeSet.Changed),
			"removed": len(changeSet.Removed), "chunks_stored": chunksStored, "symbols_stored": symbolsStored, "edges_stored": edgesStored,
			"duration_ms": finished - started,
		}
		if len(unresolvedCallerByType) > 0 || len(unresolvedCalleeByType) > 0 {
			finishPayload["edges_unresolved_missing_caller"] = unresolvedCallerByType
			finishPayload["edges_unresolved_missing_callee"] = unresolvedCalleeByType
			opts.Audit.Log(ctx, "index.edges_unresolved", map[string]interface{}{
				"message": fmt.Sprintf("Unresolved edge endpoints: %d caller misses, %d callee misses (by edge type).",
					sumMapInt64(unresolvedCallerByType), sumMapInt64(unresolvedCalleeByType)),
				"run_id": runID, "missing_caller_by_type": unresolvedCallerByType, "missing_callee_by_type": unresolvedCalleeByType,
			})
		}
		opts.Audit.Log(ctx, "index.finished", finishPayload)
	}
	// Best-effort post-run totals (A.7). Counting failures are non-fatal — we keep the
	// otherwise-successful run result and report a zero total. Errors are surfaced via audit
	// when an Auditor is wired so operators can investigate if the dashboards show 0 totals.
	var chunksTotal, symbolsTotal, edgesTotal int64
	if emb != nil {
		if n, err := emb.CountChunksByRepo(ctx, opts.RepoID); err == nil {
			chunksTotal = n
		} else if opts.Audit != nil {
			opts.Audit.LogError(ctx, "index.totals_chunks_error", map[string]interface{}{
				"message": fmt.Sprintf("Count chunks failed (totals unavailable): %v", err),
				"error":   err.Error(),
			})
		}
	}
	if n, err := meta.CountSymbols(ctx); err == nil {
		symbolsTotal = n
	} else if opts.Audit != nil {
		opts.Audit.LogError(ctx, "index.totals_symbols_error", map[string]interface{}{
			"message": fmt.Sprintf("Count symbols failed (totals unavailable): %v", err),
			"error":   err.Error(),
		})
	}
	if n, err := meta.CountEdges(ctx); err == nil {
		edgesTotal = n
	} else if opts.Audit != nil {
		opts.Audit.LogError(ctx, "index.totals_edges_error", map[string]interface{}{
			"message": fmt.Sprintf("Count edges failed (totals unavailable): %v", err),
			"error":   err.Error(),
		})
	}

	return cloneRunResultMaps(runID, len(changeSet.Added), len(changeSet.Changed), len(changeSet.Removed),
		chunksStored, symbolsStored, edgesStored, time.Now().UnixMilli()-started,
		chunksTotal, symbolsTotal, edgesTotal, unresolvedCallerByType, unresolvedCalleeByType), nil
}

func sumMapInt64(m map[string]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}

func cloneRunResultMaps(runID string, added, changed, removed, chunksStored, symbolsStored, edgesStored int,
	durationMs int64, chunksTotal, symbolsTotal, edgesTotal int64,
	unresolvedCaller, unresolvedCallee map[string]int64,
) *RunResult {
	r := &RunResult{
		RunID:         runID,
		Added:         added,
		Changed:       changed,
		Removed:       removed,
		ChunksStored:  chunksStored,
		SymbolsStored: symbolsStored,
		EdgesStored:   edgesStored,
		DurationMs:    durationMs,
		ChunksTotal:   chunksTotal,
		SymbolsTotal:  symbolsTotal,
		EdgesTotal:    edgesTotal,
	}
	if len(unresolvedCaller) > 0 {
		r.EdgesUnresolvedMissingCaller = mapsCloneInt64(unresolvedCaller)
	}
	if len(unresolvedCallee) > 0 {
		r.EdgesUnresolvedMissingCallee = mapsCloneInt64(unresolvedCallee)
	}
	return r
}

func mapsCloneInt64(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// IndexablePathsFromParsedMap returns a set of repo-relative paths from a parsed map (all keys and their variants).
// Used so only files emitted by the JS/TS indexer are indexed (config/tooling and skip-dir files are excluded).
func IndexablePathsFromParsedMap(parsedByPath map[string]*ParsedFile) map[string]struct{} {
	set := make(map[string]struct{}, len(parsedByPath))
	for k := range parsedByPath {
		set[k] = struct{}{}
	}
	return set
}

// pathInIndexableSet returns true if relPath is in the set (trying path, no leading slash, and Clean(path) to match LangIndexerFromMap lookup).
func pathInIndexableSet(relPath string, set map[string]struct{}) bool {
	p := filepath.ToSlash(relPath)
	for _, k := range []string{p, strings.TrimPrefix(p, "/"), filepath.ToSlash(filepath.Clean(p))} {
		if _, ok := set[k]; ok {
			return true
		}
	}
	return false
}

// samplePaths returns up to n FileVersion paths for diagnostic audit logging.
func samplePaths(files []FileVersion, n int) []string {
	if n <= 0 || len(files) == 0 {
		return nil
	}
	if n > len(files) {
		n = len(files)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, files[i].Path)
	}
	return out
}

// sampleSetKeys returns up to n keys from set for diagnostic audit logging. Map iteration
// order is non-deterministic; the sample is intended only for triage.
func sampleSetKeys(set map[string]struct{}, n int) []string {
	if n <= 0 || len(set) == 0 {
		return nil
	}
	out := make([]string, 0, n)
	for k := range set {
		if len(out) >= n {
			break
		}
		out = append(out, k)
	}
	return out
}

func filterFileVersionsByIndexablePaths(files []FileVersion, set map[string]struct{}) []FileVersion {
	if len(set) == 0 {
		return files
	}
	out := make([]FileVersion, 0, len(files))
	for _, f := range files {
		if pathInIndexableSet(f.Path, set) {
			out = append(out, f)
		}
	}
	return out
}

// pathToModuleFQ returns a module FQName from a repo-relative path (e.g. "src/App.tsx" -> "src.App"). Used for synthetic MODULE symbol when JS/TS indexer returns no symbols.
func pathToModuleFQ(relPath string) string {
	p := filepath.ToSlash(strings.TrimSpace(relPath))
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "root"
	}
	ext := filepath.Ext(p)
	if ext != "" {
		p = strings.TrimSuffix(p, ext)
	}
	parts := strings.Split(p, "/")
	var out []string
	for _, s := range parts {
		if s != "" && s != "." && s != ".." {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "root"
	}
	return strings.Join(out, ".")
}

// methodFQNameToClassFQName returns the class FQName for a method FQName like "pkg.Class#method" -> "pkg.Class".
// Returns ("", false) if the format is not method-style (no "#").
func methodFQNameToClassFQName(methodFQName string) (string, bool) {
	idx := strings.Index(methodFQName, "#")
	if idx <= 0 {
		return "", false
	}
	return methodFQName[:idx], true
}

// qualifiedSignatureToFQName converts JavaParser-style qualified signature to our symbol FQName.
// e.g. "com.example.Foo.bar(int)" or "com.example.Foo.bar()" -> "com.example.Foo#bar".
// So ListSymbolsByFQName can resolve callees from the advanced Java indexer.
func qualifiedSignatureToFQName(sig string) string {
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return ""
	}
	// Find last "." before "(" (method name is between that dot and the paren).
	paren := strings.Index(sig, "(")
	if paren < 0 {
		return sig
	}
	beforeParen := sig[:paren]
	lastDot := strings.LastIndex(beforeParen, ".")
	if lastDot < 0 {
		return sig
	}
	return beforeParen[:lastDot] + "#" + beforeParen[lastDot+1:]
}

func resolveJavaImportCalleeID(
	ctx context.Context,
	meta MetadataWriter,
	symbolIDByFQName, fqNameToID map[string]string,
	raw string,
) string {
	cand := NormalizeJavaImportDecl(strings.TrimSpace(raw))
	if cand == "" {
		cand = strings.TrimSpace(raw)
	}
	for s := cand; s != ""; {
		if id := symbolIDByFQName[s]; id != "" {
			return id
		}
		if id := fqNameToID[s]; id != "" {
			return id
		}
		if syms, _ := meta.ListSymbolsByFQName(ctx, s); len(syms) > 0 {
			return syms[0].ID
		}
		i := strings.LastIndex(s, ".")
		if i <= 0 {
			break
		}
		s = s[:i]
	}
	return ""
}
