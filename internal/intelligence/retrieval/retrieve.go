package retrieval

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

const maxContainerSiblingChunks = 12
const (
	maxSegmentWindowsPerFile  = 2
	maxSegmentsPerWindow      = 3
	defaultMaxConfigChunks    = 5
	defaultDependencyMaxDepth = 2
)

// MetaReader is the subset of metadata needed for symbol-aware retrieval.
type MetaReader interface {
	GetSymbolByID(ctx context.Context, id string) (*metadata.Symbol, error)
	ListSymbolsByFile(ctx context.Context, file string) ([]*metadata.Symbol, error)
	GetEdgesFrom(ctx context.Context, callerSymbolID string) ([]*metadata.Edge, error)
	GetEdgesTo(ctx context.Context, calleeSymbolID string) ([]*metadata.Edge, error)
	GetFile(ctx context.Context, file string) (*metadata.File, error)
	ListSymbolsByFQName(ctx context.Context, fqName string) ([]*metadata.Symbol, error)
}

// ChunkReader is the subset of embeddings store for listing chunks by symbol/file/repo/type.
type ChunkReader interface {
	List(ctx context.Context, opts embeddings.ListOptions) ([]embeddings.Chunk, error)
	Search(ctx context.Context, queryEmbedding []float32, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error)
}

// Retrieve gathers symbol-aware context for the given target symbol: method + enclosing container, dependencies,
// domain models, similar reference chunks (profile-aware), fixtures, and config. Uses the graph (metadata) and chunks, not raw "nearest paragraphs".
// Set req.Profile to http_api, e2e_playwright, react_feature, or nest_module for expanded graph walks (inbound edges + edge-type priorities).
func Retrieve(ctx context.Context, meta MetaReader, chunks ChunkReader, req ContextRequest) (*RetrievalContext, error) {
	if req.MaxDependencyChunks <= 0 {
		req.MaxDependencyChunks = 15
	}
	if req.MaxSimilarTests <= 0 {
		req.MaxSimilarTests = 5
	}
	if req.MaxFixtures <= 0 {
		req.MaxFixtures = 5
	}
	if req.MaxConfigChunks <= 0 {
		req.MaxConfigChunks = defaultMaxConfigChunks
	}
	if req.DependencyMaxDepth <= 0 {
		req.DependencyMaxDepth = defaultDependencyMaxDepth
	}
	depBudget, similarBudget, fixtureBudget, configBudget := allocateRetrievalSectionBudgets(req)
	req.MaxDependencyChunks = depBudget
	req.MaxSimilarTests = similarBudget
	req.MaxFixtures = fixtureBudget
	req.MaxConfigChunks = configBudget

	out := &RetrievalContext{}

	// Target method + class
	targetSym, err := meta.GetSymbolByID(ctx, req.SymbolID)
	if err != nil || targetSym == nil {
		return nil, err
	}
	methodChunk := chunkForSymbol(ctx, chunks, req.SymbolID, req.RepoID)
	out.TargetMethod = &SymbolChunk{Symbol: targetSym, Chunk: methodChunk}

	containerSym := enclosingContainer(ctx, meta, targetSym)
	if containerSym != nil {
		var classChunk *embeddings.Chunk
		if containerSym.ID != targetSym.ID {
			classChunk = chunkForSymbol(ctx, chunks, containerSym.ID, req.RepoID)
		}
		out.TargetClass = &SymbolChunk{Symbol: containerSym, Chunk: classChunk}
	}

	out.Dependencies = buildDependenciesFromGraph(ctx, meta, chunks, req, req.SymbolID)

	seen := map[string]bool{req.SymbolID: true}
	if out.TargetClass != nil && out.TargetClass.Symbol != nil {
		seen[out.TargetClass.Symbol.ID] = true
	}
	for _, d := range out.Dependencies {
		if d != nil && d.Symbol != nil {
			seen[d.Symbol.ID] = true
		}
	}

	// Domain models: same-file types first, then types parsed from target signature (params/return)
	fileSyms, _ := meta.ListSymbolsByFile(ctx, targetSym.File)
	for _, s := range fileSyms {
		if s.Kind != "class" && s.Kind != "interface" && s.Kind != "struct" && strings.ToLower(s.Kind) != "record" {
			continue
		}
		if seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		c := chunkForSymbol(ctx, chunks, s.ID, req.RepoID)
		out.DomainModels = append(out.DomainModels, &SymbolChunk{Symbol: s, Chunk: c})
	}
	// Domain models from signature: resolve type names (e.g. Java param/return types) to symbols in this repo
	f, _ := meta.GetFile(ctx, targetSym.File)
	fileModule := ""
	if f != nil {
		fileModule = strings.TrimSpace(f.Module)
	}
	for _, typeName := range typeNamesFromSignature(targetSym) {
		if typeName == "" {
			continue
		}
		var fqNames []string
		if strings.Contains(typeName, ".") {
			fqNames = []string{typeName}
		} else if fileModule != "" {
			fqNames = []string{fileModule + "." + typeName, typeName}
		} else {
			fqNames = []string{typeName}
		}
		for _, fqName := range fqNames {
			syms, _ := meta.ListSymbolsByFQName(ctx, fqName)
			for _, s := range syms {
				if s.Kind != "class" && s.Kind != "interface" && s.Kind != "struct" && strings.ToLower(s.Kind) != "record" {
					continue
				}
				if seen[s.ID] {
					continue
				}
				seen[s.ID] = true
				c := chunkForSymbol(ctx, chunks, s.ID, req.RepoID)
				out.DomainModels = append(out.DomainModels, &SymbolChunk{Symbol: s, Chunk: c})
			}
		}
	}

	var targetChunk *embeddings.Chunk
	if out.TargetMethod != nil {
		targetChunk = out.TargetMethod.Chunk
	}
	out.SimilarTests = gatherSimilarReferenceChunks(ctx, chunks, targetChunk, req, fileModule)

	profileNorm := NormalizeRetrievalProfile(req.Profile)
	if profileLoadsContainerSiblings(profileNorm) && out.TargetClass != nil && out.TargetClass.Symbol != nil {
		excludeSym := map[string]bool{req.SymbolID: true}
		for _, d := range out.Dependencies {
			if d != nil && d.Symbol != nil {
				excludeSym[d.Symbol.ID] = true
			}
		}
		appendContainerSiblingChunks(ctx, chunks, req, out.TargetClass.Symbol.ID, excludeSym, &out.RelatedChunks, maxContainerSiblingChunks)
	}

	if shouldAttachAPIContractChunks(req.Profile) {
		for _, ch := range listChunksByType(ctx, chunks, req.RepoID, req.Lang, "api_contract", 8, "") {
			if ch == nil {
				continue
			}
			cp := *ch
			out.RelatedChunks = append(out.RelatedChunks, &cp)
		}
	}

	// Fixtures / helpers: test-related chunks
	out.Fixtures = listChunksByType(ctx, chunks, req.RepoID, req.Lang, "fixture", req.MaxFixtures, "")
	if len(out.Fixtures) < req.MaxFixtures {
		extra := listChunksByPathPattern(ctx, chunks, req.RepoID, req.Lang, []string{"fixture", "helper", "builder"}, req.MaxFixtures-len(out.Fixtures))
		out.Fixtures = append(out.Fixtures, extra...)
	}
	annotateChunkGroupProvenance(out.Fixtures, "fixtures", "fixture/helper candidates for setup and test data")

	// Config: DI, Spring, test runner
	out.Config = listChunksByPathPattern(ctx, chunks, req.RepoID, req.Lang, []string{"config", "context", "spring", "test-config"}, req.MaxConfigChunks)
	annotateChunkGroupProvenance(out.Config, "config", "configuration/DI/runtime context likely needed for wiring")

	applyFailureLocalizedRetrieval(out, req.FailureHint)

	return out, nil
}

func appendContainerSiblingChunks(ctx context.Context, chunks ChunkReader, req ContextRequest, containerSymbolID string, excludeSymbolIDs map[string]bool, related *[]*embeddings.Chunk, max int) {
	if containerSymbolID == "" || max <= 0 || related == nil {
		return
	}
	seen := make(map[string]bool)
	for _, ch := range *related {
		if ch != nil && ch.ID != "" {
			seen[ch.ID] = true
		}
	}
	list, err := chunks.List(ctx, embeddings.ListOptions{
		ParentSymbolID: containerSymbolID,
		RepoID:         req.RepoID,
		Limit:          max * 4,
	})
	if err != nil {
		return
	}
	added := 0
	for i := range list {
		ch := &list[i]
		if ch.ID != "" && seen[ch.ID] {
			continue
		}
		if excludeSymbolIDs != nil && ch.SymbolID != "" && excludeSymbolIDs[ch.SymbolID] {
			continue
		}
		if ch.ID != "" {
			seen[ch.ID] = true
		}
		cp := *ch
		*related = append(*related, &cp)
		added++
		if added >= max {
			break
		}
	}
}

// RetrieveWithProfile runs Retrieve after setting base.Profile (convenience for orchestrators).
func RetrieveWithProfile(ctx context.Context, meta MetaReader, chunks ChunkReader, profile RetrievalProfile, base ContextRequest) (*RetrievalContext, error) {
	base.Profile = profile
	return Retrieve(ctx, meta, chunks, base)
}

func buildDependenciesFromGraph(ctx context.Context, meta MetaReader, chunks ChunkReader, req ContextRequest, targetID string) []*DependencyEdge {
	profile := NormalizeRetrievalProfile(req.Profile)
	max := req.MaxDependencyChunks
	if max <= 0 {
		max = 15
	}
	graph := collectGraphEdges(ctx, meta, targetID, profile, req.DependencyMaxDepth)
	seen := map[string]bool{targetID: true}
	var out []*DependencyEdge

	// Diversity cap: avoid one dominant edge kind (often IMPORTS/CONTAINS) consuming all slots.
	perTypeCap := max / 2
	if perTypeCap < 2 {
		perTypeCap = 2
	}
	kindCount := make(map[string]int)
	var deferred []graphEdge

	addOne := func(ge graphEdge) bool {
		if ge.otherID == "" || seen[ge.otherID] {
			return false
		}
		callee, _ := meta.GetSymbolByID(ctx, ge.otherID)
		if callee == nil {
			return false
		}
		seen[ge.otherID] = true
		label := ge.edgeType
		if ge.inbound && label != "" {
			label = label + " ←"
		}
		c := chunkForSymbol(ctx, chunks, callee.ID, req.RepoID)
		if c != nil {
			annotateChunkGroupProvenance([]*embeddings.Chunk{c}, "dependency", "graph dependency expansion")
		}
		out = append(out, &DependencyEdge{
			SymbolChunk: SymbolChunk{Symbol: callee, Chunk: c},
			EdgeType:    label,
			Depth:       ge.depth,
			GraphPath:   ge.path,
		})
		return true
	}

	for _, ge := range graph {
		if len(out) >= max {
			break
		}
		k := strings.ToUpper(strings.TrimSpace(ge.edgeType))
		if k == "" {
			k = "UNKNOWN"
		}
		if kindCount[k] >= perTypeCap {
			deferred = append(deferred, ge)
			continue
		}
		if addOne(ge) {
			kindCount[k]++
		}
	}

	// Fill remaining budget with deferred edges in original sorted order.
	for _, ge := range deferred {
		if len(out) >= max {
			break
		}
		_ = addOne(ge)
	}
	return out
}

func allocateRetrievalSectionBudgets(req ContextRequest) (maxDep, maxSimilar, maxFixtures, maxConfig int) {
	maxDep = req.MaxDependencyChunks
	maxSimilar = req.MaxSimilarTests
	maxFixtures = req.MaxFixtures
	maxConfig = req.MaxConfigChunks
	if maxDep < 0 {
		maxDep = 0
	}
	if maxSimilar < 0 {
		maxSimilar = 0
	}
	if maxFixtures < 0 {
		maxFixtures = 0
	}
	if maxConfig < 0 {
		maxConfig = 0
	}
	totalCap := req.MaxContextChunks
	if totalCap <= 0 {
		return maxDep, maxSimilar, maxFixtures, maxConfig
	}
	minFix := 1
	minCfg := 1
	if maxFixtures > 0 && maxFixtures < minFix {
		maxFixtures = minFix
	}
	if maxConfig > 0 && maxConfig < minCfg {
		maxConfig = minCfg
	}
	total := maxDep + maxSimilar + maxFixtures + maxConfig
	for total > totalCap {
		if maxDep > 1 {
			maxDep--
		} else if maxSimilar > 1 {
			maxSimilar--
		} else if maxFixtures > minFix {
			maxFixtures--
		} else if maxConfig > minCfg {
			maxConfig--
		} else {
			break
		}
		total = maxDep + maxSimilar + maxFixtures + maxConfig
	}
	return maxDep, maxSimilar, maxFixtures, maxConfig
}

func annotateChunkGroupProvenance(chunks []*embeddings.Chunk, sourceKind, reason string) {
	for _, ch := range chunks {
		if ch == nil {
			continue
		}
		meta := map[string]interface{}{}
		if len(ch.MetadataJSON) > 0 {
			_ = json.Unmarshal(ch.MetadataJSON, &meta)
		}
		meta["retrieval_source_kind"] = sourceKind
		meta["retrieval_reason"] = reason
		if b, err := json.Marshal(meta); err == nil {
			ch.MetadataJSON = b
		}
	}
}

func gatherSimilarReferenceChunks(ctx context.Context, chunks ChunkReader, targetChunk *embeddings.Chunk, req ContextRequest, fileModule string) []*embeddings.Chunk {
	profile := NormalizeRetrievalProfile(req.Profile)
	limit := req.MaxSimilarTests
	if limit <= 0 {
		limit = 5
	}
	types := similarChunkTypesForProfile(profile)
	assemblyPoolLimit := similarReferenceSearchPoolSize(limit)
	if assemblyPoolLimit < limit {
		assemblyPoolLimit = limit
	}
	seen := make(map[string]bool)
	var out []*embeddings.Chunk
	addChunk := func(ch *embeddings.Chunk) {
		if ch == nil {
			return
		}
		id := chunkStableKey(ch)
		if id == "" {
			id = ch.File + "\x00" + ch.ChunkType
		}
		if seen[id] {
			return
		}
		seen[id] = true
		cp := *ch
		out = append(out, &cp)
	}
	hybridMod := ""
	if targetChunk != nil {
		hybridMod = strings.TrimSpace(chunkModuleFromMetadataJSON(targetChunk.MetadataJSON))
	}
	if hybridMod == "" {
		hybridMod = strings.TrimSpace(fileModule)
	}
	if targetChunk != nil && len(targetChunk.Embedding) > 0 {
		poolSize := similarReferenceSearchPoolSize(limit)
		query := targetChunk.Embedding
		bestByKey := make(map[string]mmrScoredChunk)
		for _, ct := range types {
			searchOpts := embeddings.SearchOptions{
				RepoID:    req.RepoID,
				Lang:      req.Lang,
				ChunkType: ct,
				Limit:     poolSize,
			}
			if profile != ProfileJavaUnit && targetChunk.File != "" {
				dir := filepath.ToSlash(filepath.Dir(strings.TrimSpace(targetChunk.File)))
				if dir != "" && dir != "." {
					searchOpts.FilePrefix = dir + "/"
				}
			}
			useHybridModule := hybridMod != "" && !req.DisableHybridModuleFilter
			if useHybridModule {
				searchOpts.Module = hybridMod
			}
			similar, err := chunks.Search(ctx, targetChunk.Embedding, searchOpts)
			if err != nil {
				continue
			}
			if useHybridModule && shouldWidenHybridModuleSearch(len(similar), poolSize) {
				loose := searchOpts
				loose.Module = ""
				wide, werr := chunks.Search(ctx, targetChunk.Embedding, loose)
				if werr == nil && len(wide) > 0 {
					similar = append(append([]embeddings.SearchResult(nil), similar...), wide...)
				}
			}
			for i := range similar {
				sc := similar[i].Chunk
				key := chunkStableKey(&sc)
				if key == "" {
					continue
				}
				rel := cosineSimilarity(query, sc.Embedding)
				if rel == 0 && len(sc.Embedding) == 0 && similar[i].Distance > 0 {
					rel = 1.0 / (1.0 + similar[i].Distance)
				}
				if prev, ok := bestByKey[key]; ok && rel <= prev.relevance {
					continue
				}
				cp := sc
				bestByKey[key] = mmrScoredChunk{chunk: cp, relevance: rel}
			}
		}
		if len(bestByKey) > 0 {
			pool := make([]mmrScoredChunk, 0, len(bestByKey))
			for _, c := range bestByKey {
				pool = append(pool, c)
			}
			sortMMRPool(pool)
			lambda := normalizeSimilarMMRLambda(req.SimilarMMRLambda)
			for _, ch := range maximalMarginalRelevance(query, pool, assemblyPoolLimit, lambda) {
				addChunk(ch)
			}
		}
	}
	if len(out) < assemblyPoolLimit {
		for _, ct := range types {
			if len(out) >= assemblyPoolLimit {
				break
			}
			for _, ch := range listChunksByType(ctx, chunks, req.RepoID, req.Lang, ct, assemblyPoolLimit-len(out), hybridMod) {
				addChunk(ch)
			}
		}
	}
	if len(out) < assemblyPoolLimit {
		for _, ch := range listChunksByType(ctx, chunks, req.RepoID, req.Lang, "test", assemblyPoolLimit-len(out), hybridMod) {
			addChunk(ch)
		}
	}
	return assembleSegmentedContextWindows(out, limit)
}

type embeddingSegmentInfo struct {
	Index int
	Count int
}

type indexedSegmentChunk struct {
	ch  *embeddings.Chunk
	idx int
}

func assembleSegmentedContextWindows(in []*embeddings.Chunk, limit int) []*embeddings.Chunk {
	if len(in) == 0 || limit <= 0 {
		return in
	}
	segmentGroups := make(map[string][]*embeddings.Chunk)
	hasSegmented := false
	for _, ch := range in {
		if ch == nil {
			continue
		}
		if seg, ok := chunkEmbeddingSegmentInfo(ch); ok && seg.Count > 1 {
			hasSegmented = true
			segmentGroups[segmentGroupKey(ch)] = append(segmentGroups[segmentGroupKey(ch)], ch)
		}
	}
	if !hasSegmented {
		if len(in) > limit {
			return in[:limit]
		}
		return in
	}

	groupEmitted := make(map[string]bool)
	fileSegmentWindows := make(map[string]int)
	out := make([]*embeddings.Chunk, 0, minInt(limit, len(in)))
	for _, ch := range in {
		if ch == nil || len(out) >= limit {
			continue
		}
		seg, segmented := chunkEmbeddingSegmentInfo(ch)
		if !segmented || seg.Count <= 1 {
			out = append(out, cloneChunkForAssembly(ch))
			continue
		}
		gk := segmentGroupKey(ch)
		if groupEmitted[gk] {
			continue
		}
		groupEmitted[gk] = true
		fileKey := strings.TrimSpace(ch.File)
		if fileSegmentWindows[fileKey] >= maxSegmentWindowsPerFile {
			continue
		}
		windows := buildSegmentWindows(segmentGroups[gk], maxSegmentsPerWindow)
		for _, w := range windows {
			if len(out) >= limit || fileSegmentWindows[fileKey] >= maxSegmentWindowsPerFile {
				break
			}
			out = append(out, w)
			fileSegmentWindows[fileKey]++
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func buildSegmentWindows(chunks []*embeddings.Chunk, maxPerWindow int) []*embeddings.Chunk {
	if len(chunks) == 0 {
		return nil
	}
	if maxPerWindow <= 0 {
		maxPerWindow = maxSegmentsPerWindow
	}
	unique := make(map[int]*embeddings.Chunk)
	for _, ch := range chunks {
		if seg, ok := chunkEmbeddingSegmentInfo(ch); ok {
			if _, exists := unique[seg.Index]; !exists {
				unique[seg.Index] = ch
			}
		}
	}
	if len(unique) == 0 {
		return nil
	}
	ordered := make([]indexedSegmentChunk, 0, len(unique))
	for idx, ch := range unique {
		ordered = append(ordered, indexedSegmentChunk{ch: ch, idx: idx})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].idx < ordered[j].idx })

	var windows []*embeddings.Chunk
	start := 0
	for start < len(ordered) {
		end := start + 1
		for end < len(ordered) &&
			ordered[end].idx == ordered[end-1].idx+1 &&
			(end-start) < maxPerWindow {
			end++
		}
		windows = append(windows, mergeSegmentWindow(ordered[start:end]))
		start = end
	}
	return windows
}

func mergeSegmentWindow(parts []indexedSegmentChunk) *embeddings.Chunk {
	if len(parts) == 0 || parts[0].ch == nil {
		return nil
	}
	base := cloneChunkForAssembly(parts[0].ch)
	var b strings.Builder
	startLine := parts[0].ch.StartLine
	endLine := parts[0].ch.EndLine
	for i, p := range parts {
		if p.ch == nil {
			continue
		}
		if i == 0 {
			b.WriteString(strings.TrimSpace(p.ch.Content))
		} else {
			b.WriteString("\n\n")
			b.WriteString(strings.TrimSpace(p.ch.Content))
		}
		if p.ch.StartLine > 0 && (startLine == 0 || p.ch.StartLine < startLine) {
			startLine = p.ch.StartLine
		}
		if p.ch.EndLine > endLine {
			endLine = p.ch.EndLine
		}
	}
	base.Content = b.String()
	base.StartLine = startLine
	base.EndLine = endLine
	meta := map[string]interface{}{}
	if len(base.MetadataJSON) > 0 {
		_ = json.Unmarshal(base.MetadataJSON, &meta)
	}
	meta["retrieval_reassembled"] = true
	meta["retrieval_segment_window_start"] = parts[0].idx
	meta["retrieval_segment_window_end"] = parts[len(parts)-1].idx
	if raw, err := json.Marshal(meta); err == nil {
		base.MetadataJSON = raw
	}
	return base
}

func cloneChunkForAssembly(ch *embeddings.Chunk) *embeddings.Chunk {
	if ch == nil {
		return nil
	}
	cp := *ch
	if len(ch.Embedding) > 0 {
		cp.Embedding = append([]float32(nil), ch.Embedding...)
	}
	if len(ch.MetadataJSON) > 0 {
		cp.MetadataJSON = append([]byte(nil), ch.MetadataJSON...)
	}
	return &cp
}

func segmentGroupKey(ch *embeddings.Chunk) string {
	if ch == nil {
		return ""
	}
	return strings.TrimSpace(ch.File) + "\x00" + strings.TrimSpace(ch.ChunkType) + "\x00" + strings.TrimSpace(ch.SymbolID) + "\x00" + strings.TrimSpace(ch.ParentSymbolID)
}

func chunkEmbeddingSegmentInfo(ch *embeddings.Chunk) (embeddingSegmentInfo, bool) {
	if ch == nil || len(ch.MetadataJSON) == 0 {
		return embeddingSegmentInfo{}, false
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(ch.MetadataJSON, &raw); err != nil {
		return embeddingSegmentInfo{}, false
	}
	idx, ok1 := asInt(raw["embedding_segment_index"])
	cnt, ok2 := asInt(raw["embedding_segment_count"])
	if !ok1 || !ok2 || cnt <= 1 || idx < 0 {
		return embeddingSegmentInfo{}, false
	}
	return embeddingSegmentInfo{Index: idx, Count: cnt}, true
}

func asInt(v interface{}) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	default:
		return 0, false
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func chunkForSymbol(ctx context.Context, chunks ChunkReader, symbolID, repoID string) *embeddings.Chunk {
	list, err := chunks.List(ctx, embeddings.ListOptions{SymbolID: symbolID, RepoID: repoID, Limit: 1})
	if err != nil || len(list) == 0 {
		return nil
	}
	return &list[0]
}

func listChunksByType(ctx context.Context, chunks ChunkReader, repoID, lang, chunkType string, limit int, module string) []*embeddings.Chunk {
	if limit <= 0 {
		return nil
	}
	tryList := func(mod string) ([]embeddings.Chunk, error) {
		opts := embeddings.ListOptions{RepoID: repoID, ChunkType: chunkType, Lang: lang, Limit: limit}
		if mod != "" {
			opts.Module = mod
		}
		return chunks.List(ctx, opts)
	}
	list, err := tryList(strings.TrimSpace(module))
	if err != nil {
		return nil
	}
	if strings.TrimSpace(module) != "" && len(list) == 0 {
		list, err = tryList("")
		if err != nil {
			return nil
		}
	}
	out := make([]*embeddings.Chunk, len(list))
	for i := range list {
		out[i] = &list[i]
	}
	return out
}

func listChunksByPathPattern(ctx context.Context, chunks ChunkReader, repoID, lang string, substrings []string, limit int) []*embeddings.Chunk {
	list, err := chunks.List(ctx, embeddings.ListOptions{RepoID: repoID, Limit: 100})
	if err != nil {
		return nil
	}
	var out []*embeddings.Chunk
	for i := range list {
		if list[i].Lang != lang && lang != "" {
			continue
		}
		lower := strings.ToLower(list[i].File)
		for _, sub := range substrings {
			if strings.Contains(lower, sub) {
				out = append(out, &list[i])
				if len(out) >= limit {
					return out
				}
				break
			}
		}
	}
	return out
}

// dependencyEdgePriority returns a sort key for generic unit retrieval (lower = earlier):
// prefer behavior + collaborator seams first (CALLS/INJECTS), then structural inheritance/containment.
func dependencyEdgePriority(edgeType string) int {
	switch strings.ToLower(strings.TrimSpace(edgeType)) {
	case "calls":
		return 0
	case "injects", "injects_named", "implements_service":
		return 1
	case "extends", "registers_service":
		return 2
	case "implements":
		return 3
	case "contains":
		return 4
	case "imports":
		return 5
	default:
		return 6
	}
}

// typeNamesFromSignature extracts type-like names from the symbol's signature_json (e.g. Java param/return types).
// Returns a deduplicated list of capitalized tokens that look like class/interface names (skips primitives/keywords).
var javaPrimitives = map[string]bool{
	"void": true, "int": true, "long": true, "boolean": true, "double": true, "float": true,
	"byte": true, "short": true, "char": true,
}

var typeNameInSignatureRe = regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]*(?:\.[A-Za-z0-9]+)*)\b`)

func typeNamesFromSignature(sym *metadata.Symbol) []string {
	if sym == nil || len(sym.SignatureJSON) == 0 {
		return nil
	}
	var parsed struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(sym.SignatureJSON, &parsed); err != nil || parsed.Signature == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, m := range typeNameInSignatureRe.FindAllStringSubmatch(parsed.Signature, -1) {
		if len(m) < 2 {
			continue
		}
		tok := m[1]
		if javaPrimitives[strings.ToLower(tok)] {
			continue
		}
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}
