package errout

import (
	"strings"
	"testing"
)

// The exact shapes observed in the failing runs: only the positions move between rounds.
func TestSignatureNormalize_stableAcrossLineNumberShifts(t *testing.T) {
	tests := []struct {
		name string
		lang string
		a    string
		b    string
	}{
		{
			name: "maven javac bracket form",
			lang: "java",
			a:    "[ERROR] /workspace/src/test/java/p/OwnerControllerTest.java:[16,63] package org.springframework.boot.test.autoconfigure.web.servlet does not exist",
			b:    "[ERROR] /workspace/src/test/java/p/OwnerControllerTest.java:[33,63] package org.springframework.boot.test.autoconfigure.web.servlet does not exist",
		},
		{
			name: "msbuild paren form",
			lang: "csharp",
			a:    "/workspace/tests/FooTests.cs(11,19): error CS1002: ; expected",
			b:    "/workspace/tests/FooTests.cs(240,19): error CS1002: ; expected",
		},
		{
			name: "colon line:col form",
			lang: "typescript",
			a:    "src/foo.ts:12:3 - error TS2304: Cannot find name 'bar'.",
			b:    "src/foo.ts:120:3 - error TS2304: Cannot find name 'bar'.",
		},
		{
			name: "prose line form",
			lang: "java",
			a:    "failed at line 42, column 7: unexpected token",
			b:    "failed at line 415, column 7: unexpected token",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := SignatureNormalize(tc.lang, tc.a), SignatureNormalize(tc.lang, tc.b); got != want {
				t.Fatalf("signatures differ despite identical diagnostics:\n a: %s\n b: %s", got, want)
			}
		})
	}
}

// Different *diagnostics* must still produce different signatures — otherwise the breaker would
// stop a loop that is genuinely making progress.
func TestSignatureNormalize_distinguishesDifferentDiagnostics(t *testing.T) {
	a := SignatureNormalize("java", "[ERROR] /w/Foo.java:[16,63] package a.b.c does not exist")
	b := SignatureNormalize("java", "[ERROR] /w/Foo.java:[16,63] cannot find symbol: class MockBean")
	if a == b {
		t.Fatal("distinct diagnostics collapsed to the same signature")
	}
	c := SignatureNormalize("java", "[ERROR] /w/Bar.java:[16,63] package a.b.c does not exist")
	if a == c {
		t.Fatal("distinct files collapsed to the same signature")
	}
}

// The file name must survive normalization: the untouched-file detector reads paths out of the
// failure output, and the signature is the operator's correlation key.
func TestSignatureNormalize_keepsFileNames(t *testing.T) {
	got := SignatureNormalize("java", "[ERROR] /w/src/test/java/p/OwnerControllerTest.java:[16,63] boom")
	if !strings.Contains(got, "OwnerControllerTest.java") {
		t.Fatalf("file name lost: %s", got)
	}
	if strings.Contains(got, "[16,63]") {
		t.Fatalf("line/column not normalized: %s", got)
	}
}
