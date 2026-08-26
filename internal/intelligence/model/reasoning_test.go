package model

import "testing"

const javaBody = "package p;\n\nclass FooTests {\n\t@Test\n\tvoid a() {\n\t}\n}\n"

// The exact shape that made llmfix.singleFilePlainFallback unreachable on a reasoning model: the
// reply is a perfectly good file body behind a chain-of-thought preamble, and the gate that guards
// the recovery rejects "<think>" by name.
func TestStripReasoningBlock_removesALeadingThinkBlock(t *testing.T) {
	reply := "<think>\nThe import should be org.springframework.boot.webmvc.test.autoconfigure.\nLet me write the file.\n</think>\n" + javaBody

	got, dropped := StripReasoningBlock(reply)
	if got != javaBody {
		t.Errorf("content = %q, want the file body alone", got)
	}
	if dropped <= 0 {
		t.Errorf("dropped = %d, want the reasoning counted so the strip is not silent", dropped)
	}
}

func TestStripReasoningBlock_handlesTagVariantsAndWhitespace(t *testing.T) {
	for name, reply := range map[string]string{
		"thinking tag":    "<thinking>reasoning</thinking>\n" + javaBody,
		"leading newline": "\n\n<think>reasoning</think>\n\n" + javaBody,
		"upper case":      "<THINK>reasoning</THINK>\n" + javaBody,
	} {
		t.Run(name, func(t *testing.T) {
			if got, _ := StripReasoningBlock(reply); got != javaBody {
				t.Errorf("content = %q, want the file body alone", got)
			}
		})
	}
}

// The bound that makes this safe. A generated test may legitimately contain the literal text
// "<think>" — asserting on markup, say — and a rule that hunted for the tag anywhere would corrupt
// the file it was meant to rescue.
func TestStripReasoningBlock_onlyTouchesALeadingBlock(t *testing.T) {
	body := "package p;\n\nclass FooTests {\n\tvoid a() {\n\t\tassertThat(html).contains(\"<think>hi</think>\");\n\t}\n}\n"
	got, dropped := StripReasoningBlock(body)
	if got != body || dropped != 0 {
		t.Errorf("a tag inside the answer must be left alone; got dropped=%d content=%q", dropped, got)
	}

	json := `{"src/test/java/p/FooTests.java": "package p;"}`
	if got, dropped := StripReasoningBlock(json); got != json || dropped != 0 {
		t.Errorf("a JSON reply must pass through untouched, got dropped=%d content=%q", dropped, got)
	}
}

// A block that never closes means the reply was cut off mid-thought and holds no answer. Returning
// empty is the honest result: the caller reports an empty completion instead of handing a parser a
// document that was never written.
func TestStripReasoningBlock_unclosedBlockYieldsNothing(t *testing.T) {
	got, dropped := StripReasoningBlock("<think>I should start by checking the imports and then")
	if got != "" {
		t.Errorf("content = %q, want empty: there is no answer in a truncated thought", got)
	}
	if dropped == 0 {
		t.Error("the dropped count must reflect that everything was reasoning")
	}
}

// Stripping happens at the provider boundary AND at the plain-text contract, so it must be safe to
// apply twice.
func TestStripReasoningBlock_isIdempotent(t *testing.T) {
	once, _ := StripReasoningBlock("<think>reasoning</think>\n" + javaBody)
	twice, dropped := StripReasoningBlock(once)
	if twice != once || dropped != 0 {
		t.Errorf("second pass changed the content: dropped=%d content=%q", dropped, twice)
	}
}
