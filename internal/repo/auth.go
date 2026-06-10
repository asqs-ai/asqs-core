package repo

import (
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

// TokenAuth holds a GitHub (or similar) token for HTTPS clone/push.
// Use with CloneOptions.Auth or PushOptions.Auth.
type TokenAuth struct {
	Token string
}

// IsAzureDevOpsHTTPSURL reports whether raw looks like an Azure DevOps Repos HTTPS URL (cloud or legacy host).
func IsAzureDevOpsHTTPSURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	return strings.Contains(u, "dev.azure.com") || strings.Contains(u, "visualstudio.com")
}

// authFrom converts our Auth (TokenAuth or transport.AuthMethod) to transport.AuthMethod for go-git.
func authFrom(a interface{}) transport.AuthMethod {
	if a == nil {
		return nil
	}
	if t, ok := a.(*TokenAuth); ok && t.Token != "" {
		return &http.BasicAuth{Username: "token", Password: t.Token}
	}
	if m, ok := a.(transport.AuthMethod); ok {
		return m
	}
	return nil
}

// authFromForRemoteURL picks Basic auth shape for the host. Azure DevOps expects an empty Basic username
// with the PAT as password; GitHub and most others accept username "token".
func authFromForRemoteURL(remoteURL string, auth interface{}) transport.AuthMethod {
	if auth == nil {
		return nil
	}
	if t, ok := auth.(*TokenAuth); ok && t.Token != "" && IsAzureDevOpsHTTPSURL(remoteURL) {
		return &http.BasicAuth{Username: "", Password: t.Token}
	}
	return authFrom(auth)
}
