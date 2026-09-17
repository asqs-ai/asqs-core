package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/asqs/asqs-core/internal/audit"
	llembed "github.com/asqs/asqs-core/internal/llm/embeddings"
)

// Streaming, and why the timeout moved.
//
// Ollama's /api/chat was called with stream:false, which means the server sends NOTHING — not even
// response headers — until the whole completion is finished. Three consequences, all of them seen
// in production:
//
//   - http.Client.Timeout then measures queue time + prompt evaluation + generation. A request
//     sitting behind seven others on one local model burns the entire budget having never been
//     looked at, and reports "Client.Timeout exceeded while awaiting headers", which reads like a
//     dead server and is not;
//   - with chatMaxAttempts retries on top, a saturated queue costs timeout × attempts before a gap
//     fails. a validation run measured 20m × 5 = 100 minutes per gap, with its one
//     successful generation landing at 99.6 minutes;
//   - a genuinely wedged server and a merely slow one are indistinguishable.
//
// Streaming separates them. The budget now bounds SILENCE: how long the stream may go without
// delivering a byte. A generation that keeps producing tokens is healthy however long it runs; one
// that has gone quiet is not, however recently it started. A wedged Ollama is then reported in
// `timeout` rather than in `timeout × attempts`, and it is reported as what it is.
//
// One thing the budget must still cover: a COLD MODEL LOAD produces no bytes either. Measured
// against a 30B model on this machine, the first request after a restart waited 85 seconds before
// its first token. So general.llm.http.timeout has to exceed load time, not just inter-token time —
// the 5-minute default and any sane configured value do, but a budget tuned down to seconds would
// report a loading model as a stalled one.

// stallGuard cancels a request when its response body stops delivering bytes.
//
// It wraps the body rather than using a deadline on the whole read, because the question is "has
// anything arrived recently", not "has everything arrived yet".
type stallGuard struct {
	rc     io.ReadCloser
	cancel context.CancelFunc
	budget time.Duration

	mu     sync.Mutex
	timer  *time.Timer
	fired  bool
	closed bool
}

func newStallGuard(rc io.ReadCloser, cancel context.CancelFunc, budget time.Duration) *stallGuard {
	g := &stallGuard{rc: rc, cancel: cancel, budget: budget}
	g.timer = time.AfterFunc(budget, g.onStall)
	return g
}

func (g *stallGuard) onStall() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.fired = true
	g.mu.Unlock()
	g.cancel()
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.rc.Read(p)
	if n > 0 {
		g.mu.Lock()
		if !g.closed && !g.fired {
			g.timer.Reset(g.budget)
		}
		g.mu.Unlock()
	}
	return n, err
}

// Stalled reports whether the guard cancelled the request rather than the server or caller ending it.
func (g *stallGuard) Stalled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fired
}

func (g *stallGuard) Close() error {
	g.mu.Lock()
	g.closed = true
	g.timer.Stop()
	g.mu.Unlock()
	return g.rc.Close()
}

// maxStreamLineBytes bounds one NDJSON frame. Ollama emits a token or two per frame; a megabyte is
// far past anything legitimate and stops a malformed stream from growing the buffer without end.
const maxStreamLineBytes = 1 << 20

// readChatStream consumes Ollama's newline-delimited chat stream into a single chatResponse.
//
// Each frame carries a DELTA in message.content; the final frame carries done, done_reason and the
// token counts. Tool calls arrive whole on the frame that has them rather than in pieces, which is
// why they are appended rather than concatenated.
func readChatStream(body io.Reader) (*chatResponse, error) {
	out := &chatResponse{}
	var content strings.Builder
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), maxStreamLineBytes)
	frames := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var frame chatResponse
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			return nil, fmt.Errorf("decode stream frame: %w", err)
		}
		frames++
		content.WriteString(frame.Message.Content)
		if frame.Message.Role != "" {
			out.Message.Role = frame.Message.Role
		}
		out.Message.ToolCalls = append(out.Message.ToolCalls, frame.Message.ToolCalls...)
		if frame.DoneReason != "" {
			out.DoneReason = frame.DoneReason
		}
		if frame.PromptEvalCount > 0 {
			out.PromptEvalCount = frame.PromptEvalCount
		}
		if frame.EvalCount > 0 {
			out.EvalCount = frame.EvalCount
		}
		if frame.Done {
			out.Done = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if frames == 0 {
		return nil, errors.New("empty stream: the server closed the connection without sending a frame")
	}
	out.Message.Content = content.String()
	return out, nil
}

// stallError marks a request the stall guard cancelled, so the retry classifier can tell it from a
// caller cancelling the run — one is worth another attempt, the other must stop immediately.
type stallError struct {
	budget time.Duration
	phase  string
}

func (e *stallError) Error() string {
	return fmt.Sprintf("ollama chat: no data for %s while %s (general.llm.http.timeout); "+
		"the server accepted the request and stopped producing", e.budget, e.phase)
}

// doChat performs one attempt: send the payload, read the streamed reply, and fail fast when the
// stream goes quiet for longer than the budget.
func (c *Client) doChat(ctx context.Context, rawPayload []byte) (*chatResponse, error) {
	budget := c.stallBudget
	if budget <= 0 {
		budget = defaultStallBudget
	}
	// A context of this attempt's own, so cancelling a stalled read cannot cancel the caller's.
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.endpoint, bytes.NewReader(rawPayload))
	if err != nil {
		return nil, fmt.Errorf("ollama chat: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// The headers are the first thing that can stall: a queued request gets none until the server
	// reaches it. Guarding the round trip separately names that phase in the error.
	headerTimer := time.AfterFunc(budget, cancel)
	resp, err := c.httpClient.Do(req)
	headerTimer.Stop()
	if err != nil {
		if ctx.Err() == nil && attemptCtx.Err() != nil {
			return nil, &stallError{budget: budget, phase: "waiting for the first response header"}
		}
		return nil, fmt.Errorf("ollama chat: %w", err)
	}

	guard := newStallGuard(resp.Body, cancel, budget)
	defer guard.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(guard, 4096))
		return nil, &httpStatusError{code: resp.StatusCode, body: truncate(string(body), 512)}
	}

	out, err := readChatStream(guard)
	if err != nil {
		if guard.Stalled() && ctx.Err() == nil {
			return nil, &stallError{budget: budget, phase: "reading the streamed reply"}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ollama chat: %w", err)
	}
	return out, nil
}

// defaultStallBudget applies when no client timeout is configured at all.
const defaultStallBudget = 5 * time.Minute

// httpStatusError carries a non-2xx reply so the retry classifier can consult the code.
type httpStatusError struct {
	code int
	body string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("ollama chat: status %d: %s", e.code, e.body)
}

// isRetriableOllamaChatError decides whether another attempt can plausibly do better.
//
// A stall is retriable for the same reason a network timeout is — the server may be past whatever
// held it — but it is now reported after ONE budget rather than after a budget spent pretending to
// be a slow generation.
func isRetriableOllamaChatError(err error) bool {
	var stall *stallError
	if errors.As(err, &stall) {
		return true
	}
	var status *httpStatusError
	if errors.As(err, &status) {
		return llembed.IsRetriableHTTPStatus(status.code)
	}
	return llembed.IsRetriableChatTransport(err)
}

// auditRetry records that an attempt is being repeated.
//
// The Auditor comes from the context because an LLM client is built once per run from a config and
// never receives one — see audit.WithAuditor. Nil is normal (a client built outside a run) and
// silent.
func (c *Client) auditRetry(ctx context.Context, attempt int, lastErr error) {
	a := audit.FromContext(ctx)
	if a == nil || lastErr == nil {
		return
	}
	a.LogError(ctx, "llm.ollama_retry", map[string]any{
		"message": fmt.Sprintf("Ollama chat attempt %d of %d failed (%v); retrying against %s.",
			attempt, chatMaxAttempts, lastErr, c.endpoint),
		"attempt":      attempt,
		"max_attempts": chatMaxAttempts,
		"endpoint":     c.endpoint,
		"model":        c.model,
		"error":        lastErr.Error(),
		"stall_budget": c.stallBudget.String(),
	})
}
