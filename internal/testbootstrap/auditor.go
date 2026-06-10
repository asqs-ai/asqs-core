package testbootstrap

import "context"

// Auditor is the minimal audit sink testbootstrap needs (Log / LogError). A nil Auditor is
// allowed and treated as a no-op. Decoupled from the engine's orchestrator package so asqs-core
// does not pull in the enterprise orchestrator.
type Auditor interface {
	Log(ctx context.Context, step string, payload interface{})
	LogError(ctx context.Context, step string, payload interface{})
}
