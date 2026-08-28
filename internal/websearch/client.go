package websearch

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Client is what the tool layer holds: search + fetch under one config, one cache, one audit
// stream. Nil Client = web access off, and the tools are simply not registered.
type Client struct {
	provider Provider
	fetcher  *Fetcher
	cache    *diskCache
	offline  bool
	deny     []string
	// cannedFetchBody, when non-empty, answers every gated fetch without a network call.
	// Set only by NewForTest; the gates (ledger, allow-list, canonicalization) still apply first.
	cannedFetchBody string
	// Audit receives websearch.query / websearch.fetch events. The exact query STRING that left
	// the process is an acceptance criterion, not a nicety: it is the evidence an operator in a
	// regulated deployment shows to prove what crossed the boundary.
	Audit func(ctx context.Context, step string, payload map[string]any)
}

// New validates cfg into a Client, or returns (nil, reason) when web access must stay off.
//
// A nil Client with a reason — rather than an error — mirrors ResolveMode's shape: "off" is a
// legitimate resolution the caller audits, not a failure it retries.
func New(cfg Config) (*Client, string) {
	if !cfg.Enabled {
		return nil, "websearch is disabled by configuration"
	}
	cache := newDiskCache(cfg.CachePath, cfg.CacheTTL)
	if cfg.Offline && cache == nil {
		return nil, "offline mode requires a cache_dir; without one there is nothing to serve from"
	}
	name := strings.ToLower(strings.TrimSpace(cfg.ProviderName))
	var p Provider
	if cfg.Offline {
		// Offline mode is pure replay: no endpoint, no key, no validation of either — an
		// air-gapped host cannot satisfy them and must not need to. The configured provider NAME
		// is kept because cache keys include it; replaying a cache written by "searxng" requires
		// asking for "searxng" keys.
		if name == "" {
			return nil, "general.websearch.provider is empty; offline replay needs the name the cache was written under"
		}
		p = replayProvider{name: name}
	} else {
		var err error
		switch name {
		case "searxng":
			p, err = newSearxng(cfg.Endpoint)
		case "brave":
			p, err = newBrave(cfg.APIKey)
		case "":
			return nil, "general.websearch.provider is empty"
		default:
			return nil, fmt.Sprintf("unknown general.websearch.provider %q (searxng or brave)", cfg.ProviderName)
		}
		if err != nil {
			return nil, err.Error()
		}
	}
	return &Client{
		provider: p,
		fetcher:  NewFetcher(cfg.AllowedHosts, cfg.MaxFetchBytes),
		cache:    cache,
		offline:  cfg.Offline,
		deny:     append([]string(nil), cfg.QueryDenyTokens...),
	}, ""
}

// ProviderName reports the resolved backend for audit lines.
func (c *Client) ProviderName() string { return c.provider.Name() }

// FetchEnabled reports whether web_fetch can ever succeed under this configuration.
func (c *Client) FetchEnabled() bool { return len(c.fetcher.allowedHosts) > 0 }

// Search sanitizes, serves from cache when possible, and otherwise queries the provider. The
// sanitized string is returned so the tool layer can show the model what was actually asked.
func (c *Client) Search(ctx context.Context, rawQuery string, k int) (results []Result, sentQuery string, cached bool, err error) {
	if k <= 0 {
		k = DefaultSearchK
	}
	if k > MaxSearchK {
		k = MaxSearchK
	}
	sentQuery = SanitizeQuery(rawQuery, c.deny)
	if sentQuery == "" {
		return nil, "", false, fmt.Errorf("websearch: query is empty after sanitization; use library and API terms, not repository identifiers or paths")
	}
	key := searchCacheKey(c.provider.Name(), sentQuery, k)
	var fromCache []Result
	if c.cache.get(key, c.offline, &fromCache) {
		c.auditQuery(ctx, sentQuery, k, true)
		return fromCache, sentQuery, true, nil
	}
	if c.offline {
		// The whole point of offline mode is that a miss is an ANSWER, never a network call.
		return nil, sentQuery, false, fmt.Errorf("websearch: offline mode and this query is not cached")
	}
	results, err = c.provider.Search(ctx, sentQuery, k)
	if err != nil {
		return nil, sentQuery, false, err
	}
	c.cache.put(key, results)
	c.auditQuery(ctx, sentQuery, k, false)
	return results, sentQuery, false, nil
}

// Fetch retrieves one ledger-approved page, cache-first.
func (c *Client) Fetch(ctx context.Context, rawURL string, ledger *URLLedger) (*FetchResult, bool, error) {
	canonical, _, err := canonicalFetchURL(rawURL)
	if err != nil {
		return nil, false, err
	}
	// Ledger and allow-list gate CACHE HITS too: a cached page the model may not name is still a
	// page the model may not read this call.
	if ledger != nil && !ledger.Contains(canonical) {
		return nil, false, fmt.Errorf("websearch: %s was not returned by a web_search in this call; fetch only search results", canonical)
	}
	key := fetchCacheKey(canonical)
	var fromCache FetchResult
	if c.cache.get(key, c.offline, &fromCache) {
		if !hostAllowed(strings.ToLower(strings.TrimSpace(hostOf(canonical))), c.fetcher.allowedHosts) {
			return nil, false, fmt.Errorf("websearch: host of %s is not on allowed_hosts", canonical)
		}
		c.auditFetch(ctx, canonical, len(fromCache.Content), fromCache.Truncated, true)
		return &fromCache, true, nil
	}
	if c.offline {
		return nil, false, fmt.Errorf("websearch: offline mode and %s is not cached", canonical)
	}
	if c.cannedFetchBody != "" {
		if !hostAllowed(hostOf(canonical), c.fetcher.allowedHosts) {
			return nil, false, fmt.Errorf("websearch: host of %s is not on allowed_hosts", canonical)
		}
		return &FetchResult{URL: canonical, Content: c.cannedFetchBody}, false, nil
	}
	res, err := c.fetcher.Fetch(ctx, canonical, func(u string) bool { return ledger == nil || ledger.Contains(u) })
	if err != nil {
		return nil, false, err
	}
	c.cache.put(key, res)
	c.auditFetch(ctx, res.URL, len(res.Content), res.Truncated, false)
	return res, false, nil
}

func hostOf(canonical string) string {
	_, u, err := canonicalFetchURL(canonical)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (c *Client) auditQuery(ctx context.Context, query string, k int, cached bool) {
	if c.Audit == nil {
		return
	}
	c.Audit(ctx, "websearch.query", map[string]any{
		"message":   fmt.Sprintf("Web search (%s): %q (k=%d, cached=%v).", c.provider.Name(), query, k, cached),
		"provider":  c.provider.Name(),
		"query":     query,
		"k":         k,
		"cache_hit": cached,
	})
}

func (c *Client) auditFetch(ctx context.Context, url string, chars int, truncated, cached bool) {
	if c.Audit == nil {
		return
	}
	// URL and sizes only — never the body. B13 keeps content out of the audit log, and a fetched
	// page is content.
	c.Audit(ctx, "websearch.fetch", map[string]any{
		"message":      fmt.Sprintf("Web fetch: %s (%d chars, truncated=%v, cached=%v).", url, chars, truncated, cached),
		"url":          url,
		"result_chars": chars,
		"truncated":    truncated,
		"cache_hit":    cached,
	})
}

// URLLedger records which canonical URLs searches returned during ONE tool-loop invocation.
// web_fetch may only retrieve members. The ledger is the difference between "the model can read
// documentation a search surfaced" and "the model can point this process's network access at
// anything a fetched page suggests".
type URLLedger struct {
	mu   sync.Mutex
	seen map[string]bool
}

func NewURLLedger() *URLLedger { return &URLLedger{seen: map[string]bool{}} }

// AddResults canonicalizes and records every fetchable URL in results, quietly skipping ones that
// could never be fetched (non-https, userinfo, odd ports) — recording those would only make the
// refusal message later instead of now.
func (l *URLLedger) AddResults(results []Result) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range results {
		if canonical, _, err := canonicalFetchURL(r.URL); err == nil {
			l.seen[canonical] = true
		}
	}
}

func (l *URLLedger) Contains(canonical string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[canonical]
}

// replayProvider stands in when offline mode runs without a reachable backend. Search must never
// be called on it — the offline branch answers from cache or errors first — so calling it is a
// bug, and it says so instead of pretending.
type replayProvider struct{ name string }

func (r replayProvider) Name() string { return r.name }

func (r replayProvider) Search(context.Context, string, int) ([]Result, error) {
	return nil, fmt.Errorf("websearch: internal error: provider called in offline mode")
}
