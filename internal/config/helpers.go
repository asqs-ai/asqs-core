package config

import (
	"context"
	"strings"

	"github.com/asqs/asqs-core/internal/storage/embeddings"
	"github.com/asqs/asqs-core/internal/storage/metadata"
	"github.com/asqs/asqs-core/internal/vcs/azuredevops"
	"github.com/asqs/asqs-core/internal/vcs/bitbucket"
	"github.com/asqs/asqs-core/internal/vcs/github"
	"github.com/asqs/asqs-core/internal/vcs/gitlab"
)

// MetadataStoreConnString returns the connection string for the metadata store (symbols, edges, files).
func (c *Config) MetadataStoreConnString() string {
	return c.Database.MetadataURL
}

// EmbeddingsStoreConfig returns the config for the embeddings store (chunks + pgvector).
func (c *Config) EmbeddingsStoreConfig() embeddings.Config {
	return embeddings.Config{
		ConnString: c.Database.EmbeddingsURL,
		Dimension:  c.Database.EmbeddingsDimension,
	}
}

// GitHubClient returns a GitHub client using config. Token and default owner/repo come from config.
func (c *Config) GitHubClient() *github.Client {
	if strings.TrimSpace(c.VCS.GitHub.Token) == "" {
		return nil
	}
	return github.NewClient(
		c.VCS.GitHub.Token,
		c.VCS.GitHub.DefaultOwner,
		c.VCS.GitHub.DefaultRepo,
	)
}

// GitLabClient returns a GitLab REST client when token is set.
func (c *Config) GitLabClient() *gitlab.Client {
	if strings.TrimSpace(c.VCS.GitLab.Token) == "" {
		return nil
	}
	return gitlab.NewClient(c.VCS.GitLab.Token, c.VCS.GitLab.BaseURL)
}

// BitbucketClient returns a Bitbucket Cloud/Server API client when token is set.
func (c *Config) BitbucketClient() *bitbucket.Client {
	if strings.TrimSpace(c.VCS.Bitbucket.Token) == "" {
		return nil
	}
	return bitbucket.NewClient(c.VCS.Bitbucket.Token, c.VCS.Bitbucket.BaseURL)
}

// AzureDevOpsClient returns an Azure DevOps Git client when token and required fields are set.
func (c *Config) AzureDevOpsClient() *azuredevops.Client {
	if strings.TrimSpace(c.VCS.AzureDevOps.Token) == "" {
		return nil
	}
	return azuredevops.NewClient(
		c.VCS.AzureDevOps.Token,
		strings.TrimSpace(c.VCS.AzureDevOps.BaseURL),
		c.VCS.AzureDevOps.Organization,
		c.VCS.AzureDevOps.Project,
		c.VCS.AzureDevOps.Repository,
	)
}

// OpenMetadataStore opens the metadata Postgres store using this config.
func (c *Config) OpenMetadataStore() (*metadata.Store, error) {
	return metadata.Open(c.MetadataStoreConnString())
}

// OpenEmbeddingsStore opens the embeddings (pgvector) store using this config.
func (c *Config) OpenEmbeddingsStore(ctx context.Context) (*embeddings.Store, error) {
	return embeddings.Open(ctx, c.EmbeddingsStoreConfig())
}
