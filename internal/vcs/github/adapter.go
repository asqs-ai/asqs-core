// Package github implements the GitHub VCS provider (webhook, PR workflow client).
// It satisfies the platform-agnostic interfaces in internal/vcs so the rest of the app can stay provider-agnostic.
package github

import (
	"context"

	"github.com/asqs/asqs-core/internal/vcs"
)

// Ensure *Client implements vcs.PRWorkflowClient.
var _ vcs.PRWorkflowClient = (*Client)(nil)

// ListPRFiles implements vcs.PRWorkflowClient.
func (c *Client) ListPRFiles(ctx context.Context, pr *vcs.PRContext) ([]*vcs.PRFile, error) {
	list, err := c.listPRFiles(ctx, pr.Owner, pr.Repo, pr.PRNumber)
	if err != nil {
		return nil, err
	}
	out := make([]*vcs.PRFile, 0, len(list))
	for _, f := range list {
		out = append(out, &vcs.PRFile{
			Filename:  f.Filename,
			Additions: f.Additions,
			Deletions: f.Deletions,
			Patch:     f.Patch,
			Status:    f.Status,
		})
	}
	return out, nil
}

// CreateCheckRun implements vcs.PRWorkflowClient.
func (c *Client) CreateCheckRun(ctx context.Context, pr *vcs.PRContext, opts vcs.CheckRunOptions) (int64, error) {
	return c.createCheckRun(ctx, pr.Owner, pr.Repo, checkRunOptionsFromVCS(opts))
}

func checkRunOptionsFromVCS(o vcs.CheckRunOptions) CheckRunOptions {
	return CheckRunOptions{
		Name:          o.Name,
		HeadSHA:       o.HeadSHA,
		Status:        o.Status,
		Conclusion:    o.Conclusion,
		DetailsURL:    o.DetailsURL,
		OutputTitle:   o.OutputTitle,
		OutputSummary: o.OutputSummary,
		OutputText:    o.OutputText,
	}
}

// UpdateCheckRun implements vcs.PRWorkflowClient.
func (c *Client) UpdateCheckRun(ctx context.Context, pr *vcs.PRContext, checkRunID int64, opts vcs.CheckRunOptions) error {
	return c.updateCheckRun(ctx, pr.Owner, pr.Repo, checkRunID, checkRunOptionsFromVCS(opts))
}

// CreateComment implements vcs.PRWorkflowClient.
func (c *Client) CreateComment(ctx context.Context, pr *vcs.PRContext, body string) error {
	return c.createComment(ctx, pr.Owner, pr.Repo, pr.PRNumber, body)
}
