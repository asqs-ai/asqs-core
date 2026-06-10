package bitbucket

import (
	"fmt"
	"strings"
)

// ParseRepoURL extracts workspace and repo slug from Bitbucket Cloud-style URLs.
func ParseRepoURL(raw string) (workspace, repo string, err error) {
	u := strings.TrimSpace(raw)
	u = strings.TrimSuffix(u, ".git")
	for _, prefix := range []string{"https://", "http://", "git@", "ssh://git@"} {
		if strings.HasPrefix(strings.ToLower(u), strings.ToLower(prefix)) {
			u = u[len(prefix):]
			break
		}
	}
	if i := strings.Index(u, ":"); i > 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:]
	}
	lower := strings.ToLower(u)
	const cloud = "bitbucket.org/"
	if idx := strings.Index(lower, cloud); idx >= 0 {
		u = u[idx+len(cloud):]
	} else {
		if slash := strings.Index(u, "/"); slash >= 0 {
			u = u[slash+1:]
		}
	}
	u = strings.Trim(u, "/")
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("bitbucket: could not parse %q", raw)
	}
	return parts[0], parts[1], nil
}
