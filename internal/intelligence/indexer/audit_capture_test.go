package indexer

import (
	"context"
	"sync"
)

// Test double shared by the indexer tests. asqs-core does not carry upstream's full run_run_test
// harness, so the capturing auditor lives on its own here.
// captureAuditor records audit calls so tests can assert on emitted events. Implements
// indexer.Auditor.
type captureAuditor struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	step    string
	level   string // "info" | "error"
	payload map[string]interface{}
}

func (c *captureAuditor) Log(_ context.Context, step string, payload interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, _ := payload.(map[string]interface{})
	c.events = append(c.events, capturedEvent{step: step, level: "info", payload: m})
}

func (c *captureAuditor) LogError(_ context.Context, step string, payload interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, _ := payload.(map[string]interface{})
	c.events = append(c.events, capturedEvent{step: step, level: "error", payload: m})
}

func (c *captureAuditor) findEvent(step string) (capturedEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.step == step {
			return e, true
		}
	}
	return capturedEvent{}, false
}

// fixedDimEmbedder returns zero vectors of length dim (for tests).
type fixedDimEmbedder struct{ dim int }

func (e fixedDimEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	z := make([]float32, e.dim)
	for i := range out {
		out[i] = z
	}
	return out, nil
}
