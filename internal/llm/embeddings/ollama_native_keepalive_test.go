package llembed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
)

// TestNativeOllamaEmbedder_sendsKeepAlive: the embedding model gets the same keep_alive as the chat
// model, as a JSON number, so it is not evicted and reloaded between retrieval passes.
func TestNativeOllamaEmbedder_sendsKeepAlive(t *testing.T) {
	t.Parallel()
	var got map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err := json.Unmarshal(body, &got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ollamaEmbedAPIResponse{Embeddings: [][]float64{{1, 2}}})
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{LLM: config.LLMConfig{EmbeddingModel: "m", BaseURL: srv.URL, OllamaKeepAlive: "-1"}}
	e, err := newNativeOllamaEmbedder(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if string(got["keep_alive"]) != "-1" {
		t.Fatalf("keep_alive on the wire = %s, want -1", got["keep_alive"])
	}

	bad := &config.Config{LLM: config.LLMConfig{EmbeddingModel: "m", BaseURL: srv.URL, OllamaKeepAlive: "forever"}}
	if _, err := newNativeOllamaEmbedder(bad, ""); err == nil {
		t.Fatal("expected a malformed keep_alive to be rejected at construction")
	}
}
