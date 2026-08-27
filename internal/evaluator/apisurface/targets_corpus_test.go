package apisurface

import (
	"strings"
	"testing"
)

// A corpus of REAL compiler output, captured by compiling deliberately broken sources — not written
// from memory of what a diagnostic looks like.
//
// It exists because the per-shape regexes this file used to carry were only ever discovered by a
// production run stalling on an uncovered one. That is an expensive discovery channel: run
// api-e08817ff5df431f6bb8f1fb92e7659a2 spent twenty-five minutes and its whole output learning that
// `symbol: variable` was unhandled. A corpus turns the same discovery into a failing test.
//
// The javac entries were produced with javac 17 against a small library (a class with a static
// constant, a private method, a nested type; an enum; an interface).
//
// They were collected through the javax.tools JavaCompiler API with a DiagnosticCollector, NOT by
// reading javac's console output, because those two disagree in a way that matters here:
//
//	console javac:  location: variable w of type Widget
//	JavaCompiler:   location: variable w of type dep.Widget
//
// javac's console formatter simplifies a type to its simple name when that is unambiguous;
// Diagnostic.getMessage() does not. maven-compiler-plugin drives the API, so the fully-qualified
// form is what ASQS actually parses — and it is what the real run's audit log shows, down to the
// `path:[line,col]` prefix. A corpus captured from the console would have been a corpus of a shape
// production never sees, and would have quietly mis-tested the Java-only dotless rule in
// IsUninterestingTypeForLang.
//
// Adding a case is the intended way to extend coverage: write the broken source, compile it through
// the API path, paste the message verbatim, and assert the target it should yield.
type diagnosticCase struct {
	name string
	lang Lang
	// output is verbatim compiler stderr.
	output string
	// wantType, when set, is a KindType target that must be produced: the type whose member list
	// answers the diagnostic, and the member it rejected.
	wantType, wantMember string
	// wantSymbol, when set, is a KindSymbol target that must be produced: a name whose package the
	// classpath search has to supply.
	wantSymbol string
	// wantNone asserts the diagnostic yields nothing. Used for the shapes that carry no usable
	// lookup, so that a future pattern which starts matching them shows up here.
	wantNone bool
}

var javacCorpus = []diagnosticCase{
	{
		name: "missing method on a receiver variable",
		lang: LangJava,
		output: `cases/MissingMethodOnVar.java:[2,66] cannot find symbol
  symbol:   method rename(java.lang.String)
  location: variable w of type dep.Widget
`,
		wantType: "dep.Widget", wantMember: "rename",
	},
	{
		name: "missing static method on a type name",
		lang: LangJava,
		output: `cases/MissingMethodStatic.java:[2,46] cannot find symbol
  symbol:   method build(int)
  location: class dep.Widget
`,
		wantType: "dep.Widget", wantMember: "build",
	},
	{
		// The shape that stalled api-e08817ff5df431f6bb8f1fb92e7659a2: a method read as a property,
		// i.e. a missing `()`. javac says `variable`, not `method`.
		name: "missing field on a receiver variable",
		lang: LangJava,
		output: `cases/MissingFieldOnVar.java:[2,76] cannot find symbol
  symbol:   variable name
  location: variable w of type dep.Widget
`,
		wantType: "dep.Widget", wantMember: "name",
	},
	{
		name: "missing constant on a type",
		lang: LangJava,
		output: `cases/MissingConstOnType.java:[2,53] cannot find symbol
  symbol:   variable CEILING
  location: class dep.Widget
`,
		wantType: "dep.Widget", wantMember: "CEILING",
	},
	{
		// Extremely common and previously unhandled: the model invents an enum constant.
		name: "wrong enum constant",
		lang: LangJava,
		output: `cases/MissingEnumConst.java:[2,54] cannot find symbol
  symbol:   variable BLUE
  location: class dep.Colour
`,
		wantType: "dep.Colour", wantMember: "BLUE",
	},
	{
		name: "unimported type in a declaration",
		lang: LangJava,
		output: `cases/UnimportedTypeDecl.java:[1,39] cannot find symbol
  symbol:   class Widget
  location: class UnimportedTypeDecl
`,
		wantSymbol: "Widget",
	},
	{
		// Also previously unhandled: an unimported type used as a static access reads to javac as a
		// variable in the enclosing class, not as a class.
		name: "unimported type in an expression",
		lang: LangJava,
		output: `cases/UnimportedTypeExpr.java:[1,50] cannot find symbol
  symbol:   variable Widget
  location: class UnimportedTypeExpr
`,
		wantSymbol: "Widget",
	},
	{
		name: "missing nested type",
		lang: LangJava,
		output: `cases/MissingNestedType.java:[2,44] cannot find symbol
  symbol:   class Outer
  location: class dep.Widget
`,
		wantType: "dep.Widget", wantMember: "Outer",
	},
	{
		name: "missing method on an interface-typed receiver",
		lang: LangJava,
		output: `cases2/InterfaceMemberMissing.java:[2,53] cannot find symbol
  symbol:   method process(java.lang.String)
  location: variable h of type dep.Handler
`,
		wantType: "dep.Handler", wantMember: "process",
	},
	{
		name: "constructor arity",
		lang: LangJava,
		output: `cases/CtorArity.java:[2,41] constructor Widget in class dep.Widget cannot be applied to given types;
  required: int
  found:    int,int
  reason: actual and formal argument lists differ in length
`,
		wantType: "dep.Widget", wantMember: "Widget",
	},
	{
		name: "inaccessible member",
		lang: LangJava,
		output: `cases/PrivateAccess.java:[2,47] secret() has private access in dep.Widget
`,
		wantType: "dep.Widget", wantMember: "secret",
	},
	{
		name: "unresolved overload routes to the type that can take the argument",
		lang: LangJava,
		output: `cases2/OverloadNoSuitable.java:[2,46] no suitable method found for assertThat(int)
    method dep.Asserts.assertThat(java.lang.String) is not applicable
      (argument mismatch; int cannot be converted to java.lang.String)
    method dep.Asserts.assertThat(dep.Handler) is not applicable
      (argument mismatch; int cannot be converted to dep.Handler)
`,
		wantType: "org.assertj.core.api.Assertions", wantMember: "assertThat",
	},
	{
		// A genuine undefined local. The enclosing class becomes a type target (FilterOwnedTypes
		// drops it downstream), but the lowercase name must NOT become a classpath search: every
		// candidate would be rendered to the model as an import suggestion.
		name: "undefined local does not become a symbol search",
		lang: LangJava,
		output: `cases/UndefinedLocal.java:[1,43] cannot find symbol
  symbol:   variable orderId
  location: class UndefinedLocal
`,
	},
	{
		name: "missing type argument",
		lang: LangJava,
		output: `cases/MissingTypeArg.java:[2,40] cannot find symbol
  symbol:   class Strng
  location: class MissingTypeArg
`,
		wantSymbol: "Strng",
	},
	{
		name: "unresolved import package",
		lang: LangJava,
		output: `cases/MissingPackage.java:[1,19] package dep.nowhere does not exist
cases/MissingPackage.java:[2,35] cannot find symbol
  symbol:   class Thing
  location: class MissingPackage
`,
		wantSymbol: "Thing",
	},
}

// The families that carry no usable lookup today. They are pinned so that the day one of them
// starts producing a target, it is a deliberate change with a test behind it rather than a
// surprise. `is not abstract and does not override` is the strongest candidate of these — the
// supertype's member list is exactly the contract a generated test double has to satisfy.
var javacUncoveredCorpus = []diagnosticCase{
	{
		name: "not abstract / unimplemented interface method",
		lang: LangJava,
		output: `cases/NotAbstract.java:[2,1] NotAbstract is not abstract and does not override abstract method handle(java.lang.String) in dep.Handler
`,
	},
	{
		name: "anonymous class missing an interface method",
		lang: LangJava,
		output: `cases2/AnonImplMissing.java:[2,62] <anonymous AnonImplMissing$1> is not abstract and does not override abstract method handle(java.lang.String) in dep.Handler
`,
	},
	{
		name: "unreported checked exception",
		lang: LangJava,
		output: `cases2/UnreportedException.java:[2,53] unreported exception java.io.IOException; must be caught or declared to be thrown
`,
	},
	{
		name: "generic bound violation",
		lang: LangJava,
		output: `cases2/GenericBounds.java:[2,42] type argument java.lang.String is not within bounds of type-variable T
`,
	},
	{
		name: "incompatible types",
		lang: LangJava,
		output: `cases/IncompatibleTypes.java:[2,68] incompatible types: dep.Widget.Inner cannot be converted to java.lang.String
`,
	},
	{
		name: "argument type mismatch on a non-overloaded call",
		lang: LangJava,
		output: `cases/WrongArgTypes.java:[2,55] incompatible types: java.lang.String cannot be converted to int
`,
	},
	{
		name: "bad operand types",
		lang: LangJava,
		output: `cases/BadOperand.java:[2,57] bad operand types for binary operator '>'
  first type:  dep.Widget
  second type: int
`,
	},
	{
		name: "@Override on a method that overrides nothing",
		lang: LangJava,
		output: `cases/BadOverride.java:[2,64] method does not override or implement a method from a supertype
`,
	},
}

// Roslyn and tsc. Before these patterns existed, every entry here produced nothing, on every fix
// round of every C# and TypeScript run.
var roslynAndTscCorpus = []diagnosticCase{
	{
		name:     "CS1061 missing member",
		lang:     LangCSharp,
		output:   `/w/tests/FooTests.cs(7,13): error CS1061: 'Widget' does not contain a definition for 'Rename' and no accessible extension method 'Rename' accepting a first argument of type 'Widget' could be found (are you missing a using directive or an assembly reference?) [/w/tests/Tests.csproj]`,
		wantType: "Widget", wantMember: "Rename",
	},
	{
		name:     "CS0117 missing constant",
		lang:     LangCSharp,
		output:   `/w/tests/FooTests.cs(9,20): error CS0117: 'Colour' does not contain a definition for 'Blue' [/w/tests/Tests.csproj]`,
		wantType: "Colour", wantMember: "Blue",
	},
	{
		name:       "CS0246 missing type",
		lang:       LangCSharp,
		output:     `/w/tests/FooTests.cs(3,9): error CS0246: The type or namespace name 'IAPIRequestArgs' could not be found (are you missing a using directive or an assembly reference?) [/w/tests/Tests.csproj]`,
		wantSymbol: "IAPIRequestArgs",
	},
	{
		name:       "CS0103 name not in context",
		lang:       LangCSharp,
		output:     `/w/tests/FooTests.cs(5,9): error CS0103: The name 'RequestOptions' does not exist in the current context [/w/tests/Tests.csproj]`,
		wantSymbol: "RequestOptions",
	},
	{
		name:     "CS1729 constructor arity",
		lang:     LangCSharp,
		output:   `/w/tests/FooTests.cs(6,9): error CS1729: 'Widget' does not contain a constructor that takes 2 arguments [/w/tests/Tests.csproj]`,
		wantType: "Widget", wantMember: "Widget",
	},
	{
		name:     "CS0122 inaccessible",
		lang:     LangCSharp,
		output:   `/w/tests/FooTests.cs(8,9): error CS0122: 'Widget.secret()' is inaccessible due to its protection level [/w/tests/Tests.csproj]`,
		wantType: "Widget", wantMember: "secret",
	},
	{
		name:     "TS2339 missing property",
		lang:     LangNode,
		output:   `src/foo.spec.ts(12,31): error TS2339: Property 'reqest' does not exist on type 'Playwright'.`,
		wantType: "Playwright", wantMember: "reqest",
	},
	{
		// tsc has already computed the correction here; resolving the type surfaces it either way.
		name:     "TS2551 near-miss property",
		lang:     LangNode,
		output:   `src/foo.spec.ts(14,9): error TS2551: Property 'hasStatuss' does not exist on type 'APIResponseAssertions'. Did you mean 'hasStatus'?`,
		wantType: "APIResponseAssertions", wantMember: "hasStatuss",
	},
	{
		name:       "TS2304 cannot find name",
		lang:       LangNode,
		output:     `src/foo.spec.ts(3,1): error TS2304: Cannot find name 'RequestOptions'.`,
		wantSymbol: "RequestOptions",
	},
	{
		// The TypeScript spelling of the invented-type failure that stalled the Java run.
		name:       "TS2305 no exported member",
		lang:       LangNode,
		output:     `src/foo.spec.ts(1,10): error TS2305: Module '"playwright"' has no exported member 'APIRequestArgs'.`,
		wantSymbol: "APIRequestArgs",
	},
	{
		// An inline type literal names no declaration a provider can resolve, so it must not spend
		// one of the bounded target slots on a guaranteed miss.
		name:     "TS2339 on an inline literal type yields nothing",
		lang:     LangNode,
		output:   `src/foo.spec.ts(9,3): error TS2339: Property 'x' does not exist on type '{ a: string; }'.`,
		wantNone: true,
	},
}

func runCorpus(t *testing.T, cases []diagnosticCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// FilterUninterestingTypes is applied because lookupAPISurface applies it, and a target
			// the filter drops is a target the fixer never sees. Its dotless rule is Java-only for
			// exactly this reason.
			got := FilterUninterestingTypes(c.lang, ParseTargets(c.output))
			if c.wantNone {
				if len(got) != 0 {
					t.Fatalf("want no targets, got %v", got)
				}
				return
			}
			if c.wantType != "" {
				var ok bool
				for _, g := range got {
					if g.Kind == KindType && g.Name == c.wantType && g.Member == c.wantMember {
						ok = true
					}
				}
				if !ok {
					t.Errorf("want type:%s#%s, got %v", c.wantType, c.wantMember, got)
				}
			}
			if c.wantSymbol != "" {
				var ok bool
				for _, g := range got {
					if g.Kind == KindSymbol && g.Name == c.wantSymbol {
						ok = true
					}
				}
				if !ok {
					t.Errorf("want symbol:%s, got %v", c.wantSymbol, got)
				}
			}
			// A lowercase name must never become a classpath search, whatever else the diagnostic
			// produced. See javacNameIsUnresolvedType.
			for _, g := range got {
				if g.Kind == KindSymbol && g.Name != "" && !startsUpperASCII(g.Name) {
					t.Errorf("lowercase name became a classpath search: %v", g)
				}
			}
		})
	}
}

func TestCorpus_javacCoveredShapes(t *testing.T) { runCorpus(t, javacCorpus) }
func TestCorpus_roslynAndTscShapes(t *testing.T) { runCorpus(t, roslynAndTscCorpus) }

// The uncovered families, pinned as uncovered.
func TestCorpus_javacUncoveredShapesArePinned(t *testing.T) {
	for _, c := range javacUncoveredCorpus {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseTargets(c.output); len(got) != 0 {
				t.Fatalf("this shape now yields %v — if that is intended, move the case into javacCorpus with its expected target", got)
			}
		})
	}
}

// Every `cannot find symbol` case must name a target, because that family is parsed structurally
// rather than per shape: a pair nobody anticipated still resolves through the same path.
func TestCorpus_everyCannotFindSymbolCaseResolves(t *testing.T) {
	for _, c := range javacCorpus {
		if !strings.Contains(c.output, "cannot find symbol") {
			continue
		}
		if got := ParseTargets(c.output); len(got) == 0 {
			t.Errorf("%s: a cannot-find-symbol diagnostic produced no target at all", c.name)
		}
	}
}
