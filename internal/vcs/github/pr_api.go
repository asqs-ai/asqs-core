package github

import (
	"context"
	"fmt"

	"github.com/google/go-github/v62/github"
)

// PRFile is a file changed in a pull request.
type PRFile struct {
	Filename  string
	Additions int
	Deletions int
	Patch     string // unified diff snippet; may be empty for binary
	Status    string // "added", "removed", "modified"
}

// listPRFiles lists files changed in a pull request (diff). Used by vcs.PRWorkflowClient adapter.
func (c *Client) listPRFiles(ctx context.Context, owner, repo string, prNumber int) ([]*PRFile, error) {
	if owner == "" {
		owner = c.owner
	}
	if repo == "" {
		repo = c.repo
	}
	files, _, err := c.client.PullRequests.ListFiles(ctx, owner, repo, prNumber, nil)
	if err != nil {
		return nil, fmt.Errorf("github: list PR files: %w", err)
	}
	out := make([]*PRFile, 0, len(files))
	for _, f := range files {
		status := "modified"
		if f.GetStatus() != "" {
			status = f.GetStatus()
		}
		out = append(out, &PRFile{
			Filename:  f.GetFilename(),
			Additions: f.GetAdditions(),
			Deletions: f.GetDeletions(),
			Patch:     f.GetPatch(),
			Status:    status,
		})
	}
	return out, nil
}

// createComment posts a comment on a pull request (issue comment).
func (c *Client) createComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	if owner == "" {
		owner = c.owner
	}
	if repo == "" {
		repo = c.repo
	}
	_, _, err := c.client.Issues.CreateComment(ctx, owner, repo, prNumber, &github.IssueComment{Body: github.String(body)})
	if err != nil {
		return fmt.Errorf("github: create comment: %w", err)
	}
	return nil
}

// CheckRunOptions configures creating or updating a check run.
type CheckRunOptions struct {
	Name          string
	HeadSHA       string
	Status        string // "queued", "in_progress", "completed"
	Conclusion    string // "success", "failure", "neutral", "cancelled", "skipped"; set when status=completed
	DetailsURL    string
	OutputTitle   string
	OutputSummary string
	OutputText    string
}

// createCheckRun creates a check run for the given commit. Requires GitHub App or token with checks scope.
func (c *Client) createCheckRun(ctx context.Context, owner, repo string, opts CheckRunOptions) (int64, error) {
	if owner == "" {
		owner = c.owner
	}
	if repo == "" {
		repo = c.repo
	}
	in := &github.CreateCheckRunOptions{
		Name:    opts.Name,
		HeadSHA: opts.HeadSHA,
	}
	if opts.Status != "" {
		in.Status = github.String(opts.Status)
	}
	if opts.DetailsURL != "" {
		in.DetailsURL = github.String(opts.DetailsURL)
	}
	if opts.OutputTitle != "" || opts.OutputSummary != "" || opts.OutputText != "" {
		in.Output = &github.CheckRunOutput{
			Title:   github.String(opts.OutputTitle),
			Summary: github.String(opts.OutputSummary),
			Text:    github.String(opts.OutputText),
		}
	}
	if opts.Conclusion != "" {
		in.Conclusion = github.String(opts.Conclusion)
	}
	run, _, err := c.client.Checks.CreateCheckRun(ctx, owner, repo, *in)
	if err != nil {
		return 0, fmt.Errorf("github: create check run: %w", err)
	}
	return run.GetID(), nil
}

// updateCheckRun updates an existing check run (e.g. set status to completed and conclusion).
func (c *Client) updateCheckRun(ctx context.Context, owner, repo string, checkRunID int64, opts CheckRunOptions) error {
	if owner == "" {
		owner = c.owner
	}
	if repo == "" {
		repo = c.repo
	}
	in := &github.UpdateCheckRunOptions{
		Name:   opts.Name,
		Status: github.String(opts.Status),
	}
	if opts.Conclusion != "" {
		in.Conclusion = github.String(opts.Conclusion)
	}
	if opts.DetailsURL != "" {
		in.DetailsURL = github.String(opts.DetailsURL)
	}
	if opts.OutputTitle != "" || opts.OutputSummary != "" || opts.OutputText != "" {
		in.Output = &github.CheckRunOutput{
			Title:   github.String(opts.OutputTitle),
			Summary: github.String(opts.OutputSummary),
			Text:    github.String(opts.OutputText),
		}
	}
	_, _, err := c.client.Checks.UpdateCheckRun(ctx, owner, repo, checkRunID, *in)
	if err != nil {
		return fmt.Errorf("github: update check run: %w", err)
	}
	return nil
}
