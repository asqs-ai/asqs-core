// Package llm provides a configurable, extensible LLM integration. It builds
// model.ChatCompleter and model.Embedder from config (OpenAI today; Anthropic, Google later).
package llm

import (
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/llm/anthropic"
	llembed "github.com/asqs/asqs-core/internal/llm/embeddings"
	"github.com/asqs/asqs-core/internal/llm/ollama"
	"github.com/asqs/asqs-core/internal/llm/openai"
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

// NewChatCompleter returns a ChatCompleter for the configured default provider and model. Returns (nil, nil) when cfg.LLM.Provider is empty.
func NewChatCompleter(cfg *config.Config) (model.ChatCompleter, error) {
	return NewChatCompleterForStep(cfg, "")
}

// NewEmbedder returns an Embedder for the configured provider.
// When EmbeddingProvider is set (e.g. openai while Provider is anthropic), that provider and its key are used so you can use Anthropic for chat and OpenAI for embeddings. Returns (nil, nil) when both Provider and EmbeddingProvider are empty.
func NewEmbedder(cfg *config.Config) (model.Embedder, error) {
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
		return llembed.NewProviderEmbedder(cfg, p, key)
	default:
		return nil, fmt.Errorf("llm: unsupported embedding provider %q (supported: openai, azure_openai, cohere, voyage, vertex, ollama, bedrock; set embedding_provider when chat uses a provider without embeddings)", p)
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
