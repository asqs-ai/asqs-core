package github

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/asqs/asqs-core/internal/repo"
)

// BranchAndPROptions configures the full flow: clone → branch → apply changes → commit → push → create PR.
type BranchAndPROptions struct {
	// CloneURL is the repo URL (e.g. https://github.com/owner/repo or git@github.com:owner/repo.git).
	CloneURL string
	// Token is the GitHub token for clone, push, and PR (use repo.TokenAuth{Token: token} for Auth).
	Token string
	// WorkDir is the parent directory to clone into; the repo will be at WorkDir/<repo-name> or WorkDir if CloneDir is set.
	WorkDir string
	// CloneDir is optional; if set, clone into this directory (under WorkDir or absolute). If empty, a temp or repo-named dir is used.
	CloneDir string
	// HeadBranch is the new branch to create and push (e.g. "qualitybot/add-tests-123").
	HeadBranch string
	// BaseBranch is the target branch for the PR (e.g. "main").
	BaseBranch string
	// PRTitle and PRBody are the pull request title and body.
	PRTitle string
	PRBody  string
	// Draft creates a draft PR when true.
	Draft bool
	// CloneDepth limits clone depth; 0 = full clone.
	CloneDepth int
}

// ApplyFunc is called with the cloned repo so the caller can add files and commit. Must add, then commit.
type ApplyFunc func(r *repo.Repo) error

// BranchAndPR clones the repo, creates HeadBranch, runs apply (add + commit), pushes, and creates a PR.
// The caller must not push or create the PR; this flow does it. ApplyFunc should use r.Add(...) and r.Commit(...).
func (c *Client) BranchAndPR(ctx context.Context, opts BranchAndPROptions, apply ApplyFunc) (*PullRequest, error) {
	if opts.CloneURL == "" || opts.Token == "" || opts.HeadBranch == "" || opts.PRTitle == "" {
		return nil, fmt.Errorf("github: BranchAndPR requires CloneURL, Token, HeadBranch, PRTitle")
	}
	owner, repoName, err := ParseRepoURL(opts.CloneURL)
	if err != nil {
		return nil, err
	}
	dir := opts.CloneDir
	if dir == "" {
		if opts.WorkDir == "" {
			return nil, fmt.Errorf("github: BranchAndPR requires WorkDir or CloneDir")
		}
		dir = filepath.Join(opts.WorkDir, repoName)
	}
	auth := &repo.TokenAuth{Token: opts.Token}
	cloneOpts := repo.CloneOptions{
		URL:   opts.CloneURL,
		Dir:   dir,
		Depth: opts.CloneDepth,
		Auth:  auth,
	}
	r, err := repo.Clone(ctx, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("github: clone: %w", err)
	}
	if err := r.CreateAndCheckoutBranch(opts.HeadBranch); err != nil {
		return nil, fmt.Errorf("github: create branch: %w", err)
	}
	if err := apply(r); err != nil {
		return nil, fmt.Errorf("github: apply: %w", err)
	}
	if err := r.Push(ctx, repo.PushOptions{RemoteName: "origin", Branch: opts.HeadBranch, Auth: auth}); err != nil {
		return nil, fmt.Errorf("github: push: %w", err)
	}
	pr, err := c.CreatePullRequest(ctx, CreatePullRequestOptions{
		Owner: owner,
		Repo:  repoName,
		Title: opts.PRTitle,
		Body:  opts.PRBody,
		Head:  opts.HeadBranch,
		Base:  opts.BaseBranch,
		Draft: opts.Draft,
	})
	if err != nil {
		return nil, fmt.Errorf("github: create PR: %w", err)
	}
	return pr, nil
}
