package vcs

import (
	"context"
	"fmt"
)

// PRWorkflowOptions configures the default PR workflow (check run name, comment, optional WorkflowFunc).
type PRWorkflowOptions struct {
	CheckRunName   string
	CommentOnStart string
	PushGranted    bool
	// WorkflowFunc is the actual work (clone, index, generate, evaluate, push, open PR). Receives platform-agnostic PRContext and PRFile list.
	WorkflowFunc func(ctx context.Context, pr *PRContext, files []*PRFile) error
}

// DefaultPRWorkflow is a generic PRWorkflowRunner that uses any PRWorkflowClient.
type DefaultPRWorkflow struct {
	Options PRWorkflowOptions
}

// RunPRWorkflow runs: list files → create check run → optional comment → WorkflowFunc → update check run.
func (w *DefaultPRWorkflow) RunPRWorkflow(ctx context.Context, pr *PRContext, client PRWorkflowClient, action string) error {
	_ = action
	files, err := client.ListPRFiles(ctx, pr)
	if err != nil {
		return fmt.Errorf("pr workflow: list files: %w", err)
	}
	checkRunID, err := client.CreateCheckRun(ctx, pr, CheckRunOptions{
		Name:          w.Options.CheckRunName,
		HeadSHA:       pr.HeadSHA,
		Status:        "in_progress",
		OutputTitle:   w.Options.CheckRunName,
		OutputSummary: "Analyzing PR…",
	})
	if err != nil {
		return fmt.Errorf("pr workflow: create check run: %w", err)
	}
	if w.Options.CommentOnStart != "" {
		_ = client.CreateComment(ctx, pr, w.Options.CommentOnStart)
	}
	var workflowErr error
	if w.Options.WorkflowFunc != nil {
		workflowErr = w.Options.WorkflowFunc(ctx, pr, files)
	}
	conclusion := "success"
	summary := "Completed."
	if workflowErr != nil {
		conclusion = "failure"
		summary = workflowErr.Error()
	}
	if err := client.UpdateCheckRun(ctx, pr, checkRunID, CheckRunOptions{
		Name:          w.Options.CheckRunName,
		Status:        "completed",
		Conclusion:    conclusion,
		OutputTitle:   w.Options.CheckRunName,
		OutputSummary: summary,
	}); err != nil {
		return fmt.Errorf("pr workflow: update check run: %w", err)
	}
	return workflowErr
}
