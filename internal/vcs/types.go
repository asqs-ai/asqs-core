// Package vcs defines platform-agnostic types and interfaces for version control.
//
// The MVP supports GitHub; the design is extendable to other platforms (e.g. GitLab, Bitbucket)
// by implementing WebhookProvider and PRWorkflowClient for each platform. The webhook HTTP handler,
// gating rules, and PR workflow runner are provider-agnostic and dispatch by pr.Provider using
// a map of clients keyed by provider name (ProviderGitHub, ProviderGitLab, etc.).
package vcs

import "context"

// Provider identifies the VCS platform (e.g. "github", "gitlab").
const (
	ProviderGitHub      = "github"
	ProviderGitLab      = "gitlab"
	ProviderBitbucket   = "bitbucket"
	ProviderAzureDevOps = "azure_devops"
)

// PRContext is the normalized pull/merge request context from any provider's webhook.
// Used by gating and workflow; provider-specific parsers produce this.
type PRContext struct {
	Provider   string // ProviderGitHub, ProviderGitLab, etc.
	Owner      string
	Repo       string
	PRNumber   int
	BaseRef    string // target branch, e.g. "main"
	HeadRef    string
	HeadSHA    string
	Draft      bool
	CloneURL   string
	RepoSizeKB int
}

// PRFile is a file changed in a PR (platform-agnostic).
type PRFile struct {
	Filename  string
	Additions int
	Deletions int
	Patch     string
	Status    string // "added", "removed", "modified"
}

// GateResult is the result of running gating rules (provider-agnostic).
type GateResult struct {
	Pass   bool
	Reason string
	Failed []string
}

// CheckRunOptions configures creating or updating a check/status run (GitHub check run, GitLab status, etc.).
type CheckRunOptions struct {
	Name          string
	HeadSHA       string
	Status        string // "queued", "in_progress", "completed"
	Conclusion    string // "success", "failure", etc.; set when status=completed
	DetailsURL    string
	OutputTitle   string
	OutputSummary string
	OutputText    string
}

// GateRunner runs gating rules against a PR context. Implementations are provider-agnostic (they only need PRContext).
type GateRunner interface {
	RunGates(ctx context.Context, pr *PRContext) GateResult
}

// PRWorkflowClient is the platform-specific client for PR workflow operations (list files, check runs, comments).
// Implemented by each provider (e.g. github.Client, gitlab.Client).
type PRWorkflowClient interface {
	ListPRFiles(ctx context.Context, pr *PRContext) ([]*PRFile, error)
	CreateCheckRun(ctx context.Context, pr *PRContext, opts CheckRunOptions) (checkRunID int64, err error)
	UpdateCheckRun(ctx context.Context, pr *PRContext, checkRunID int64, opts CheckRunOptions) error
	CreateComment(ctx context.Context, pr *PRContext, body string) error
}

// PRWorkflowRunner runs the post-gate workflow: read diffs, create check run, comment, run WorkflowFunc, update check run.
// Uses PRWorkflowClient so the same runner works with any provider.
type PRWorkflowRunner interface {
	RunPRWorkflow(ctx context.Context, pr *PRContext, client PRWorkflowClient, action string) error
}

// WebhookProvider parses and verifies a provider's webhook request and returns normalized PRContext.
// Each platform implements this (GitHub: X-GitHub-Event + X-Hub-Signature-256; GitLab: X-Gitlab-Event + token).
type WebhookProvider interface {
	// VerifyAndParse reads the request body, verifies signature (if configured), and parses into PRContext.
	// Returns (nil, action, nil) for unsupported event/action (e.g. not pull_request opened/synchronize).
	VerifyAndParse(r *HTTPRequest) (pr *PRContext, action string, err error)
}

// HTTPRequest is a minimal view of an HTTP request for webhook verification and parsing.
// Allows providers to work with standard or test requests.
type HTTPRequest struct {
	Method  string
	Headers map[string]string
	Body    []byte
}
