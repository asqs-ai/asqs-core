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
			Image:  strings.TrimSpace(cfg.Indexer.DockerJavaImage),
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
