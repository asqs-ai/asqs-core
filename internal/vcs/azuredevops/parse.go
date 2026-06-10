package azuredevops

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseRepoURL extracts organization, project, and repository name from Azure DevOps HTTPS URLs.
// For SSH URLs (git@ssh.dev.azure.com:v3/...), use ParseRepoFromRemote instead.
func ParseRepoURL(raw string) (organization, project, repository string, err error) {
	return parseHTTPSAzure(raw)
}

// ParseRepoFromRemote extracts organization, project, and repository from an Azure DevOps
// HTTPS clone URL or an SSH URL of the form git@{ssh.dev.azure.com|vs-ssh.visualstudio.com}:v3/{org}/{project}/{repo}.
func ParseRepoFromRemote(raw string) (organization, project, repository string, err error) {
	if o, p, r, e := parseHTTPSAzure(raw); e == nil {
		return o, p, r, nil
	}
	if o, p, r, e := parseSSHv3Azure(raw); e == nil {
		return o, p, r, nil
	}
	return "", "", "", fmt.Errorf("azuredevops: could not parse %q", strings.TrimSpace(raw))
}

func parseHTTPSAzure(raw string) (organization, project, repository string, err error) {
	u := strings.TrimSpace(raw)
	lu := strings.ToLower(u)
	if !strings.Contains(lu, "dev.azure.com") && !strings.Contains(lu, "visualstudio.com") {
		return "", "", "", fmt.Errorf("azuredevops: not an Azure DevOps host")
	}
	if !strings.HasPrefix(lu, "http://") && !strings.HasPrefix(lu, "https://") {
		return "", "", "", fmt.Errorf("azuredevops: not an http(s) URL")
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", "", "", err
	}
	path := strings.Trim(parsed.Path, "/")
	parts := strings.Split(path, "/")
	for i := range parts {
		if dec, err := url.PathUnescape(parts[i]); err == nil {
			parts[i] = dec
		}
	}
	// dev.azure.com/org/project/_git/repo
	if len(parts) >= 4 && strings.EqualFold(parts[2], "_git") {
		rep := strings.TrimSuffix(parts[3], ".git")
		return parts[0], parts[1], rep, nil
	}
	// visualstudio.com/org/project/_git/repo
	if strings.Contains(strings.ToLower(parsed.Host), "visualstudio.com") && len(parts) >= 4 && strings.EqualFold(parts[2], "_git") {
		rep := strings.TrimSuffix(parts[3], ".git")
		return parts[0], parts[1], rep, nil
	}
	return "", "", "", fmt.Errorf("azuredevops: could not parse https %q", raw)
}

// parseSSHv3Azure parses git@ssh.dev.azure.com:v3/org/project/repo and git@vs-ssh.visualstudio.com:v3/...
func parseSSHv3Azure(raw string) (organization, project, repository string, err error) {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "git@") {
		return "", "", "", fmt.Errorf("azuredevops: not ssh git URL")
	}
	at := strings.IndexByte(s, '@')
	colon := strings.IndexByte(s[at+1:], ':')
	if colon < 0 {
		return "", "", "", fmt.Errorf("azuredevops: ssh: missing host:path")
	}
	hostStart := at + 1
	host := strings.ToLower(s[hostStart : hostStart+colon])
	if !strings.Contains(host, "dev.azure.com") && !strings.Contains(host, "visualstudio.com") {
		return "", "", "", fmt.Errorf("azuredevops: not an Azure DevOps SSH host")
	}
	afterColon := s[hostStart+colon+1:]
	if len(afterColon) < 4 || !strings.EqualFold(afterColon[:3], "v3/") {
		return "", "", "", fmt.Errorf("azuredevops: ssh: expected v3/ path after host")
	}
	path := strings.Trim(afterColon[3:], "/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("azuredevops: ssh: path must be org/project/repo")
	}
	for i := range parts {
		if dec, err := url.PathUnescape(parts[i]); err == nil {
			parts[i] = dec
		}
	}
	repo := strings.TrimSuffix(parts[2], ".git")
	return parts[0], parts[1], repo, nil
}
