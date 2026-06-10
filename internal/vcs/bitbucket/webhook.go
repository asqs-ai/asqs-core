package bitbucket

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asqs/asqs-core/internal/vcs"
)

// Webhook implements vcs.WebhookProvider for Bitbucket pull request events.
type Webhook struct {
	Secret string // optional; Bitbucket can send X-Hub-Signature-256 when configured
}

type bbRepo struct {
	FullName string `json:"full_name"`
	Links    struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

type bbBranch struct {
	Name string `json:"name"`
}

type bbCommit struct {
	Hash string `json:"hash"`
}

type bbPullRequest struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Source struct {
		Branch bbBranch `json:"branch"`
		Commit bbCommit `json:"commit"`
	} `json:"source"`
	Destination struct{ Branch bbBranch } `json:"destination"`
	Draft       bool                      `json:"draft"`
}

type bbHookBody struct {
	PullRequest bbPullRequest `json:"pullrequest"`
	Repository  bbRepo        `json:"repository"`
}

// VerifyAndParse implements vcs.WebhookProvider.
func (h *Webhook) VerifyAndParse(r *vcs.HTTPRequest) (*vcs.PRContext, string, error) {
	event := r.Headers["X-Event-Key"]
	if !strings.HasPrefix(event, "pullrequest:") {
		return nil, "", nil
	}
	action := strings.TrimPrefix(event, "pullrequest:")
	var body bbHookBody
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return nil, "", fmt.Errorf("webhook: parse: %w", err)
	}
	if action != "created" && action != "updated" && action != "approved" {
		return nil, action, nil
	}
	fn := strings.Trim(body.Repository.FullName, "/")
	if fn == "" {
		return nil, "", fmt.Errorf("webhook: missing repository.full_name")
	}
	idx := strings.Index(fn, "/")
	if idx < 0 {
		return nil, "", fmt.Errorf("webhook: bad full_name %q", fn)
	}
	ws, repo := fn[:idx], fn[idx+1:]
	clone := body.Repository.Links.HTML.Href
	return &vcs.PRContext{
		Provider: vcs.ProviderBitbucket,
		Owner:    ws,
		Repo:     repo,
		PRNumber: body.PullRequest.ID,
		BaseRef:  body.PullRequest.Destination.Branch.Name,
		HeadRef:  body.PullRequest.Source.Branch.Name,
		HeadSHA:  body.PullRequest.Source.Commit.Hash,
		Draft:    body.PullRequest.Draft,
		CloneURL: clone,
	}, action, nil
}
