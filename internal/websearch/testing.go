package websearch

import (
	"context"
)

// TestConfig assembles a Client over canned data for OTHER packages' tests. It lives in the
// package proper rather than an _test file because the tools package needs it, and an exported
// seam beats exporting the Client's internals.
type TestConfig struct {
	// Results is what every Search returns (capped at k).
	Results []Result
	// AllowedHosts gates fetch exactly as in production.
	AllowedHosts []string
	// FetchBody is returned for any ledger-approved, allow-listed fetch. No network is involved.
	FetchBody string
}

// NewForTest builds a network-free Client: canned search results, canned fetch bodies, all
// production gating (sanitization, ledger, allow-list) still in force — which is the point, since
// the gates are what the callers' tests exercise.
func NewForTest(cfg TestConfig) (*Client, string) {
	return &Client{
		provider: cannedProvider{results: cfg.Results},
		fetcher: &Fetcher{
			allowedHosts: append([]string(nil), cfg.AllowedHosts...),
			maxBytes:     DefaultMaxFetchBytes,
		},
		cannedFetchBody: cfg.FetchBody,
	}, ""
}

type cannedProvider struct{ results []Result }

func (p cannedProvider) Name() string { return "canned" }

func (p cannedProvider) Search(_ context.Context, _ string, k int) ([]Result, error) {
	if k > len(p.results) {
		k = len(p.results)
	}
	return append([]Result(nil), p.results[:k]...), nil
}
