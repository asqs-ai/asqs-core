package llembed

import (
	"strings"

	geclient "github.com/milosgajdos/go-embeddings/client"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/llm/httpcfg"
)

func trimAPIBase(u string) string {
	return strings.TrimSuffix(strings.TrimSpace(u), "/")
}

// GeHTTP builds a go-embeddings HTTP wrapper using llm HTTP tuning.
func GeHTTP(cfg *config.Config) *geclient.HTTP {
	return geclient.NewHTTP(geclient.WithHTTPClient(httpcfg.HTTPClient(&cfg.LLM)))
}

// GeHTTPWithBearer returns GeHTTP but injects Authorization when token is non-empty (e.g. Ollama gateway).
func GeHTTPWithBearer(cfg *config.Config, bearerToken string) *geclient.HTTP {
	return geclient.NewHTTP(geclient.WithHTTPClient(httpcfg.HTTPClientWithBearer(&cfg.LLM, bearerToken)))
}
