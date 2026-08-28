// Package llm provides a configurable, extensible LLM integration. It builds
// model.ChatCompleter and model.Embedder from config (OpenAI today; Anthropic, Google later).
package llm

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/llm/anthropic"
	llembed "github.com/asqs/asqs-core/internal/llm/embeddings"
	"github.com/asqs/asqs-core/internal/llm/ollama"
	"github.com/asqs/asqs-core/internal/llm/openai"
	"github.com/asqs/asqs-core/internal/storage/embeddings"
)

// StepDoc is the config step name for doc generation.
const StepDoc = "doc"

// StepGeneration is the config step name for test and overview generation.
const StepGeneration = "generation"

// StepFixer is the config step name for the LLM fixer.
const StepFixer = "fixer"

// stepConfig holds resolved provider, model, and API key for a step.
type stepConfig struct {
	Provider string
	Model    string
	APIKey   string
}

func resolveStepConfig(cfg *config.Config, step string) stepConfig {
	getKey := func(stepKey, stepKeyFromEnv string) string {
		if stepKeyFromEnv != "" {
			if k := os.Getenv(stepKeyFromEnv); k != "" {
				return k
			}
		}
		if stepKey != "" {
			return stepKey
		}
		if cfg.LLM.APIKeyFromEnv != "" {
			return os.Getenv(cfg.LLM.APIKeyFromEnv)
		}
		return cfg.LLM.APIKey
	}
	out := stepConfig{
		Provider: strings.ToLower(strings.TrimSpace(cfg.LLM.Provider)),
		Model:    strings.TrimSpace(cfg.LLM.Model),
		APIKey:   getKey(cfg.LLM.APIKey, cfg.LLM.APIKeyFromEnv),
	}
	switch step {
	case StepDoc:
		if p := strings.TrimSpace(cfg.LLM.DocProvider); p != "" {
			out.Provider = strings.ToLower(p)
		}
		if m := strings.TrimSpace(cfg.LLM.DocModel); m != "" {
			out.Model = m
		}
		out.APIKey = getKey(cfg.LLM.DocAPIKey, cfg.LLM.DocAPIKeyFromEnv)
	case StepGeneration:
		if p := strings.TrimSpace(cfg.LLM.GenerationProvider); p != "" {
			out.Provider = strings.ToLower(p)
		}
		if m := strings.TrimSpace(cfg.LLM.GenerationModel); m != "" {
			out.Model = m
		}
		out.APIKey = getKey(cfg.LLM.GenerationAPIKey, cfg.LLM.GenerationAPIKeyFromEnv)
	case StepFixer:
		if p := strings.TrimSpace(cfg.LLM.FixerProvider); p != "" {
			out.Provider = strings.ToLower(p)
		}
		if m := strings.TrimSpace(cfg.LLM.FixerModel); m != "" {
			out.Model = m
		}
		out.APIKey = getKey(cfg.LLM.FixerAPIKey, cfg.LLM.FixerAPIKeyFromEnv)
	}
	return out
}

// NewChatCompleterWithModel returns a ChatCompleter for the configured default provider using the given model ID. If modelID is empty, cfg.LLM.Model is used. Use when only the model differs (same provider/key).
func NewChatCompleterWithModel(cfg *config.Config, modelID string) (model.ChatCompleter, error) {
	p := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	if p == "" {
		return nil, nil
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = cfg.LLM.Model
	}
	switch p {
	case "openai", "azure_openai":
		return openai.NewClientWithKeyAndModel(cfg, "", modelID)
	case "anthropic":
		return anthropic.NewClientWithKeyAndModel(cfg, "", modelID)
	case "ollama":
		return ollama.NewClientWithKeyAndModel(cfg, "", modelID)
	default:
		return nil, fmt.Errorf("llm: unsupported provider %q (supported: openai, azure_openai, anthropic, ollama)", p)
	}
}

// NewChatCompleterForStep returns a ChatCompleter for the given step ("doc", "generation", "fixer", or "" for default). Each step can use a different provider and API key (e.g. openai for generation, anthropic for docs and fixer). Step "" uses the default provider and model.
func NewChatCompleterForStep(cfg *config.Config, step string) (model.ChatCompleter, error) {
	sc := resolveStepConfig(cfg, step)
	if sc.Provider == "" {
		return nil, nil
	}
	modelID := sc.Model
	switch sc.Provider {
	case "openai", "azure_openai":
		client, err := openai.NewClientWithKeyAndModel(cfg, sc.APIKey, modelID)
		if err != nil {
			return nil, err
		}
		return client, nil
	case "anthropic":
		client, err := anthropic.NewClientWithKeyAndModel(cfg, sc.APIKey, modelID)
		if err != nil {
			return nil, err
		}
		return client, nil
	case "ollama":
		client, err := ollama.NewClientWithKeyAndModel(cfg, sc.APIKey, modelID)
		if err != nil {
			return nil, err
		}
		return client, nil
	default:
		return nil, fmt.Errorf("llm: unsupported provider %q for step %q (supported: openai, azure_openai, anthropic, ollama)", sc.Provider, step)
	}
}

// EffectiveProviderForStep reports the lowercased provider name a step resolves to after the
// per-step override / default-provider fallback — the same resolution NewChatCompleterForStep
// applies. Exported so orchestration can make provider-aware decisions (notably: whether the fixer's
// provider enforces a JSON schema as a grammar) without re-deriving the fallback chain.
func EffectiveProviderForStep(cfg *config.Config, step string) string {
	if cfg == nil {
		return ""
	}
	return resolveStepConfig(cfg, step).Provider
}

// NewChatCompleter returns a ChatCompleter for the configured default provider and model. Returns (nil, nil) when cfg.LLM.Provider is empty.
func NewChatCompleter(cfg *config.Config) (model.ChatCompleter, error) {
	return NewChatCompleterForStep(cfg, "")
}

// BuildStepCompleters builds the per-step chat completers (base/default, doc, generation, fixer)
// and wraps every one with a SINGLE shared concurrency limiter sized by cfg.LLM.MaxConcurrent
// (default model.DefaultLLMMaxConcurrent). Sharing one limiter across the steps yields a global
// in-flight LLM-request cap, so parallelizing the test/doc/overview/fixer workstreams can never
// exceed the provider's safe concurrency. Each step falls back to the base completer when its
// provider is unset (matching prior call-site behaviour); per-step construction errors are
// swallowed (base is used). err is non-nil only when the base/default completer failed to build
// (callers may log it; generation is then skipped). Returns all-nil when cfg.LLM.Provider is empty.
func BuildStepCompleters(cfg *config.Config) (base, doc, gen, fixer model.ChatCompleter, lim *model.LLMLimiter, err error) {
	if cfg == nil || strings.TrimSpace(cfg.LLM.Provider) == "" {
		return nil, nil, nil, nil, nil, nil
	}
	lim = model.NewLLMLimiter(cfg.LLM.MaxConcurrent)
	base, err = NewChatCompleter(cfg)
	doc, _ = NewChatCompleterForStep(cfg, StepDoc)
	if doc == nil {
		doc = base
	}
	gen, _ = NewChatCompleterForStep(cfg, StepGeneration)
	if gen == nil {
		gen = base
	}
	fixer, _ = NewChatCompleterForStep(cfg, StepFixer)
	if fixer == nil {
		fixer = base
	}
	// Ollama tool support is probed BEFORE wrapping, while the concrete client is still reachable.
	//
	// Self-hosted open models are a first-class deployment here, and their tool support is a
	// property of the model's chat template rather than of the server or the family — it changes
	// between tags. Probing is the only way to know; without it the client conservatively reports
	// no tool calling and every Ollama run falls to the prompted tier, which is one lookup per turn
	// instead of parallel native calls.
	probeOllamaToolSupport(cfg, base, doc, gen, fixer)

	base = model.NewConcurrencyLimitedCompleter(base, lim)
	doc = model.NewConcurrencyLimitedCompleter(doc, lim)
	gen = model.NewConcurrencyLimitedCompleter(gen, lim)
	fixer = model.NewConcurrencyLimitedCompleter(fixer, lim)
	return base, doc, gen, fixer, lim, err
}

// NewEmbedder returns an Embedder for the configured provider.
// When EmbeddingProvider is set (e.g. openai while Provider is anthropic), that provider and its key are used so you can use Anthropic for chat and OpenAI for embeddings. Returns (nil, nil) when both Provider and EmbeddingProvider are empty.
// Every provider is wrapped so its vectors are L2-normalized before they reach the store — see
// llembed.L2Normalize for why the ANN metric and the scoring metric must agree.
func NewEmbedder(cfg *config.Config) (model.Embedder, error) {
	inner, err := newRawEmbedder(cfg)
	if err != nil || inner == nil {
		return inner, err
	}
	return llembed.NewNormalizingEmbedder(inner), nil
}

// NewCachedEmbedder is NewEmbedder plus the content-addressed memo, when a cache store is available
// and llm.disable_embedding_cache is not set.
//
// The cache wraps the NORMALIZED embedder so a hit is indistinguishable from a fresh embed: cached
// vectors are already unit length. Caching outside normalization would re-normalize on every hit;
// caching inside it would store raw vectors that later reads use directly.
func NewCachedEmbedder(cfg *config.Config, cache llembed.EmbeddingCache, dim int) (model.Embedder, error) {
	emb, err := NewEmbedder(cfg)
	if err != nil || emb == nil {
		return emb, err
	}
	if cfg.LLM.DisableEmbeddingCache || cache == nil || dim <= 0 {
		return emb, nil
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.EmbeddingProvider))
	if provider == "" {
		provider = strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	}
	return llembed.NewCachingEmbedder(emb, cache, provider, cfg.LLM.EmbeddingModel, dim, embeddings.CacheKey), nil
}

// newRawEmbedder builds the provider embedder without the normalization wrapper.
func newRawEmbedder(cfg *config.Config) (model.Embedder, error) {
	p := strings.ToLower(strings.TrimSpace(cfg.LLM.EmbeddingProvider))
	if p == "" {
		p = strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	}
	if p == "" {
		return nil, nil
	}
	var key string
	if cfg.LLM.EmbeddingProvider != "" {
		key = cfg.LLM.EmbeddingAPIKey
		if cfg.LLM.EmbeddingAPIKeyFromEnv != "" {
			if v := os.Getenv(cfg.LLM.EmbeddingAPIKeyFromEnv); v != "" {
				key = v
			}
		}
	}
	if key == "" {
		key = cfg.LLM.APIKey
		if cfg.LLM.APIKeyFromEnv != "" {
			if v := os.Getenv(cfg.LLM.APIKeyFromEnv); v != "" {
				key = v
			}
		}
	}
	switch p {
	case "azure_openai":
		client, err := openai.NewClientWithKeyForEmbedding(cfg, key)
		if err != nil {
			return nil, err
		}
		return client, nil
	case "openai", "cohere", "voyage", "vertex", "vertexai", "ollama", "bedrock":
		// Ollama is supported for embeddings, but a code/chat model (e.g. codestral) has no
		// embeddings and embedding_model may be unset. When the fallback is enabled, use it
		// rather than failing in newNativeOllamaEmbedder ("llm.embedding_model required").
		if p == "ollama" && strings.TrimSpace(cfg.LLM.EmbeddingModel) == "" {
			if fb := embeddingFallbackModel(cfg); fb != "" {
				log.Printf("[asqs] llm: ollama embedding_model unset; using embedding fallback model %q (ensure `ollama pull %s` and database.embeddings_dimension matches)", fb, fb)
				return llembed.NewOllamaEmbedderForModel(cfg, key, fb)
			}
		}
		return llembed.NewProviderEmbedder(cfg, p, key)
	default:
		// The configured provider (e.g. anthropic) has no embeddings API. When enabled, fall
		// back to a local Ollama embedding model rather than failing the index. No cloud key
		// is needed (the fallback targets a local Ollama server).
		if fb := embeddingFallbackModel(cfg); fb != "" {
			log.Printf("[asqs] llm: provider %q has no embeddings; falling back to local Ollama model %q (ensure `ollama pull %s` and database.embeddings_dimension matches)", p, fb, fb)
			return llembed.NewOllamaEmbedderForModel(cfg, "", fb)
		}
		return nil, fmt.Errorf("llm: unsupported embedding provider %q (supported: openai, azure_openai, cohere, voyage, vertex, ollama, bedrock; set embedding_provider, or enable llm.embedding_fallback to use a local Ollama model like %q)", p, DefaultEmbeddingFallbackModel)
	}
}

// NewClient returns both a ChatCompleter and an Embedder for the configured provider.
// Convenience when both are needed (e.g. generator + indexer). Same instance when provider supports both.
func NewClient(cfg *config.Config) (model.ChatCompleter, model.Embedder, error) {
	cc, err := NewChatCompleter(cfg)
	if err != nil {
		return nil, nil, err
	}
	emb, err := NewEmbedder(cfg)
	if err != nil {
		return nil, nil, err
	}
	return cc, emb, nil
}
