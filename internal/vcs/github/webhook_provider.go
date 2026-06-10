package github

import (
	"fmt"

	"github.com/asqs/asqs-core/internal/vcs"
)

// GitHubWebhook implements vcs.WebhookProvider for GitHub pull_request events.
type GitHubWebhook struct {
	Secret string
}

// VerifyAndParse implements vcs.WebhookProvider. Verifies X-Hub-Signature-256 and parses into vcs.PRContext.
func (h *GitHubWebhook) VerifyAndParse(r *vcs.HTTPRequest) (*vcs.PRContext, string, error) {
	if r.Headers["X-GitHub-Event"] != "pull_request" {
		return nil, "", nil
	}
	sig := r.Headers["X-Hub-Signature-256"]
	if !VerifyWebhookSignature(h.Secret, r.Body, sig) {
		return nil, "", fmt.Errorf("webhook: invalid signature")
	}
	pr, action, err := ParsePRWebhook(r.Body)
	if err != nil {
		return nil, "", err
	}
	if pr == nil {
		return nil, action, nil
	}
	return &vcs.PRContext{
		Provider:   vcs.ProviderGitHub,
		Owner:      pr.Owner,
		Repo:       pr.Repo,
		PRNumber:   pr.PRNumber,
		BaseRef:    pr.BaseRef,
		HeadRef:    pr.HeadRef,
		HeadSHA:    pr.HeadSHA,
		Draft:      pr.Draft,
		CloneURL:   pr.CloneURL,
		RepoSizeKB: pr.RepoSizeKB,
	}, action, nil
}
