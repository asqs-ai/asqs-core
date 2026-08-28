package retrieval

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
)

// FusionMode selects how per-channel candidate lists are combined.
type FusionMode string

const (
	// FusionDense is the previous behaviour: one dense search per chunk_type, merged by keeping the
	// best raw cosine per chunk.
	FusionDense FusionMode = "dense"
	// FusionRRF fuses the dense lists and a lexical list by reciprocal rank.
	FusionRRF FusionMode = "rrf"
)

// NormalizeFusionMode maps config input to a canonical mode. Empty defaults to dense so behaviour
// is unchanged until the mode is explicitly enabled and measured.
func NormalizeFusionMode(s string) FusionMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(FusionRRF), "reciprocal_rank", "hybrid":
		return FusionRRF
	default:
		return FusionDense
	}
}

// rrfK is the standard RRF constant (Cormack et al., 2009). It damps the contribution of top ranks
// so a single channel cannot dominate, and makes the score robust to lists of different lengths.
const rrfK = 60

// FuseRRF combines ranked lists by reciprocal rank: score(d) = Σ 1/(k + rank(d) + 1).
//
// This fixes two defects with one mechanism.
//
// **The lexical gap.** Adding a lexical channel is only useful if its results survive the merge.
// RRF guarantees that: a chunk ranked 1st lexically and 8th densely still scores highly, because
// contributions add across channels.
//
// **Invalid cross-corpus score comparison.** The previous merge kept the best raw cosine per chunk
// across up to six per-chunk_type searches. Each chunk_type has its own similarity distribution —
// `api_contract` chunks are short and formulaic and cluster at high cosine against almost anything,
// while `test` chunks are long and varied — so the pool was systematically skewed toward whichever
// type produced higher absolute numbers, independent of usefulness. RRF compares *ranks within each
// list*, never scores across lists, so that skew disappears without needing per-type calibration.
func FuseRRF(lists [][]embeddings.SearchResult) map[string]float64 {
	score := map[string]float64{}
	for _, list := range lists {
		// One contribution per document per list. RRF's whole premise is "how highly did each
		// channel rank this?", so a document appearing twice in one list must not score twice —
		// it would be indistinguishable from two channels agreeing. This is not hypothetical:
		// the module-widening path used to concatenate its two searches into a single list,
		// guaranteeing duplicates for every chunk both searches found.
		seen := make(map[string]bool, len(list))
		for rank := range list {
			key := chunkStableKey(&list[rank].Chunk)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			score[key] += 1.0 / float64(rrfK+rank+1)
		}
	}
	return score
}

// normalizeFusedScores rescales RRF scores into [0,1], preserving order.
//
// RRF scores are tiny and their range depends on the number of channels: with two lists they span
// roughly 0.005-0.033. MMR then computes `lambda*relevance - (1-lambda)*maxSim`, where maxSim is a
// cosine in [0,1]. At the default lambda of 0.5 that makes the relevance term about sixty times
// weaker than the redundancy term, so MMR stops ranking by relevance almost entirely and becomes
// "pick whatever is least similar to what is already picked".
//
// MMR is defined for a relevance signal on the same scale as its similarity measure; feeding it raw
// RRF scores is a unit error, not a tuning choice. Rescaling is order-preserving, so the fusion
// result itself is unchanged — only the trade-off against diversity is restored.
//
// Min-max is used rather than dividing by the theoretical maximum (nLists/(rrfK+1)) because the
// latter compresses the range whenever no document tops every list, which is the normal case. The
// cost is sensitivity to a single outlier; that trade is measurable with the B06 suite and should
// be revisited there rather than assumed.
func normalizeFusedScores(fused map[string]float64) map[string]float64 {
	if len(fused) == 0 {
		return fused
	}
	first := true
	var lo, hi float64
	for _, v := range fused {
		if first {
			lo, hi, first = v, v, false
			continue
		}
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	out := make(map[string]float64, len(fused))
	spread := hi - lo
	for k, v := range fused {
		if spread <= 0 {
			// Every candidate tied: any constant works, and 1 keeps them all fully relevant so
			// MMR falls through to its diversity tie-break rather than zeroing the pool.
			out[k] = 1
			continue
		}
		out[k] = (v - lo) / spread
	}
	return out
}

// mergeByBestDistance combines two dense result lists into one properly ranked list.
//
// The module-widening path used to do `append(similar, wide...)`, which produces a list whose tail
// is not ordered by relevance and whose overlap is duplicated. Under `dense` that is invisible —
// the merge keeps the best cosine per chunk and ordering never matters — but RRF consumes RANKS, so
// a mis-ordered concatenation feeds it fabricated positions.
//
// The final sort key is total: (distance, file, start_line, id). A coarse score with no unique final
// key is the same defect that made SearchLexical's ranking irreproducible and gap ordering
// non-deterministic before B05.
func mergeByBestDistance(a, b []embeddings.SearchResult) []embeddings.SearchResult {
	if len(b) == 0 {
		return a
	}
	byKey := make(map[string]embeddings.SearchResult, len(a)+len(b))
	order := make([]string, 0, len(a)+len(b))
	add := func(list []embeddings.SearchResult) {
		for i := range list {
			key := chunkStableKey(&list[i].Chunk)
			if key == "" {
				continue
			}
			prev, ok := byKey[key]
			if !ok {
				byKey[key] = list[i]
				order = append(order, key)
				continue
			}
			if list[i].Distance < prev.Distance {
				byKey[key] = list[i]
			}
		}
	}
	add(a)
	add(b)

	out := make([]embeddings.SearchResult, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// identifierSplit matches camelCase / PascalCase boundaries so `OrderService` also contributes
// `Order` and `Service` as query terms.
var identifierSplit = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// LexicalQueryForTarget synthesizes a lexical query from the target symbol.
//
// There is no user query in this system — ContextRequest carries a SymbolID and the dense query
// vector is the target chunk's own embedding — so a lexical channel needs a query built from what
// the target is: its simple name, its enclosing type, and the type names in its signature.
//
// Identifier splitting matters: a `simple` tsvector tokenizes `OrderService` as one lexeme, so a
// chunk mentioning `Order` and `Service` separately would not match without it.
func LexicalQueryForTarget(sym *metadata.Symbol, signatureTypeNames []string) string {
	if sym == nil {
		return ""
	}
	var parts []string
	fq := strings.TrimSpace(sym.FQName)
	if fq != "" {
		parts = append(parts, simpleNameOf(fq))
		if enc := enclosingTypeNameOf(fq); enc != "" {
			parts = append(parts, enc)
		}
	}
	parts = append(parts, signatureTypeNames...)

	seen := map[string]bool{}
	var terms []string
	for _, p := range parts {
		for _, tok := range splitIdentifier(p) {
			tok = strings.TrimSpace(tok)
			if len(tok) < 2 || seen[strings.ToLower(tok)] {
				continue
			}
			seen[strings.ToLower(tok)] = true
			terms = append(terms, tok)
		}
	}
	return strings.Join(terms, " ")
}

// splitIdentifier yields the identifier itself plus its camelCase parts.
func splitIdentifier(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := []string{s}
	spaced := identifierSplit.ReplaceAllString(s, "${1} ${2}")
	for _, f := range strings.FieldsFunc(spaced, func(r rune) bool {
		return r == ' ' || r == '_' || r == '.' || r == '#' || r == '<' || r == '>' || r == ',' || r == '[' || r == ']'
	}) {
		if f != s {
			out = append(out, f)
		}
	}
	return out
}

// simpleNameOf returns the final segment of a fully-qualified name. BareFQName first: a B25
// parameter list can contain dots and commas ("M(Outer.Inner)"), and "last separator wins" over
// the raw string would return garbage like "Inner)".
func simpleNameOf(fq string) string {
	fq = metadata.BareFQName(fq)
	if i := strings.LastIndexAny(fq, ".#"); i >= 0 && i+1 < len(fq) {
		return fq[i+1:]
	}
	return fq
}

// enclosingTypeNameOf returns the type portion of a member FQName ("a.b.Order#place" -> "Order").
func enclosingTypeNameOf(fq string) string {
	fq = metadata.BareFQName(fq)
	i := strings.LastIndex(fq, "#")
	if i < 0 {
		return ""
	}
	typePart := fq[:i]
	if j := strings.LastIndex(typePart, "."); j >= 0 && j+1 < len(typePart) {
		return typePart[j+1:]
	}
	return typePart
}

// lexicalChannel runs the lexical search for a target, returning a ranked list to fuse.
// Returns nil when no query can be synthesized or the store has no lexical index yet.
func lexicalChannel(ctx context.Context, chunks ChunkReader, req ContextRequest, target *embeddings.Chunk, poolSize int, module string) []embeddings.SearchResult {
	ls, ok := chunks.(lexicalSearcher)
	if !ok || target == nil {
		return nil
	}
	q := strings.TrimSpace(req.LexicalQuery)
	if q == "" {
		return nil
	}
	opts := embeddings.SearchOptions{
		RepoID: req.RepoID,
		Lang:   req.Lang,
		Limit:  poolSize,
		// The dense channel reaches this pool only through the profile's chunk-type allowlist,
		// which can never name dependency_doc (see similarChunkTypesForProfile and its pin test).
		// The lexical channel has no type filter at all — synthesized queries carry framework
		// vocabulary ("Pageable", "findAll") that dependency documentation matches heavily, so
		// without this exclusion B55 ingestion would push library text into the MMR pool and
		// displace repository chunks. Dependency docs stay opt-in surfaces: get_symbol fallback
		// and an explicit search_code chunk_type.
		ExcludeChunkType: embeddings.ChunkTypeDependencyDoc,
		// Embeddings are REQUIRED here, not an optimisation to skip. These results are inserted
		// into the MMR pool, and MMR scores diversity as cosine against already-picked chunks:
		// a nil embedding yields similarity 0, i.e. "maximally novel", so every lexical-only hit
		// outranks dense hits that are legitimately similar to one another. SearchOptions.
		// OmitEmbedding says exactly this ("not for anything that reaches MMR"); the previous
		// value contradicted it, and SearchLexical ignored the flag anyway.
		OmitEmbedding: false,
	}
	if module != "" && !req.DisableHybridModuleFilter {
		opts.Module = module
	}
	out, err := ls.SearchLexical(ctx, q, opts)
	if err != nil {
		// A corpus without content_tsv is an expected, benign state: dense-only is the correct
		// fallback and the operator already knows migrate is pending.
		if errors.Is(err, embeddings.ErrLexicalIndexUnavailable) {
			return nil
		}
		// Anything else means `fusion: rrf` silently became `fusion: dense`. That is precisely how
		// B09's flag stayed inert while its A/B was recorded as meaningful, so it is counted and
		// surfaced rather than discarded. See LexicalFailures.
		recordLexicalFailure(err)
		return nil
	}
	return out
}

// lexicalFailures counts lexical-channel errors that were NOT "no index yet", and keeps the most
// recent one.
//
// A counter is a poor substitute for an audit event, and this is one: ContextRequest carries no
// auditor, and threading one through Retrieve for this alone would touch every call site. What
// makes it worth having anyway is that it has a reader — `qualitybot retrieval-eval` prints it in
// its summary. A silent lexical failure invalidates exactly that command's output, so the number
// belongs where the measurement is read. (Contrast llembed.TruncationCount, which nothing reads.)
var lexicalFailures struct {
	sync.Mutex
	count int
	last  error
}

func recordLexicalFailure(err error) {
	lexicalFailures.Lock()
	defer lexicalFailures.Unlock()
	lexicalFailures.count++
	lexicalFailures.last = err
}

// LexicalFailures returns the number of lexical-channel failures since process start and the most
// recent error. A non-zero count during a retrieval-eval run means the reported numbers are for
// dense retrieval regardless of the -fusion flag.
func LexicalFailures() (int, error) {
	lexicalFailures.Lock()
	defer lexicalFailures.Unlock()
	return lexicalFailures.count, lexicalFailures.last
}

// lexicalSearcher is implemented by stores with a lexical index. Kept optional so a ChunkReader
// without one (tests, older stores) simply contributes no lexical channel.
type lexicalSearcher interface {
	SearchLexical(ctx context.Context, query string, opts embeddings.SearchOptions) ([]embeddings.SearchResult, error)
}
