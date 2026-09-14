// Package httpcfg builds HTTP clients for LLM providers using llm timeout settings.
package httpcfg

import (
	"net/http"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
)

const (
	DefaultHTTPClientTimeout         = 5 * time.Minute
	DefaultHTTPResponseHeaderTimeout = 2 * time.Minute
)

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (b *bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

// responseHeaderTimeout returns Transport.ResponseHeaderTimeout for this LLM client.
// Ollama POST /api/chat (non-streaming) often does not send HTTP headers until generation has progressed;
// the default 2m header deadline then aborts long TTFT runs and shows up as proxy "context canceled".
// When forOllama is true and HTTPResponseHeaderTimeout is unset, returns 0 (disabled); overall timeout
// still applies via http.Client.Timeout.
func responseHeaderTimeout(llm *config.LLMConfig, forOllama bool) time.Duration {
	if llm != nil {
		if s := strings.TrimSpace(llm.HTTPResponseHeaderTimeout); s != "" {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
		}
	}
	if forOllama {
		return 0
	}
	return DefaultHTTPResponseHeaderTimeout
}

// ClientTimeout is the configured general.llm.http.timeout, or the default when unset.
//
// Exported because a STREAMING client cannot use it as http.Client.Timeout: that deadline bounds the
// whole exchange including the body, so it would kill a long but perfectly healthy generation. The
// streaming caller takes this value and applies it as a stall budget instead — the time allowed to
// pass with no byte arriving. See ollama.Client.Complete.
func ClientTimeout(llm *config.LLMConfig) time.Duration {
	timeout := DefaultHTTPClientTimeout
	if llm != nil {
		if s := strings.TrimSpace(llm.HTTPTimeout); s != "" {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				timeout = d
			}
		}
	}
	return timeout
}

func newHTTPClient(llm *config.LLMConfig, bearerToken string, forOllama bool) *http.Client {
	timeout := ClientTimeout(llm)
	headerTO := responseHeaderTimeout(llm, forOllama)

	tr := http.DefaultTransport
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		ct := t.Clone()
		ct.MaxIdleConnsPerHost = 32
		ct.IdleConnTimeout = 90 * time.Second
		ct.TLSHandshakeTimeout = 30 * time.Second
		ct.ResponseHeaderTimeout = headerTO
		if llm != nil && llm.HTTPDisableKeepAlives {
			ct.DisableKeepAlives = true
			ct.MaxIdleConns = 0
			ct.MaxIdleConnsPerHost = 0
		}
		tr = ct
	}
	c := &http.Client{
		Timeout:   timeout,
		Transport: tr,
	}
	if strings.TrimSpace(bearerToken) == "" {
		return c
	}
	rt := c.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	c.Transport = &bearerRoundTripper{base: rt, token: strings.TrimSpace(bearerToken)}
	return c
}

// HTTPClient returns an http.Client configured from cfg.LLM (timeouts, keep-alives).
func HTTPClient(llm *config.LLMConfig) *http.Client {
	return newHTTPClient(llm, "", false)
}

// HTTPClientWithBearer is like HTTPClient but adds Authorization: Bearer when token is non-empty.
func HTTPClientWithBearer(llm *config.LLMConfig, bearerToken string) *http.Client {
	return newHTTPClient(llm, bearerToken, false)
}

// HTTPClientWithBearerForOllama is like HTTPClientWithBearer but disables the default response-header
// deadline when llm.HTTPResponseHeaderTimeout is unset (see responseHeaderTimeout).
func HTTPClientWithBearerForOllama(llm *config.LLMConfig, bearerToken string) *http.Client {
	return newHTTPClient(llm, bearerToken, true)
}

// StreamingHTTPClientForOllama is HTTPClientWithBearerForOllama with no overall deadline.
//
// http.Client.Timeout bounds the entire exchange, body included, so on a streaming response it caps
// TOTAL generation time — the opposite of what a streaming client wants. The caller enforces
// ClientTimeout as a stall budget instead: a stream that is still delivering tokens is healthy
// however long it runs, and one that has gone quiet is not, however recently it started.
func StreamingHTTPClientForOllama(llm *config.LLMConfig, bearerToken string) *http.Client {
	c := newHTTPClient(llm, bearerToken, true)
	c.Timeout = 0
	return c
}
