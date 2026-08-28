package websearch

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostAllowed(t *testing.T) {
	allowed := []string{"docs.spring.io", "*.oracle.com", "react.dev"}
	cases := map[string]bool{
		"docs.spring.io":      true,
		"DOCS.SPRING.IO":      true,
		"docs.spring.io.":     true, // trailing-dot FQDN form
		"evil-docs.spring.io": false,
		"spring.io":           false, // apex not listed
		"docs.oracle.com":     true,  // wildcard subdomain
		"a.b.oracle.com":      true,
		"oracle.com":          false, // wildcard does not match its own apex
		"notoracle.com":       false,
		"oracle.com.evil.com": false,
		"react.dev":           true,
		"react.dev.attack.io": false,
	}
	for host, want := range cases {
		if got := hostAllowed(host, allowed); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
	if hostAllowed("docs.spring.io", nil) {
		t.Error("an empty allow-list must allow nothing")
	}
}

func TestCanonicalFetchURL(t *testing.T) {
	if _, _, err := canonicalFetchURL("http://docs.spring.io/x"); err == nil {
		t.Error("http must be refused")
	}
	if _, _, err := canonicalFetchURL("https://user:pass@docs.spring.io/x"); err == nil {
		t.Error("userinfo must be refused")
	}
	if _, _, err := canonicalFetchURL("https://docs.spring.io:8443/x"); err == nil {
		t.Error("non-443 port must be refused")
	}
	got, _, err := canonicalFetchURL("HTTPS://Docs.Spring.IO:443/a/b?x=1#frag")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://docs.spring.io/a/b?x=1" {
		t.Errorf("canonical = %q", got)
	}
}

func TestIPDisallowed(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1", "fe80::1", "fc00::1", "0.0.0.0", "224.0.0.1"}
	for _, s := range blocked {
		if why := ipDisallowed(net.ParseIP(s)); why == "" {
			t.Errorf("%s must be refused", s)
		}
	}
	for _, s := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "100.128.0.1"} {
		if why := ipDisallowed(net.ParseIP(s)); why != "" {
			t.Errorf("%s wrongly refused: %s", s, why)
		}
	}
}

// The rebinding case: a public NAME resolving to a private ADDRESS. Only an injected resolver can
// exercise it, which is why the resolver is a seam.
func TestFetcher_refusesDNSRebindToPrivate(t *testing.T) {
	f := NewFetcher([]string{"docs.spring.io"}, 0)
	f.proxyFunc = nil
	f.resolve = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}, nil
	}
	_, err := f.Fetch(context.Background(), "https://docs.spring.io/x", nil)
	if err == nil || !strings.Contains(err.Error(), "10.0.0.5") {
		t.Fatalf("a name resolving to a private address must be refused naming the address; got %v", err)
	}
}

func TestFetcher_refusesLedgerMissAndAllowlistMiss(t *testing.T) {
	f := NewFetcher([]string{"docs.spring.io"}, 0)
	if _, err := f.Fetch(context.Background(), "https://evil.example.com/x", nil); err == nil {
		t.Error("host off the allow-list must be refused")
	}
	ledger := NewURLLedger()
	if _, err := f.Fetch(context.Background(), "https://docs.spring.io/x",
		func(u string) bool { return ledger.Contains(u) }); err == nil || !strings.Contains(err.Error(), "web_search") {
		t.Errorf("a URL no search returned must be refused with the reason; got %v", err)
	}
}

func TestFetcher_emptyAllowlistFailsClosed(t *testing.T) {
	f := NewFetcher(nil, 0)
	if _, err := f.Fetch(context.Background(), "https://docs.spring.io/x", nil); err == nil {
		t.Fatal("an empty allow-list must disable fetch entirely")
	}
}

// panickingTransport proves no egress: any dial panics the test.
type panickingTransport struct{ t *testing.T }

func (p panickingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.t.Fatal("an HTTP request left the process while websearch was disabled or offline")
	return nil, nil
}

func TestClient_offlineNeverEgresses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "websearch-cache.json")
	// Seed the cache through a real (cached) search against a local stub.
	stub := &stubProvider{results: []Result{{Title: "Spring Docs", URL: "https://docs.spring.io/a", Snippet: "s"}}}
	c := &Client{provider: stub, fetcher: NewFetcher([]string{"docs.spring.io"}, 0), cache: newDiskCache(dir, time.Hour)}
	if _, _, _, err := c.Search(context.Background(), "spring boot test", 3); err != nil {
		t.Fatal(err)
	}
	// Now go offline. The provider NAME must match the one the cache was written under — keys
	// include it — while the backend itself must never be reached; explodingProvider fails the
	// test on any call.
	off := &Client{provider: explodingProvider{t}, fetcher: NewFetcher([]string{"docs.spring.io"}, 0), cache: newDiskCache(dir, time.Hour), offline: true}
	res, _, cached, err := off.Search(context.Background(), "spring boot test", 3)
	if err != nil || !cached || len(res) != 1 {
		t.Fatalf("offline cached search: res=%v cached=%v err=%v", res, cached, err)
	}
	if _, _, _, err := off.Search(context.Background(), "never cached query", 3); err == nil {
		t.Fatal("an offline cache miss must be an error, not a network call")
	}
}

func TestClient_cacheReplayIsDeterministic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "websearch-cache.json")
	stub := &stubProvider{results: []Result{{Title: "A", URL: "https://docs.spring.io/a", Snippet: "one"}}}
	c := &Client{provider: stub, fetcher: NewFetcher([]string{"docs.spring.io"}, 0), cache: newDiskCache(dir, time.Hour)}
	first, _, _, err := c.Search(context.Background(), "query", 3)
	if err != nil {
		t.Fatal(err)
	}
	// The provider now returns something DIFFERENT; the cache must win.
	stub.results = []Result{{Title: "B", URL: "https://docs.spring.io/b", Snippet: "two"}}
	second, _, cached, err := c.Search(context.Background(), "query", 3)
	if err != nil || !cached {
		t.Fatalf("second search: cached=%v err=%v", cached, err)
	}
	if len(second) != 1 || second[0].Title != first[0].Title {
		t.Fatalf("replay differs: first=%v second=%v", first, second)
	}
}

func TestSanitizeQuery(t *testing.T) {
	deny := []string{"acmecorp", "com.acmecorp"}
	got := SanitizeQuery("spring boot @SpringBootTest com.acmecorp.orders src/main/java/App.java pageable", deny)
	for _, banned := range []string{"acmecorp", "src/main"} {
		if strings.Contains(strings.ToLower(got), banned) {
			t.Errorf("sanitized query leaked %q: %q", banned, got)
		}
	}
	for _, want := range []string{"spring", "boot", "@SpringBootTest", "pageable"} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitized query lost the public term %q: %q", want, got)
		}
	}
}

func TestExtractReadableText(t *testing.T) {
	page := `<html><head><script>evil()</script><style>x{}</style></head><body>
	<nav>Skip</nav><h2>@SpringBootTest</h2><p>Boots the  full   context.</p>
	<pre><code>@SpringBootTest(webEnvironment = RANDOM_PORT)</code></pre>
	<span>zero&#8203;width</span></body></html>`
	got := ExtractReadableText(page)
	if strings.Contains(got, "evil()") || strings.Contains(got, "Skip") {
		t.Errorf("dropped subtrees leaked: %q", got)
	}
	if !strings.Contains(got, "## @SpringBootTest") {
		t.Errorf("heading lost: %q", got)
	}
	if !strings.Contains(got, "@SpringBootTest(webEnvironment = RANDOM_PORT)") {
		t.Errorf("code block lost: %q", got)
	}
	if strings.Contains(got, "​") {
		t.Error("zero-width rune survived extraction")
	}
	if !strings.Contains(got, "zerowidth") {
		t.Errorf("text around the invisible rune lost: %q", got)
	}
}

// The redirect hop is server-chosen and exactly as untrusted as a model-chosen URL. The policy
// lives in CheckRedirect, which is tested directly: every hop must be https on an allow-listed
// host, and depth is bounded.
func TestFetcher_redirectPolicy(t *testing.T) {
	f := NewFetcher([]string{"docs.spring.io", "learn.microsoft.com"}, 0)
	target, _, err := canonicalFetchURL("https://docs.spring.io/x")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	client := f.clientFor(context.Background(), parsed)
	prior := func(n int) []*http.Request {
		out := make([]*http.Request, n)
		for i := range out {
			out[i], _ = http.NewRequest(http.MethodGet, "https://docs.spring.io/hop", nil)
		}
		return out
	}
	hop := func(u string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, u, nil)
		return r
	}

	if err := client.CheckRedirect(hop("https://evil.example.com/next"), prior(1)); err == nil || !strings.Contains(err.Error(), "allowed_hosts") {
		t.Errorf("redirect to a host off the allow-list must be refused: %v", err)
	}
	if err := client.CheckRedirect(hop("http://docs.spring.io/next"), prior(1)); err == nil {
		t.Error("redirect downgrading to http must be refused")
	}
	if err := client.CheckRedirect(hop("https://learn.microsoft.com/next"), prior(1)); err != nil {
		t.Errorf("redirect to another allow-listed https host must pass: %v", err)
	}
	if err := client.CheckRedirect(hop("https://docs.spring.io/next"), prior(3)); err == nil {
		t.Error("redirect depth must be bounded")
	}
}

type stubProvider struct{ results []Result }

func (s *stubProvider) Search(ctx context.Context, q string, k int) ([]Result, error) {
	return append([]Result(nil), s.results...), nil
}
func (s *stubProvider) Name() string { return "stub" }

type explodingProvider struct{ t *testing.T }

func (e explodingProvider) Search(ctx context.Context, q string, k int) ([]Result, error) {
	e.t.Fatal("provider was called in offline mode")
	return nil, nil
}
func (e explodingProvider) Name() string { return "stub" } // must match the caching run's provider name

// Offline mode must construct WITHOUT an endpoint or key: an air-gapped host cannot supply them.
func TestNew_offlineNeedsNoEndpointOrKey(t *testing.T) {
	c, reason := New(Config{Enabled: true, Offline: true, ProviderName: "searxng", CachePath: filepath.Join(t.TempDir(), "websearch-cache.json")})
	if c == nil {
		t.Fatalf("offline client not constructed: %s", reason)
	}
	if c.ProviderName() != "searxng" {
		t.Errorf("offline provider name = %q; cache keys depend on it", c.ProviderName())
	}
	if c2, reason := New(Config{Enabled: true, Offline: true, ProviderName: "searxng"}); c2 != nil {
		t.Error("offline without a cache_dir must resolve to off")
	} else if !strings.Contains(reason, "cache") {
		t.Errorf("reason should name the cache: %s", reason)
	}
}
