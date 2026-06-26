package llmfix

import (
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// RC4: the anti-pattern block must carry concrete Mockito-misuse guidance so the generator and the
// test-step fixer stop producing the failures observed in the field (mocking Integer/String/final
// framework types, UnnecessaryStubbingException under strict stubs, when() on a non-mock, inventing
// constructors / treating entities as enums).
func TestGarbageTestAntiPatternsBlock_includesMockitoGuidance(t *testing.T) {
	block := GarbageTestAntiPatternsBlock
	wantSubstrings := []string{
		"Mockito misuse",
		"Cannot mock/spy wrapper types",
		"RuntimeHints",
		"MissingMethodInvocation",
		"Mockito mock or spy",
		"STRICT_STUBS",
		"Unnecessary stubbings",
		"lenient",
		"not an enum",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(block, want) {
			t.Errorf("GarbageTestAntiPatternsBlock missing Mockito guidance substring %q", want)
		}
	}
}

// The guidance must actually reach the test-step fixer system prompt (StepTest / StepTestE2E) and
// must not leak into compile-step prompts (which never deal with Mockito strictness).
func TestFixTestGarbageAntiPatternsBlock_carriesMockitoGuidanceOnTestSteps(t *testing.T) {
	for _, step := range []evaluator.SandboxStep{evaluator.StepTest, evaluator.StepTestE2E} {
		got := fixTestGarbageAntiPatternsBlock(evaluator.FixRequest{Step: step, Lang: "java"})
		if !strings.Contains(got, "Mockito misuse") {
			t.Errorf("step %s: fixer test-repair prompt should include Mockito misuse guidance", step)
		}
	}
	if got := fixTestGarbageAntiPatternsBlock(evaluator.FixRequest{Step: evaluator.StepCompile, Lang: "java"}); got != "" {
		t.Errorf("StepCompile should not carry the test garbage/Mockito block; got %q", got)
	}
}
