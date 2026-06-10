package config

import (
	"strings"

	"github.com/asqs/asqs-core/internal/vcs"
	"github.com/asqs/asqs-core/internal/vcs/azuredevops"
	"github.com/asqs/asqs-core/internal/vcs/bitbucket"
	"github.com/asqs/asqs-core/internal/vcs/github"
	"github.com/asqs/asqs-core/internal/vcs/gitlab"
)

func normalizeVCSProvider(p string) string {
	s := strings.ToLower(strings.TrimSpace(p))
	if s == "" {
		return vcs.ProviderGitHub
	}
	return s
}

// ActiveVCSToken returns the API token for the configured VCS provider.
func (c *Config) ActiveVCSToken() string {
	switch normalizeVCSProvider(c.VCS.Provider) {
	case vcs.ProviderGitLab:
		return strings.TrimSpace(c.VCS.GitLab.Token)
	case vcs.ProviderBitbucket:
		return strings.TrimSpace(c.VCS.Bitbucket.Token)
	case vcs.ProviderAzureDevOps:
		return strings.TrimSpace(c.VCS.AzureDevOps.Token)
	default:
		return strings.TrimSpace(c.VCS.GitHub.Token)
	}
}

// cloneURLIsAzureDevOpsHTTPS reports whether raw is an Azure DevOps Repos HTTPS URL (duplicates repo.IsAzureDevOpsHTTPSURL to avoid config→repo import).
func cloneURLIsAzureDevOpsHTTPS(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	return strings.Contains(u, "dev.azure.com") || strings.Contains(u, "visualstudio.com")
}

// CloneURLIsAzureDevOpsHTTPS reports whether raw is an Azure DevOps Repos HTTPS URL (exported for ship/REST routing).
func CloneURLIsAzureDevOpsHTTPS(raw string) bool {
	return cloneURLIsAzureDevOpsHTTPS(raw)
}

// CloneAuthTokenForURL returns the PAT/token to use for HTTPS git to this remote.
// When remoteURL is Azure DevOps and vcs.azure_devops.token is set (YAML or ASQS_AZURE_DEVOPS_TOKEN after MergeEnvFromOS),
// that value is used even if vcs.provider is github — so API/scheduled runs can clone Azure project repos without switching the whole config provider.
func (c *Config) CloneAuthTokenForURL(remoteURL string) string {
	if c == nil {
		return ""
	}
	if cloneURLIsAzureDevOpsHTTPS(remoteURL) {
		if t := strings.TrimSpace(c.VCS.AzureDevOps.Token); t != "" {
			return t
		}
	}
	return c.ActiveVCSToken()
}

// ActiveWebhookListenAddress returns the webhook HTTP listen address for the active provider, or empty if disabled.
func (c *Config) ActiveWebhookListenAddress() string {
	switch normalizeVCSProvider(c.VCS.Provider) {
	case vcs.ProviderGitLab:
		return strings.TrimSpace(c.VCS.GitLab.Webhook.ListenAddress)
	case vcs.ProviderBitbucket:
		return strings.TrimSpace(c.VCS.Bitbucket.Webhook.ListenAddress)
	case vcs.ProviderAzureDevOps:
		return strings.TrimSpace(c.VCS.AzureDevOps.Webhook.ListenAddress)
	default:
		return strings.TrimSpace(c.VCS.GitHub.Webhook.ListenAddress)
	}
}

// ActiveWebhookSecret returns the shared secret for webhook verification (provider-specific header).
func (c *Config) ActiveWebhookSecret() string {
	switch normalizeVCSProvider(c.VCS.Provider) {
	case vcs.ProviderGitLab:
		return strings.TrimSpace(c.VCS.GitLab.Webhook.Secret)
	case vcs.ProviderBitbucket:
		return strings.TrimSpace(c.VCS.Bitbucket.Webhook.Secret)
	case vcs.ProviderAzureDevOps:
		return strings.TrimSpace(c.VCS.AzureDevOps.Webhook.Secret)
	default:
		return strings.TrimSpace(c.VCS.GitHub.Webhook.Secret)
	}
}

// ActiveGating returns gating rules for the active provider.
func (c *Config) ActiveGating() GatingConfig {
	switch normalizeVCSProvider(c.VCS.Provider) {
	case vcs.ProviderGitLab:
		return c.VCS.GitLab.Gating
	case vcs.ProviderBitbucket:
		return c.VCS.Bitbucket.Gating
	case vcs.ProviderAzureDevOps:
		return c.VCS.AzureDevOps.Gating
	default:
		return c.VCS.GitHub.Gating
	}
}

// ActiveShip returns ship settings for the active provider.
func (c *Config) ActiveShip() ShipConfig {
	switch normalizeVCSProvider(c.VCS.Provider) {
	case vcs.ProviderGitLab:
		return c.VCS.GitLab.Ship
	case vcs.ProviderBitbucket:
		return c.VCS.Bitbucket.Ship
	case vcs.ProviderAzureDevOps:
		return c.VCS.AzureDevOps.Ship
	default:
		return c.VCS.GitHub.Ship
	}
}

// ActiveDefaultOwnerRepo returns default owner and repository slug for ship/API when not inferred from the clone URL.
func (c *Config) ActiveDefaultOwnerRepo() (owner, repo string) {
	switch normalizeVCSProvider(c.VCS.Provider) {
	case vcs.ProviderGitLab:
		return strings.TrimSpace(c.VCS.GitLab.DefaultNamespace), strings.TrimSpace(c.VCS.GitLab.DefaultProject)
	case vcs.ProviderBitbucket:
		return strings.TrimSpace(c.VCS.Bitbucket.DefaultWorkspace), strings.TrimSpace(c.VCS.Bitbucket.DefaultRepo)
	case vcs.ProviderAzureDevOps:
		// Repo slug only; org/project live in AzureDevOpsVCSConfig for the client.
		return strings.TrimSpace(c.VCS.AzureDevOps.Organization), strings.TrimSpace(c.VCS.AzureDevOps.Repository)
	default:
		return strings.TrimSpace(c.VCS.GitHub.DefaultOwner), strings.TrimSpace(c.VCS.GitHub.DefaultRepo)
	}
}

// ParseRepoFromCloneURL extracts owner and repo slug from a clone URL for the active provider.
// Azure DevOps HTTPS and SSH (v3) URLs are recognized by host so ship/parse work even when vcs.provider is still github.
func (c *Config) ParseRepoFromCloneURL(cloneURL string) (owner, repo string, err error) {
	lu := strings.ToLower(strings.TrimSpace(cloneURL))
	if strings.Contains(lu, "dev.azure.com") || strings.Contains(lu, "visualstudio.com") {
		org, _, rep, err := azuredevops.ParseRepoFromRemote(cloneURL)
		if err == nil {
			return org, rep, nil
		}
	}
	switch normalizeVCSProvider(c.VCS.Provider) {
	case vcs.ProviderGitLab:
		return gitlab.ParseRepoURL(cloneURL)
	case vcs.ProviderBitbucket:
		return bitbucket.ParseRepoURL(cloneURL)
	case vcs.ProviderAzureDevOps:
		org, _, rep, err := azuredevops.ParseRepoURL(cloneURL)
		if err != nil {
			return "", "", err
		}
		return org, rep, nil
	default:
		return github.ParseRepoURL(cloneURL)
	}
}
