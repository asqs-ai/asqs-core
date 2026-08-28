package model

// Capabilities declares which CompleteOptions fields a provider actually acts on.
//
// CompleteOptions has fields the four providers support in different subsets, and the unsupported
// ones were silently discarded: Anthropic ignored Structured and Temperature (`_ = opts.Temperature`),
// Ollama ignored Structured AND MaxTokens and never reported Usage. Nothing recorded the drop, so
// a caller that set a field could not tell whether it had any effect.
//
// The concrete cost of that silence: `first_wave_metrics.llm_total_tokens` and `tokens_to_stable`
// are always 0 on the Ollama path, and `config-codestral.local.yaml` ships in the repo — so the
// entire cost side of the measurement loop was blind on a supported configuration.
//
// The zero value declares "supports nothing", which is the safe default: a caller seeing it
// degrades explicitly rather than assuming support.
type Capabilities struct {
	// StructuredOutput: the provider constrains output to CompleteOptions.Structured's schema.
	// False means a caller must fall back to instructing the model in prose and parsing defensively.
	StructuredOutput bool
	// Temperature: CompleteOptions.Temperature reaches the provider.
	Temperature bool
	// MaxTokens: CompleteOptions.MaxTokens reaches the provider as an output cap.
	MaxTokens bool
	// UsageReporting: CompleteResult.Usage is populated from provider-reported token counts.
	UsageReporting bool
	// PromptCaching: the provider supports an explicit cache directive on a stable prompt prefix.
	PromptCaching bool
	// ToolCalling: the provider can be given tool definitions and return tool calls.
	// Reserved for the tool-calling work; no provider declares it yet.
	ToolCalling bool
	// StructuredWithTools: Structured and Tools may be honoured on the SAME request.
	//
	// False means structured output is implemented as a grammar constraint over the whole
	// generation, which makes tool-call emission impossible — the model literally cannot produce
	// its tool-call syntax under the schema grammar. Measured on Ollama (qwen3-coder:30b,
	// 2026-08-18): an identical lookup-requiring prompt produced a get_symbol call on every trial
	// without `format` and zero calls on every trial with it, the model silently guessing instead.
	// A caller that sends both gets a request that LOOKS tool-enabled and never calls a tool,
	// which is precisely the silent degradation the capability contract exists to surface.
	StructuredWithTools bool
	// ToolChoiceNoneWithTools: a request may declare Tools while setting ToolChoice to
	// ToolChoiceNone, and the provider will bar the model from calling them.
	//
	// This is how a tool loop's forced final turn keeps the conversation valid and the prompt
	// prefix cacheable while still forbidding another lookup. It matters most on Anthropic, where
	// the alternative — dropping the tools field — makes the request INVALID: a history containing
	// tool_use/tool_result blocks is rejected without a tools declaration. On providers that
	// declare false (Ollama's /api/chat has no tool_choice at all), the final turn must withhold
	// the tools field and rely on an explicit instruction in the message text instead.
	ToolChoiceNoneWithTools bool
}

// CapabilityReporter is implemented by providers that declare what they support.
//
// This is deliberately a SEPARATE interface rather than a method on ChatCompleter: seventeen types
// implement ChatCompleter, ten of them test mocks, and widening that interface would break every
// one of them to gain nothing a type assertion does not already give. A completer that does not
// implement this reports the conservative zero value through CapabilitiesOf, which is the same
// "assume nothing" semantics.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// DeclaredCapabilitiesOf returns cc's capabilities and whether cc declared them at all.
//
// The two-value form matters: an undeclared completer is **unknown**, not **incapable**. Degrading
// on the zero value would silently disable structured output for any custom or test ChatCompleter
// that simply has not been updated — a behaviour regression dressed up as caution. Callers must
// therefore degrade only when a provider has *explicitly* declared non-support:
//
//	caps, declared := model.DeclaredCapabilitiesOf(llm)
//	if declared && !caps.StructuredOutput { … degrade explicitly … }
//
// For an undeclared provider the existing runtime fallbacks still apply (e.g.
// IsStructuredOutputAPIError retrying unstructured when the provider rejects a schema).
func DeclaredCapabilitiesOf(cc ChatCompleter) (Capabilities, bool) {
	if r, ok := cc.(CapabilityReporter); ok {
		return r.Capabilities(), true
	}
	return Capabilities{}, false
}

// CapabilitiesOf returns cc's declared capabilities, or the zero value when cc declares none.
// Prefer DeclaredCapabilitiesOf when the answer decides whether to degrade — see its doc comment
// for why "undeclared" must not be treated as "unsupported".
func CapabilitiesOf(cc ChatCompleter) Capabilities {
	caps, _ := DeclaredCapabilitiesOf(cc)
	return caps
}

// DegradedCapability names a capability a caller requested but the provider does not support.
// Used as an audit payload so a degradation is declared rather than silent.
type DegradedCapability struct {
	Capability string // "structured_output", "temperature", "max_tokens", "usage_reporting"
	Provider   string
	Detail     string
}
