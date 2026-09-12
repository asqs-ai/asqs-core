package extendmerge

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/asqs/asqs-core/internal/evaluator"
)

// Recovery paths for generated payloads that the write gates used to drop outright.
//
// asqs-go run api-5a67a414d4ba22496fcc23e1143076fa (Java) and api-d01f66ab5b4c58f8e129844d98f8e370
// (NestJS) lost four generated tests at write time between them: a methods-only body returned for
// a NEW file, a follower told to extend a file its owner never created, and a full JS module with
// two top-level suites that the single-suite unwrap could not reduce. Each was a payload with
// usable tests inside; the gates were right that it could not be written AS IS, and wrong that
// nothing could be done. These helpers do the deterministic part and leave anything ambiguous to
// the previous behaviour (skip), so a recovery never writes something the gates would have refused.

// wrapJavaMembersAsTestClass turns a methods-only Java payload destined for a NEW file into a
// compilation unit: the package the path implies, the imports the payload declared at its top
// plus the framework imports its body evidently needs (javaInferredImports), and a class named
// after the file. When it declines, the returned reason says which condition failed — asqs-go run
// api-0e0b356b780481deb471faa745dab0bc lost OrderService#createOrder to a silent refusal.
func wrapJavaMembersAsTestClass(path, payload string) (out string, declined string, ok bool) {
	return wrapMembersAsTestClass(path, payload, ".java")
}

// wrapCSharpMembersAsTestClass is the C# counterpart: usings hoisted and inferred
// (csharpInferredUsings), a file-scoped namespace kept when the fragment declared one, and a
// public class named after the file.
func wrapCSharpMembersAsTestClass(path, payload string) (out string, declined string, ok bool) {
	return wrapMembersAsTestClass(path, payload, ".cs")
}

// wrapMembersAsTestClass is the shared implementation for the two languages whose test files are
// a type body: it is the shape javac/roslyn demand, and the path determines every part of the
// shell the fragment lacks.
func wrapMembersAsTestClass(path, payload, wantExt string) (string, string, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != wantExt {
		return "", "not a " + wantExt + " file", false
	}
	body := strings.ReplaceAll(strings.TrimSpace(payload), "\r\n", "\n")
	if body == "" {
		return "", "empty payload", false
	}
	if strings.Contains(body, "```") {
		return "", "markdown fence in payload", false
	}
	if reason := evaluator.EmptyTestFileReason(path, body); reason != "" {
		return "", reason, false
	}
	// A package/namespace line without a type is still a fragment: the shell is what is missing,
	// and the path decides the package anyway. A file-scoped C# namespace is kept.
	var namespace string
	body, namespace = stripLeadingPackageLine(body, ext)
	// Imports are lifted first: with them still in place the payload reads as a compilation unit,
	// and that is the shape this helper must NOT touch (it already has its shell).
	imports, remainder := hoistTopLevelImports(path, body)
	remainder = strings.TrimSpace(remainder)
	if remainder == "" {
		return "", "nothing but imports in payload", false
	}
	if kind := classifyExtendPayload(path, remainder); kind != payloadMembersOnly {
		return "", "payload already declares a type of its own", false
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !javaIdentifierRE.MatchString(name) {
		return "", fmt.Sprintf("file name %q is not a valid type name", name), false
	}
	table := javaInferredImports
	if ext == ".cs" {
		table = csharpInferredUsings
	}
	imports = append(imports, inferredImportsFor(remainder, table, imports, ext)...)
	var b strings.Builder
	switch ext {
	case ".java":
		if pkg := javaPackageForPath(path); pkg != "" {
			fmt.Fprintf(&b, "package %s;\n\n", pkg)
		}
		for _, d := range imports {
			b.WriteString(d.render(".java"))
			b.WriteString("\n")
		}
		if len(imports) > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "class %s {\n\n%s\n}\n", name, indentLines(remainder, "    "))
	case ".cs":
		for _, d := range imports {
			b.WriteString(d.render(".cs"))
			b.WriteString("\n")
		}
		if len(imports) > 0 {
			b.WriteString("\n")
		}
		if namespace != "" {
			fmt.Fprintf(&b, "namespace %s;\n\n", namespace)
		}
		fmt.Fprintf(&b, "public class %s\n{\n%s\n}\n", name, indentLines(remainder, "    "))
	}
	out := b.String()
	if reason := evaluator.SyntacticShellReason(path, out); reason != "" {
		return "", "wrapped file fails the syntactic gate: " + reason, false
	}
	return out, "", true
}

// stripLeadingPackageLine removes a leading `package x;` (Java) or file-scoped `namespace X;` (C#)
// from a fragment and returns the C# namespace so the wrap can keep it.
func stripLeadingPackageLine(body, ext string) (string, string) {
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		switch {
		case ext == ".java" && strings.HasPrefix(t, "package ") && strings.HasSuffix(t, ";"):
			return strings.TrimSpace(strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")), ""
		case ext == ".cs" && strings.HasPrefix(t, "namespace ") && strings.HasSuffix(t, ";"):
			ns := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "namespace "), ";"))
			return strings.TrimSpace(strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")), ns
		}
		break
	}
	return body, ""
}

var javaIdentifierRE = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)

// ---------------------------------------------------------------------------------------------
// Framework imports a wrapped fragment needs but did not declare.
//
// asqs-go run api-0e0b356b780481deb471faa745dab0bc: the follower fragment for
// OrderService#cancelOrder was wrapped and written, and the first compile failed on
// `cannot find symbol: class Test` — the body used @Test without importing it. Everything in these
// tables is a fixed one-to-one mapping the model simply omitted; anything ambiguous (assertThat,
// project types, mocked fields) is left to the fixer.
// ---------------------------------------------------------------------------------------------

// javaInferredImports maps a token used in a Java test body to the import that declares it.
var javaInferredImports = map[string]string{
	"@Test": "org.junit.jupiter.api.Test", "@BeforeEach": "org.junit.jupiter.api.BeforeEach",
	"@AfterEach": "org.junit.jupiter.api.AfterEach", "@BeforeAll": "org.junit.jupiter.api.BeforeAll",
	"@AfterAll": "org.junit.jupiter.api.AfterAll", "@DisplayName": "org.junit.jupiter.api.DisplayName",
	"@Nested": "org.junit.jupiter.api.Nested", "@Disabled": "org.junit.jupiter.api.Disabled",
	"@ParameterizedTest": "org.junit.jupiter.params.ParameterizedTest",
	"@ValueSource":       "org.junit.jupiter.params.provider.ValueSource", "@CsvSource": "org.junit.jupiter.params.provider.CsvSource",
	"@MethodSource": "org.junit.jupiter.params.provider.MethodSource", "@EnumSource": "org.junit.jupiter.params.provider.EnumSource",
	"@NullSource": "org.junit.jupiter.params.provider.NullSource", "@ExtendWith": "org.junit.jupiter.api.extension.ExtendWith",
	"MockitoExtension": "org.mockito.junit.jupiter.MockitoExtension",
	"@Mock":            "org.mockito.Mock", "@InjectMocks": "org.mockito.InjectMocks", "@Spy": "org.mockito.Spy", "@Captor": "org.mockito.Captor",
	"ArgumentCaptor": "org.mockito.ArgumentCaptor",
	"assertEquals(":  "static org.junit.jupiter.api.Assertions.assertEquals", "assertNotEquals(": "static org.junit.jupiter.api.Assertions.assertNotEquals",
	"assertTrue(": "static org.junit.jupiter.api.Assertions.assertTrue", "assertFalse(": "static org.junit.jupiter.api.Assertions.assertFalse",
	"assertNull(": "static org.junit.jupiter.api.Assertions.assertNull", "assertNotNull(": "static org.junit.jupiter.api.Assertions.assertNotNull",
	"assertThrows(": "static org.junit.jupiter.api.Assertions.assertThrows", "assertDoesNotThrow(": "static org.junit.jupiter.api.Assertions.assertDoesNotThrow",
	"assertSame(": "static org.junit.jupiter.api.Assertions.assertSame", "assertArrayEquals(": "static org.junit.jupiter.api.Assertions.assertArrayEquals",
	"assertIterableEquals(": "static org.junit.jupiter.api.Assertions.assertIterableEquals", "assertAll(": "static org.junit.jupiter.api.Assertions.assertAll",
	"assertInstanceOf(": "static org.junit.jupiter.api.Assertions.assertInstanceOf", "fail(": "static org.junit.jupiter.api.Assertions.fail",
	"mock(": "static org.mockito.Mockito.mock", "when(": "static org.mockito.Mockito.when", "verify(": "static org.mockito.Mockito.verify",
	"spy(": "static org.mockito.Mockito.spy", "never(": "static org.mockito.Mockito.never", "times(": "static org.mockito.Mockito.times",
	"doThrow(": "static org.mockito.Mockito.doThrow", "doReturn(": "static org.mockito.Mockito.doReturn", "doNothing(": "static org.mockito.Mockito.doNothing",
	"verifyNoInteractions(": "static org.mockito.Mockito.verifyNoInteractions", "verifyNoMoreInteractions(": "static org.mockito.Mockito.verifyNoMoreInteractions",
	"any(": "static org.mockito.ArgumentMatchers.any", "anyString(": "static org.mockito.ArgumentMatchers.anyString", "anyInt(": "static org.mockito.ArgumentMatchers.anyInt",
	"anyLong(": "static org.mockito.ArgumentMatchers.anyLong", "eq(": "static org.mockito.ArgumentMatchers.eq", "argThat(": "static org.mockito.ArgumentMatchers.argThat",
	"Optional<": "java.util.Optional", "Optional.": "java.util.Optional", "List<": "java.util.List", "List.of(": "java.util.List",
	"Map<": "java.util.Map", "Map.of(": "java.util.Map", "Set<": "java.util.Set", "Set.of(": "java.util.Set",
	"ArrayList<": "java.util.ArrayList", "HashMap<": "java.util.HashMap", "HashSet<": "java.util.HashSet",
	"Instant.": "java.time.Instant", "Instant ": "java.time.Instant", "LocalDate.": "java.time.LocalDate", "LocalDate ": "java.time.LocalDate",
	"LocalDateTime.": "java.time.LocalDateTime", "LocalDateTime ": "java.time.LocalDateTime", "Duration.": "java.time.Duration",
	"UUID.": "java.util.UUID", "UUID ": "java.util.UUID", "BigDecimal": "java.math.BigDecimal",
}

// csharpInferredUsings maps a token used in a C# test body to the using directive it needs.
var csharpInferredUsings = map[string]string{
	"[Fact]": "Xunit", "[Theory]": "Xunit", "[InlineData(": "Xunit", "[MemberData(": "Xunit", "Assert.Equal(": "Xunit", "Assert.True(": "Xunit",
	"[Test]": "NUnit.Framework", "[TestFixture]": "NUnit.Framework", "[TestCase(": "NUnit.Framework", "[SetUp]": "NUnit.Framework", "[TearDown]": "NUnit.Framework",
	"Assert.That(": "NUnit.Framework", "Assert.AreEqual(": "NUnit.Framework",
	"[TestMethod]": "Microsoft.VisualStudio.TestTools.UnitTesting", "[TestClass]": "Microsoft.VisualStudio.TestTools.UnitTesting",
	"[TestInitialize]": "Microsoft.VisualStudio.TestTools.UnitTesting", "[DataTestMethod]": "Microsoft.VisualStudio.TestTools.UnitTesting", "[DataRow(": "Microsoft.VisualStudio.TestTools.UnitTesting",
	"Mock<": "Moq", "It.IsAny<": "Moq", "Times.": "Moq",
	"Task<": "System.Threading.Tasks", "Task ": "System.Threading.Tasks", "async Task": "System.Threading.Tasks",
	"List<": "System.Collections.Generic", "Dictionary<": "System.Collections.Generic", "IEnumerable<": "System.Collections.Generic", "IList<": "System.Collections.Generic",
	".ToList()": "System.Linq", ".Select(": "System.Linq", ".Where(": "System.Linq", ".Any(": "System.Linq", ".First(": "System.Linq", ".Count()": "System.Linq",
	"Exception": "System", "Console.": "System", "DateTime": "System", "Guid.": "System", "Guid ": "System", "TimeSpan": "System",
	// The libraries a .NET test reaches for once it stops being a pure unit test. Every one of
	// these was missing, so a recovered fragment using them compiled to CS0246 and the round that
	// wrote it spent its budget on an import the table already knew how to supply.
	".Should()": "FluentAssertions", "Substitute.For<": "NSubstitute", "A.Fake<": "FakeItEasy",
	"WebApplicationFactory<": "Microsoft.AspNetCore.Mvc.Testing",
	"TestServer":             "Microsoft.AspNetCore.TestHost",
	"HttpClient":             "System.Net.Http",
	"GetFromJsonAsync":       "System.Net.Http.Json", "PostAsJsonAsync": "System.Net.Http.Json",
	"JsonSerializer.":         "System.Text.Json",
	"DbContextOptionsBuilder": "Microsoft.EntityFrameworkCore", "UseInMemoryDatabase(": "Microsoft.EntityFrameworkCore",
	".ToListAsync()":    "Microsoft.EntityFrameworkCore",
	"ServiceCollection": "Microsoft.Extensions.DependencyInjection",
	"AddScoped<":        "Microsoft.Extensions.DependencyInjection", "GetRequiredService<": "Microsoft.Extensions.DependencyInjection",
	"NullLogger": "Microsoft.Extensions.Logging.Abstractions", "ILogger<": "Microsoft.Extensions.Logging",
	"File.ReadAllText(": "System.IO", "Path.Combine(": "System.IO", "Stream": "System.IO",
	"IPage": "Microsoft.Playwright", "PageTest": "Microsoft.Playwright.NUnit",
}

// inferredImportsFor returns the imports in table whose token appears in body, minus those already
// covered by declared, in a stable order. Coverage is judged on the last path segment (the simple
// name or the static member), which is how a reader would judge it too.
func inferredImportsFor(body string, table map[string]string, declared []importDecl, ext string) []importDecl {
	have := map[string]bool{}
	for _, d := range declared {
		have[d.path] = true
		if i := strings.LastIndex(d.path, "."); i >= 0 {
			have[d.path[i+1:]] = true
		}
	}
	seen := map[string]bool{}
	var paths []string
	for token, imp := range table {
		if !strings.Contains(body, token) || seen[imp] {
			continue
		}
		key := strings.TrimPrefix(imp, "static ")
		simple := key
		if i := strings.LastIndex(key, "."); i >= 0 {
			simple = key[i+1:]
		}
		if have[key] || have[simple] {
			continue
		}
		seen[imp] = true
		paths = append(paths, imp)
	}
	sort.Strings(paths)
	out := make([]importDecl, 0, len(paths))
	for _, p := range paths {
		d := importDecl{kind: importPlain, path: p}
		if strings.HasPrefix(p, "static ") {
			d.kind = importStatic
			d.path = strings.TrimPrefix(p, "static ")
		}
		out = append(out, d)
	}
	return out
}

// jsImportDeclRE matches one ES import declaration, possibly spanning lines. Group 1 is the
// clause (`X`, `* as X`, `{ a, b as c }`, or combinations), group 3 the module specifier.
var jsImportDeclRE = regexp.MustCompile(`(?ms)^import\s+(?:type\s+)?([^;'"]+?)\s+from\s+(['"])([^'"]+)['"]\s*;?[ \t]*$`)

// jsSideEffectImportRE matches `import 'module';`.
var jsSideEffectImportRE = regexp.MustCompile(`(?m)^import\s+(['"])([^'"]+)['"]\s*;?[ \t]*$`)

// jsTopLevelDeclRE matches a column-0 binding declaration whose name would collide on append.
var jsTopLevelDeclRE = regexp.MustCompile(`(?m)^(?:export\s+)?(?:const|let|var|function|class|type|interface|enum)\s+([A-Za-z_$][\w$]*)`)

// jsImportClause is one parsed import declaration.
type jsImportClause struct {
	typeOnly bool
	def      string   // default binding
	ns       string   // namespace binding (`* as ns`)
	named    []string // named specifiers as written (`a`, `b as c`, `type d`)
	module   string
	start    int
	end      int
}

// jsParseImports returns every import declaration in s with its byte span, in source order.
func jsParseImports(s string) []jsImportClause {
	var out []jsImportClause
	for _, m := range jsImportDeclRE.FindAllStringSubmatchIndex(s, -1) {
		clause := s[m[2]:m[3]]
		c := jsImportClause{module: s[m[6]:m[7]], start: m[0], end: m[1]}
		c.typeOnly = strings.HasPrefix(strings.TrimSpace(s[m[0]+len("import"):m[2]]), "type")
		rest := strings.TrimSpace(clause)
		if i := strings.Index(rest, "{"); i >= 0 {
			j := strings.LastIndex(rest, "}")
			if j < i {
				return nil
			}
			for _, spec := range strings.Split(rest[i+1:j], ",") {
				if spec = strings.TrimSpace(spec); spec != "" {
					c.named = append(c.named, spec)
				}
			}
			rest = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest[:i]), ","))
		}
		for _, part := range strings.Split(rest, ",") {
			part = strings.TrimSpace(part)
			switch {
			case part == "":
			case strings.HasPrefix(part, "*"):
				fields := strings.Fields(part)
				if len(fields) == 3 && fields[1] == "as" {
					c.ns = fields[2]
				} else {
					return nil
				}
			default:
				c.def = part
			}
		}
		out = append(out, c)
	}
	for _, m := range jsSideEffectImportRE.FindAllStringSubmatchIndex(s, -1) {
		out = append(out, jsImportClause{module: s[m[4]:m[5]], start: m[0], end: m[1]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// jsSpecLocalName returns the local binding a named specifier introduces: `b as c` → c, `type d`
// → d, `a` → a.
func jsSpecLocalName(spec string) string {
	spec = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(spec), "type "))
	if i := strings.LastIndex(spec, " as "); i >= 0 {
		return strings.TrimSpace(spec[i+4:])
	}
	return spec
}

// jsImportedBindings returns every local name the file's imports introduce, plus the modules it
// imports for side effects only.
func jsImportedBindings(s string) (names map[string]bool, sideEffects map[string]bool) {
	names, sideEffects = map[string]bool{}, map[string]bool{}
	for _, c := range jsParseImports(s) {
		if c.def == "" && c.ns == "" && len(c.named) == 0 {
			sideEffects[c.module] = true
			continue
		}
		if c.def != "" {
			names[c.def] = true
		}
		if c.ns != "" {
			names[c.ns] = true
		}
		for _, spec := range c.named {
			names[jsSpecLocalName(spec)] = true
		}
	}
	return names, sideEffects
}

func (c jsImportClause) render() string {
	var parts []string
	if c.def != "" {
		parts = append(parts, c.def)
	}
	if c.ns != "" {
		parts = append(parts, "* as "+c.ns)
	}
	if len(c.named) > 0 {
		parts = append(parts, "{ "+strings.Join(c.named, ", ")+" }")
	}
	kw := "import "
	if c.typeOnly {
		kw = "import type "
	}
	if len(parts) == 0 {
		return kw + "'" + c.module + "';"
	}
	return kw + strings.Join(parts, ", ") + " from '" + c.module + "';"
}

// jsTopLevelTestRE matches a test registered at module level without a suite around it, which
// Jest, Vitest and Playwright all accept.
var jsTopLevelTestRE = regexp.MustCompile(`(?m)^(?:it|test)(?:\.\w+)*\s*\(`)

// jsPayloadAppendableToSuite reports whether a JS payload that could not be unwrapped may instead
// be appended whole to a file that keeps its own suite: it must contribute at least one column-0
// suite or top-level test, carry no CommonJS require, and hold no JSX (the scanners cannot read
// it). One suite is normally the unwrap's job; it lands here only when the unwrap could not scan
// the body, and appending it whole is still valid at module level.
func jsPayloadAppendableToSuite(existing, payload string) bool {
	if jsSuiteKind(existing) == "" {
		return false // the no-suite branch owns this case
	}
	if strings.Contains(payload, "require(") || strings.Contains(payload, "</") || strings.Contains(payload, "/>") {
		return false
	}
	return jsTopLevelDescribeRE.MatchString(payload) || jsTopLevelTestRE.MatchString(payload)
}

// jsUnwrapFailureCause explains why unwrapCompilationUnit had no opinion on a JS payload, for the
// skip reason. asqs-go run api-cfc3279416a7d8c7d2690799800c7647 refused
// src/orders/orders.service.test.ts twice with "could not be unwrapped" and nothing more.
func jsUnwrapFailureCause(payload string) string {
	switch {
	case strings.Contains(payload, "</") || strings.Contains(payload, "/>"):
		return "payload contains JSX, which the scanner cannot classify"
	case strings.Contains(payload, "require("):
		return "payload uses CommonJS require"
	}
	n := len(jsTopLevelDescribeRE.FindAllStringIndex(payload, -1))
	switch n {
	case 0:
		if jsTopLevelTestRE.MatchString(payload) {
			return "payload has no top-level suite, only module-level tests"
		}
		return "payload has no top-level suite or test"
	case 1:
		return "payload's single suite body could not be scanned (unbalanced braces or an unterminated literal)"
	default:
		return fmt.Sprintf("payload has %d top-level suites and no primary", n)
	}
}

// jsAppendModulePayload appends a complete JS/TS module payload to existing: its imports are
// merged into the file's import block with bindings the file already has removed, and the rest of
// the module lands after the file's last line. ok is false when a top-level binding of the payload
// is already declared by the file (appending would redeclare it) or when an import cannot be read.
func jsAppendModulePayload(existing, payload string) (string, bool) {
	existing = strings.ReplaceAll(existing, "\r\n", "\n")
	payload = strings.ReplaceAll(strings.TrimSpace(payload), "\r\n", "\n")
	if strings.Contains(payload, "</") || strings.Contains(payload, "/>") {
		return "", false // JSX: the scanners cannot classify `/`
	}
	imports := jsParseImports(payload)
	if imports == nil && strings.Contains(payload, "\nimport ") {
		return "", false
	}
	have, haveSideEffects := jsImportedBindings(existing)

	// Strip the payload's imports, rebuilding only what the file does not already bind.
	var newImports []string
	var remainder strings.Builder
	last := 0
	for _, c := range imports {
		remainder.WriteString(payload[last:c.start])
		last = c.end
		if c.def == "" && c.ns == "" && len(c.named) == 0 {
			if !haveSideEffects[c.module] {
				newImports = append(newImports, c.render())
			}
			continue
		}
		keep := jsImportClause{typeOnly: c.typeOnly, module: c.module}
		if c.def != "" && !have[c.def] {
			keep.def = c.def
		}
		if c.ns != "" && !have[c.ns] {
			keep.ns = c.ns
		}
		for _, spec := range c.named {
			if !have[jsSpecLocalName(spec)] {
				keep.named = append(keep.named, spec)
			}
		}
		if keep.def == "" && keep.ns == "" && len(keep.named) == 0 {
			continue
		}
		if keep.ns != "" && len(keep.named) > 0 {
			// `* as ns, { a }` is not a legal clause; split it.
			named := keep.named
			keep.named = nil
			newImports = append(newImports, keep.render())
			keep = jsImportClause{typeOnly: c.typeOnly, module: c.module, named: named}
		}
		newImports = append(newImports, keep.render())
	}
	remainder.WriteString(payload[last:])
	rest := strings.TrimSpace(remainder.String())
	if rest == "" {
		return "", false
	}

	// A binding the file already declares at the top level would be redeclared by the append.
	declared := map[string]bool{}
	for _, m := range jsTopLevelDeclRE.FindAllStringSubmatch(existing, -1) {
		declared[m[1]] = true
	}
	for name := range have {
		declared[name] = true
	}
	for _, m := range jsTopLevelDeclRE.FindAllStringSubmatch(rest, -1) {
		if declared[m[1]] {
			return "", false
		}
	}

	head := existing
	if len(newImports) > 0 {
		block := strings.Join(newImports, "\n") + "\n"
		if ex := jsParseImports(existing); len(ex) > 0 {
			at := ex[len(ex)-1].end
			head = existing[:at] + "\n" + block + existing[at:]
		} else {
			head = block + "\n" + existing
		}
	}
	return strings.TrimRight(head, "\n") + "\n\n" + rest + "\n", true
}
