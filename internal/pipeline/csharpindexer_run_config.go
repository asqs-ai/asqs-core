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
			Image:  strings.TrimSpace(cfg.Indexer.DockerDotNetIndexerImage),
			CLI:    strings.TrimSpace(cfg.Indexer.DockerCLI),
			Memory: strings.TrimSpace(cfg.Indexer.DockerMemory),
			// FROZEN (CP37): Network and CPUs are deliberately unset. All three indexers resolve an
			// empty network to "none", so the isolation posture is the constant — verified before
			// freezing, since the struct comment claimed "default none" while the code passed the
			// empty string straight through, which reads like a bug and is not one. CPUs never
			// needed tuning independently of Memory.
		}
	}
	return out
}
