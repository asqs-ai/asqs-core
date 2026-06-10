package gitlab

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/vcs"
)

// Webhook implements vcs.WebhookProvider for GitLab Merge Request Hook.
type Webhook struct {
	Secret string // compared to X-Gitlab-Token when non-empty
}

type glProject struct {
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	GitSSHURL         string `json:"git_ssh_url"`
}

type glObjectAttrs struct {
	IID            int    `json:"iid"`
	Action         string `json:"action"`
	State          string `json:"state"`
	SourceBranch   string `json:"source_branch"`
	TargetBranch   string `json:"target_branch"`
	WorkInProgress bool   `json:"work_in_progress"`
	LastCommit     struct {
		ID string `json:"id"`
	} `json:"last_commit"`
}

type glWebhookBody struct {
	ObjectKind       string        `json:"object_kind"`
	ObjectAttributes glObjectAttrs `json:"object_attributes"`
	Project          glProject     `json:"project"`
}

// VerifyAndParse implements vcs.WebhookProvider.
func (h *Webhook) VerifyAndParse(r *vcs.HTTPRequest) (*vcs.PRContext, string, error) {
	ev := strings.ToLower(strings.TrimSpace(r.Headers["X-Gitlab-Event"]))
	if !strings.Contains(ev, "merge") {
		return nil, "", nil
	}
	if strings.TrimSpace(h.Secret) != "" {
		if r.Headers["X-Gitlab-Token"] != h.Secret {
			return nil, "", fmt.Errorf("webhook: invalid X-Gitlab-Token")
		}
	}
	var body glWebhookBody
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return nil, "", fmt.Errorf("webhook: parse body: %w", err)
	}
	if body.ObjectKind != "merge_request" {
		return nil, "", nil
	}
	action := strings.ToLower(strings.TrimSpace(body.ObjectAttributes.Action))
	if action != "open" && action != "reopen" && action != "update" && action != "approved" {
		return nil, action, nil
	}
	pwn := strings.Trim(body.Project.PathWithNamespace, "/")
	if pwn == "" {
		return nil, "", fmt.Errorf("webhook: missing project.path_with_namespace")
	}
	lastSlash := strings.LastIndex(pwn, "/")
	if lastSlash < 0 {
		return nil, "", fmt.Errorf("webhook: invalid path_with_namespace %q", pwn)
	}
	ns, proj := pwn[:lastSlash], pwn[lastSlash+1:]
	cloneURL := body.Project.HTTPURLToRepo
	if cloneURL == "" {
		cloneURL = body.Project.GitSSHURL
	}
	return &vcs.PRContext{
		Provider: vcs.ProviderGitLab,
		Owner:    ns,
		Repo:     proj,
		PRNumber: body.ObjectAttributes.IID,
		BaseRef:  body.ObjectAttributes.TargetBranch,
		HeadRef:  body.ObjectAttributes.SourceBranch,
		HeadSHA:  body.ObjectAttributes.LastCommit.ID,
		Draft:    body.ObjectAttributes.WorkInProgress,
		CloneURL: cloneURL,
	}, action, nil
}
