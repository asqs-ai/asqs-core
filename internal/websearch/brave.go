package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// braveProvider queries the Brave Search API — the hosted option for deployments that accept
// search egress and hold an API key. Chosen as the second backend because it has a plain
// documented JSON API with header auth; the historical default for this niche, the Bing Search
// API, was retired by Microsoft in August 2025 and is not coming back.
type braveProvider struct {
	apiKey string
	client *http.Client
}

const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

func newBrave(apiKey string) (*braveProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("websearch: brave requires websearch.api_key or api_key_from_env")
	}
	return &braveProvider{apiKey: apiKey, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (p *braveProvider) Name() string { return "brave" }

func (p *braveProvider) Search(ctx context.Context, query string, k int) ([]Result, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("count", strconv.Itoa(k))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", p.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("websearch: brave: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("websearch: brave: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("websearch: brave: parse: %w", err)
	}
	out := make([]Result, 0, k)
	for _, r := range parsed.Web.Results {
		if len(out) >= k {
			break
		}
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Description, PublishedAt: r.PageAge})
	}
	return out, nil
}
