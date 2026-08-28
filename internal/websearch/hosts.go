package websearch

import (
	"net/url"
	"strings"
)

// DefaultAllowedHosts is the shipped allow-list: official documentation hosts for every technology
// this system generates tests for — Java/Spring, C#/.NET, JS/TS/Node, React/Angular/Nest, and the
// test frameworks themselves. It is deliberately OFFICIAL-DOCS-ONLY:
//
//   - Q&A platforms (stackoverflow.com) are excluded by default. They are the highest-value source
//     for error messages and the highest-risk one — arbitrary user-authored content is exactly the
//     prompt-injection surface the review focus describes. Operators who accept that trade add the
//     host themselves.
//   - Wildcard entries are avoided where exact hosts suffice; "*.github.io" in particular would
//     allow-list every GitHub user on earth. assertj.github.io is listed EXACTLY for that reason.
//   - Package registries (npmjs.com, nuget.org) ARE listed: package READMEs are author-written,
//     but they are where a dependency's usage documentation actually lives, and every fetched byte
//     passes the same untrusted-content framing regardless of host.
var DefaultAllowedHosts = []string{
	// Java
	"docs.oracle.com",
	"docs.spring.io",
	"spring.io",
	"javadoc.io",
	"junit.org",
	"site.mockito.org",
	"assertj.github.io",
	"maven.apache.org",
	"docs.gradle.org",
	"testcontainers.com",
	"kotlinlang.org",
	// .NET
	"learn.microsoft.com",
	"xunit.net",
	"nunit.org",
	"docs.nunit.org",
	"fluentassertions.com",
	"www.nuget.org",
	// JS / TS / Node
	"developer.mozilla.org",
	"nodejs.org",
	"www.typescriptlang.org",
	"www.npmjs.com",
	"expressjs.com",
	// Frontend frameworks
	"react.dev",
	"angular.dev",
	"angular.io",
	"rxjs.dev",
	"redux.js.org",
	"docs.nestjs.com",
	"nestjs.com",
	// JS test frameworks
	"jestjs.io",
	"vitest.dev",
	"mochajs.org",
	"playwright.dev",
	"docs.cypress.io",
	"testing-library.com",
}

// hostAllowed reports whether host matches the allow-list: exact match, or a "*.suffix" entry
// matching any SUBdomain of suffix (the bare suffix itself must be listed separately — a wildcard
// that also matched its apex would be two rules pretending to be one).
func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(host, "."+rest) {
				return true
			}
			continue
		}
		if host == a {
			return true
		}
	}
	return false
}

// canonicalFetchURL normalizes a model-supplied URL for ledger comparison and enforcement, and
// rejects everything web_fetch will never retrieve. The rules are checked here, ONCE, so the
// ledger, the allow-list and the dialer all see the same string.
func canonicalFetchURL(raw string) (string, *url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", nil, errFetchURL("unparseable", raw)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", nil, errFetchURL("scheme must be https", raw)
	}
	if u.User != nil {
		// Credentials in a URL are either an exfiltration attempt or a mistake; both are refused.
		return "", nil, errFetchURL("userinfo is not allowed", raw)
	}
	if p := u.Port(); p != "" && p != "443" {
		return "", nil, errFetchURL("port must be 443", raw)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", nil, errFetchURL("empty host", raw)
	}
	u.Scheme = "https"
	u.Host = host // drops an explicit :443, lowercases
	u.Fragment = ""
	return u.String(), u, nil
}

type fetchURLError struct{ reason, raw string }

func (e fetchURLError) Error() string { return "websearch: refusing URL (" + e.reason + "): " + e.raw }

func errFetchURL(reason, raw string) error { return fetchURLError{reason: reason, raw: raw} }
