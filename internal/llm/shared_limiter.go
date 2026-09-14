package llm

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// One in-flight cap per LLM ENDPOINT, for the whole process.
//
// BuildStepCompleters builds a limiter and shares it across a run's four step completers, which is
// right as far as it goes. It is called from NewRunnerFromConfig, and the API server calls that
// once per run (workflowinvoker.FactoryInvoker.Run) — so the cap was per run, and N concurrent runs
// multiplied it. With general.llm.max_concurrent: 8 and two runs in flight, sixteen requests could
// be outstanding against one local Ollama serving one model, which is not a cap so much as a
// suggestion.
//
// The cap exists to protect a SERVER, so the key is the server: provider plus base URL. Two runs
// against the same endpoint share one limiter; a run against a different endpoint gets its own.
var (
	sharedLimitersMu sync.Mutex
	sharedLimiters   = map[string]*model.LLMLimiter{}
	sharedLimiterCap = map[string]int{}
)

// sharedLimiterFor returns the process-wide limiter for this endpoint, creating it on first use.
//
// A semaphore's size is fixed once created, so the first configuration to reach an endpoint sets
// it. A later run asking for a different size is reported rather than silently ignored: an operator
// who lowered max_concurrent and saw no change would otherwise have nothing to go on.
func sharedLimiterFor(llm *config.LLMConfig) *model.LLMLimiter {
	key := limiterKey(llm)
	want := model.ResolveLLMMaxConcurrent(llm.MaxConcurrent)

	sharedLimitersMu.Lock()
	defer sharedLimitersMu.Unlock()
	if lim, ok := sharedLimiters[key]; ok {
		if have := sharedLimiterCap[key]; have != want {
			fmt.Fprintf(os.Stderr,
				"  llm: max_concurrent for %s is %d for this process (set by the first run to use it); "+
					"this run asked for %d. Restart to change it.\n", key, have, want)
		}
		return lim
	}
	lim := model.NewLLMLimiter(llm.MaxConcurrent)
	sharedLimiters[key] = lim
	sharedLimiterCap[key] = want
	return lim
}

// limiterKey identifies the server a cap protects. An empty base URL is the provider's own default
// endpoint, which is still one server.
func limiterKey(llm *config.LLMConfig) string {
	provider := strings.ToLower(strings.TrimSpace(llm.Provider))
	base := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(llm.BaseURL), "/"))
	if base == "" {
		base = "(provider default)"
	}
	return provider + " " + base
}
