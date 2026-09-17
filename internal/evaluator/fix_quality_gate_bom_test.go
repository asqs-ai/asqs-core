package evaluator

import (
	"strings"
	"testing"
)

// bomRune is written as an escape: a literal byte-order mark in Go source is itself a compile error
// outside the first position, which is a small demonstration of why this gate had an opinion at all.
const bomRune = "\ufeff"

// A UTF-8 byte-order mark opens a large share of the .cs files in the world: Visual Studio writes
// one by default, so an EXISTING repository file starts with it and every extend-mode payload
// inherits it after the merge. The gate called it "illegal" and refused the write.
//
// Verified against the compiler rather than assumed: a .cs file whose first three bytes are the BOM
// builds with 0 errors (dotnet build, SDK 10.0.201).
func TestSyntacticShellReason_leadingByteOrderMarkIsNotIllegal(t *testing.T) {
	body := "using System;\n\npublic class FooTests\n{\n    [Fact]\n    public void Works() { }\n}\n"

	if reason := SyntacticShellReason("tests/FooTests.cs", bomRune+body); reason != "" {
		t.Fatalf("refused a file that opens with a BOM: %s", reason)
	}
	if reason := SyntacticShellReason("tests/FooTests.cs", body); reason != "" {
		t.Fatalf("refused the same file without a BOM: %s", reason)
	}
}

// A BOM anywhere other than the first character is a real stray character in code position, and the
// compiler does reject it. Skipping the leading one must not blind the scan to the rest.
func TestSyntacticShellReason_byteOrderMarkInsideTheFileIsStillIllegal(t *testing.T) {
	body := "using System;\n\npublic class FooTests\n{\n    [Fact]\n    public void " + bomRune + "Works() { }\n}\n"
	reason := SyntacticShellReason("tests/FooTests.cs", body)
	if reason == "" {
		t.Fatal("a BOM in the middle of the file must still be refused")
	}
	if !strings.Contains(reason, "illegal character") {
		t.Fatalf("reason = %q; want it to name the illegal character", reason)
	}
}
