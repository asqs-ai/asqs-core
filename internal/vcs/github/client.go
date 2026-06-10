package github

import (
	"context"
	"fmt"

	"github.com/google/go-github/v62/github"
	"golang.org/x/oauth2"
)

// Client is a GitHub API client for creating PRs and interacting with repos.
type Client struct {
	client *github.Client
	owner  string // default owner for API calls
	repo   string // default repo name
}

// NewClient returns a GitHub client authenticated with the given token.
// owner and repo are optional defaults for CreatePullRequest; they can be overridden per call.
func NewClient(token string, owner, repo string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	hc := oauth2.NewClient(context.Background(), ts)
	return &Client{
		client: github.NewClient(hc),
		owner:  owner,
		repo:   repo,
	}
}

// CreatePullRequestOptions configures a new pull request.
type CreatePullRequestOptions struct {
	Owner string // GitHub owner/org (defaults to client default)
	Repo  string // repo name (defaults to client default)
	Title string
	Body  string
	Head  string // branch to merge from (e.g. "qualitybot/add-tests-123")
	Base  string // branch to merge into (e.g. "main"); empty = default branch
	Draft bool
}

// PullRequest is the result of creating a PR.
type PullRequest struct {
	Number  int
	URL     string
	HTMLURL string
}

// CreatePullRequest creates a pull request and returns its number and URL.
func (c *Client) CreatePullRequest(ctx context.Context, opts CreatePullRequestOptions) (*PullRequest, error) {
	owner := opts.Owner
	if owner == "" {
		owner = c.owner
	}
	repo := opts.Repo
	if repo == "" {
		repo = c.repo
	}
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("github: owner and repo required")
	}
	if opts.Title == "" || opts.Head == "" {
		return nil, fmt.Errorf("github: title and head branch required")
	}
	if opts.Base == "" {
		opts.Base = "main"
	}
	in := &github.NewPullRequest{
		Title: github.String(opts.Title),
		Head:  github.String(opts.Head),
		Base:  github.String(opts.Base),
		Draft: github.Bool(opts.Draft),
	}
	if opts.Body != "" {
		in.Body = github.String(opts.Body)
	}
	pr, _, err := c.client.PullRequests.Create(ctx, owner, repo, in)
	if err != nil {
		return nil, fmt.Errorf("github: create PR: %w", err)
	}
	out := &PullRequest{Number: pr.GetNumber(), URL: pr.GetURL(), HTMLURL: pr.GetHTMLURL()}
	if out.URL == "" {
		out.URL = pr.GetHTMLURL()
	}
	return out, nil
}

// ListPullRequestsByHead lists open pull requests whose head branch matches owner:branch (e.g. "myorg:quality-bot").
// Returns an empty slice when none exist so the caller can create a PR only when len(prs) == 0.
func (c *Client) ListPullRequestsByHead(ctx context.Context, owner, repoName, head string) ([]*PullRequest, error) {
	if owner == "" {
		owner = c.owner
	}
	if repoName == "" {
		repoName = c.repo
	}
	if owner == "" || repoName == "" {
		return nil, fmt.Errorf("github: owner and repo required")
	}
	opts := &github.PullRequestListOptions{State: "open", Head: head}
	prs, _, err := c.client.PullRequests.List(ctx, owner, repoName, opts)
	if err != nil {
		return nil, fmt.Errorf("github: list PRs head=%q: %w", head, err)
	}
	out := make([]*PullRequest, 0, len(prs))
	for _, pr := range prs {
		out = append(out, &PullRequest{Number: pr.GetNumber(), URL: pr.GetURL(), HTMLURL: pr.GetHTMLURL()})
	}
	return out, nil
}

// ParseRepoURL returns owner and repo from a GitHub URL:
// https://github.com/owner/repo, https://github.com/owner/repo.git, or git@github.com:owner/repo.
func ParseRepoURL(url string) (owner, repo string, err error) {
	const host = "github.com/"
	const hostSSH = "github.com:"
	// Strip protocol
	for _, p := range []string{"https://", "http://", "git@"} {
		if len(url) > len(p) && url[:len(p)] == p {
			url = url[len(p):]
			break
		}
	}
	// url is now "github.com/owner/repo" or "github.com:owner/repo"
	var rest string
	if len(url) > len(host) && url[:len(host)] == host {
		rest = url[len(host):]
	} else if len(url) > len(hostSSH) && url[:len(hostSSH)] == hostSSH {
		rest = url[len(hostSSH):]
	} else {
		return "", "", fmt.Errorf("github: invalid repo URL (expected github.com): %q", url)
	}
	// rest is "owner/repo" or "owner/repo.git"
	i := 0
	for i < len(rest) && rest[i] != '/' {
		i++
	}
	if i == 0 || i >= len(rest) {
		return "", "", fmt.Errorf("github: invalid repo URL %q", url)
	}
	owner = rest[:i]
	repo = rest[i+1:]
	if len(repo) > 4 && repo[len(repo)-4:] == ".git" {
		repo = repo[:len(repo)-4]
	}
	return owner, repo, nil
}
