package gitlab

import (
	"fmt"
	"strings"
)

// ParseRepoURL extracts namespace and project name from common GitLab clone URLs.
func ParseRepoURL(raw string) (namespace, project string, err error) {
	u := strings.TrimSpace(raw)
	u = strings.TrimSuffix(u, ".git")
	for _, prefix := range []string{"https://", "http://", "git@", "ssh://git@"} {
		if strings.HasPrefix(strings.ToLower(u), strings.ToLower(prefix)) {
			u = u[len(prefix):]
			break
		}
	}
	// git@host:group/repo.git style
	if i := strings.Index(u, ":"); i > 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:]
	}
	// strip host
	lower := strings.ToLower(u)
	const marker = "gitlab.com/"
	if idx := strings.Index(lower, marker); idx >= 0 {
		u = u[idx+len(marker):]
	} else {
		// self-hosted: first path after host
		if slash := strings.Index(u, "/"); slash >= 0 {
			u = u[slash+1:]
		}
	}
	u = strings.Trim(u, "/")
	if u == "" {
		return "", "", fmt.Errorf("gitlab: could not parse repo URL %q", raw)
	}
	parts := strings.Split(u, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("gitlab: expected namespace/project in URL %q", raw)
	}
	project = parts[len(parts)-1]
	namespace = strings.Join(parts[:len(parts)-1], "/")
	return namespace, project, nil
}
