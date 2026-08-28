package websearch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// Fetcher retrieves ONE model-chosen page under the full guard stack. Every rule here exists
// because web_fetch is an SSRF surface pointed at the customer's network: the model supplies the
// URL, and "the model" includes whatever a previously fetched page talked it into.
type Fetcher struct {
	allowedHosts []string
	maxBytes     int64
	// resolve is the DNS seam. Tests inject resolvers that return private addresses for public
	// names — the rebinding case — which cannot be exercised against real DNS.
	resolve func(ctx context.Context, host string) ([]net.IP, error)
	// proxyFunc reports the proxy for a URL, honouring HTTP(S)_PROXY. When a proxy is in play the
	// proxy performs the dial and IP vetting cannot see the true target; the hostname allow-list
	// is then the operative gate. That trade is documented rather than hidden: in proxied
	// deployments the operator's allow-list IS the SSRF boundary.
	proxyFunc func(*url.URL) (*url.URL, error)
	timeout   time.Duration
}

// NewFetcher builds a fetcher over the allow-list. maxBytes 0 = DefaultMaxFetchBytes.
func NewFetcher(allowedHosts []string, maxBytes int64) *Fetcher {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxFetchBytes
	}
	return &Fetcher{
		allowedHosts: append([]string(nil), allowedHosts...),
		maxBytes:     maxBytes,
		resolve: func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, nil
		},
		proxyFunc: httpproxy.FromEnvironment().ProxyFunc(),
		timeout:   60 * time.Second,
	}
}

// ipDisallowed reports why an IP must not be dialed, or "" when it may.
//
// The list is OWASP's SSRF target inventory: loopback, RFC1918, link-local (which includes every
// cloud metadata endpoint at 169.254.169.254), CGNAT, unique-local and multicast. Unspecified
// (0.0.0.0 / ::) is refused too — on several platforms connecting to it reaches localhost.
func ipDisallowed(ip net.IP) string {
	switch {
	case ip == nil:
		return "unresolvable"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private range"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return "link-local (cloud metadata lives here)"
	case ip.IsMulticast():
		return "multicast"
	case ip.IsUnspecified():
		return "unspecified"
	}
	// 100.64.0.0/10 (CGNAT) is not covered by IsPrivate and is routinely internal.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return "carrier-grade NAT range"
	}
	return ""
}

// FetchResult is one retrieved, extracted page.
type FetchResult struct {
	URL       string // canonical form actually fetched
	Content   string // extracted text, code blocks preserved
	Truncated bool   // body exceeded the byte cap
}

// Fetch retrieves url under the guard stack. ledgerCheck answers whether the canonical URL was
// returned by a search THIS call — the caller owns that ledger; nil means no ledger gate (used
// only by tests).
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, ledgerCheck func(canonical string) bool) (*FetchResult, error) {
	if len(f.allowedHosts) == 0 {
		// Fail closed: no allow-list, no fetch. An empty list must never mean "everything".
		return nil, fmt.Errorf("websearch: web_fetch is disabled: allowed_hosts is empty")
	}
	canonical, u, err := canonicalFetchURL(rawURL)
	if err != nil {
		return nil, err
	}
	if !hostAllowed(u.Hostname(), f.allowedHosts) {
		return nil, fmt.Errorf("websearch: host %q is not on allowed_hosts", u.Hostname())
	}
	if ledgerCheck != nil && !ledgerCheck(canonical) {
		// The model may only fetch what a search returned THIS call. An arbitrary model-supplied
		// URL — including one suggested by a previously fetched page — is exactly the SSRF and
		// exfiltration shape this tool must not have.
		return nil, fmt.Errorf("websearch: %s was not returned by a web_search in this call; fetch only search results", canonical)
	}

	client := f.clientFor(ctx, u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "asqs-websearch/1.0 (+test-generation; reads documentation)")
	req.Header.Set("Accept", "text/html, text/plain, text/markdown")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websearch: fetch %s: %w", canonical, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("websearch: fetch %s: HTTP %d", canonical, resp.StatusCode)
	}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	switch ct {
	case "text/html", "application/xhtml+xml", "text/plain", "text/markdown", "":
	default:
		// Binary and script content types have no documentation value and plenty of risk.
		return nil, fmt.Errorf("websearch: fetch %s: content-type %q is not fetchable", canonical, ct)
	}

	limited := io.LimitReader(resp.Body, f.maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("websearch: fetch %s: read: %w", canonical, err)
	}
	truncated := int64(len(body)) > f.maxBytes
	if truncated {
		body = body[:f.maxBytes]
	}

	content := string(body)
	if ct == "text/html" || ct == "application/xhtml+xml" || ct == "" {
		content = ExtractReadableText(content)
	}
	return &FetchResult{URL: canonical, Content: content, Truncated: truncated}, nil
}

// clientFor builds the HTTP client for one fetch: rebinding-safe dialing when this process dials,
// plain proxied transport when a proxy will dial instead (the allow-list already gated the host).
func (f *Fetcher) clientFor(ctx context.Context, target *url.URL) *http.Client {
	viaProxy := false
	if f.proxyFunc != nil {
		if p, err := f.proxyFunc(target); err == nil && p != nil {
			viaProxy = true
		}
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
		// No connection reuse across fetches: a fetcher is built per client and fetches are rare;
		// a pooled connection pinned to a vetted IP would outlive the vetting.
		DisableKeepAlives: true,
	}
	if !viaProxy {
		transport.Proxy = nil
		transport.DialContext = f.vettedDial
	}
	return &http.Client{
		Timeout:   f.timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Every hop is re-validated: a redirect is a server-chosen URL, which is exactly as
			// untrusted as a model-chosen one. Bounded depth, https-only, allow-listed host —
			// and the vetted dialer applies to the new host automatically on the next connect.
			if len(via) >= 3 {
				return fmt.Errorf("websearch: too many redirects")
			}
			canonical, u, err := canonicalFetchURL(req.URL.String())
			if err != nil {
				return err
			}
			if !hostAllowed(u.Hostname(), f.allowedHosts) {
				return fmt.Errorf("websearch: redirect to %q refused: host not on allowed_hosts", canonical)
			}
			return nil
		},
	}
}

// vettedDial resolves the host itself, refuses any disallowed address, and connects to a VETTED
// IP rather than letting the runtime re-resolve. Resolving twice — once to check, once to dial —
// is the classic DNS-rebinding hole: the second answer need not match the first.
func (f *Fetcher) vettedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if why := ipDisallowed(ip); why != "" {
			return nil, fmt.Errorf("websearch: refusing to dial %s: %s", host, why)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	ips, err := f.resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("websearch: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("websearch: resolve %s: no addresses", host)
	}
	// ALL addresses must be clean, not just the one dialed: a name that resolves to one public
	// and one private address is a rebinding setup, and refusing it outright is cheaper than
	// reasoning about which answer the attacker controls.
	for _, ip := range ips {
		if why := ipDisallowed(ip); why != "" {
			return nil, fmt.Errorf("websearch: refusing %s: resolves to %s (%s)", host, ip, why)
		}
	}
	var d net.Dialer
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}
