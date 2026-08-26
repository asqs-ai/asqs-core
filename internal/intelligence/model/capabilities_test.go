package model

import (
	"context"
	"testing"
)

type plainCompleter struct{}

func (plainCompleter) Complete(context.Context, []Message, CompleteOptions) (*CompleteResult, error) {
	return &CompleteResult{}, nil
}

type declaringCompleter struct{ caps Capabilities }

func (declaringCompleter) Complete(context.Context, []Message, CompleteOptions) (*CompleteResult, error) {
	return &CompleteResult{}, nil
}
func (d declaringCompleter) Capabilities() Capabilities { return d.caps }

// TestDeclaredCapabilitiesOf_undeclaredIsUnknownNotUnsupported is the important behavioural
// property. Treating "undeclared" as "unsupported" would silently disable structured output for
// every custom or not-yet-updated ChatCompleter — a regression dressed up as caution.
func TestDeclaredCapabilitiesOf_undeclaredIsUnknownNotUnsupported(t *testing.T) {
	caps, declared := DeclaredCapabilitiesOf(plainCompleter{})
	if declared {
		t.Fatal("a completer without Capabilities() must report declared=false")
	}
	if caps != (Capabilities{}) {
		t.Errorf("undeclared capabilities should be the zero value, got %+v", caps)
	}
	// The contract callers rely on: with declared=false they must NOT degrade.
	if declared && !caps.StructuredOutput {
		t.Error("this branch must be unreachable for an undeclared completer")
	}
}

func TestDeclaredCapabilitiesOf_declaring(t *testing.T) {
	want := Capabilities{StructuredOutput: true, Temperature: true, MaxTokens: true, UsageReporting: true}
	caps, declared := DeclaredCapabilitiesOf(declaringCompleter{caps: want})
	if !declared {
		t.Fatal("a completer with Capabilities() must report declared=true")
	}
	if caps != want {
		t.Errorf("caps = %+v, want %+v", caps, want)
	}
}

// A provider that declares non-support is the only case where a caller degrades.
func TestDeclaredCapabilitiesOf_explicitNonSupport(t *testing.T) {
	caps, declared := DeclaredCapabilitiesOf(declaringCompleter{caps: Capabilities{Temperature: true, MaxTokens: true, UsageReporting: true}})
	if !declared {
		t.Fatal("declared should be true")
	}
	if caps.StructuredOutput {
		t.Fatal("StructuredOutput should be false")
	}
	if !(declared && !caps.StructuredOutput) {
		t.Error("the degradation condition should hold for an explicit non-support declaration")
	}
}

func TestCapabilitiesOf_nilSafe(t *testing.T) {
	if caps := CapabilitiesOf(nil); caps != (Capabilities{}) {
		t.Errorf("CapabilitiesOf(nil) = %+v, want zero value", caps)
	}
}
