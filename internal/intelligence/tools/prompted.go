package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/intelligence/model"
	"github.com/asqs/asqs-core/internal/jsonx"
)

// Mode is how tool access is delivered to a given model.
type Mode string

const (
	// ModeNative uses the provider's own tool-calling API.
	ModeNative Mode = "native"
	// ModePrompted describes the tools in the system prompt and parses one call out of the reply.
	ModePrompted Mode = "prompted"
	// ModeOneShot is no tool access: the model gets the pre-assembled context and one turn.
	ModeOneShot Mode = "one_shot"
)

// ResolveMode picks the best tool delivery this model supports.
//
// The order is native → prompted → one-shot, and each step down is a real loss of capability, so
// the reason is returned for the caller to audit. A silent downgrade is how "tools are enabled" and
// "the model never called a tool" coexist for weeks without anyone noticing.
//
// declared reports whether the provider actually declared its capabilities (B08's two-value form).
// Undeclared is not the same as incapable: a provider that never implemented CapabilityReporter
// gets prompted mode rather than being written off, because prompted mode works on anything that
// can follow an instruction.
func ResolveMode(caps model.Capabilities, declared, toolsEnabled, allowPrompted bool) (Mode, string) {
	if !toolsEnabled {
		return ModeOneShot, "tools are disabled by configuration"
	}
	if declared && caps.ToolCalling {
		return ModeNative, ""
	}
	if !allowPrompted {
		if !declared {
			return ModeOneShot, "provider does not declare its capabilities and prompted tools are disabled"
		}
		return ModeOneShot, "provider does not support native tool calling and prompted tools are disabled"
	}
	if !declared {
		return ModePrompted, "provider does not declare its capabilities; falling back to prompted tools"
	}
	return ModePrompted, "provider does not support native tool calling; falling back to prompted tools"
}

// PromptedInstructions renders the tool catalog as a system-prompt section for models without
// native tool calling.
//
// Exactly one call per turn is requested. Multi-call turns are impractical here: the model has no
// call ids to correlate results with, and a model that emits three objects in one reply usually
// emits them inconsistently. One useful lookup per turn is a large improvement over none, which is
// the entire point of this mode.
func PromptedInstructions(defs []model.ToolDefinition) string {
	if len(defs) == 0 {
		return ""
	}
	sorted := append([]model.ToolDefinition(nil), defs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder
	b.WriteString("## Available lookups\n\n")
	b.WriteString("You may request ONE lookup before answering. To do so, reply with a single JSON " +
		"object and nothing else:\n\n")
	b.WriteString("```json\n{\"tool\": \"<name>\", \"arguments\": { … }}\n```\n\n")
	b.WriteString("The result is returned to you and you may then answer. If you do not need a " +
		"lookup, answer directly — do not emit the JSON object.\n\n")
	for _, d := range sorted {
		fmt.Fprintf(&b, "- **%s** — %s\n", d.Name, strings.TrimSpace(d.Description))
		if d.Schema != nil {
			if raw, err := d.Schema.MarshalJSON(); err == nil {
				fmt.Fprintf(&b, "  arguments: `%s`\n", compactJSON(raw))
			}
		}
	}
	return b.String()
}

func compactJSON(raw []byte) string {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return string(raw)
	}
	return out.String()
}

// ParsePromptedCall extracts a tool call from a prompted-mode reply.
//
// The contract is deliberately forgiving in one direction and strict in the other: any amount of
// prose, fencing or trailing commentary around the object is tolerated, but anything that is not a
// well-formed call naming a KNOWN tool yields (nil, false) — "no tool call this turn" — never an
// error and never a partial call. A model that was simply answering must not be misread as calling
// a tool, and a hallucinated tool name must not reach the dispatcher.
func ParsePromptedCall(content string, known []model.ToolDefinition) (*model.ToolCall, bool) {
	if strings.TrimSpace(content) == "" {
		return nil, false
	}
	names := make(map[string]string, len(known))
	for _, d := range known {
		names[strings.ToLower(strings.TrimSpace(d.Name))] = d.Name
	}

	// Scan every object in the reply, not just the first: models put an example or a thought before
	// the real call often enough that taking only the first object loses real lookups.
	for i := 0; i < len(content); {
		j := strings.Index(content[i:], "{")
		if j < 0 {
			break
		}
		start := i + j
		obj := jsonx.ExtractObjectFrom(content, start)
		if obj == "" {
			break
		}
		i = start + len(obj)

		var probe struct {
			Tool      string          `json:"tool"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Args      json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal([]byte(obj), &probe); err != nil {
			continue
		}
		// "name" is accepted alongside "tool" because models trained on the OpenAI function shape
		// reach for it; rejecting that spelling would fail calls that are otherwise perfectly good.
		raw := strings.TrimSpace(probe.Tool)
		if raw == "" {
			raw = strings.TrimSpace(probe.Name)
		}
		canonical, ok := names[strings.ToLower(raw)]
		if !ok {
			continue
		}
		args := probe.Arguments
		if len(strings.TrimSpace(string(args))) == 0 {
			args = probe.Args
		}
		norm, err := model.NormalizeToolArgs("prompted", canonical, 0, string(args))
		if err != nil {
			// Malformed arguments in prompted mode are not worth failing the turn over: the model
			// gets no result and answers from what it has, which is the pre-tool behaviour.
			continue
		}
		return &model.ToolCall{ID: "prompted_0", Name: canonical, Args: norm}, true
	}
	return nil, false
}
