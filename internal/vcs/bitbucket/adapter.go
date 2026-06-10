package bitbucket

import (
	"context"

	"github.com/asqs/asqs-core/internal/vcs"
)

// Adapter implements vcs.PRWorkflowClient.
type Adapter struct {
	Client *Client
}

var _ vcs.PRWorkflowClient = (*Adapter)(nil)

func bbBuildState(status, conclusion string) string {
	if status == "completed" {
		if conclusion == "success" {
			return "SUCCESSFUL"
		}
		return "FAILED"
	}
	if status == "in_progress" || status == "queued" {
		return "INPROGRESS"
	}
	return "INPROGRESS"
}

// ListPRFiles implements vcs.PRWorkflowClient.
func (a *Adapter) ListPRFiles(ctx context.Context, pr *vcs.PRContext) ([]*vcs.PRFile, error) {
	stats, err := a.Client.ListPRDiffStats(ctx, pr.Owner, pr.Repo, pr.PRNumber)
	if err != nil {
		return nil, err
	}
	out := make([]*vcs.PRFile, 0, len(stats))
	for _, s := range stats {
		name := s.New.Path
		if name == "" {
			name = s.Old.Path
		}
		st := s.Status
		if st == "" {
			st = "modified"
		}
		out = append(out, &vcs.PRFile{Filename: name, Status: st})
	}
	return out, nil
}

// CreateCheckRun implements vcs.PRWorkflowClient.
func (a *Adapter) CreateCheckRun(ctx context.Context, pr *vcs.PRContext, opts vcs.CheckRunOptions) (int64, error) {
	st := bbBuildState(opts.Status, opts.Conclusion)
	desc := opts.OutputSummary
	if desc == "" {
		desc = opts.Name
	}
	if pr.HeadSHA == "" {
		return 0, nil
	}
	if err := a.Client.SetBuildStatus(ctx, pr.Owner, pr.Repo, pr.HeadSHA, opts.Name, st, opts.Name, desc, opts.DetailsURL); err != nil {
		return 0, err
	}
	return int64(pr.PRNumber), nil
}

// UpdateCheckRun implements vcs.PRWorkflowClient.
func (a *Adapter) UpdateCheckRun(ctx context.Context, pr *vcs.PRContext, _ int64, opts vcs.CheckRunOptions) error {
	if pr.HeadSHA == "" {
		return nil
	}
	st := bbBuildState(opts.Status, opts.Conclusion)
	desc := opts.OutputSummary
	if desc == "" {
		desc = opts.Name
	}
	return a.Client.SetBuildStatus(ctx, pr.Owner, pr.Repo, pr.HeadSHA, opts.Name, st, opts.Name, desc, opts.DetailsURL)
}

// CreateComment implements vcs.PRWorkflowClient.
func (a *Adapter) CreateComment(ctx context.Context, pr *vcs.PRContext, body string) error {
	return a.Client.CreatePRComment(ctx, pr.Owner, pr.Repo, pr.PRNumber, body)
}
