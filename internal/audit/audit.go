// Package audit provides the structured audit sink for asqs-core runs: one JSON object per step,
// appended to a file, without ever blocking the pipeline.
//
// asqs-core previously discarded every structured audit payload — the pipeline's stderr auditor
// printed the step name and threw the payload away, so every counter emitted to make a silent
// failure visible was write-only. The upstream product persists audit rows to Postgres behind an
// async queue with drop-and-count overflow; the open-core analogue is this JSONL file sink, with
// the same non-blocking contract.
//
// Prompt bodies are redacted by default. Payload fields carrying prompt or completion text contain
// repository source code, extracted configuration, and compiler/test output, so the sink stores
// {sha256, len} for such fields unless dumping is explicitly enabled for a post-mortem.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultQueueSize bounds the in-flight audit backlog. Overflow drops entries (counted) rather
// than blocking: audit logging must never be able to stall or fail a run.
const DefaultQueueSize = 2048

// Options configures a JSONLLogger.
type Options struct {
	// DumpPrompts restores full prompt/completion text in payloads. Off by default — see the
	// package comment for why redaction is the default, not an optimisation.
	DumpPrompts bool
	// QueueSize bounds the in-flight backlog; 0 means DefaultQueueSize.
	QueueSize int
}

// entry is one queued audit line. The payload is marshaled at enqueue time so the line records
// what was true when Log was called — a caller mutating its payload map afterwards cannot corrupt
// the line, and the drain goroutine never touches caller-owned data.
type entry struct {
	step    string
	level   string
	payload json.RawMessage
	at      time.Time
}

// line is the wire shape: one of these per line in the output file.
type line struct {
	TS      string          `json:"ts"`
	Step    string          `json:"step"`
	Level   string          `json:"level"`
	Payload json.RawMessage `json:"payload"`
}

// JSONLLogger appends one JSON object per audit entry to a writer (normally a file), draining a
// bounded queue on a single goroutine so no pipeline goroutine ever waits on the write.
//
// Lifecycle: Close after all logging has completed — it drains the queue, so the tail of the run
// is not lost. Log calls after Close are dropped no-ops; Log concurrent WITH Close is not
// supported (the same contract as the upstream audit logger).
type JSONLLogger struct {
	w           io.WriteCloser
	dumpPrompts bool

	ch       chan entry
	done     chan struct{}
	dropped  atomic.Int64
	written  atomic.Int64
	closed   atomic.Bool
	closeOne sync.Once
	closeErr error
}

// NewJSONLLogger opens (appending, creating if absent) the audit file at path and starts the
// drain goroutine. The parent directory is created because O_CREATE makes the file, not the
// directory, and "audit.file_path: .asqs/audit.log" is the shape operators reach for.
func NewJSONLLogger(path string, opts Options) (*JSONLLogger, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("audit: file path required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("audit: create directory for %q: %w", path, err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("audit: open %q: %w", path, err)
	}
	return newWithWriter(f, opts), nil
}

// newWithWriter is the seam tests use to inject a slow or failing writer.
func newWithWriter(w io.WriteCloser, opts Options) *JSONLLogger {
	size := opts.QueueSize
	if size <= 0 {
		size = DefaultQueueSize
	}
	l := &JSONLLogger{
		w:           w,
		dumpPrompts: opts.DumpPrompts,
		ch:          make(chan entry, size),
		done:        make(chan struct{}),
	}
	go l.drain()
	return l
}

// Log records one step at level info. payload can be a map, struct, or nil.
func (l *JSONLLogger) Log(_ context.Context, step string, payload interface{}) {
	l.enqueue(step, "info", payload)
}

// LogError records a step at level error. payload typically includes "error": message.
func (l *JSONLLogger) LogError(_ context.Context, step string, payload interface{}) {
	l.enqueue(step, "error", payload)
}

func (l *JSONLLogger) enqueue(step, level string, payload interface{}) {
	if l == nil || l.closed.Load() {
		return
	}
	raw := marshalPayload(payload)
	select {
	case l.ch <- entry{step: step, level: level, payload: raw, at: time.Now().UTC()}:
	default:
		// Queue full. Dropping is the correct trade — observability data must never be able to
		// stall the pipeline — but it is counted so the loss is visible.
		l.dropped.Add(1)
	}
}

// marshalPayload freezes the payload at log time. A payload that cannot be marshaled becomes a
// line that says so, rather than silently losing the whole entry.
func marshalPayload(payload interface{}) json.RawMessage {
	if payload == nil {
		return json.RawMessage("null")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		b, _ = json.Marshal(map[string]string{
			"audit_marshal_error": err.Error(),
			"go_type":             fmt.Sprintf("%T", payload),
		})
	}
	return b
}

// drain writes queued entries on a single goroutine. Redaction happens here, off the hot path.
func (l *JSONLLogger) drain() {
	defer close(l.done)
	for e := range l.ch {
		payload := e.payload
		if !l.dumpPrompts {
			payload = RedactPayload(payload)
		}
		b, err := json.Marshal(line{
			TS:      e.at.Format(time.RFC3339Nano),
			Step:    e.step,
			Level:   e.level,
			Payload: payload,
		})
		if err != nil {
			l.dropped.Add(1)
			continue
		}
		if _, err := l.w.Write(append(b, '\n')); err != nil {
			l.dropped.Add(1)
			continue
		}
		l.written.Add(1)
	}
}

// Dropped returns the number of audit entries lost to queue overflow or write errors. A non-zero
// value means the audit log is incomplete for this run; surface it rather than letting the loss
// be silent.
func (l *JSONLLogger) Dropped() int64 {
	if l == nil {
		return 0
	}
	return l.dropped.Load()
}

// Written returns the number of audit entries successfully persisted.
func (l *JSONLLogger) Written() int64 {
	if l == nil {
		return 0
	}
	return l.written.Load()
}

// Close drains the queue and closes the file. Without the drain the tail of every run would be
// lost: the process exits while the last entries are still in flight. Safe to call more than
// once; every call returns the first close's error.
func (l *JSONLLogger) Close() error {
	if l == nil {
		return nil
	}
	l.closeOne.Do(func() {
		l.closed.Store(true)
		close(l.ch)
		<-l.done
		l.closeErr = l.w.Close()
	})
	return l.closeErr
}

// --- Redaction ------------------------------------------------------------------------------

// isRedactedKey reports whether a payload key MAY carry prompt or completion text by convention:
// any key containing "prompt" or "completion" (prompt, system_prompt, completion_text, …), plus
// "messages", which is a chat transcript and therefore both at once.
//
// The name match alone is not sufficient — see carriesText. A count is not a secret, and hashing one
// destroys the observability the event exists for.
func isRedactedKey(k string) bool {
	k = strings.ToLower(k)
	return strings.Contains(k, "prompt") || strings.Contains(k, "completion") || k == "messages"
}

// RedactPayload replaces the value of every prompt-carrying field in a marshaled payload with
// {"sha256": …, "len": …} — enough to correlate and size the text without storing it. len counts
// bytes: of the string itself for string values, of the canonical JSON encoding otherwise. A
// payload that does not parse as JSON is returned unchanged.
func RedactPayload(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, changed := redactValue(v)
	if !changed {
		return raw
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

func redactValue(v interface{}) (interface{}, bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		changed := false
		for k, val := range t {
			if isRedactedKey(k) && carriesText(val) {
				t[k] = digest(val)
				changed = true
				continue
			}
			if r, c := redactValue(val); c {
				t[k] = r
				changed = true
			}
		}
		return t, changed
	case []interface{}:
		changed := false
		for i, val := range t {
			if r, c := redactValue(val); c {
				t[i] = r
				changed = true
			}
		}
		return t, changed
	default:
		return v, false
	}
}

// carriesText reports whether a value could actually BE prompt or completion text.
//
// The key-name convention over-matches: `prompt_tokens`, `prompt_bytes` and `max_prompt_tokens` are
// COUNTS, and hashing a count protects nothing while destroying the thing the field exists for.
// CP28's generate.prompt_budget event was shipping `prompt_tokens: {sha256, len:4}` — the token
// figure that CP29's provider-reported ground truth is supposed to be calibrated against, replaced
// by a hash of its own digits. Found by reading a real run's audit log, which is what the log is for.
//
// Strings and structures containing them are redacted; numbers and booleans pass through.
func carriesText(v interface{}) bool {
	switch t := v.(type) {
	case string:
		return true
	case map[string]interface{}:
		for _, val := range t {
			if carriesText(val) {
				return true
			}
		}
		return false
	case []interface{}:
		for _, val := range t {
			if carriesText(val) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// digest is the redacted replacement for one prompt-carrying value.
func digest(v interface{}) map[string]interface{} {
	var b []byte
	if s, ok := v.(string); ok {
		b = []byte(s)
	} else {
		var err error
		b, err = json.Marshal(v)
		if err != nil {
			b = []byte(fmt.Sprintf("%v", v))
		}
	}
	sum := sha256.Sum256(b)
	return map[string]interface{}{
		"sha256": hex.EncodeToString(sum[:]),
		"len":    len(b),
	}
}
