package evaluator

import "testing"

// A model that wraps its whole answer in one markdown fence has produced the right file with the
// wrong envelope. The fix path has unwrapped this since it existed (llmfix.extractFixResponseCodeBlock);
// the generate path only refused it, and on the generate path a refusal destroys the artifact.
// asqs-go run api-8a6ad5508ad43b22f3ff969b2d1da5cd lost e2e/routes/checkout.spec.ts that way, and
// api-fd5599a24f84dbe71e0c831b3266e4f4 lost a .ts spec before it.
func TestUnwrapSingleCodeFence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{
			name:   "fenced with language tag",
			in:     "```typescript\nimport { test } from '@playwright/test';\ntest('a', async () => {});\n```",
			want:   "import { test } from '@playwright/test';\ntest('a', async () => {});",
			wantOK: true,
		},
		{
			name:   "fenced with no language tag",
			in:     "```\nclass A {}\n```",
			want:   "class A {}",
			wantOK: true,
		},
		{
			name:   "leading and trailing whitespace around the fence",
			in:     "\n\n```ts\nconst a = 1;\n```\n\n",
			want:   "const a = 1;",
			wantOK: true,
		},
		{
			name:   "unfenced content is returned untouched",
			in:     "class A {}\n",
			want:   "class A {}\n",
			wantOK: false,
		},
		{
			name: "prose before the fence is not a wrapper",
			in:   "Here is the file:\n```ts\nconst a = 1;\n```",
			want: "Here is the file:\n```ts\nconst a = 1;\n```",
		},
		{
			name: "prose after the fence is not a wrapper",
			in:   "```ts\nconst a = 1;\n```\nHope that helps!",
			want: "```ts\nconst a = 1;\n```\nHope that helps!",
		},
		{
			name: "two blocks are not one wrapper",
			in:   "```ts\nconst a = 1;\n```\n```ts\nconst b = 2;\n```",
			want: "```ts\nconst a = 1;\n```\n```ts\nconst b = 2;\n```",
		},
		{
			name: "an inner fence survives unwrapping and must not be accepted",
			in:   "```ts\n// see ```bash\nconst a = 1;\n```",
			want: "```ts\n// see ```bash\nconst a = 1;\n```",
		},
		{
			name: "a language tag with spaces is prose, not a fence info string",
			in:   "```here is the file\nconst a = 1;\n```",
			want: "```here is the file\nconst a = 1;\n```",
		},
		{
			name: "an empty fenced block yields nothing usable",
			in:   "```ts\n\n```",
			want: "```ts\n\n```",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := UnwrapSingleCodeFence(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("content =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// The unwrapped body must still face the gate: unwrapping is an envelope repair, not a waiver.
func TestUnwrapSingleCodeFence_unwrappedBodyStillGated(t *testing.T) {
	const fenced = "```java\npackage p;\n\nclass FooTest {\n}\n```"
	body, ok := UnwrapSingleCodeFence(fenced)
	if !ok {
		t.Fatal("expected the wrapper to be recognised")
	}
	if reason := SyntacticShellReason("src/test/java/p/FooTest.java", body); reason != "" {
		t.Fatalf("unwrapped body should clear the fence check, got %q", reason)
	}
	if reason := EmptyTestFileReason("src/test/java/p/FooTest.java", body); reason == "" {
		t.Fatal("an unwrapped body with no @Test must still be refused as an empty test file")
	}
}
