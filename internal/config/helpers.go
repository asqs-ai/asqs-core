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
		MaxConns:   c.poolMaxConns(),
	}
}

// MetadataStoreConfig returns pool configuration for the metadata store.
func (c *Config) MetadataStoreConfig() metadata.Config {
	return metadata.Config{
		ConnString: c.MetadataStoreConnString(),
		MaxConns:   c.poolMaxConns(),
	}
}

// poolMaxConns is the per-pool connection ceiling: database.max_open_conns when set, otherwise 0,
// which leaves pgxpool's own default of max(4, NumCPU). asqs-core's gap loop is sequential, so
// that default is comfortably above what a run holds concurrently; the upstream derivation from
// the gap worker count returns if the loop ever goes concurrent. Note both stores size from this,
// so two pools per process can hold up to twice the configured value.
func (c *Config) poolMaxConns() int32 {
	if c.Database.MaxOpenConns > 0 {
		return int32(c.Database.MaxOpenConns)
	}
	return 0
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
	return metadata.OpenWithConfig(context.Background(), c.MetadataStoreConfig())
}

// OpenEmbeddingsStore opens the embeddings (pgvector) store using this config.
func (c *Config) OpenEmbeddingsStore(ctx context.Context) (*embeddings.Store, error) {
	return embeddings.Open(ctx, c.EmbeddingsStoreConfig())
}
