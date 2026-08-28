package tools

import "github.com/asqs/asqs-core/internal/intelligence/model"

// GenerationBounds is the config subset the loop needs, kept as an interface-free struct so this
// package does not import internal/config (which imports retrieval, which would cycle).
type GenerationBounds struct {
	ToolsEnabled         bool
	PromptedToolsEnabled bool
	MaxToolTurns         int
	MaxToolCallsPerTurn  int
	MaxToolCallsPerRun   int
	MaxToolResultChars   int
}

// LoopOptionsFor turns configuration and a provider's declared capabilities into loop bounds.
//
// Returning the resolution reason alongside the options is deliberate: the caller audits it. A
// downgrade from native to prompted, or to one-shot, changes what the model can do, and discovering
// that weeks later from a quality regression is much more expensive than one audit line.
func LoopOptionsFor(b GenerationBounds, caps model.Capabilities, declared bool) (LoopOptions, string) {
	mode, reason := ResolveMode(caps, declared, b.ToolsEnabled, b.PromptedToolsEnabled)
	return LoopOptions{
		Mode:            mode,
		MaxTurns:        b.MaxToolTurns,
		MaxCallsPerTurn: b.MaxToolCallsPerTurn,
		MaxCallsPerRun:  b.MaxToolCallsPerRun,
		MaxResultChars:  b.MaxToolResultChars,
	}, reason
}
