package azuredevops

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/vcs"
)

// Webhook implements vcs.WebhookProvider for Azure DevOps git pull request service hook payloads.
type Webhook struct {
	Secret string // optional shared secret (custom header) when configured
}

type adoResource struct {
	PullRequestID         int           `json:"pullRequestId"`
	SourceRefName         string        `json:"sourceRefName"`
	TargetRefName         string        `json:"targetRefName"`
	Repository            adoRepository `json:"repository"`
	LastMergeSourceCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
}

type adoRepository struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Project struct {
		Name string `json:"name"`
	} `json:"project"`
}

type adoHookBody struct {
	EventType string      `json:"eventType"`
	Resource  adoResource `json:"resource"`
}

func branchFromRef(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "refs/heads/")
	return ref
}

// VerifyAndParse implements vcs.WebhookProvider.
func (h *Webhook) VerifyAndParse(r *vcs.HTTPRequest) (*vcs.PRContext, string, error) {
	if strings.TrimSpace(h.Secret) != "" {
		if r.Headers["X-ASQS-Webhook-Secret"] != h.Secret {
			return nil, "", fmt.Errorf("webhook: invalid secret")
		}
	}
	var body adoHookBody
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return nil, "", fmt.Errorf("webhook: parse: %w", err)
	}
	if !strings.Contains(strings.ToLower(body.EventType), "pullrequest") {
		return nil, "", nil
	}
	res := body.Resource
	if res.PullRequestID == 0 || res.Repository.Name == "" {
		return nil, "", nil
	}
	org := ""
	if parts := strings.Split(strings.TrimPrefix(res.Repository.URL, "https://"), "/"); len(parts) > 0 {
		if strings.Contains(parts[0], "dev.azure.com") && len(parts) > 1 {
			org = parts[1]
		}
	}
	head := branchFromRef(res.SourceRefName)
	base := branchFromRef(res.TargetRefName)
	return &vcs.PRContext{
		Provider: vcs.ProviderAzureDevOps,
		Owner:    org,
		Repo:     res.Repository.Name,
		PRNumber: res.PullRequestID,
		BaseRef:  base,
		HeadRef:  head,
		HeadSHA:  res.LastMergeSourceCommit.CommitID,
		CloneURL: res.Repository.URL,
	}, body.EventType, nil
}
