package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/asqs/asqs-core/internal/pathsafe"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// resolveSymbol finds one symbol by fully-qualified name.
//
// Several symbols can share an FQName — overloads within this repository. Rather than picking
// silently, the ambiguity is reported: a tool that answers about a different overload than the
// model asked about is worse than one that says which candidates exist.
//
// Cross-repository collisions are no longer among the causes: every lookup below is scoped to
// r.RepoID, which is what makes the "every tool is repo-scoped through RepoID" claim on Registry
// true rather than aspirational.
func (r *Registry) resolveSymbol(ctx context.Context, fqName string) (*metadata.Symbol, error) {
	fqName = strings.TrimSpace(fqName)
	if fqName == "" {
		return nil, fmt.Errorf("fq_name is required")
	}
	syms, err := r.Meta.ListSymbolsByFQName(ctx, r.RepoID, fqName)
	if err != nil {
		return nil, err
	}
	live := appendLiveSymbols(nil, syms)
	if len(live) == 0 {
		// B25: C# FQNames carry parameter lists and generic markers, but a model that read
		// "OrderService#GetOrder" in prose asks with the bare form. The indexer stores that form
		// in signature_json.bare_fq_name; stores that support the lookup resolve it here.
		// Overloads come back as multiple candidates and fall into the deterministic-first
		// handling below, same as same-FQName collisions always have.
		live = r.appendBareFQMatches(ctx, live, fqName)
	}
	if len(live) == 0 {
		// A model that has just been reading repo-relative PATHS asks with the separator it read:
		// "src/app/core/session-context.service.SessionContextService" for a symbol the index
		// stores dot-joined as "src.app.core.session-context.service.SessionContextService". Run
		// api-3c56b784842358e936ec60e505209bc6 lost three get_symbol / expand_symbol calls to that
		// one keystroke, on a symbol that WAS indexed.
		//
		// Reached only after the exact lookup missed, which is what makes it safe: the kinds whose
		// FQName legitimately carries a slash — E2E_SPEC:e2e/smoke.spec.ts,
		// PAGE_ROUTE:/checkout@src.app.app.routes:L21 — resolve on the first rung and never arrive
		// here. A misspelled one misses either way.
		if norm := normalizeFQNameSeparators(fqName); norm != fqName {
			if normSyms, nerr := r.Meta.ListSymbolsByFQName(ctx, r.RepoID, norm); nerr == nil {
				live = appendLiveSymbols(live, normSyms)
			}
			if len(live) == 0 {
				live = r.appendBareFQMatches(ctx, live, norm)
			}
		}
	}
	if len(live) == 0 {
		// The caller's own spelling, not a normalized one: an operator reading the audit needs to
		// see what the model actually asked for.
		return nil, noSymbolError{fq: fqName}
	}
	if len(live) > 1 {
		sort.Slice(live, func(i, j int) bool {
			if live[i].File != live[j].File {
				return live[i].File < live[j].File
			}
			return live[i].StartLine < live[j].StartLine
		})
	}
	return live[0], nil
}

// appendLiveSymbols appends the non-nil entries of syms to live. Every resolution rung filters the
// same way, and a nil entry from a store is not an error — it is a row the store could not hydrate.
func appendLiveSymbols(live []*metadata.Symbol, syms []*metadata.Symbol) []*metadata.Symbol {
	for _, s := range syms {
		if s != nil {
			live = append(live, s)
		}
	}
	return live
}

// appendBareFQMatches resolves fq through the optional bareFQLookup capability and appends what it
// finds. A store without the capability, or a lookup error, contributes nothing: this is a fallback
// rung, and turning its failure into the caller's failure would mask the real miss.
func (r *Registry) appendBareFQMatches(ctx context.Context, live []*metadata.Symbol, fq string) []*metadata.Symbol {
	bl, ok := r.Meta.(bareFQLookup)
	if !ok {
		return live
	}
	bare, err := bl.ListSymbolsByBareFQName(ctx, r.RepoID, metadata.BareFQName(fq))
	if err != nil {
		return live
	}
	return appendLiveSymbols(live, bare)
}

// normalizeFQNameSeparators rewrites path separators as the "." the indexer joins FQName segments
// with. Returns fq unchanged when it carries none, so callers can compare and skip the extra query.
func normalizeFQNameSeparators(fq string) string {
	if !strings.ContainsAny(fq, `/\`) {
		return fq
	}
	return strings.NewReplacer("/", ".", `\`, ".").Replace(fq)
}

// bareFQLookup is the optional store capability behind the B25 name-only fallback; the concrete
// metadata.Store implements it, test fakes opt in when they need it.
type bareFQLookup interface {
	ListSymbolsByBareFQName(ctx context.Context, repoID, bareFQ string) ([]*metadata.Symbol, error)
}

// noSymbolError is resolveSymbol's "nothing indexed under that name" miss — distinct from store
// errors so getSymbol can fall back to dependency documentation on a miss without masking real
// failures. The message is what the model sees; keep it phrased as before.
type noSymbolError struct{ fq string }

func (e noSymbolError) Error() string { return fmt.Sprintf("no symbol named %q is indexed", e.fq) }

func (r *Registry) getSymbol(ctx context.Context, args []byte) (string, error) {
	var in struct {
		FQName string `json:"fq_name"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	sym, err := r.resolveSymbol(ctx, in.FQName)
	if err != nil {
		var miss noSymbolError
		if errors.As(err, &miss) {
			// The miss ladder, cheapest-to-answer first and additive over B55: ingested dependency
			// docs, then compiler-authoritative member lists from the build classpath, then one web
			// search. Every rung exists because a fixer's questions are mostly about THIRD-PARTY
			// types the repo index by definition cannot answer — and a bare miss sent the model
			// back to guessing signatures, one wasted compile round per guess.
			if out, ok := r.dependencyDocForSymbol(ctx, miss.fq); ok {
				return out, nil
			}
			if r.ThirdPartySurface != nil {
				if out, ok := r.ThirdPartySurface(ctx, miss.fq); ok && strings.TrimSpace(out) != "" {
					return r.capped(out), nil
				}
			}
			if out, ok := r.webSearchForSymbolMiss(ctx, miss.fq); ok {
				return out, nil
			}
			return "", fmt.Errorf("%s; the index covers only this repository's sources%s", err, missFollowUpHint(r.Web != nil))
		}
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s:%d-%d (%s %s)\n", sym.FQName, sym.File, sym.StartLine, sym.EndLine, sym.Lang, sym.Kind)
	if sym.InDegreeNonTest > 0 || sym.OutDegree > 0 {
		fmt.Fprintf(&b, "callers: %d, callees: %d\n", sym.InDegreeNonTest, sym.OutDegree)
	}
	if r.Chunks != nil {
		list, err := r.Chunks.List(ctx, embeddings.ListOptions{
			SymbolID: sym.ID, RepoID: r.RepoID, Limit: 1, OmitEmbedding: true,
		})
		if err == nil && len(list) > 0 {
			b.WriteString("\n")
			b.WriteString(list[0].Content)
		}
	}
	return r.capped(b.String()), nil
}

// dependencyDocForSymbol answers a get_symbol miss from ingested dependency documentation
// (Spec B55): a model asking about a third-party type gets the installed dependency's own docs,
// labeled as such, instead of a dead end. Tried names, in order: the exact input as fq_name; the
// input with a trailing #member stripped (models append members to type names); the simple type
// name. Bounded at three List calls, and only on the miss path.
func (r *Registry) dependencyDocForSymbol(ctx context.Context, fq string) (string, bool) {
	if r.Chunks == nil {
		return "", false
	}
	base := fq
	if i := strings.Index(base, "#"); i > 0 {
		base = base[:i]
	}
	tries := []map[string]string{{"fq_name": fq}}
	if base != fq {
		tries = append(tries, map[string]string{"fq_name": base})
	}
	if i := strings.LastIndexAny(base, "./"); i >= 0 && i+1 < len(base) {
		tries = append(tries, map[string]string{"simple_name": base[i+1:]})
	} else {
		tries = append(tries, map[string]string{"simple_name": base})
	}
	for _, filter := range tries {
		fj, err := json.Marshal(filter)
		if err != nil {
			continue
		}
		list, err := r.Chunks.List(ctx, embeddings.ListOptions{
			RepoID:           r.RepoID,
			ChunkType:        embeddings.ChunkTypeDependencyDoc,
			MetadataContains: fj,
			Limit:            1,
			OmitEmbedding:    true,
		})
		if err != nil || len(list) == 0 {
			continue
		}
		c := list[0]
		// The store applied the ChunkType filter; re-check anyway so a filter regression can
		// never dress repository chunks up as dependency docs (or vice versa).
		if c.ChunkType != embeddings.ChunkTypeDependencyDoc {
			continue
		}
		var meta struct {
			Coordinate string `json:"coordinate"`
			Source     string `json:"dependency_source"`
		}
		_ = json.Unmarshal(c.MetadataJSON, &meta)
		var b strings.Builder
		fmt.Fprintf(&b, "%s is not defined in this repository; it comes from dependency %s (%s).\n", fq, meta.Coordinate, meta.Source)
		b.WriteString("Documentation from the installed version:\n\n")
		b.WriteString(c.Content)
		return r.capped(b.String()), true
	}
	return "", false
}

func (r *Registry) expandSymbol(ctx context.Context, args []byte) (string, error) {
	var in struct {
		FQName    string   `json:"fq_name"`
		Direction string   `json:"direction"`
		Depth     int      `json:"depth"`
		EdgeTypes []string `json:"edge_types"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	sym, err := r.resolveSymbol(ctx, in.FQName)
	if err != nil {
		return "", err
	}

	callees, callers := true, false
	switch strings.ToLower(strings.TrimSpace(in.Direction)) {
	case "", "callees":
	case "callers":
		callees, callers = false, true
	case "both":
		callers = true
	default:
		return "", fmt.Errorf("direction must be callers, callees or both; got %q", in.Direction)
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 2
	}
	if depth > 5 {
		// The tool is interactive; deeper walks return more than a turn can use and cost more than
		// they inform. The store's own cap is higher for batch callers.
		depth = 5
	}

	rows, err := r.Meta.ExpandGraph(ctx, r.RepoID, sym.ID, metadata.ExpandGraphOptions{
		Callees: callees, Callers: callers, MaxDepth: depth, MaxNodes: 40, EdgeTypes: in.EdgeTypes,
	})
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return fmt.Sprintf("no related symbols found for %s", sym.FQName), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d symbol(s) related to %s:\n", len(rows), sym.FQName)
	for _, row := range rows {
		dir := "calls"
		if row.Inbound {
			dir = "called by"
		}
		fmt.Fprintf(&b, "  [depth %d, %s, %s] %s  %s:%d\n",
			row.Depth, dir, row.EdgeType, row.Symbol.FQName, row.Symbol.File, row.Symbol.StartLine)
	}
	return r.capped(b.String()), nil
}

// maxSearchResults bounds how many chunks one search_code call may return.
//
// The ceiling used to be 20, chosen when each result was emitted whole and the registry cap simply
// cut the list short — so asking for 20 really meant "give me as many as fit", and the model never
// learned the rest had been dropped. Now that the cap is shared out per result (see shareBudget),
// a large k does not truncate the LIST, it shrinks every SNIPPET: at 20 each result gets roughly
// 290 characters, which is a fragment rather than a readable match. Eight keeps every snippet near
// 700 characters, still enough to see a method body and judge relevance.
const maxSearchResults = 8

func (r *Registry) searchCode(ctx context.Context, args []byte) (string, error) {
	var in struct {
		Query     string `json:"query"`
		ChunkType string `json:"chunk_type"`
		K         int    `json:"k"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return "", fmt.Errorf("query is required")
	}
	k := in.K
	if k <= 0 {
		k = 5
	}
	if k > maxSearchResults {
		k = maxSearchResults
	}
	opts := embeddings.SearchOptions{
		RepoID: r.RepoID, Lang: r.Lang, ChunkType: strings.TrimSpace(in.ChunkType),
		Limit: k, OmitEmbedding: true,
	}
	if opts.ChunkType == "" {
		// Dependency docs (B55) answer targeted lookups, not open code search: unasked-for
		// library text in every search_code answer would displace repository results. Passing
		// chunk_type:"dependency_doc" opts in explicitly.
		opts.ExcludeChunkType = embeddings.ChunkTypeDependencyDoc
	}

	// Dense search needs an embedding of the query text. Without an embedder the lexical channel
	// still answers a literal-identifier search, which is most of what this tool is asked for —
	// degrading to it beats refusing.
	var results []embeddings.SearchResult
	if r.Embedder != nil {
		vecs, err := r.Embedder.Embed(ctx, []string{q})
		if err == nil && len(vecs) == 1 {
			results, err = r.Chunks.Search(ctx, vecs[0], opts)
			if err != nil {
				return "", err
			}
		}
	}
	if len(results) == 0 {
		lex, err := r.Chunks.SearchLexical(ctx, q, opts)
		if err != nil {
			return "", err
		}
		results = lex
	}
	if len(results) == 0 {
		return fmt.Sprintf("no indexed code matched %q", q), nil
	}

	// Every result gets its own slice of the budget, rather than concatenating full chunk bodies
	// and letting the registry cap guillotine whatever comes last. Emitting them whole meant a
	// k=5 search over average Java chunks reached the 6000-char cap inside the first two or three
	// results and the rest were cut off mid-list — the model asked for five matches, got two and
	// a half, and nothing in the output said the others existed. Same bytes either way; this way
	// they buy five readable snippets instead of two verbatim ones and a cliff.
	header := fmt.Sprintf("%d result(s) for %q:\n", len(results), q)
	frames := make([]string, len(results))
	for i := range results {
		c := &results[i].Chunk
		frames[i] = fmt.Sprintf("\n--- %s:%d-%d (%s)\n", c.File, c.StartLine, c.EndLine, c.ChunkType)
	}
	bodies := make([]string, len(results))
	for i := range results {
		bodies[i] = results[i].Chunk.Content
	}
	budgets := shareBudget(bodies, r.maxChars()-len(header)-framesLen(frames)-len(results))

	var b strings.Builder
	b.WriteString(header)
	perResultCut := false
	for i := range results {
		body, cut := fitWithin(bodies[i], budgets[i])
		perResultCut = perResultCut || cut
		b.WriteString(frames[i])
		b.WriteString(body)
		b.WriteString("\n")
	}
	out := r.capped(b.String())
	if perResultCut {
		// capped() only reports ITS cut. A drilldown that reads the flag must not be told the
		// result was complete when this function trimmed the bodies itself.
		r.lastTruncated = true
	}
	return out, nil
}

func framesLen(frames []string) int {
	n := 0
	for _, f := range frames {
		n += len(f)
	}
	return n
}

// shareBudget divides total across items, max-min fair: an item shorter than its equal share takes
// only what it needs and donates the remainder to the longer ones.
//
// An even split would be simpler and worse. Search results are wildly uneven — a three-line getter
// next to a 200-line controller — and splitting evenly would pad the getter's allowance with space
// it cannot use while the controller, the result actually worth reading, gets clipped at the same
// arbitrary line. Allocating shortest-first lets every small result through whole and pools what
// they leave behind for the ones that need it.
func shareBudget(items []string, total int) []int {
	out := make([]int, len(items))
	if len(items) == 0 {
		return out
	}
	if total <= 0 {
		return out
	}
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return len(items[order[a]]) < len(items[order[b]]) })

	remaining, left := total, len(order)
	for _, idx := range order {
		share := remaining / left
		if n := len(items[idx]); n < share {
			share = n
		}
		out[idx] = share
		remaining -= share
		left--
	}
	return out
}

// fitWithin trims s so the returned string — truncation marker included — is at most budget bytes.
//
// The marker has to be paid for out of the same budget, or a per-result allowance of N produces
// N+47 bytes and the sum overshoots the cap this function exists to respect. Its own length
// depends on how much it says was shown, so the fit is found by stepping down the few bytes the
// digit count can move.
func fitWithin(s string, budget int) (string, bool) {
	if len(s) <= budget {
		return s, false
	}
	if budget <= 0 {
		return "", true
	}
	marker := func(shown int) string {
		return fmt.Sprintf("\n… [truncated: %d of %d characters shown]", shown, len(s))
	}
	shown := budget - len(marker(budget))
	for shown > 0 && shown+len(marker(shown)) > budget {
		shown--
	}
	if shown <= 0 {
		// No room for content and a marker both. Say the result was dropped rather than emitting
		// a marker with nothing under it.
		return "", true
	}
	// Never split a rune: the tail would be invalid UTF-8, which the OpenAI client then rewrites
	// to U+FFFD, putting a replacement character in the model's view of the source.
	for shown > 0 && !utf8.RuneStart(s[shown]) {
		shown--
	}
	if shown <= 0 {
		return "", true
	}
	return s[:shown] + marker(shown), true
}

func (r *Registry) findTestsFor(ctx context.Context, args []byte) (string, error) {
	var in struct {
		FQName string `json:"fq_name"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	sym, err := r.resolveSymbol(ctx, in.FQName)
	if err != nil {
		return "", err
	}
	edges, err := r.Meta.GetEdgesTo(ctx, r.RepoID, sym.ID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, e := range edges {
		if e == nil || !strings.EqualFold(strings.TrimSpace(e.EdgeType), metadata.EdgeTypeTestsSource) {
			continue
		}
		caller, err := r.Meta.GetSymbolByID(ctx, r.RepoID, e.CallerSymbolID)
		if err != nil || caller == nil || seen[caller.ID] {
			continue
		}
		seen[caller.ID] = true
		fmt.Fprintf(&b, "  %s  %s:%d\n", caller.FQName, caller.File, caller.StartLine)
	}
	if b.Len() == 0 {
		return fmt.Sprintf("no existing tests cover %s", sym.FQName), nil
	}
	return r.capped(fmt.Sprintf("tests covering %s:\n%s", sym.FQName, b.String())), nil
}

// maxReadLines bounds read_file_range regardless of the requested span.
const maxReadLines = 400

func (r *Registry) readFileRange(args []byte) (string, error) {
	var in struct {
		Path  string `json:"path"`
		Start int    `json:"start"`
		End   int    `json:"end"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	root := strings.TrimSpace(r.RepoRoot)
	if root == "" {
		return "", fmt.Errorf("read_file_range is unavailable: no repository root is configured")
	}
	// Containment is checked against the repo root before the path is ever opened. This is the same
	// helper the Copilot permission gate uses; a model-supplied path is exactly as untrusted.
	rel, ok := pathsafe.ContainedRelPath(in.Path, root)
	if !ok {
		return "", fmt.Errorf("path %q is outside the repository", in.Path)
	}
	if in.Start <= 0 {
		in.Start = 1
	}
	if in.End < in.Start {
		return "", fmt.Errorf("end (%d) must be >= start (%d)", in.End, in.Start)
	}
	if in.End-in.Start+1 > maxReadLines {
		in.End = in.Start + maxReadLines - 1
	}

	full := filepath.Join(root, filepath.FromSlash(rel))
	// Reject anything that is not a regular file: a symlink pointing outside the repo would pass
	// the textual containment check and then be followed by ReadFile.
	info, err := os.Lstat(full)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", rel)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", rel, err)
	}
	lines := strings.Split(string(data), "\n")
	if in.Start > len(lines) {
		return "", fmt.Errorf("%s has %d lines; start %d is past the end", rel, len(lines), in.Start)
	}
	end := in.End
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d-%d\n", rel, in.Start, end)
	for i := in.Start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}
	return r.capped(b.String()), nil
}
