package azuredevops

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls Azure DevOps Git REST APIs.
type Client struct {
	baseURL      string // https://dev.azure.com/{org}
	organization string
	project      string
	repository   string
	pat          string
	httpClient   *http.Client
}

// NewClient constructs a client. baseURL empty => https://dev.azure.com/{organization}
func NewClient(pat, baseURL, organization, project, repository string) *Client {
	organization = strings.TrimSpace(organization)
	project = strings.TrimSpace(project)
	repository = strings.TrimSpace(repository)
	if baseURL == "" {
		baseURL = "https://dev.azure.com/" + url.PathEscape(organization)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL:      baseURL,
		organization: organization,
		project:      project,
		repository:   repository,
		pat:          strings.TrimSpace(pat),
		httpClient: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

func (c *Client) authHeader() string {
	// PAT as Basic with empty username
	b := base64.StdEncoding.EncodeToString([]byte(":" + c.pat))
	return "Basic " + b
}

func (c *Client) do(ctx context.Context, method, fullURL string, body io.Reader, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.authHeader())
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
		return fmt.Errorf("azure devops %s: %s: %s", method, resp.Status, truncate(string(b), 400))
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("azure devops decode: %w", err)
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

type adoPR struct {
	PullRequestID int `json:"pullRequestId"`
	URL           string
	Links         interface{}
	// Web URL often in _links
	WebURL string `json:"url"`
}

type adoPRList struct {
	Value []adoPR `json:"value"`
}

// repoPath returns API path segment for repository.
func (c *Client) pullRequestsURL(extraQuery string) string {
	base := fmt.Sprintf("%s/_apis/git/repositories/%s/pullrequests",
		c.baseURL+"/"+url.PathEscape(c.project),
		url.PathEscape(c.repository))
	q := "api-version=7.1"
	if extraQuery != "" {
		q = extraQuery + "&" + q
	}
	return base + "?" + q
}

// ListActivePullRequestsBySourceBranch lists active PRs from refs/heads/branch.
func (c *Client) ListActivePullRequestsBySourceBranch(ctx context.Context, branch string) ([]adoPR, error) {
	ref := url.QueryEscape("refs/heads/" + branch)
	u := c.pullRequestsURL("searchCriteria.status=active&searchCriteria.sourceRefName=" + ref)
	var out adoPRList
	if err := c.do(ctx, http.MethodGet, u, nil, &out); err != nil {
		return nil, err
	}
	return out.Value, nil
}

// CreatePullRequest opens a PR from sourceBranch to targetBranch (short branch names, refs added internally).
func (c *Client) CreatePullRequest(ctx context.Context, title, description, sourceBranch, targetBranch string) (*adoPR, error) {
	src := "refs/heads/" + strings.TrimPrefix(sourceBranch, "refs/heads/")
	tgt := "refs/heads/" + strings.TrimPrefix(targetBranch, "refs/heads/")
	payload := map[string]interface{}{
		"sourceRefName":      src,
		"targetRefName":      tgt,
		"title":              title,
		"description":        description,
		"supportsIterations": true,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var pr adoPR
	u := c.pullRequestsURL("")
	if err := c.do(ctx, http.MethodPost, u, bytes.NewReader(b), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// GetPRIterationsCommits — skip; use simple comment API

// CreateThread adds a PR comment thread.
func (c *Client) CreateThread(ctx context.Context, prID int, content string) error {
	payload := map[string]interface{}{
		"comments": []map[string]interface{}{
			{"parentCommentId": 0, "content": content, "commentType": 1},
		},
		"status": 1,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/_apis/git/repositories/%s/pullrequests/%d/threads?api-version=7.1",
		c.baseURL+"/"+url.PathEscape(c.project),
		url.PathEscape(c.repository),
		prID)
	return c.do(ctx, http.MethodPost, u, bytes.NewReader(b), nil)
}

// SetPullRequestStatus sets a status on the PR's last merge commit (gen2 API simplified).
func (c *Client) CreatePullRequestStatus(ctx context.Context, prID int, genre, name, state, description string) error {
	// GET PR for lastMergeSourceCommit
	u := fmt.Sprintf("%s/_apis/git/repositories/%s/pullrequests/%d?api-version=7.1",
		c.baseURL+"/"+url.PathEscape(c.project),
		url.PathEscape(c.repository),
		prID)
	var pr struct {
		LastMergeSourceCommit struct {
			CommitID string `json:"commitId"`
		} `json:"lastMergeSourceCommit"`
	}
	if err := c.do(ctx, http.MethodGet, u, nil, &pr); err != nil {
		return err
	}
	if pr.LastMergeSourceCommit.CommitID == "" {
		return fmt.Errorf("azuredevops: no commit on PR %d", prID)
	}
	statusPayload := map[string]interface{}{
		"state":       state,
		"description": description,
		"context": map[string]string{
			"name":  name,
			"genre": genre,
		},
	}
	b, err := json.Marshal(statusPayload)
	if err != nil {
		return err
	}
	su := fmt.Sprintf("%s/_apis/git/repositories/%s/commits/%s/statuses?api-version=7.1",
		c.baseURL+"/"+url.PathEscape(c.project),
		url.PathEscape(c.repository),
		url.PathEscape(pr.LastMergeSourceCommit.CommitID))
	return c.do(ctx, http.MethodPost, su, bytes.NewReader(b), nil)
}
