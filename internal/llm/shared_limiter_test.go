package llm

import (
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// The cap protects a server, so two runs against the same endpoint must share it. It used to be
// built per run — NewRunnerFromConfig calls BuildStepCompleters, and the API server calls that once
// per run — so with max_concurrent 8 and two runs in flight, sixteen requests could be outstanding
// against one local Ollama serving one model.
func TestSharedLimiterFor_runsAgainstOneEndpointShareOneCap(t *testing.T) {
	resetSharedLimiters(t)

	cfg := config.LLMConfig{Provider: "ollama", BaseURL: "http://127.0.0.1:11434", MaxConcurrent: 2}
	first := sharedLimiterFor(&cfg)
	second := sharedLimiterFor(&cfg) // a second run, same endpoint
	if first != second {
		t.Fatal("two runs against the same endpoint got separate limiters; the cap is per run again")
	}
	if first.Cap() != 2 {
		t.Fatalf("cap = %d; want the configured 2", first.Cap())
	}
}

// A different endpoint is a different server and gets its own cap.
func TestSharedLimiterFor_separateEndpointsKeepSeparateCaps(t *testing.T) {
	resetSharedLimiters(t)

	local := config.LLMConfig{Provider: "ollama", BaseURL: "http://127.0.0.1:11434", MaxConcurrent: 2}
	remote := config.LLMConfig{Provider: "openai", MaxConcurrent: 8}
	if sharedLimiterFor(&local) == sharedLimiterFor(&remote) {
		t.Fatal("two different endpoints share one cap")
	}
}

// A semaphore cannot be resized, so the first configuration wins. The point of the test is that the
// second run still gets a working limiter rather than a surprise.
func TestSharedLimiterFor_laterDifferentSizeKeepsTheFirst(t *testing.T) {
	resetSharedLimiters(t)

	first := sharedLimiterFor(&config.LLMConfig{Provider: "ollama", BaseURL: "http://x", MaxConcurrent: 2})
	second := sharedLimiterFor(&config.LLMConfig{Provider: "ollama", BaseURL: "http://x", MaxConcurrent: 9})
	if first != second {
		t.Fatal("a differently-sized request created a second limiter for one endpoint")
	}
	if second.Cap() != 2 {
		t.Fatalf("cap = %d; want the first configuration's 2", second.Cap())
	}
}

func resetSharedLimiters(t *testing.T) {
	t.Helper()
	sharedLimitersMu.Lock()
	defer sharedLimitersMu.Unlock()
	sharedLimiters = map[string]*model.LLMLimiter{}
	sharedLimiterCap = map[string]int{}
}
