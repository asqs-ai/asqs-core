package gitlab

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

// Client is a minimal GitLab REST v4 client for merge requests and commit statuses.
type Client struct {
	baseURL    string // e.g. https://gitlab.com/api/v4
	token      string
	httpClient *http.Client
}

// NewClient returns a GitLab API client. baseURL empty defaults to https://gitlab.com/api/v4.
func NewClient(token, baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://gitlab.com/api/v4"
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

func (c *Client) projectPathID(namespace, project string) string {
	return url.PathEscape(namespace + "/" + project)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
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
		return fmt.Errorf("gitlab API %s %s: %s: %s", method, path, resp.Status, truncate(string(b), 500))
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("gitlab decode: %w", err)
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

// MergeRequest is a subset of GitLab API response.
type MergeRequest struct {
	IID    int    `json:"iid"`
	WebURL string `json:"web_url"`
	Title  string `json:"title"`
	State  string `json:"state"`
}

// ListOpenMergeRequestsBySourceBranch lists open MRs for a source branch.
func (c *Client) ListOpenMergeRequestsBySourceBranch(ctx context.Context, namespace, project, sourceBranch string) ([]MergeRequest, error) {
	pid := c.projectPathID(namespace, project)
	q := url.Values{}
	q.Set("state", "opened")
	q.Set("source_branch", sourceBranch)
	path := fmt.Sprintf("/projects/%s/merge_requests?%s", pid, q.Encode())
	var list []MergeRequest
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// CreateMergeRequest opens a new merge request.
func (c *Client) CreateMergeRequest(ctx context.Context, namespace, project, sourceBranch, targetBranch, title, description string) (*MergeRequest, error) {
	pid := c.projectPathID(namespace, project)
	payload := map[string]interface{}{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
	}
	if description != "" {
		payload["description"] = description
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var mr MergeRequest
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%s/merge_requests", pid), bytes.NewReader(b), &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

type mrChange struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	DeletedFile bool   `json:"deleted_file"`
	RenamedFile bool   `json:"renamed_file"`
}

type mrChangesResp struct {
	Changes []mrChange `json:"changes"`
}

// ListMRFiles returns changed files for an MR by IID.
func (c *Client) ListMRFiles(ctx context.Context, namespace, project string, iid int) ([]mrChange, error) {
	pid := c.projectPathID(namespace, project)
	var out mrChangesResp
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/changes", pid, iid)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Changes, nil
}

// CreateMRNote adds a comment on the merge request.
func (c *Client) CreateMRNote(ctx context.Context, namespace, project string, iid int, body string) error {
	pid := c.projectPathID(namespace, project)
	b, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/projects/%s/merge_requests/%d/notes", pid, iid), bytes.NewReader(b), nil)
}

// SetCommitStatus creates or updates a commit status (used as check-run equivalent).
func (c *Client) SetCommitStatus(ctx context.Context, namespace, project, sha, name, state, description, targetURL string) error {
	pid := c.projectPathID(namespace, project)
	q := url.Values{}
	q.Set("state", state)
	q.Set("name", name)
	if description != "" {
		q.Set("description", description)
	}
	if targetURL != "" {
		q.Set("target_url", targetURL)
	}
	path := fmt.Sprintf("/projects/%s/statuses/%s?%s", pid, url.PathEscape(sha), q.Encode())
	return c.do(ctx, http.MethodPost, path, nil, nil)
}
