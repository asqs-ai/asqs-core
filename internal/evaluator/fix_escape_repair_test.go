package evaluator

import (
	"strings"
	"testing"
)

// Every shape TestIllegalEscapeReason_RejectsJava rejects inside a STRING has one mechanical
// repair; after it the gate must accept the file. Char literals stay rejections.
func TestRepairIllegalEscapes_java(t *testing.T) {
	const path = "src/test/java/p/OwnerControllerE2EIT.java"
	cases := []struct{ name, body, want string }{
		{"regex digit class", `assertThat(s).matches("\d+");`, `assertThat(s).matches("\\d+");`},
		{"regex word class", `Pattern.compile("^\w+$");`, `Pattern.compile("^\\w+$");`},
		{"escaped forward slash", `page.locator("a[href=\/owners]");`, `page.locator("a[href=\\/owners]");`},
		{"windows path keeps the legal tab escape", `Path.of("C:\temp\out.txt");`, `Path.of("C:\temp\\out.txt");`},
		{"several in one literal", `String r = "\d{2}\.\d{2}";`, `String r = "\\d{2}\\.\\d{2}";`},
		{"already doubled stays", `Pattern.compile("\\d+");`, `Pattern.compile("\\d+");`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, repairs := RepairIllegalEscapes(path, javaFile(tc.body))
			if got != javaFile(tc.want) {
				t.Fatalf("repaired =\n%s\nwant\n%s", got, javaFile(tc.want))
			}
			if tc.body != tc.want && len(repairs) == 0 {
				t.Fatal("no repairs reported")
			}
			if tc.body == tc.want && len(repairs) != 0 {
				t.Fatalf("spurious repairs %v", repairs)
			}
			if reason := IllegalEscapeReason(path, got); reason != "" {
				t.Fatalf("gate still rejects the repaired file: %s", reason)
			}
		})
	}
	// Repairs carry the line and the escape as written.
	_, repairs := RepairIllegalEscapes(path, javaFile(`String r = "\d";`))
	if len(repairs) != 1 || repairs[0].Escape != `\d` || repairs[0].Line != 6 {
		t.Fatalf("repairs = %+v", repairs)
	}
	if got := DescribeEscapeRepairs(repairs); got != `\d at line 6` {
		t.Fatalf("DescribeEscapeRepairs = %q", got)
	}
}

func TestRepairIllegalEscapes_leavesLegalAndUnmodelledInputAlone(t *testing.T) {
	const path = "src/test/java/p/OwnerControllerE2EIT.java"
	untouched := []string{
		`String s = "line\nnext";`,
		`String s = "back\\slash";`,
		`String s = "unicode\u0041";`,
		`String s = "octal\101 and \0 and \377";`,
		`char c = '\d';`,                   // a doubled char literal does not compile either
		`String s = "no escapes at all";`,  // nothing to do
		`// "\d" in a comment is not code`, // comments are skipped
		`/* "\d" */ String s = "";`,        // block comments too
		"String s = \"\"\"\n\\d\n\"\"\";",  // text block: unmodelled, bail
	}
	for _, body := range untouched {
		got, repairs := RepairIllegalEscapes(path, javaFile(body))
		if got != javaFile(body) || len(repairs) != 0 {
			t.Errorf("%q was modified: %v\n%s", body, repairs, got)
		}
	}
	// Other languages are never touched.
	src := `const re = "\d+";`
	if got, repairs := RepairIllegalEscapes("src/x.test.ts", src); got != src || len(repairs) != 0 {
		t.Errorf("TypeScript modified: %v", repairs)
	}
}

func TestRepairIllegalEscapes_csharp(t *testing.T) {
	const path = "tests/P.Tests/OwnerControllerE2ETests.cs"
	got, repairs := RepairIllegalEscapes(path, csFile(`var re = new Regex("\d+");`))
	if !strings.Contains(got, `new Regex("\\d+")`) || len(repairs) != 1 {
		t.Fatalf("got %v\n%s", repairs, got)
	}
	verbatim := csFile(`var re = new Regex(@"\d+");`)
	if got, repairs := RepairIllegalEscapes(path, verbatim); got != verbatim || len(repairs) != 0 {
		t.Fatalf("verbatim string modified: %v", repairs)
	}
}
