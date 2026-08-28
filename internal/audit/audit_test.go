package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []line {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []line
	for _, ln := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if ln == "" {
			continue
		}
		var l line
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatalf("line %q is not valid JSON: %v", ln, err)
		}
		out = append(out, l)
	}
	return out
}

func TestJSONLLogger_roundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := NewJSONLLogger(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l.Log(ctx, "index.start", map[string]interface{}{"files": 3})
	l.LogError(ctx, "index.error", map[string]interface{}{"error": "boom"})
	l.Log(ctx, "plan.done", nil)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0].Step != "index.start" || lines[0].Level != "info" {
		t.Errorf("line 0 = %+v, want step index.start level info", lines[0])
	}
	if _, err := time.Parse(time.RFC3339Nano, lines[0].TS); err != nil {
		t.Errorf("ts %q does not parse as RFC3339Nano: %v", lines[0].TS, err)
	}
	var p0 map[string]interface{}
	if err := json.Unmarshal(lines[0].Payload, &p0); err != nil || p0["files"] != float64(3) {
		t.Errorf("line 0 payload = %s, want {\"files\":3}", lines[0].Payload)
	}
	if lines[1].Step != "index.error" || lines[1].Level != "error" {
		t.Errorf("line 1 = %+v, want step index.error level error", lines[1])
	}
	if string(lines[2].Payload) != "null" {
		t.Errorf("nil payload serialised as %s, want null", lines[2].Payload)
	}
	if got := l.Written(); got != 3 {
		t.Errorf("Written() = %d, want 3", got)
	}
	if got := l.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0", got)
	}
}

func TestJSONLLogger_redactsPromptFields(t *testing.T) {
	promptText := "You are a test generator. Repo source follows…"
	wantSum := sha256.Sum256([]byte(promptText))
	wantHex := hex.EncodeToString(wantSum[:])

	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := NewJSONLLogger(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Log(context.Background(), "generate.request", map[string]interface{}{
		"prompt":          promptText,
		"system_prompt":   "system text",
		"completion_text": "generated test body",
		"messages":        []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
		"nested":          map[string]interface{}{"fix_prompt": "inner text", "attempt": 2},
		"gap":             "com.example.Foo#bar",
		"tokens":          123,
	})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	var p map[string]interface{}
	if err := json.Unmarshal(lines[0].Payload, &p); err != nil {
		t.Fatal(err)
	}

	// Every prompt-carrying field is a {sha256, len} digest…
	for _, key := range []string{"prompt", "system_prompt", "completion_text", "messages"} {
		d, ok := p[key].(map[string]interface{})
		if !ok {
			t.Fatalf("%s = %v (%T), want a digest object", key, p[key], p[key])
		}
		if _, ok := d["sha256"].(string); !ok {
			t.Errorf("%s digest has no sha256: %v", key, d)
		}
		if _, ok := d["len"].(float64); !ok {
			t.Errorf("%s digest has no len: %v", key, d)
		}
		if raw, ok := d["sha256"].(string); ok && strings.Contains(raw, "generator") {
			t.Errorf("%s still contains prompt text", key)
		}
	}
	// …with the right hash and length for the string case.
	d := p["prompt"].(map[string]interface{})
	if d["sha256"] != wantHex {
		t.Errorf("prompt sha256 = %v, want %s", d["sha256"], wantHex)
	}
	if d["len"] != float64(len(promptText)) {
		t.Errorf("prompt len = %v, want %d", d["len"], len(promptText))
	}
	// Nested prompt-carrying fields are redacted too; their siblings survive.
	nested := p["nested"].(map[string]interface{})
	if _, ok := nested["fix_prompt"].(map[string]interface{}); !ok {
		t.Errorf("nested.fix_prompt not redacted: %v", nested["fix_prompt"])
	}
	if nested["attempt"] != float64(2) {
		t.Errorf("nested.attempt = %v, want 2", nested["attempt"])
	}
	// Ordinary fields are untouched.
	if p["gap"] != "com.example.Foo#bar" || p["tokens"] != float64(123) {
		t.Errorf("non-prompt fields were modified: gap=%v tokens=%v", p["gap"], p["tokens"])
	}
}

func TestJSONLLogger_dumpPromptsRestoresContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := NewJSONLLogger(path, Options{DumpPrompts: true})
	if err != nil {
		t.Fatal(err)
	}
	l.Log(context.Background(), "generate.request", map[string]interface{}{"prompt": "full text stays"})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	var p map[string]interface{}
	if err := json.Unmarshal(lines[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["prompt"] != "full text stays" {
		t.Errorf("with DumpPrompts, prompt = %v, want the full text", p["prompt"])
	}
}

// blockingWriter blocks the first Write until released, then passes everything through.
type blockingWriter struct {
	entered  chan struct{} // signalled once, when the first Write starts
	release  chan struct{} // closed to unblock
	enterOne sync.Once

	mu    sync.Mutex
	wrote int
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.enterOne.Do(func() {
		close(w.entered)
		<-w.release
	})
	w.mu.Lock()
	w.wrote++
	w.mu.Unlock()
	return len(p), nil
}
func (w *blockingWriter) Close() error { return nil }

// The acceptance in the plan: a drain goroutine blocked for 30 s must not stall the pipeline —
// entries are dropped and counted instead. The writer here blocks indefinitely until released,
// which proves the same independence without the wait.
func TestJSONLLogger_overflowDropsInsteadOfBlocking(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	l := newWithWriter(w, Options{QueueSize: 2})
	ctx := context.Background()

	// First entry: wait until the drain goroutine is provably stuck inside Write.
	l.Log(ctx, "s0", nil)
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("drain never reached the writer")
	}

	// Nine more while the writer is blocked: 2 fit in the queue, 7 must drop. Every Log call
	// must return promptly — the guard channel fails the test on a stall.
	done := make(chan struct{})
	go func() {
		for i := 1; i < 10; i++ {
			l.Log(ctx, "s", map[string]interface{}{"i": i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Log blocked while the drain goroutine was stuck in a write")
	}
	if got := l.Dropped(); got != 7 {
		t.Errorf("Dropped() = %d while blocked, want 7 (queue 2 + 1 in flight out of 10)", got)
	}

	close(w.release)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := l.Written(); got != 3 {
		t.Errorf("Written() = %d after release, want 3", got)
	}
}

// errorWriter fails every write.
type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func (errorWriter) Close() error              { return nil }

func TestJSONLLogger_writeErrorCountsDropped(t *testing.T) {
	l := newWithWriter(errorWriter{}, Options{})
	l.Log(context.Background(), "s", nil)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if got := l.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d, want 1 (failed write)", got)
	}
	if got := l.Written(); got != 0 {
		t.Errorf("Written() = %d, want 0", got)
	}
}

func TestJSONLLogger_closeIdempotentAndLogAfterCloseIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := NewJSONLLogger(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Log(context.Background(), "before", nil)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	l.Log(context.Background(), "after", nil) // must not panic on the closed channel
	if lines := readLines(t, path); len(lines) != 1 || lines[0].Step != "before" {
		t.Errorf("post-Close log leaked into the file: %+v", lines)
	}
}

func TestNewJSONLLogger_createsParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".asqs", "audit.jsonl")
	l, err := NewJSONLLogger(path, Options{})
	if err != nil {
		t.Fatalf("nested path: %v", err)
	}
	l.Log(context.Background(), "s", nil)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if lines := readLines(t, path); len(lines) != 1 {
		t.Errorf("got %d lines, want 1", len(lines))
	}
}

// The payload is frozen at Log time: mutating the map afterwards must not change the line.
func TestJSONLLogger_payloadFrozenAtLogTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := NewJSONLLogger(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := map[string]interface{}{"state": "original"}
	l.Log(context.Background(), "s", p)
	p["state"] = "mutated"
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	var got map[string]interface{}
	if err := json.Unmarshal(lines[0].Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["state"] != "original" {
		t.Errorf("payload = %v, want the value at Log time", got["state"])
	}
}

func TestJSONLLogger_unmarshalablePayloadStillProducesALine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := NewJSONLLogger(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Log(context.Background(), "s", map[string]interface{}{"bad": make(chan int)})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	var p map[string]interface{}
	if err := json.Unmarshal(lines[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p["audit_marshal_error"]; !ok {
		t.Errorf("payload = %v, want an audit_marshal_error explanation", p)
	}
}

// A count is not a secret, and hashing one destroys what the field exists for.
//
// The key-name convention over-matches by design — it is a smoke alarm, not a classifier — but
// `prompt_tokens`, `prompt_bytes` and `max_prompt_tokens` are numbers. CP28's
// generate.prompt_budget event was shipping `prompt_tokens: {sha256, len: 4}`: the token figure
// CP29's provider-reported ground truth is meant to be calibrated against, replaced by a hash of its
// own digits. Found by reading a real run's audit log.
func TestRedactPayload_keepsNumericPromptFields(t *testing.T) {
	in := []byte(`{"prompt_tokens":8234,"prompt_bytes":41170,"over_budget":false,"prompt":"class Foo { secret }"}`)
	out := RedactPayload(in)

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if v, ok := got["prompt_tokens"].(float64); !ok || v != 8234 {
		t.Errorf("prompt_tokens = %v, want the number 8234 — a count carries no prompt text", got["prompt_tokens"])
	}
	if v, ok := got["prompt_bytes"].(float64); !ok || v != 41170 {
		t.Errorf("prompt_bytes = %v, want the number", got["prompt_bytes"])
	}
	if v, ok := got["over_budget"].(bool); !ok || v {
		t.Errorf("over_budget = %v, want the boolean", got["over_budget"])
	}
	// The actual prompt text must still be redacted — narrowing the rule must not open it.
	m, ok := got["prompt"].(map[string]interface{})
	if !ok || m["sha256"] == nil {
		t.Errorf("prompt = %v, want {sha256, len}; prompt BODIES must still be redacted", got["prompt"])
	}
}

// A structure that contains text anywhere under a prompt-shaped key is still redacted whole.
func TestRedactPayload_redactsNestedText(t *testing.T) {
	in := []byte(`{"messages":[{"role":"system","content":"repository source"}]}`)
	var got map[string]interface{}
	if err := json.Unmarshal(RedactPayload(in), &got); err != nil {
		t.Fatal(err)
	}
	m, ok := got["messages"].(map[string]interface{})
	if !ok || m["sha256"] == nil {
		t.Errorf("messages = %v, want {sha256, len}", got["messages"])
	}
}
