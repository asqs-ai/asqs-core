package evaluator

import "strings"

import "testing"

// The escape gate disabled itself for the WHOLE FILE when it saw `"""`, `@"` or `@$"` anywhere.
// One verbatim path or SQL string — which is exactly what a C# test of a repository layer contains
// — switched off the check for every ordinary literal in the same file, and an unrepaired `\d`
// then cost a compile round.
//
// stringLiteralEnd already tokenises every C# string form, so the scan can skip the literals it
// does not model and keep checking the ones it does.
func TestIllegalEscapeReason_checksOrdinaryLiteralsBesideAVerbatimOne(t *testing.T) {
	const src = `using System;
public class T
{
    const string Root = @"C:\builds\shop";
    public void M()
    {
        var pattern = "\d+";
    }
}`
	got := illegalEscapeReason(src, ".cs")
	if got == "" {
		t.Fatal("no reason: the ordinary literal's illegal escape went unchecked because a verbatim string was present")
	}
	if !strings.Contains(got, `\d`) {
		t.Errorf("reason = %q, want it to name the illegal escape", got)
	}
}

// The verbatim literal itself must never be reported: inside @"…" a backslash IS a backslash, and
// `C:\dir` is correct C#. Rejecting it would destroy a correct file.
func TestIllegalEscapeReason_verbatimBackslashIsNotAnEscape(t *testing.T) {
	for _, src := range []string{
		`public class T { const string P = @"C:\dir\sub"; }`,
		`public class T { const string P = $@"C:\{x}\sub"; }`,
		`public class T { const string P = @$"C:\{x}\sub"; }`,
		"public class T { const string P = \"\"\"\n        C:\\dir\\sub\n        \"\"\"; }",
	} {
		if got := illegalEscapeReason(src, ".cs"); got != "" {
			t.Errorf("illegalEscapeReason(%q) = %q, want none", src, got)
		}
	}
}

// A verbatim string doubles a quote to escape it. Losing sync there would make the rest of the file
// read as a string, or as code when it is a string.
func TestIllegalEscapeReason_verbatimDoubledQuote(t *testing.T) {
	const src = `public class T
{
    const string Sql = @"SELECT ""Id"" FROM ""Orders"" WHERE Path LIKE 'C:\%'";
    public void M() { var bad = "\d"; }
}`
	got := illegalEscapeReason(src, ".cs")
	if got == "" {
		t.Fatal("the scanner lost the ordinary literal after a verbatim string with doubled quotes")
	}
	if !strings.Contains(got, `\d`) {
		t.Errorf("reason = %q, want it to name the illegal escape after the verbatim literal", got)
	}
}

// The repair path shares the bail rules, so it has to gain the same reach — a gate that rejects
// what the repairer will not repair spends the round it was meant to save.
func TestRepairIllegalEscapes_repairsBesideAVerbatimString(t *testing.T) {
	const src = `public class T
{
    const string Root = @"C:\builds";
    public void M() { var p = "\d+"; }
}`
	out, repairs := RepairIllegalEscapes("T.cs", src)
	if len(repairs) == 0 {
		t.Fatal("no repair: the ordinary literal was skipped because a verbatim string was present")
	}
	if !strings.Contains(out, `"\\d+"`) {
		t.Errorf("output did not double the escape:\n%s", out)
	}
	if !strings.Contains(out, `@"C:\builds"`) {
		t.Errorf("the verbatim literal was altered:\n%s", out)
	}
}
