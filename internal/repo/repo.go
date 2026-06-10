// Package repo provides repository abstraction: clone, branch, commit, push, and path resolution.
package repo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Repo represents a local git repository.
type Repo struct {
	Path string // absolute path to repo root
	repo *git.Repository
}

// Clone clones the repository into dir and returns a Repo. Dir must be empty or not exist.
func Clone(ctx context.Context, opts CloneOptions) (*Repo, error) {
	if opts.URL == "" {
		return nil, fmt.Errorf("repo: clone URL required")
	}
	dir := opts.Dir
	if dir == "" {
		return nil, fmt.Errorf("repo: clone Dir required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("repo: abs path: %w", err)
	}
	if IsAzureDevOpsHTTPSURL(opts.URL) {
		if pat, ok := patFromTokenAuth(opts.Auth); ok {
			if err := cloneAzureGitCLI(ctx, abs, opts, pat); err != nil {
				return nil, fmt.Errorf("repo: clone: %w", err)
			}
			return Open(abs)
		}
	}
	cloneOpts := &git.CloneOptions{
		URL:   opts.URL,
		Depth: opts.Depth,
	}
	if opts.Auth != nil {
		cloneOpts.Auth = authFromForRemoteURL(opts.URL, opts.Auth)
	}
	if opts.Branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(opts.Branch)
		// When FetchAllRefs is true, fetch all refs so origin/<ship-branch> exists for later checkout (avoids non-fast-forward on second run).
		cloneOpts.SingleBranch = !opts.FetchAllRefs
	}
	_, err = git.PlainClone(abs, false, cloneOpts)
	if err != nil {
		return nil, fmt.Errorf("repo: clone: %w", err)
	}
	return Open(abs)
}

// RemoteURL returns the first configured URL for the named remote (e.g. "origin"), or "" if unknown.
func (r *Repo) RemoteURL(remoteName string) string {
	return r.remoteURL(remoteName)
}

// remoteURL returns the first configured URL for the named remote, or "" if unknown.
func (r *Repo) remoteURL(remoteName string) string {
	if remoteName == "" {
		remoteName = "origin"
	}
	cfg, err := r.repo.Config()
	if err != nil {
		return ""
	}
	rem, ok := cfg.Remotes[remoteName]
	if !ok || len(rem.URLs) == 0 {
		return ""
	}
	return rem.URLs[0]
}

// Open opens an existing local repository at path.
func Open(path string) (*Repo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("repo: abs path: %w", err)
	}
	r, err := git.PlainOpen(abs)
	if err != nil {
		return nil, fmt.Errorf("repo: open: %w", err)
	}
	return &Repo{Path: abs, repo: r}, nil
}

// CurrentBranch returns the short name of the current branch (e.g. "main").
func (r *Repo) CurrentBranch() (string, error) {
	ref, err := r.repo.Head()
	if err != nil {
		return "", fmt.Errorf("repo: head: %w", err)
	}
	return ref.Name().Short(), nil
}

// CreateBranch creates a new branch from the current HEAD and does not checkout.
func (r *Repo) CreateBranch(name string) error {
	head, err := r.repo.Head()
	if err != nil {
		return fmt.Errorf("repo: head: %w", err)
	}
	ref := plumbing.NewBranchReferenceName(name)
	err = r.repo.Storer.SetReference(plumbing.NewHashReference(ref, head.Hash()))
	if err != nil {
		return fmt.Errorf("repo: create branch %q: %w", name, err)
	}
	return nil
}

// CheckoutBranch checks out the given branch (create from HEAD if it doesn't exist).
func (r *Repo) CheckoutBranch(name string) error {
	w, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("repo: worktree: %w", err)
	}
	ref := plumbing.NewBranchReferenceName(name)
	err = w.Checkout(&git.CheckoutOptions{Branch: ref})
	if err != nil {
		return fmt.Errorf("repo: checkout %q: %w", name, err)
	}
	return nil
}

// CreateAndCheckoutBranch creates a branch and checks it out (convenience for PR workflow).
func (r *Repo) CreateAndCheckoutBranch(name string) error {
	if err := r.CreateBranch(name); err != nil {
		return err
	}
	return r.CheckoutBranch(name)
}

// Fetch fetches from the given remote (e.g. origin). Use before checking for remote branches or merging.
func (r *Repo) Fetch(ctx context.Context, opts FetchOptions) error {
	if opts.RemoteName == "" {
		opts.RemoteName = "origin"
	}
	remoteURL := r.remoteURL(opts.RemoteName)
	if IsAzureDevOpsHTTPSURL(remoteURL) {
		if pat, ok := patFromTokenAuth(opts.Auth); ok {
			if err := fetchAzureGitCLI(ctx, r.Path, opts.RemoteName, pat); err != nil {
				return fmt.Errorf("repo: fetch %s: %w", opts.RemoteName, err)
			}
			return nil
		}
	}
	fo := &git.FetchOptions{RemoteName: opts.RemoteName}
	if opts.Auth != nil {
		fo.Auth = authFromForRemoteURL(remoteURL, opts.Auth)
	}
	err := r.repo.Fetch(fo)
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("repo: fetch %s: %w", opts.RemoteName, err)
	}
	return nil
}

// HasRemoteBranch reports whether the given branch exists on the remote (e.g. after Fetch).
// RemoteName is typically "origin".
func (r *Repo) HasRemoteBranch(remoteName, branch string) (bool, error) {
	if remoteName == "" {
		remoteName = "origin"
	}
	ref, err := r.repo.Reference(plumbing.NewRemoteReferenceName(remoteName, branch), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return false, nil
		}
		return false, fmt.Errorf("repo: reference %s/%s: %w", remoteName, branch, err)
	}
	return ref != nil && !ref.Hash().IsZero(), nil
}

// CheckoutBranchFromRemote checks out a remote branch into a local branch of the same name (create local if needed).
// Call Fetch first. If the local branch already exists, it is checked out and updated from remote.
func (r *Repo) CheckoutBranchFromRemote(remoteName, branch string) error {
	if remoteName == "" {
		remoteName = "origin"
	}
	remoteRef := plumbing.NewRemoteReferenceName(remoteName, branch)
	ref, err := r.repo.Reference(remoteRef, true)
	if err != nil {
		return fmt.Errorf("repo: remote ref %s/%s: %w", remoteName, branch, err)
	}
	localRef := plumbing.NewBranchReferenceName(branch)
	// Create or update local branch to point at remote
	if err := r.repo.Storer.SetReference(plumbing.NewHashReference(localRef, ref.Hash())); err != nil {
		return fmt.Errorf("repo: set ref %s: %w", branch, err)
	}
	w, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("repo: worktree: %w", err)
	}
	if err := w.Checkout(&git.CheckoutOptions{Branch: localRef}); err != nil {
		return fmt.Errorf("repo: checkout %s: %w", branch, err)
	}
	return nil
}

// MergeBranch merges the given remote branch (e.g. "origin/main") into the current branch.
// Uses the git CLI for reliable merge (go-git does not support full merge). The ref can be "origin/main" or "main" if a local ref exists.
func (r *Repo) MergeBranch(ctx context.Context, remoteRef string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", r.Path, "merge", "--no-edit", remoteRef)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("repo: merge %s: %w", remoteRef, err)
	}
	return nil
}

// Add stages paths (files or dirs). Use "." for all.
func (r *Repo) Add(paths ...string) error {
	w, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("repo: worktree: %w", err)
	}
	for _, p := range paths {
		if _, err := w.Add(p); err != nil {
			return fmt.Errorf("repo: add %q: %w", p, err)
		}
	}
	return nil
}

// Commit creates a commit with the staged changes. Author and Committer are set to a default if nil.
func (r *Repo) Commit(msg string, author *object.Signature) error {
	w, err := r.repo.Worktree()
	if err != nil {
		return fmt.Errorf("repo: worktree: %w", err)
	}
	now := time.Now()
	if author == nil {
		author = &object.Signature{Name: "QualityBot", Email: "qualitybot@local", When: now}
	} else if author.When.IsZero() {
		c := *author
		c.When = now
		author = &c
	}
	_, err = w.Commit(msg, &git.CommitOptions{Author: author})
	if err != nil {
		return fmt.Errorf("repo: commit: %w", err)
	}
	return nil
}

// Push pushes the current branch (or opts.Branch if set) to the remote.
func (r *Repo) Push(ctx context.Context, opts PushOptions) error {
	if opts.RemoteName == "" {
		opts.RemoteName = "origin"
	}
	branch := opts.Branch
	if branch == "" {
		ref, err := r.repo.Head()
		if err != nil {
			return fmt.Errorf("repo: head: %w", err)
		}
		branch = ref.Name().Short()
	}
	// Effective remote URL: an explicit override (e.g. SSH→HTTPS for token auth) wins over the
	// configured remote URL, and drives both Azure detection and the auth shape below.
	remoteURL := r.remoteURL(opts.RemoteName)
	if opts.RemoteURL != "" {
		remoteURL = opts.RemoteURL
	}
	if IsAzureDevOpsHTTPSURL(remoteURL) {
		if pat, ok := patFromTokenAuth(opts.Auth); ok {
			if err := pushAzureGitCLI(ctx, r.Path, opts.RemoteName, branch, pat); err != nil {
				return fmt.Errorf("repo: push: %w", err)
			}
			return nil
		}
	}
	refspec := config.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)
	po := &git.PushOptions{
		RemoteName: opts.RemoteName,
		RefSpecs:   []config.RefSpec{refspec},
		RemoteURL:  opts.RemoteURL,
	}
	if opts.Auth != nil {
		po.Auth = authFromForRemoteURL(remoteURL, opts.Auth)
	}
	err := r.repo.Push(po)
	if err != nil {
		return fmt.Errorf("repo: push: %w", err)
	}
	return nil
}

// AbsPath returns the absolute path for a relative path under the repo.
func (r *Repo) AbsPath(rel string) string {
	return filepath.Join(r.Path, filepath.FromSlash(rel))
}

// WriteFile writes content to a file under the repo, creating parent dirs.
func (r *Repo) WriteFile(relPath string, content []byte) error {
	full := r.AbsPath(relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0644)
}
