package config

import (
	"strings"
	"testing"
)

// An upstream (enterprise) template must FAIL against core, naming the first key core does not have.
//
// This is the acceptance criterion that keeps the two schemas honestly separate. They share section
// names and most paths, which makes the files look interchangeable — and a lenient loader would have
// accepted an enterprise template while silently ignoring exactly the sections that make it
// enterprise, leaving an operator with a run configured by a file they believed described something
// else.
//
// Core's v2 is NOT a strict subset, and assuming it is would be a mistake worth avoiding here: gap
// caps live at indexer.policy.max_gaps in core and retrieval.plan.max_gaps upstream, and each tree
// has keys the other lacks. So the two schemas are related but independent, and neither file can be
// fed to the other engine.
//
// The v1 detector must not fire here either: an upstream v2 file is not a v1 file, and reporting it
// as one would send someone to rewrite a document whose layout is already right.
func TestUpstreamTemplateIsRejected(t *testing.T) {
	// A minimal document in core's own layout carrying an enterprise-only section. Written inline
	// rather than read from the sibling checkout, because a test that silently skips when a path is
	// missing is a test that stops running.
	upstream := []byte(`schema_version: 2
general:
  database:
    metadata_url: postgres://x
serve:
  listen_address: ":8080"
`)
	_, err := UnmarshalSchemaV2(upstream)
	if err == nil {
		t.Fatal("an enterprise template loaded against core; the serve section was silently ignored")
	}
	msg := err.Error()
	if !strings.Contains(msg, "serve") {
		t.Errorf("the error does not name the enterprise-only section:\n%s", msg)
	}
	if strings.Contains(msg, "pre-v2 layout") {
		t.Errorf("an upstream v2 file was misreported as v1, which would send an operator to rewrite "+
			"a correctly-shaped document:\n%s", msg)
	}
}

// The same must hold for an enterprise-only KEY inside a section core does have — the subtler case,
// since the section name gives no warning.
func TestUpstreamOnlyKeyInSharedSectionIsRejected(t *testing.T) {
	doc := []byte(`general:
  database:
    metadata_url: postgres://x
  llm:
    provider: ollama
    prompt_caching: true
`)
	_, err := UnmarshalSchemaV2(doc)
	if err == nil {
		t.Fatal("an enterprise-only key inside general.llm was accepted")
	}
	if !strings.Contains(err.Error(), "prompt_caching") {
		t.Errorf("the error does not name the offending key:\n%v", err)
	}
}
