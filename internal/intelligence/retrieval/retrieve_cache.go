package retrieval

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/singleflight"
)

// normalizeContextRequestForRetrieveKey returns a copy of req with the same defaults applied as
// Retrieve() at the start of that function, so cache keys align with effective retrieval parameters.
func normalizeContextRequestForRetrieveKey(req ContextRequest) ContextRequest {
	out := req
	out.SymbolID = strings.TrimSpace(req.SymbolID)
	out.Lang = strings.TrimSpace(req.Lang)
	out.RepoID = strings.TrimSpace(req.RepoID)
	out.Profile = NormalizeRetrievalProfile(req.Profile)
	out.FailureHint = req.FailureHint
	out.DisableHybridModuleFilter = req.DisableHybridModuleFilter
	if out.MaxDependencyChunks <= 0 {
		out.MaxDependencyChunks = 15
	}
	if out.MaxSimilarTests <= 0 {
		out.MaxSimilarTests = 5
	}
	if out.MaxFixtures <= 0 {
		out.MaxFixtures = 5
	}
	if out.MaxConfigChunks <= 0 {
		out.MaxConfigChunks = defaultMaxConfigChunks
	}
	if out.DependencyMaxDepth <= 0 {
		out.DependencyMaxDepth = defaultDependencyMaxDepth
	}
	out.SimilarMMRLambda = normalizeSimilarMMRLambda(req.SimilarMMRLambda)
	return out
}

// retrievalCacheKey is a stable JSON key for within-run memoization (symbol + profile + budgets + hint flags).
func retrievalCacheKey(req ContextRequest) string {
	n := normalizeContextRequestForRetrieveKey(req)
	type key struct {
		SymbolID      string  `json:"symbol_id"`
		Lang          string  `json:"lang"`
		RepoID        string  `json:"repo_id"`
		Profile       string  `json:"profile"`
		MaxDep        int     `json:"max_dep"`
		MaxSim        int     `json:"max_sim"`
		MaxFix        int     `json:"max_fix"`
		MaxCfg        int     `json:"max_cfg"`
		MaxCtx        int     `json:"max_ctx"`
		DepDepth      int     `json:"dep_depth"`
		Lambda        float64 `json:"lambda"`
		FailureHint   string  `json:"failure_hint"`
		DisableHybrid bool    `json:"disable_hybrid"`
	}
	b, err := json.Marshal(key{
		SymbolID:      n.SymbolID,
		Lang:          n.Lang,
		RepoID:        n.RepoID,
		Profile:       string(n.Profile),
		MaxDep:        n.MaxDependencyChunks,
		MaxSim:        n.MaxSimilarTests,
		MaxFix:        n.MaxFixtures,
		MaxCfg:        n.MaxConfigChunks,
		MaxCtx:        n.MaxContextChunks,
		DepDepth:      n.DependencyMaxDepth,
		Lambda:        n.SimilarMMRLambda,
		FailureHint:   n.FailureHint,
		DisableHybrid: n.DisableHybridModuleFilter,
	})
	if err != nil {
		return n.SymbolID + "|" + string(n.Profile) + "|" + n.RepoID
	}
	return string(b)
}

// withinRunRetrieveCache memoizes successful Retrieve results for one plan build (CreateTestPlan / CreateE2ETestPlan).
// Concurrent requests for the same key share one Retrieve via singleflight; later lookups hit an in-memory map.
type withinRunRetrieveCache struct {
	mu       sync.Mutex
	fresh    map[string]*RetrievalContext
	sf       singleflight.Group
	fastHits atomic.Int64
	coalesce atomic.Int64
}

func newWithinRunRetrieveCache() *withinRunRetrieveCache {
	return &withinRunRetrieveCache{fresh: make(map[string]*RetrievalContext)}
}

func (c *withinRunRetrieveCache) fastPathHits() int64 {
	if c == nil {
		return 0
	}
	return c.fastHits.Load()
}

func (c *withinRunRetrieveCache) coalesceHits() int64 {
	if c == nil {
		return 0
	}
	return c.coalesce.Load()
}

// getOrRetrieve returns cached context for key when present; otherwise runs fn once per key (coalescing concurrent callers).
func (c *withinRunRetrieveCache) getOrRetrieve(ctx context.Context, key string, fn func() (*RetrievalContext, error)) (*RetrievalContext, error) {
	if c == nil {
		return fn()
	}
	c.mu.Lock()
	if v, ok := c.fresh[key]; ok {
		c.mu.Unlock()
		c.fastHits.Add(1)
		return v, nil
	}
	c.mu.Unlock()

	v, err, shared := c.sf.Do(key, func() (interface{}, error) {
		// Another goroutine may have finished and populated fresh after our outer map miss
		// but before we entered Do — avoid running fn twice (see within-run cache tests).
		c.mu.Lock()
		if existing, ok := c.fresh[key]; ok {
			c.mu.Unlock()
			return existing, nil
		}
		c.mu.Unlock()
		out, e := fn()
		if e != nil || out == nil {
			return nil, e
		}
		c.mu.Lock()
		if existing, ok := c.fresh[key]; ok {
			c.mu.Unlock()
			return existing, nil
		}
		c.fresh[key] = out
		c.mu.Unlock()
		return out, nil
	})
	if shared {
		c.coalesce.Add(1)
	}
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.(*RetrievalContext), nil
}
