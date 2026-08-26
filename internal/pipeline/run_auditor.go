package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/audit"
)

// runAuditor is the auditor shape shared by every stage interface the pipeline drives: indexer,
// retrieval, evaluator and testbootstrap each declare an identical two-method Auditor, so one
// value of this interface satisfies all four.
type runAuditor interface {
	Log(ctx context.Context, step string, payload interface{})
	LogError(ctx context.Context, step string, payload interface{})
}

// stderrAuditor prints a compact line per step to stderr and discards the payload. It is the
// interactive UX of a run and the fallback when no audit file is configured; the structured
// payloads land only when teeAuditor adds the JSONL sink.
type stderrAuditor struct{}

func (stderrAuditor) Log(_ context.Context, step string, _ interface{}) {
	fmt.Fprintf(os.Stderr, "  · %s\n", step)
}
func (stderrAuditor) LogError(_ context.Context, step string, payload interface{}) {
	fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", step, payload)
}

// teeAuditor keeps the stderr step line exactly as before and forwards every entry, payload
// included, to the JSONL sink.
type teeAuditor struct {
	sink *audit.JSONLLogger
}

func (t teeAuditor) Log(ctx context.Context, step string, payload interface{}) {
	stderrAuditor{}.Log(ctx, step, payload)
	t.sink.Log(ctx, step, payload)
}
func (t teeAuditor) LogError(ctx context.Context, step string, payload interface{}) {
	stderrAuditor{}.LogError(ctx, step, payload)
	t.sink.LogError(ctx, step, payload)
}

// buildRunAuditor returns the auditor for one run and a close function to defer. With no path the
// run behaves exactly as before: stderr lines, payloads discarded. With a path, payloads are also
// appended to the JSONL file, and the close function drains the sink and reports any dropped
// entries — a silent, incomplete audit log is worse than a noisy one.
//
// A path whose file cannot be opened degrades to the stderr auditor with a warning rather than
// failing the run: the audit file is optional, and an optional sink must never take the run down
// — nor, worse, silently disable the stderr lines with it.
func buildRunAuditor(path string, dumpPrompts bool) (runAuditor, func()) {
	if strings.TrimSpace(path) == "" {
		return stderrAuditor{}, func() {}
	}
	sink, err := audit.NewJSONLLogger(path, audit.Options{DumpPrompts: dumpPrompts})
	if err != nil {
		fmt.Fprintf(os.Stderr, "asqs-core: %v — continuing without the audit file\n", err)
		return stderrAuditor{}, func() {}
	}
	return teeAuditor{sink: sink}, func() {
		if err := sink.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "asqs-core: audit: close: %v\n", err)
		}
		if d := sink.Dropped(); d > 0 {
			fmt.Fprintf(os.Stderr, "asqs-core: audit: %d entrie(s) dropped — the audit log at %s is incomplete\n", d, path)
		}
	}
}
