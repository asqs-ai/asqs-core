package azuredevops

import (
	"context"

	"github.com/asqs/asqs-core/internal/vcs"
)

// Adapter implements vcs.PRWorkflowClient using organization/project/repo from the wrapped client.
type Adapter struct {
	Client *Client
}

var _ vcs.PRWorkflowClient = (*Adapter)(nil)

func adoState(status, conclusion string) string {
	if status == "completed" {
		if conclusion == "success" {
			return "succeeded"
		}
		return "failed"
	}
	return "pending"
}

// ListPRFiles implements vcs.PRWorkflowClient (MVP: empty; diff API is iteration-scoped).
func (a *Adapter) ListPRFiles(ctx context.Context, pr *vcs.PRContext) ([]*vcs.PRFile, error) {
	_ = ctx
	_ = pr
	return nil, nil
}

// CreateCheckRun implements vcs.PRWorkflowClient via commit statuses on the PR head.
func (a *Adapter) CreateCheckRun(ctx context.Context, pr *vcs.PRContext, opts vcs.CheckRunOptions) (int64, error) {
	st := adoState(opts.Status, opts.Conclusion)
	desc := opts.OutputSummary
	if desc == "" {
		desc = opts.Name
	}
	if err := a.Client.CreatePullRequestStatus(ctx, pr.PRNumber, "qualitybot", opts.Name, st, desc); err != nil {
		return 0, err
	}
	return int64(pr.PRNumber), nil
}

// UpdateCheckRun implements vcs.PRWorkflowClient.
func (a *Adapter) UpdateCheckRun(ctx context.Context, pr *vcs.PRContext, _ int64, opts vcs.CheckRunOptions) error {
	st := adoState(opts.Status, opts.Conclusion)
	desc := opts.OutputSummary
	if desc == "" {
		desc = opts.Name
	}
	return a.Client.CreatePullRequestStatus(ctx, pr.PRNumber, "qualitybot", opts.Name, st, desc)
}

// CreateComment implements vcs.PRWorkflowClient.
func (a *Adapter) CreateComment(ctx context.Context, pr *vcs.PRContext, body string) error {
	return a.Client.CreateThread(ctx, pr.PRNumber, body)
}
