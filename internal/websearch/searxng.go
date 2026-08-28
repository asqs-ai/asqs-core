package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// searxngProvider queries a self-hosted SearXNG instance (JSON API). It keeps search queries
// inside the customer boundary — the strongest egress posture — but it is something to OPERATE,
// and the target deployments mostly cannot run an additional service, which is why brave is the
// shipped default and this stays an option for those who can.
//
// SearXNG note for operators: the JSON format must be enabled in settings.yml
// (`search.formats: [html, json]`); the container image is `searxng/searxng`.
type searxngProvider struct {
	endpoint string
	client   *http.Client
}

func newSearxng(endpoint string) (*searxngProvider, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("websearch: searxng requires websearch.endpoint (e.g. http://searxng:8080)")
	}
	if _, err := url.Parse(endpoint); err != nil {
		return nil, fmt.Errorf("websearch: searxng endpoint: %w", err)
	}
	return &searxngProvider{
		endpoint: endpoint,
		// The endpoint is operator infrastructure, not a model-chosen URL: the plain client with
		// environment proxy support is correct here, and cluster-internal http is legitimate.
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (p *searxngProvider) Name() string { return "searxng" }

func (p *searxngProvider) Search(ctx context.Context, query string, k int) ([]Result, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "asqs-websearch/1.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websearch: searxng: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("websearch: searxng: HTTP %d (is search.formats: [html, json] enabled in settings.yml?)", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("websearch: searxng: parse: %w", err)
	}
	out := make([]Result, 0, k)
	for _, r := range parsed.Results {
		if len(out) >= k {
			break
		}
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content, PublishedAt: r.PublishedDate})
	}
	return out, nil
}
