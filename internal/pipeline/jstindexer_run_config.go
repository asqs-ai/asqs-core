package pipeline

import (
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	jstindexer "github.com/asqs/asqs-core/tools/js-ts-indexer"
)

// jstindexerRunConfig maps indexer YAML/env to jstindexer.RunIndexerConfig when the JS/TS indexer runs.
func jstindexerRunConfig(cfg *config.Config, timeout time.Duration) jstindexer.RunIndexerConfig {
	out := jstindexer.RunIndexerConfig{
		Timeout:          timeout,
		SkipPathPrefixes: nil,
		JsonlOutSpec:     "",
	}
	if cfg == nil {
		return out
	}
	out.SkipPathPrefixes = cfg.Indexer.SkipPathPrefixes
	out.JsonlOutSpec = cfg.Indexer.JSTJsonlOut
	if strings.EqualFold(strings.TrimSpace(cfg.Indexer.Execution), "docker") {
		out.Docker = &jstindexer.NodeDockerConfig{
			Image:   strings.TrimSpace(cfg.Indexer.DockerNodeImage),
			CLI:     strings.TrimSpace(cfg.Indexer.DockerCLI),
			Memory:  strings.TrimSpace(cfg.Indexer.DockerMemory),
			CPUs:    cfg.Indexer.DockerCPUs,
			Network: strings.TrimSpace(cfg.Indexer.DockerNetwork),
			HeapMB:  cfg.Indexer.DockerNodeHeapMB,
		}
	}
	return out
}
