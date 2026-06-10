package repo

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// patFromTokenAuth returns the PAT string when auth is a non-empty *TokenAuth.
func patFromTokenAuth(auth interface{}) (string, bool) {
	if auth == nil {
		return "", false
	}
	t, ok := auth.(*TokenAuth)
	if !ok || t == nil || strings.TrimSpace(t.Token) == "" {
		return "", false
	}
	return strings.TrimSpace(t.Token), true
}

// azureGitAuthorizationArgs returns git -c arguments that set the Authorization header
// Azure DevOps expects for HTTPS Git (Basic with empty username and PAT as password).
// See https://learn.microsoft.com/en-us/azure/devops/organizations/accounts/use-personal-access-tokens-to-authenticate
func azureGitAuthorizationArgs(pat string) []string {
	enc := base64.StdEncoding.EncodeToString([]byte(":" + pat))
	return []string{"-c", fmt.Sprintf("http.extraHeader=Authorization: Basic %s", enc)}
}

func cloneAzureGitCLI(ctx context.Context, absRepoDir string, opts CloneOptions, pat string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("azure devops git clone requires git in PATH: %w", err)
	}
	parent := filepath.Dir(absRepoDir)
	base := filepath.Base(absRepoDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("mkdir clone parent: %w", err)
	}
	args := append(azureGitAuthorizationArgs(pat), "clone")
	if opts.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(opts.Depth))
	}
	if strings.TrimSpace(opts.Branch) != "" {
		args = append(args, "-b", strings.TrimSpace(opts.Branch))
	}
	if opts.FetchAllRefs {
		args = append(args, "--no-single-branch")
	}
	args = append(args, opts.URL, base)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = parent
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func fetchAzureGitCLI(ctx context.Context, repoDir, remoteName, pat string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("azure devops git fetch requires git in PATH: %w", err)
	}
	if remoteName == "" {
		remoteName = "origin"
	}
	args := append(azureGitAuthorizationArgs(pat), "-C", repoDir, "fetch", remoteName)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func pushAzureGitCLI(ctx context.Context, repoDir, remoteName, branch, pat string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("azure devops git push requires git in PATH: %w", err)
	}
	if remoteName == "" {
		remoteName = "origin"
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("push branch required")
	}
	ref := fmt.Sprintf("refs/heads/%[1]s:refs/heads/%[1]s", branch)
	args := append(azureGitAuthorizationArgs(pat), "-C", repoDir, "push", remoteName, ref)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
