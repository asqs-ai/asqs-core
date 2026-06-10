package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client targets Bitbucket Cloud REST API 2.0 by default.
type Client struct {
	baseURL    string // https://api.bitbucket.org/2.0
	token      string
	httpClient *http.Client
}

// NewClient returns a Bitbucket client. baseURL empty uses Cloud API.
func NewClient(token, baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.bitbucket.org/2.0"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL: baseURL,
		token:   strings.TrimSpace(token),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bitbucket API %s %s: %s: %s", method, path, resp.Status, truncate(string(b), 400))
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("bitbucket decode: %w", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type bbPR struct {
	ID    int `json:"id"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

type bbPage struct {
	Values []bbPR `json:"values"`
}

// ListOpenPullRequestsBySourceBranch returns open PRs whose source branch matches.
func (c *Client) ListOpenPullRequestsBySourceBranch(ctx context.Context, workspace, repo, branch string) ([]bbPR, error) {
	q := fmt.Sprintf(`state="OPEN" AND source.branch.name="%s"`, branch)
	path := fmt.Sprintf("/repositories/%s/%s/pullrequests?%s", url.PathEscape(workspace), url.PathEscape(repo), url.Values{"q": {q}}.Encode())
	var page bbPage
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	return page.Values, nil
}

// CreatePullRequest opens a PR from sourceBranch into targetBranch.
func (c *Client) CreatePullRequest(ctx context.Context, workspace, repo, title, description, sourceBranch, targetBranch string) (*bbPR, error) {
	payload := map[string]interface{}{
		"title": title,
		"source": map[string]interface{}{
			"branch": map[string]string{"name": sourceBranch},
		},
		"destination": map[string]interface{}{
			"branch": map[string]string{"name": targetBranch},
		},
	}
	if description != "" {
		payload["description"] = description
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var pr bbPR
	path := fmt.Sprintf("/repositories/%s/%s/pullrequests", url.PathEscape(workspace), url.PathEscape(repo))
	if err := c.do(ctx, http.MethodPost, path, bytes.NewReader(b), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

type diffStat struct {
	New struct {
		Path string `json:"path"`
	} `json:"new"`
	Old struct {
		Path string `json:"path"`
	} `json:"old"`
	Status string `json:"status"`
}

type diffStatPage struct {
	Values []diffStat `json:"values"`
}

// ListPRDiffStats lists files changed in a PR.
func (c *Client) ListPRDiffStats(ctx context.Context, workspace, repo string, prID int) ([]diffStat, error) {
	path := fmt.Sprintf("/repositories/%s/%s/pullrequests/%d/diffstat", url.PathEscape(workspace), url.PathEscape(repo), prID)
	var page diffStatPage
	if err := c.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return nil, err
	}
	return page.Values, nil
}

// CreatePRComment adds a comment on the PR.
func (c *Client) CreatePRComment(ctx context.Context, workspace, repo string, prID int, content string) error {
	b, err := json.Marshal(map[string]interface{}{"content": map[string]string{"raw": content}})
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repositories/%s/%s/pullrequests/%d/comments", url.PathEscape(workspace), url.PathEscape(repo), prID)
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(b), nil)
}

// SetBuildStatus sets a commit build status (check-run equivalent).
func (c *Client) SetBuildStatus(ctx context.Context, workspace, repo, commitSHA, key, state, name, description, urlStr string) error {
	payload := map[string]string{
		"state":       state,
		"key":         key,
		"name":        name,
		"description": description,
		"url":         urlStr,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repositories/%s/%s/commit/%s/statuses/build", url.PathEscape(workspace), url.PathEscape(repo), url.PathEscape(commitSHA))
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(b), nil)
}
