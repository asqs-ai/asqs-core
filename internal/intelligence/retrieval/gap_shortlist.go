package retrieval

import (
	"context"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// shortlistMultiplier controls how many top-ranked candidates get a chunk fetch for branch-intent
// scoring. 4×MaxGaps (40 by default) is enough headroom for branch evidence to reorder the head of
// the list, while staying ~3 round-trip batches at maxConcurrencyListGaps.
const shortlistMultiplier = 4

// enclosingTypeSymbol resolves the type that declares sym, memoised per declaring FQName because a
// file's methods all share one. Returns nil when the type cannot be resolved — callers must treat
// that as "unknown", never as a licence to guess.
func enclosingTypeSymbol(ctx context.Context, meta GapMetaReader, repoID string, sym *metadata.Symbol, cache *sync.Map) *metadata.Symbol {
	if meta == nil || sym == nil {
		return nil
	}
	classFQ, ok := classFQFromMethodOrType(sym)
	if !ok || classFQ == "" {
		return nil
	}
	key := classFQ + "\x00" + sym.File
	if cache != nil {
		if v, hit := cache.Load(key); hit {
			s, _ := v.(*metadata.Symbol)
			return s
		}
	}
	var found *metadata.Symbol
	if candidates, err := meta.ListSymbolsByFQName(ctx, repoID, classFQ); err == nil {
		for _, c := range candidates {
			if c == nil || c.ID == "" || c.File != sym.File {
				continue
			}
			if isEnclosingTypeKind(c.Kind) {
				found = c
				break
			}
		}
	}
	if cache != nil {
		cache.Store(key, found)
	}
	return found
}

// isEnclosingTypeKind covers every kind that can declare members. hasInboundTestsSourceTrace
// originally accepted only "class", so the TESTS_SOURCE penalty silently never fired for methods
// declared on interfaces, records, or structs — a latent bug fixed here alongside the reuse.
func isEnclosingTypeKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "class", "interface", "record", "struct", "enum":
		return true
	default:
		return false
	}
}

// hasInboundTestsSourceTraceWithType is hasInboundTestsSourceTrace with the declaring type already
// resolved, so the eligibility filter and the traceability penalty share one lookup.
func hasInboundTestsSourceTraceWithType(ctx context.Context, meta GapMetaReader, repoID string, sym, enclosing *metadata.Symbol) bool {
	if sym == nil || sym.ID == "" || meta == nil {
		return false
	}
	if edgesTo, err := meta.GetEdgesTo(ctx, repoID, sym.ID); err == nil {
		for _, e := range edgesTo {
			if edgeTypeEqual(e, metadata.EdgeTypeTestsSource) {
				return true
			}
		}
	}
	if enclosing == nil || enclosing.ID == "" {
		return false
	}
	edgesClass, err := meta.GetEdgesTo(ctx, repoID, enclosing.ID)
	if err != nil {
		return false
	}
	for _, e := range edgesClass {
		if edgeTypeEqual(e, metadata.EdgeTypeTestsSource) {
			return true
		}
	}
	return false
}

// refineShortlistWithBranchIntents adds branch-intent evidence to the top of an already-sorted
// candidate list and re-sorts it.
//
// Branch intents are the strongest available signal for "this symbol has behaviour worth testing",
// but they need the symbol's source chunk. Computing them for every candidate would mean one chunk
// fetch per symbol across the whole repo; restricting it to the shortlist keeps the cost bounded
// while still letting branch evidence decide the head of the list, which is all that survives
// MaxGaps anyway.
//
// A nil chunks reader (or an empty list) returns the input untouched.
func refineShortlistWithBranchIntents(ctx context.Context, chunks ChunkReader, opts PlanOptions, list []*TestGap) []*TestGap {
	if chunks == nil || len(list) == 0 {
		return list
	}
	limit := opts.MaxGaps * shortlistMultiplier
	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}
	shortlist := list[:limit]

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrencyListGaps)
	var mu sync.Mutex
	for _, gap := range shortlist {
		gap := gap
		if gap == nil || gap.Symbol == nil || gap.Symbol.ID == "" {
			continue
		}
		g.Go(func() error {
			c := chunkForSymbol(gctx, chunks, gap.Symbol.ID, opts.RepoID)
			if c == nil || strings.TrimSpace(c.Content) == "" {
				return nil
			}
			intents := inferBranchIntentsFromContent(c.Content)
			if len(intents) == 0 {
				return nil
			}
			// Re-score with branch evidence: recompute the delta rather than adding blindly, so
			// the stage-1 contribution is not counted twice.
			bonus := TestabilityScore(gap.Symbol, 0, intents) - TestabilityScore(gap.Symbol, 0, nil)
			mu.Lock()
			gap.BranchIntents = intents
			gap.TestabilityScore += bonus
			gap.Priority += bonus
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	return sortByPriority(list)
}

// auditGapFilter records how many candidates the eligibility filter dropped and why. Without a
// per-reason breakdown a mis-tuned rule is invisible — the plan just silently gets smaller.
func auditGapFilter(ctx context.Context, opts PlanOptions, byReason map[string]int, totalCandidates, remaining int) {
	if opts.Audit == nil || len(byReason) == 0 {
		return
	}
	opts.Audit.Log(ctx, "plan.gaps_filtered_ineligible", map[string]interface{}{
		"message":          "Dropped gap candidates that do not represent testable behaviour (interface members with no body, framework @Bean factories, trivial accessors).",
		"by_reason":        byReason,
		"total_candidates": totalCandidates,
		"remaining":        remaining,
	})
}

// auditGapFilterFallback fires when the filter removed every candidate. The run continues on the
// unfiltered ranking: a plan of weak gaps is more useful than an empty run, and this event is what
// tells an operator the repo has no strongly testable surface left.
func auditGapFilterFallback(ctx context.Context, opts PlanOptions, byReason map[string]int, totalCandidates int) {
	if opts.Audit == nil {
		return
	}
	opts.Audit.LogError(ctx, "plan.all_candidates_filtered", map[string]interface{}{
		"message":          "Every gap candidate was filtered as untestable (interface members, framework config beans, trivial accessors). Falling back to the unfiltered ranking so the run still produces a plan — but the selected symbols carry little behaviour to test.",
		"by_reason":        byReason,
		"total_candidates": totalCandidates,
	})
}
