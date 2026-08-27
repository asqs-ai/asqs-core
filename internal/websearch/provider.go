// Package websearch gives the tool loop bounded, auditable access to external documentation
// (Spec B54). It is the FIRST component in this system that sends data out, and every design
// decision below follows from that: the search endpoint is operator-configured infrastructure,
// while every URL a MODEL chooses passes scheme pinning, a host allow-list, a per-call ledger and
// rebinding-safe dialing before one byte is fetched.
package websearch

import (
	"context"
	"time"
)

// Result is one search hit. Snippets only — page bodies are web_fetch's job, one page at a time,
// so a single search cannot flood the context window with attacker-influenceable text.
type Result struct {
	Title       string
	URL         string
	Snippet     string
	PublishedAt string // provider-reported, best-effort; empty when unknown
}

// Provider answers a query with ranked results.
//
// An interface rather than one hardcoded backend: search backends differ in endpoint shape,
// auth, and result schema at least as much as LLM providers do, and hardcoding one would repeat
// the mistake B08 fixed for completions. Two implementations ship — SearXNG (self-hosted, no
// key, queries stay inside the customer boundary) and Brave (hosted, key) — chosen because the
// obvious third option no longer exists: Microsoft retired the Bing Search APIs in August 2025.
type Provider interface {
	Search(ctx context.Context, query string, k int) ([]Result, error)
	Name() string
}

// Config assembles a Client from operator configuration. Zero value = disabled.
type Config struct {
	Enabled bool
	// ProviderName selects the backend: "searxng" or "brave".
	ProviderName string
	// Endpoint is the SearXNG base URL. Operator-configured infrastructure, so cluster-internal
	// http (e.g. http://searxng:8080 inside OpenShift) is legitimate here — the https-only rule
	// guards MODEL-chosen URLs, and this is not one.
	Endpoint string
	// APIKey authenticates hosted providers (Brave).
	APIKey string
	// AllowedHosts gates web_fetch. Exact hosts or "*.example.org" wildcards. EMPTY DISABLES
	// FETCH — an empty allow-list must fail closed, never open.
	AllowedHosts []string
	// CachePath is the single JSON file holding the content-addressed replay cache (same shape and
	// atomic-replace discipline as the project-intel cache). Empty disables caching, and with it
	// offline mode.
	CachePath string
	// CacheTTL bounds cache entry age. 0 = DefaultCacheTTL.
	CacheTTL time.Duration
	// MaxFetchBytes caps one fetched page. 0 = DefaultMaxFetchBytes.
	MaxFetchBytes int64
	// Offline serves ONLY from cache and never egresses (ASQS_WEBSEARCH_OFFLINE=1). A cache miss
	// is an answer ("offline, not cached"), not a network call.
	Offline bool
	// QueryDenyTokens are lowercase substrings that must never leave the process in a query —
	// derived at wiring time from the repository's identity (repo_id segments, its own root
	// packages), not hand-maintained.
	QueryDenyTokens []string
}

const (
	// DefaultCacheTTL keeps replays stable for a week — long enough to pin an A/B pair, short
	// enough that documentation drift eventually surfaces.
	DefaultCacheTTL = 7 * 24 * time.Hour
	// DefaultMaxFetchBytes bounds one page read. Framework doc pages run 50-500 KB; 2 MB is
	// headroom, not an invitation.
	DefaultMaxFetchBytes = 2 << 20
	// DefaultSearchK is how many results a search returns when the model does not say.
	DefaultSearchK = 5
	// MaxSearchK bounds what the model may ask for.
	MaxSearchK = 8
)
