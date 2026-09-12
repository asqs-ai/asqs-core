package runner

import "github.com/asqs/asqs-core/internal/evaluator"

// testOutputReportsNoTestFiles reports whether a runner exited non-zero because it discovered no
// tests, rather than because a test failed.
//
// Language-keyed because the question is the same in every ecosystem and the answer is not: vitest
// says "No test files found", VSTest says "No test is available in <dll>". Only the JS form was
// recognised, so a C# run that discovered nothing spent its whole fix budget asking the fixer to
// repair tests that were never executed.
func testOutputReportsNoTestFiles(lang, out string) bool {
	switch {
	case isJSLang(lang):
		return jsTestOutputReportsNoTestFiles(out)
	case isCSharpLang(lang):
		return evaluator.DotnetNoTestsRan(out)
	default:
		return false
	}
}
