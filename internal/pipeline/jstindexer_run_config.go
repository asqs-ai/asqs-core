package pipeline

import (
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/config"
	jstindexer "github.com/asqs/asqs-core/tools/js-ts-indexer"
)

// jsonlOutTemp routes the JS/TS indexer's JSONL through a temp file rather than stdout.
const jsonlOutTemp = "temp"

// jstindexerRunConfig maps indexer YAML/env to jstindexer.RunIndexerConfig when the JS/TS indexer runs.
func jstindexerRunConfig(cfg *config.Config, timeout time.Duration) jstindexer.RunIndexerConfig {
	out := jstindexer.RunIndexerConfig{
		Timeout:          timeout,
		SkipPathPrefixes: nil,
		// FROZEN (CP37, indexer.jst_jsonl_out): always the temp-file route. Node writes the same
		// JSONL to a file via --jsonl-out and Go reads it after exit, instead of streaming it back
		// over stdout. Docker execution already worked this way; local execution streamed, and a
		// single very large JSONL record could hit pipe limits there. This is the one DELIBERATE
		// behaviour change in the freeze — it is invisible in the config goldens by construction,
		// because the goldens capture configuration and this is a runtime path.
		JsonlOutSpec: jsonlOutTemp,
	}
	if cfg == nil {
		return out
	}
	out.SkipPathPrefixes = cfg.Indexer.SkipPathPrefixes
	if strings.EqualFold(strings.TrimSpace(cfg.Indexer.Execution), "docker") {
		out.Docker = &jstindexer.NodeDockerConfig{
			Image:  strings.TrimSpace(cfg.Indexer.DockerNodeImage),
			CLI:    strings.TrimSpace(cfg.Indexer.DockerCLI),
			Memory: strings.TrimSpace(cfg.Indexer.DockerMemory),
			// FROZEN (CP37): Network and CPUs are deliberately unset. All three indexers resolve an
			// empty network to "none", so the isolation posture is the constant — verified before
			// freezing, since the struct comment claimed "default none" while the code passed the
			// empty string straight through, which reads like a bug and is not one. CPUs never
			// needed tuning independently of Memory.
			HeapMB: cfg.Indexer.DockerNodeHeapMB,
		}
	}
	return out
}
