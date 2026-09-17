package audit

import "context"

// Auditor is the run-scoped audit sink. The same two methods every consumer in this repository
// declares for itself; declared once here so a value can be carried on a context.
type Auditor interface {
	Log(ctx context.Context, step string, payload interface{})
	LogError(ctx context.Context, step string, payload interface{})
}

// The run's Auditor, carried on the context.
//
// Most of the pipeline receives an Auditor as a parameter, because most of the pipeline is called
// from code that has one. The LLM clients are not: internal/llm is built once per run by
// BuildStepCompleters, from a config, with no run in sight — so the one place that knows a request
// was retried had no way to say so. A validation run silently retried five times per gap against a wedged
// Ollama, spent 100 minutes a gap doing it, and left a single "context deadline exceeded" per gap
// in the trail with no hint that 35 attempts had been made.
//
// Installed once per run, beside the run's other per-run context values.

type ctxKey struct{}

// WithAuditor returns a context whose FromContext yields a. A nil a leaves ctx unchanged.
func WithAuditor(ctx context.Context, a Auditor) context.Context {
	if a == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, a)
}

// FromContext returns the Auditor installed by WithAuditor, or nil when the context carries none.
// Callers must tolerate nil: a client built outside a run has no audit trail to write to, and that
// is not an error.
func FromContext(ctx context.Context) Auditor {
	if ctx == nil {
		return nil
	}
	if a, ok := ctx.Value(ctxKey{}).(Auditor); ok {
		return a
	}
	return nil
}
