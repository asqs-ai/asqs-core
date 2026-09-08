package evaluator

import "testing"

// asqs-go run api-cfc3279416a7d8c7d2690799800c7647, rounds 4-7: the same unhandled rejection at
// billing.service.test.ts:45 while the source excerpt jest printed under it alternated, because
// the fixer inserted and removed a statement above the failing one. The identity must survive
// that, and a different error at the same line must still reset it.
func TestPrimarySiteStreakSignature_ignoresShiftedSourceExcerpt(t *testing.T) {
	head := "> nestjs-test@0.0.1 test:asqs\n> jest\n\nPASS src/common/filters/http-exception.filter.test.ts\n"
	tail := "Node.js v22.23.2\n"
	a := head + "/workspace/src/billing/billing.service.test.ts:45\n            const error = new Error('Network Failure');\n                          ^\n\nError: Network Failure\n    at Object.<anonymous> (/workspace/src/billing/billing.service.test.ts:45:51)\n    at Promise.then.completed (/workspace/node_modules/jest-circus/build/utils.js:298:28)\n" + tail
	b := head + "/workspace/src/billing/billing.service.test.ts:45\n            mockHttpClient.ping.mockRejectedValue(new Error('Network Failure'));\n                                                  ^\n\nError: Network Failure\n    at Object.<anonymous> (/workspace/src/billing/billing.service.test.ts:45:51)\n    at Promise.then.completed (/workspace/node_modules/jest-circus/build/utils.js:298:28)\n" + tail
	site := ParsePrimaryFailureSite(a)
	if !site.OK {
		t.Fatal("fixture must yield a primary site")
	}
	sa, sb := primarySiteStreakSignature("typescript", site, a), primarySiteStreakSignature("typescript", site, b)
	if sa != sb {
		t.Fatalf("a shifted source excerpt must not change the streak identity: %s vs %s", sa, sb)
	}
	c := head + "/workspace/src/billing/billing.service.test.ts:45\n            const error = new Error('Network Failure');\n                          ^\n\nTypeError: mockHttpClient.ping is not a function\n    at Object.<anonymous> (/workspace/src/billing/billing.service.test.ts:45:51)\n" + tail
	if sc := primarySiteStreakSignature("typescript", site, c); sc == sa {
		t.Fatal("a different error at the same line must produce a different identity")
	}
}

func TestStreakSignatureBasis_keepsDiagnosticLinesOnly(t *testing.T) {
	block := "[ERROR] /w/src/test/java/p/FooTest.java:[5,6] cannot find symbol\n  symbol:   class Test\n  location: class p.FooTest\n        Foo x = new Foo();\n              ^\n"
	want := "[ERROR] /w/src/test/java/p/FooTest.java:[5,6] cannot find symbol\n  symbol:   class Test\n  location: class p.FooTest"
	if got := streakSignatureBasis(block); got != want {
		t.Fatalf("basis =\n%s\nwant\n%s", got, want)
	}
	if got := streakSignatureBasis("        only excerpt\n    ^\n"); got != "        only excerpt\n    ^\n" {
		t.Fatalf("a block with no diagnostic line must fall back to itself, got %q", got)
	}
}
