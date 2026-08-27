package llmfix

import (
	"context"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
	"github.com/asqs/asqs-core/internal/intelligence/model"
)

// recordingCompleter replays canned responses in order and records the CompleteOptions each call
// was made with, so a test can assert whether structured output was requested per turn.
type recordingCompleter struct {
	replies []string
	errs    []error
	opts    []model.CompleteOptions
	msgs    [][]model.Message
}

func (c *recordingCompleter) Complete(_ context.Context, msgs []model.Message, opts model.CompleteOptions) (*model.CompleteResult, error) {
	i := len(c.opts)
	c.opts = append(c.opts, opts)
	c.msgs = append(c.msgs, msgs)
	if i < len(c.errs) && c.errs[i] != nil {
		return nil, c.errs[i]
	}
	if i >= len(c.replies) {
		return &model.CompleteResult{Content: "still not json", StopReason: "stop"}, nil
	}
	return &model.CompleteResult{Content: c.replies[i], StopReason: "stop"}, nil
}

func fixReqOneArtifact() evaluator.FixRequest {
	return evaluator.FixRequest{
		Step:          evaluator.StepCompile,
		Lang:          "csharp",
		ErrorOutput:   "error CS1002: ; expected",
		ArtifactPaths: []string{"tests/FooTests.cs"},
		Files:         map[string]string{"tests/FooTests.cs": "public class FooTests { }"},
	}
}

type recordingFixAudit struct{ steps []string }

func (a *recordingFixAudit) Log(_ context.Context, step string, _ interface{}) {
	a.steps = append(a.steps, step)
}

// If structured output produced the unparseable text, asking for it again on the repair turn
// reproduces the failure. The repair turn must always be unstructured.
func TestFix_repairTurnDropsStructuredOutput(t *testing.T) {
	llm := &recordingCompleter{replies: []string{
		"here is your fix, buddy",                                      // main turn: unparseable
		`{"tests/FooTests.cs":"public class FooTests { void A(){} }"}`, // repair turn: good
	}}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: false}

	resp, err := f.Fix(context.Background(), fixReqOneArtifact())
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("Files = %v, want one entry", resp.Files)
	}
	if len(llm.opts) < 2 {
		t.Fatalf("expected at least a main turn and a repair turn, got %d calls", len(llm.opts))
	}
	if llm.opts[len(llm.opts)-1].Structured != nil {
		t.Fatal("repair turn must not request structured output")
	}
}

// Two failed JSON attempts with exactly one artifact in scope: ask for the raw file instead of
// giving up the whole round. Before this, one unparseable response ended the run.
func TestFix_singleFileFallbackSynthesizesMap(t *testing.T) {
	raw := "using Xunit;\n\npublic class FooTests\n{\n    [Fact]\n    public void A() { }\n}\n"
	llm := &recordingCompleter{replies: []string{
		"sorry, here's the fix:", // main turn
		"still prose, not JSON",  // repair turn
		raw,                      // plain-source fallback
	}}
	aud := &recordingFixAudit{}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true, Audit: aud}

	resp, err := f.Fix(context.Background(), fixReqOneArtifact())
	if err != nil {
		t.Fatalf("Fix should have recovered via the plain-source fallback: %v", err)
	}
	got, ok := resp.Files["tests/FooTests.cs"]
	if !ok {
		t.Fatalf("fallback did not key content by the artifact path: %v", resp.Files)
	}
	if !strings.Contains(got, "[Fact]") {
		t.Fatalf("fallback content lost the test body:\n%s", got)
	}
	found := false
	for _, s := range aud.steps {
		if s == "llmfix.single_file_fallback_used" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected llmfix.single_file_fallback_used audit event, got %v", aud.steps)
	}
}

// The fallback must not become a way to launder garbage onto disk.
func TestFix_singleFileFallbackRejectsSyntacticGarbage(t *testing.T) {
	llm := &recordingCompleter{replies: []string{
		"not json",
		"still not json",
		"```csharp\npublic class FooTests { }\n```", // fenced => SyntacticShellReason rejects
	}}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true}

	if _, err := f.Fix(context.Background(), fixReqOneArtifact()); err == nil {
		t.Fatal("expected an error: a markdown-fenced reply must not be accepted as a fix")
	}
}

// With more than one artifact in scope and nothing — not the failed replies, not the failure
// output — singling one out, there is no unambiguous target for a plain-source reply, so the
// fallback must not fire.
func TestFix_singleFileFallbackSkippedForMultipleArtifacts(t *testing.T) {
	req := fixReqOneArtifact()
	req.ArtifactPaths = append(req.ArtifactPaths, "tests/BarTests.cs")
	req.Files["tests/BarTests.cs"] = "public class BarTests { }"

	llm := &recordingCompleter{replies: []string{"not json", "still not json", "public class X {}"}}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true}

	if _, err := f.Fix(context.Background(), req); err == nil {
		t.Fatal("expected an error; nothing disambiguates a target among the artifacts")
	}
	if len(llm.opts) > 2 {
		t.Fatalf("fallback turn should not have been issued; got %d calls", len(llm.opts))
	}
}

// The generalisation that would have mattered in run api-eb300211385b9616dc6cf81bd513369b's fatal
// rounds: four artifacts in scope, both JSON attempts unusable — but when exactly one in-scope
// artifact is named by the model's own failed replies (or, failing that, by the error output), the
// plain-source recovery has its unambiguous target after all.
func TestFix_plainFallbackTargetsArtifactNamedInReply(t *testing.T) {
	raw := "using Xunit;\n\npublic class BarTests\n{\n    [Fact]\n    public void A() { }\n}\n"
	req := fixReqOneArtifact()
	req.ArtifactPaths = append(req.ArtifactPaths, "tests/BarTests.cs")
	req.Files["tests/BarTests.cs"] = "public class BarTests { }"

	llm := &recordingCompleter{replies: []string{
		"I will fix BarTests.cs as follows: (prose, no JSON)", // main turn names exactly one artifact
		"still prose, not JSON",                               // repair turn
		raw,                                                   // plain-source fallback
	}}
	aud := &recordingFixAudit{}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true, Audit: aud}

	resp, err := f.Fix(context.Background(), req)
	if err != nil {
		t.Fatalf("Fix should have recovered via the reply-named fallback: %v", err)
	}
	if _, ok := resp.Files["tests/BarTests.cs"]; !ok {
		t.Fatalf("fallback keyed the wrong artifact: %v", resp.Files)
	}
	// The ask must name the resolved target, not some other artifact.
	lastMsgs := llm.msgs[len(llm.msgs)-1]
	ask := lastMsgs[len(lastMsgs)-1].Content
	if !strings.Contains(ask, "tests/BarTests.cs") {
		t.Fatalf("fallback ask does not name the resolved target: %q", ask)
	}
	found := false
	for _, s := range aud.steps {
		if s == "llmfix.single_file_fallback_used" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected llmfix.single_file_fallback_used, got %v", aud.steps)
	}
}

func TestFix_plainFallbackTargetsArtifactNamedInErrorOutput(t *testing.T) {
	raw := "using Xunit;\n\npublic class FooTests\n{\n    [Fact]\n    public void A() { }\n}\n"
	req := fixReqOneArtifact()
	req.ArtifactPaths = append(req.ArtifactPaths, "tests/BarTests.cs")
	req.Files["tests/BarTests.cs"] = "public class BarTests { }"
	// The failure names exactly one artifact (by basename, as compiler positions do); the replies
	// name none.
	req.ErrorOutput = "FooTests.cs(12,3): error CS1002: ; expected"

	llm := &recordingCompleter{replies: []string{"not json", "still not json", raw}}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true}

	resp, err := f.Fix(context.Background(), req)
	if err != nil {
		t.Fatalf("Fix should have recovered via the error-named fallback: %v", err)
	}
	if _, ok := resp.Files["tests/FooTests.cs"]; !ok {
		t.Fatalf("fallback keyed the wrong artifact: %v", resp.Files)
	}
}

func TestPlainFallbackTarget(t *testing.T) {
	base := evaluator.FixRequest{ArtifactPaths: []string{"a/OwnerTests.java", "a/PetTests.java"}}
	for _, tc := range []struct {
		name          string
		req           evaluator.FixRequest
		a1, a2        string
		want, wantHow string
	}{
		{name: "single artifact wins outright",
			req:  evaluator.FixRequest{ArtifactPaths: []string{"a/OwnerTests.java"}},
			want: "a/OwnerTests.java", wantHow: "single_artifact_scope"},
		{name: "reply naming one artifact resolves",
			req: base, a1: "editing OwnerTests.java now", want: "a/OwnerTests.java", wantHow: "reply_named_artifact"},
		{name: "reply naming both refuses, error naming one resolves",
			req: func() evaluator.FixRequest {
				r := base
				r.ErrorOutput = "at a/PetTests.java:31"
				return r
			}(), a1: "OwnerTests.java and PetTests.java", want: "a/PetTests.java", wantHow: "error_named_artifact"},
		{name: "nothing names anything",
			req: base, a1: "prose", want: "", wantHow: ""},
		{name: "bare class name is not evidence",
			req: base, a1: "the OwnerTests class is broken", want: "", wantHow: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, how := plainFallbackTarget(tc.req, tc.a1, tc.a2)
			if got != tc.want || how != tc.wantHow {
				t.Fatalf("plainFallbackTarget = (%q, %q), want (%q, %q)", got, how, tc.want, tc.wantHow)
			}
		})
	}
	// Two artifacts sharing a basename, mentioned only by basename: several, not one.
	shared := evaluator.FixRequest{ArtifactPaths: []string{"a/T.java", "b/T.java"}}
	if got, _ := plainFallbackTarget(shared, "see T.java", ""); got != "" {
		t.Fatalf("shared basename must refuse, got %q", got)
	}
}

func TestClassifyFixParseFailure(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "   ", "empty_response"},
		{"prose", "Sure! Here is the corrected file.", "not_json"},
		{"truncated mid-string", `{"a.java":"class A {`, "truncated_json"},
		{"truncated mid-object", `{"a.java":"class A {}"`, "truncated_json"},
		{"complete json", `{"a.java":"class A {}"}`, "not_json"},
		{"braces inside string do not count", `{"a.java":"if (x) { y(); }"}`, "not_json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyFixParseFailure(tc.in); got != tc.want {
				t.Fatalf("classifyFixParseFailure(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// The recovery that was unreachable on a reasoning model. singleFilePlainFallback asks for a raw
// file body and gates the answer on SyntacticShellReason, which rejects a "<think>" prefix by name
// — so before StripReasoningBlock existed the fallback returned (nil, false) for every reply,
// however good. Run api-0c344e6bc0658e0db06506efb9d964f5 had an unambiguous target, ran this path,
// and produced nothing.
func TestFix_plainSourceFallbackRecoversFromAReasoningModel(t *testing.T) {
	body := "public class FooTests { void A(){} }"
	llm := &recordingCompleter{replies: []string{
		"I cannot produce JSON here",                         // main turn: unparseable
		"still not json",                                     // repair turn: unparseable
		"<think>\nLet me write the file.\n</think>\n" + body, // plain-source fallback
	}}
	aud := &payloadAudit{}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true, Audit: aud}

	resp, err := f.Fix(context.Background(), fixReqOneArtifact())
	if err != nil {
		t.Fatalf("the plain-source fallback should have recovered the round: %v", err)
	}
	got := resp.Files["tests/FooTests.cs"]
	if strings.Contains(got, "<think>") {
		t.Errorf("the reasoning block reached the file body: %q", got)
	}
	if strings.TrimSpace(got) != body {
		t.Errorf("recovered content = %q, want the file body alone", got)
	}
	if aud.find("llmfix.single_file_fallback_used") == nil {
		t.Errorf("the recovery must be audited; steps = %v", aud.steps)
	}
}

// A reply that is prose rather than a file body must still be refused — the fallback exists to
// recover a real repair, not to lower the bar for accepting one — and the refusal must now say so.
func TestFix_plainSourceFallbackStillRefusesProse(t *testing.T) {
	llm := &recordingCompleter{replies: []string{
		"I cannot produce JSON here",
		"still not json",
		"<think>reasoning</think>\nSure! Here is what I would change: rename the method.",
	}}
	aud := &payloadAudit{}
	f := &Fixer{LLM: llm, DisableStructuredFixOutput: true, Audit: aud}

	if _, err := f.Fix(context.Background(), fixReqOneArtifact()); err == nil {
		t.Fatal("prose is not a file body and must not be laundered into a write")
	}
	if aud.find("llmfix.single_file_fallback_rejected") == nil {
		t.Errorf("the refusal must be audited so the path is not silent; steps = %v", aud.steps)
	}
}
