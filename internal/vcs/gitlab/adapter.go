package gitlab

import (
	"context"
	"fmt"

	"github.com/asqs/asqs-core/internal/vcs"
)

// Adapter wraps Client to implement vcs.PRWorkflowClient (Owner=namespace, Repo=project).
type Adapter struct {
	Client *Client
}

var _ vcs.PRWorkflowClient = (*Adapter)(nil)

func gitlabStateFromCheck(status, conclusion string) string {
	if status == "completed" {
		if conclusion == "success" {
			return "success"
		}
		return "failed"
	}
	if status == "in_progress" || status == "queued" {
		return "running"
	}
	return "pending"
}

// ListPRFiles implements vcs.PRWorkflowClient.
func (a *Adapter) ListPRFiles(ctx context.Context, pr *vcs.PRContext) ([]*vcs.PRFile, error) {
	changes, err := a.Client.ListMRFiles(ctx, pr.Owner, pr.Repo, pr.PRNumber)
	if err != nil {
		return nil, err
	}
	out := make([]*vcs.PRFile, 0, len(changes))
	for i := range changes {
		ch := &changes[i]
		name := ch.NewPath
		if name == "" {
			name = ch.OldPath
		}
		status := "modified"
		if ch.NewFile {
			status = "added"
		}
		if ch.DeletedFile {
			status = "removed"
		}
		out = append(out, &vcs.PRFile{
			Filename: name,
			Patch:    ch.Diff,
			Status:   status,
		})
	}
	return out, nil
}

// CreateCheckRun implements vcs.PRWorkflowClient using GitLab commit statuses.
func (a *Adapter) CreateCheckRun(ctx context.Context, pr *vcs.PRContext, opts vcs.CheckRunOptions) (int64, error) {
	state := gitlabStateFromCheck(opts.Status, opts.Conclusion)
	desc := opts.OutputSummary
	if desc == "" {
		desc = opts.Name
	}
	if err := a.Client.SetCommitStatus(ctx, pr.Owner, pr.Repo, pr.HeadSHA, opts.Name, state, desc, opts.DetailsURL); err != nil {
		return 0, err
	}
	return 1, nil
}

// UpdateCheckRun implements vcs.PRWorkflowClient.
func (a *Adapter) UpdateCheckRun(ctx context.Context, pr *vcs.PRContext, _ int64, opts vcs.CheckRunOptions) error {
	state := gitlabStateFromCheck(opts.Status, opts.Conclusion)
	desc := opts.OutputSummary
	if desc == "" {
		desc = opts.Name
	}
	return a.Client.SetCommitStatus(ctx, pr.Owner, pr.Repo, pr.HeadSHA, opts.Name, state, desc, opts.DetailsURL)
}

// CreateComment implements vcs.PRWorkflowClient.
func (a *Adapter) CreateComment(ctx context.Context, pr *vcs.PRContext, body string) error {
	if err := a.Client.CreateMRNote(ctx, pr.Owner, pr.Repo, pr.PRNumber, body); err != nil {
		return fmt.Errorf("gitlab: comment: %w", err)
	}
	return nil
}
