package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/config"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

func TestComplete_nonStreaming(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/api/chat") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.Stream {
			t.Fatal("expected stream false")
		}
		if req.Model != "mistral" {
			t.Fatalf("model: %#v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider: "ollama",
			Model:    "mistral",
			BaseURL:  srv.URL,
		},
	}
	c, err := NewClientWithKeyAndModel(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "ping"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "ok" {
		t.Fatalf("content=%q", out.Content)
	}
}

func TestComplete_withOllamaNumCtx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.Options == nil {
			t.Fatal("expected options")
		}
		nv, ok := req.Options["num_ctx"].(float64)
		if !ok || int(nv) != 8192 {
			t.Fatalf("options.num_ctx = %#v", req.Options["num_ctx"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"x"},"done":true}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider:     "ollama",
			Model:        "mistral",
			BaseURL:      srv.URL,
			OllamaNumCtx: 8192,
		},
	}
	c, err := NewClientWithKeyAndModel(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Complete(context.Background(), []model.Message{{Role: "user", Content: "ping"}}, model.CompleteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "x" {
		t.Fatalf("content=%q", out.Content)
	}
}
