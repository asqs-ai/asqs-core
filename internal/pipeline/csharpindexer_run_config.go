package pipeline

import (
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	csharpindexer "github.com/asqs/asqs-core/tools/csharp-indexer"
)

// csharpindexerRunConfig maps indexer YAML/env to csharpindexer.RunConfig.
func csharpindexerRunConfig(cfg *config.Config, timeout time.Duration) csharpindexer.RunConfig {
	out := csharpindexer.RunConfig{Timeout: timeout}
	if cfg == nil {
		return out
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Indexer.Execution), "docker") {
		out.Docker = &csharpindexer.DotnetDockerConfig{
			Image:   strings.TrimSpace(cfg.Indexer.DockerDotNetIndexerImage),
			CLI:     strings.TrimSpace(cfg.Indexer.DockerCLI),
			Memory:  strings.TrimSpace(cfg.Indexer.DockerMemory),
			CPUs:    cfg.Indexer.DockerCPUs,
			Network: strings.TrimSpace(cfg.Indexer.DockerNetwork),
		}
	}
	return out
}
