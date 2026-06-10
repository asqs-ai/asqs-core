package llembed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/llm/httpcfg"
)

// Ollama superseded POST /api/embeddings with POST /api/embed (see Ollama docs). go-embeddings still calls
// /api/embeddings, which breaks on current servers (often EOF / empty body on decode).
const ollamaEmbedBatchMax = 128

// defaultOllamaAPIRoot uses 127.0.0.1 so we dial IPv4 loopback. Resolving "localhost" often prefers [::1]
// first; a server listening only on 127.0.0.1:11434 then returns connection refused.
const defaultOllamaAPIRoot = "http://127.0.0.1:11434/api"

type nativeOllamaEmbedder struct {
	cfg    *config.Config
	model  string
	client *http.Client
	apiURL string // e.g. http://127.0.0.1:11434/api/embed
}

func ollamaEmbedAPIRoot(cfg *config.Config) string {
	u := trimAPIBase(cfg.LLM.BaseURL)
	if u == "" {
		return defaultOllamaAPIRoot
	}
	if strings.HasSuffix(u, "/api") {
		return u
	}
	return u + "/api"
}

func newNativeOllamaEmbedder(cfg *config.Config, apiKey string) (model.Embedder, error) {
	modelID := strings.TrimSpace(cfg.LLM.EmbeddingModel)
	if modelID == "" {
		return nil, fmt.Errorf("ollama embeddings: llm.embedding_model required")
	}
	root := ollamaEmbedAPIRoot(cfg)
	apiURL := strings.TrimSuffix(root, "/") + "/embed"
	maybeLogResolvedOllamaEmbed(apiURL, modelID)
	return &nativeOllamaEmbedder{
		cfg:    cfg,
		model:  modelID,
		client: httpcfg.HTTPClientWithBearerForOllama(&cfg.LLM, apiKey),
		apiURL: apiURL,
	}, nil
}

func maybeLogResolvedOllamaEmbed(apiURL, modelID string) {
	if s := strings.TrimSpace(strings.ToLower(os.Getenv("ASQS_LOG_RESOLVED_LLM_ENDPOINTS"))); s != "1" && s != "true" && s != "yes" {
		return
	}
	log.Printf("[asqs] llm ollama embed: url=%s model=%s", apiURL, modelID)
}

type ollamaEmbedAPIRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Truncate *bool    `json:"truncate,omitempty"`
}

type ollamaEmbedAPIResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

func float64VectorsToFloat32(in [][]float64) [][]float32 {
	out := make([][]float32, len(in))
	for i, row := range in {
		v := make([]float32, len(row))
		for j, x := range row {
			v[j] = float32(x)
		}
		out[i] = v
	}
	return out
}

func (o *nativeOllamaEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	texts = NormalizeTexts(texts)
	if len(texts) == 0 {
		return nil, nil
	}
	all := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += ollamaEmbedBatchMax {
		end := start + ollamaEmbedBatchMax
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]
		vecs, err := o.embedBatch(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("ollama embeddings: %w", err)
		}
		all = append(all, vecs...)
	}
	return all, nil
}

func (o *nativeOllamaEmbedder) embedBatch(ctx context.Context, batch []string) ([][]float32, error) {
	truncateTrue := true
	payload := ollamaEmbedAPIRequest{
		Model:    o.model,
		Input:    batch,
		Truncate: &truncateTrue,
	}
	raw, err := json.Marshal(&payload)
	if err != nil {
		return nil, err
	}

	var result [][]float32
	err = RunEmbedRetries(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.apiURL, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := o.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("status %d: %s", resp.StatusCode, truncateErrBody(body, 512))
		}
		var parsed ollamaEmbedAPIResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(parsed.Embeddings) != len(batch) {
			return fmt.Errorf("expected %d embeddings, got %d", len(batch), len(parsed.Embeddings))
		}
		for _, row := range parsed.Embeddings {
			if len(row) == 0 {
				return fmt.Errorf("empty embedding vector from Ollama")
			}
		}
		result = float64VectorsToFloat32(parsed.Embeddings)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func truncateErrBody(b []byte, n int) string {
	s := string(bytes.TrimSpace(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
