package repo

// CloneOptions configures cloning a repository.
type CloneOptions struct {
	// URL is the repository URL (https or git).
	URL string
	// Dir is the local directory to clone into. Must be empty or not exist.
	Dir string
	// Branch to checkout after clone. If empty, uses remote default (e.g. main).
	Branch string
	// Depth limits clone depth (1 = shallow). 0 means full clone.
	Depth int
	// FetchAllRefs: when true and Branch is set, clone fetches all refs (not just Branch) so remote branches like the ship branch exist for later checkout. Default false = single-branch clone.
	FetchAllRefs bool
	// Auth is optional; use for private repos (e.g. GitHub token in URL or Auth).
	Auth interface{} // *http.BasicAuth or ssh.AuthMethod for go-git
}

// PushOptions configures pushing to a remote.
type PushOptions struct {
	// RemoteName is typically "origin".
	RemoteName string
	// Branch is the branch to push (e.g. "qualitybot/add-tests-123").
	Branch string
	// RemoteURL, when non-empty, overrides the named remote's URL for this push. Use it to push to a
	// canonical HTTPS URL even when the origin remote is SSH (git@host:owner/repo), so a token can
	// authenticate. Auth is matched against this URL. Empty = use the configured remote URL.
	RemoteURL string
	// Auth for push (e.g. token-based auth for GitHub).
	Auth interface{}
}

// FetchOptions configures fetching from a remote.
type FetchOptions struct {
	// RemoteName is typically "origin".
	RemoteName string
	// Auth for private repos (e.g. token-based auth for GitHub).
	Auth interface{}
}
