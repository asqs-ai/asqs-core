package config

// GitLabVCSConfig configures GitLab (Cloud or self-managed) API access and ship.
type GitLabVCSConfig struct {
	// Token is a personal, project, or group access token with api scope.
	Token string `yaml:"token" env:"GITLAB_TOKEN"`
	// BaseURL is the GitLab API root, e.g. https://gitlab.com/api/v4 (default) or https://gitlab.example.com/api/v4.
	BaseURL string `yaml:"base_url" env:"GITLAB_BASE_URL"`
	// DefaultNamespace is the group or username (first segment of namespace/project path).
	DefaultNamespace string `yaml:"default_namespace" env:"GITLAB_DEFAULT_NAMESPACE"`
	// DefaultProject is the project name (second segment of path under namespace).
	DefaultProject string `yaml:"default_project" env:"GITLAB_DEFAULT_PROJECT"`

	Ship ShipConfig `yaml:"ship"`
}

// BitbucketVCSConfig configures Bitbucket Cloud or Server API access.
type BitbucketVCSConfig struct {
	Token string `yaml:"token" env:"BITBUCKET_TOKEN"`
	// BaseURL for Bitbucket Server, e.g. https://bitbucket.company.com/rest/api/1.0 — empty = Bitbucket Cloud (api.bitbucket.org/2.0).
	BaseURL string `yaml:"base_url" env:"BITBUCKET_BASE_URL"`
	// DefaultWorkspace is the Bitbucket workspace (Cloud) or project key (Server) depending on host.
	DefaultWorkspace string `yaml:"default_workspace" env:"BITBUCKET_DEFAULT_WORKSPACE"`
	// DefaultRepo is the repository slug.
	DefaultRepo string `yaml:"default_repo" env:"BITBUCKET_DEFAULT_REPO"`

	Ship ShipConfig `yaml:"ship"`
}

// AzureDevOpsVCSConfig configures Azure DevOps (Repos) REST API.
type AzureDevOpsVCSConfig struct {
	// Token is a PAT with Code (read/write) and optionally Pull Request scopes.
	Token string `yaml:"token" env:"AZURE_DEVOPS_TOKEN"`
	// BaseURL is the organization collection root, e.g. https://dev.azure.com/myorg — empty uses Organization to build URL.
	BaseURL string `yaml:"base_url" env:"AZURE_DEVOPS_BASE_URL"`
	// Organization (Azure DevOps org name).
	Organization string `yaml:"organization" env:"AZURE_DEVOPS_ORGANIZATION"`
	// Project is the team project name.
	Project string `yaml:"project" env:"AZURE_DEVOPS_PROJECT"`
	// Repository is the git repo name within the project.
	Repository string `yaml:"repository" env:"AZURE_DEVOPS_REPOSITORY"`

	Ship ShipConfig `yaml:"ship"`
}
