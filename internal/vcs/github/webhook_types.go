package github

// PRWebhookPayload is the minimal pull_request webhook payload we need (action, PR fields).
// GitHub sends full payload; we decode only what we use.
type PRWebhookPayload struct {
	Action      string       `json:"action"`
	Number      int          `json:"number"`
	PullRequest *PRPayload   `json:"pull_request"`
	Repository  *RepoPayload `json:"repository"`
}

// PRPayload is the pull_request object in the webhook.
type PRPayload struct {
	Number  int         `json:"number"`
	Draft   bool        `json:"draft"`
	Base    *RefPayload `json:"base"`
	Head    *RefPayload `json:"head"`
	HTMLURL string      `json:"html_url"`
	Title   string      `json:"title"`
}

// RefPayload is base/head branch ref.
type RefPayload struct {
	Ref  string       `json:"ref"` // e.g. "main", "feature/foo"
	SHA  string       `json:"sha"`
	Repo *RepoPayload `json:"repo"`
}

// RepoPayload is repository info in the webhook.
type RepoPayload struct {
	FullName string `json:"full_name"` // "owner/repo"
	CloneURL string `json:"clone_url"`
	Private  bool   `json:"private"`
	Size     int    `json:"size"` // GitHub's repo size (KB); 0 if not set
}
