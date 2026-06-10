package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// WebhookConfig configures the PR webhook listener.
type WebhookConfig struct {
	Secret string // GitHub webhook secret for X-Hub-Signature-256 verification; empty = skip verify
}

// PRContext is the extracted context from a PR webhook for gating and workflow.
type PRContext struct {
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

// ParsePRWebhook parses a pull_request webhook payload into PRContext.
// Returns error if payload is invalid or action is not handled (opened, synchronize).
func ParsePRWebhook(body []byte) (*PRContext, string, error) {
	var payload PRWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, "", fmt.Errorf("webhook: parse payload: %w", err)
	}
	if payload.PullRequest == nil || payload.Repository == nil {
		return nil, "", fmt.Errorf("webhook: missing pull_request or repository")
	}
	action := strings.ToLower(payload.Action)
	if action != "opened" && action != "synchronize" {
		return nil, action, nil // not an error; caller may ignore
	}
	pr := payload.PullRequest
	repo := payload.Repository
	owner, repoName := splitFullName(repo.FullName)
	if owner == "" || repoName == "" {
		return nil, "", fmt.Errorf("webhook: invalid repository full_name %q", repo.FullName)
	}
	baseRef := ""
	if pr.Base != nil {
		baseRef = pr.Base.Ref
	}
	headRef := ""
	headSHA := ""
	if pr.Head != nil {
		headRef = pr.Head.Ref
		headSHA = pr.Head.SHA
	}
	cloneURL := repo.CloneURL
	if cloneURL == "" {
		cloneURL = fmt.Sprintf("https://github.com/%s/%s.git", owner, repoName)
	}
	return &PRContext{
		Owner:      owner,
		Repo:       repoName,
		PRNumber:   pr.Number,
		BaseRef:    baseRef,
		HeadRef:    headRef,
		HeadSHA:    headSHA,
		Draft:      pr.Draft,
		CloneURL:   cloneURL,
		RepoSizeKB: repo.Size,
	}, action, nil
}

func splitFullName(full string) (owner, repo string) {
	i := strings.Index(full, "/")
	if i <= 0 || i >= len(full)-1 {
		return "", ""
	}
	return full[:i], full[i+1:]
}

// VerifyWebhookSignature verifies X-Hub-Signature-256 (HMAC-SHA256) with the given secret.
// If secret is empty, returns true (skip verification). Body must be the raw request body.
func VerifyWebhookSignature(secret string, body []byte, signature string) bool {
	if secret == "" {
		return true
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	expected, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), expected)
}
