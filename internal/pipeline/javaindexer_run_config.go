package pipeline

import (
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	javaindexer "github.com/asqs/asqs-core/tools/java-indexer"
)

// javaindexerRunJARConfig maps indexer YAML/env to javaindexer.RunJARConfig (advanced JAR only).
func javaindexerRunJARConfig(cfg *config.Config, timeout time.Duration) javaindexer.RunJARConfig {
	out := javaindexer.RunJARConfig{Timeout: timeout}
	if cfg == nil {
		return out
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Indexer.Execution), "docker") {
		out.Docker = &javaindexer.JavaDockerConfig{
			Image:   strings.TrimSpace(cfg.Indexer.DockerJavaImage),
			CLI:     strings.TrimSpace(cfg.Indexer.DockerCLI),
			Memory:  strings.TrimSpace(cfg.Indexer.DockerMemory),
			CPUs:    cfg.Indexer.DockerCPUs,
			Network: strings.TrimSpace(cfg.Indexer.DockerNetwork),
		}
	}
	return out
}
