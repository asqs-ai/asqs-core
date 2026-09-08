package extendmerge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asqs/asqs-core/internal/evaluator"
)

func readRepoFile(t *testing.T, repo, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

const javaMembersOnlyPayload = `import static org.junit.jupiter.api.Assertions.assertEquals;
import org.junit.jupiter.api.Test;

@Test
void createOrder_returnsId() {
    OrderService s = new OrderService();
    assertEquals(1, s.createOrder("a").id());
}
`

// asqs-go run api-5a67a414d4ba22496fcc23e1143076fa: the owner gap for OrderServiceTest.java returned a
// methods-only body for a file that did not exist, and the write was refused ("no class ...
// declaration"). The shell is fully determined by the path; build it.
func TestWriteGeneratedFiles_wrapsMethodsOnlyJavaPayloadForNewFile(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/com/example/javatest/service/OrderServiceTest.java"
	n, paths, skips := Write(repo, []Item{{Path: rel, Content: javaMembersOnlyPayload}})
	if n != 1 || len(skips) != 0 {
		t.Fatalf("wrote %d, skips=%v", n, skips)
	}
	if len(paths) != 1 || paths[0] != rel {
		t.Fatalf("paths = %v", paths)
	}
	got := readRepoFile(t, repo, rel)
	for _, want := range []string{
		"package com.example.javatest.service;",
		"import static org.junit.jupiter.api.Assertions.assertEquals;",
		"import org.junit.jupiter.api.Test;",
		"class OrderServiceTest {",
		"    @Test\n    void createOrder_returnsId() {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("wrapped file lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "class OrderServiceTest") < strings.Index(got, "import org.junit") {
		t.Errorf("imports must precede the class:\n%s", got)
	}
}

func TestWrapJavaMembersAsTestClass_refusesWhatItCannotShape(t *testing.T) {
	for name, payload := range map[string]string{
		"already a compilation unit": "package a;\nclass FooTest { @Test void t() {} }",
		"no test marker":             "void helper() {}",
		"fenced":                     "```java\n@Test void t() {}\n```",
		"unbalanced":                 "@Test\nvoid t() {\n",
	} {
		if out, declined, ok := wrapJavaMembersAsTestClass("src/test/java/a/FooTest.java", payload); ok || declined == "" {
			t.Errorf("%s: wrapped although it should not have (declined=%q):\n%s", name, declined, out)
		}
	}
	if _, _, ok := wrapJavaMembersAsTestClass("src/foo.test.ts", "@Test void t() {}"); ok {
		t.Error("non-Java path wrapped")
	}
}

// The follower of a shared test path is told to extend a file its owner never created. Instead of
// "could not read existing file", the payload becomes the file.
func TestWriteGeneratedFiles_extendWithMissingTargetCreatesTheFile(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/com/example/javatest/service/OrderServiceTest.java"
	full := "package com.example.javatest.service;\n\nimport org.junit.jupiter.api.Test;\n\nclass OrderServiceTest {\n    @Test\n    void cancelOrder_marksCancelled() {\n    }\n}\n"
	n, _, skips := Write(repo, []Item{{Path: rel, Content: full, ExtendExisting: true}})
	if n != 1 || len(skips) != 0 {
		t.Fatalf("full unit: wrote %d, skips=%v", n, skips)
	}
	if got := readRepoFile(t, repo, rel); got != full {
		t.Errorf("file differs from the payload:\n%s", got)
	}

	repo2 := t.TempDir()
	n, _, skips = Write(repo2, []Item{{Path: rel, Content: javaMembersOnlyPayload, ExtendExisting: true}})
	if n != 1 || len(skips) != 0 {
		t.Fatalf("members-only: wrote %d, skips=%v", n, skips)
	}
	if got := readRepoFile(t, repo2, rel); !strings.Contains(got, "class OrderServiceTest {") {
		t.Errorf("members-only payload was not wrapped:\n%s", got)
	}
}

const existingJestSuite = `import { Test } from '@nestjs/testing';
import { describe, it, expect, beforeEach } from '@jest/globals';
import { OrdersService } from './orders.service';

describe('OrdersService.getAllOrdersLegacy', () => {
  let service: OrdersService;
  beforeEach(async () => {
    const module = await Test.createTestingModule({ providers: [OrdersService] }).compile();
    service = module.get(OrdersService);
  });
  it('returns the legacy list', () => {
    expect(service.getAllOrdersLegacy()).toEqual([]);
  });
});
`

const twoSuitePayload = `import { Test } from '@nestjs/testing';
import { describe, it, expect, jest } from '@jest/globals';
import { OrdersService } from './orders.service';
import { CreateOrderDto } from './dto/create-order.dto';

const validDto = (): CreateOrderDto => ({ sku: 'A', qty: 1 });

describe('OrdersService.createOrder', () => {
  it('assigns an id', async () => {
    const module = await Test.createTestingModule({ providers: [OrdersService] }).compile();
    const svc = module.get(OrdersService);
    expect(svc.createOrder(validDto()).id).toBeDefined();
  });
});

describe('OrdersService.createOrder validation', () => {
  it('rejects a zero quantity', () => {
    expect(() => new OrdersService().createOrder({ sku: 'A', qty: 0 })).toThrow();
  });
});
`

// asqs-go run api-d01f66ab5b4c58f8e129844d98f8e370: OrdersService.createOrder came back as a full module
// with two top-level suites; the single-suite unwrap refused it and the test was lost. Two
// suites are valid at module level, so the module is appended with its imports merged.
func TestWriteGeneratedFiles_appendsMultiSuiteJSPayloadToExistingSuite(t *testing.T) {
	rel := "src/orders/orders.service.test.ts"
	repo := writeTemp(t, rel, existingJestSuite)
	n, _, skips := Write(repo, []Item{{Path: rel, Content: twoSuitePayload, ExtendExisting: true, SourceSymbolFile: "src/orders/orders.service.ts"}})
	if n != 1 {
		t.Fatalf("expected the append to land; wrote %d, skips=%v", n, skips)
	}
	got := readRepoFile(t, repo, rel)
	for _, want := range []string{
		"describe('OrdersService.getAllOrdersLegacy'",
		"describe('OrdersService.createOrder'",
		"describe('OrdersService.createOrder validation'",
		"const validDto = ()",
		"import { CreateOrderDto } from './dto/create-order.dto';",
		"import { jest } from '@jest/globals';",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("merged file lacks %q:\n%s", want, got)
		}
	}
	if c := strings.Count(got, "import { Test } from '@nestjs/testing'"); c != 1 {
		t.Errorf("Test imported %d times:\n%s", c, got)
	}
	if c := strings.Count(got, "from './orders.service'"); c != 1 {
		t.Errorf("OrdersService imported %d times:\n%s", c, got)
	}
	// Imports stay above the code.
	if strings.LastIndex(got, "\nimport ") > strings.Index(got, "describe('OrdersService.getAllOrdersLegacy'") {
		t.Errorf("an import landed below the first suite:\n%s", got)
	}
}

func TestWriteGeneratedFiles_multiSuiteAppendRefusesRedeclaredBinding(t *testing.T) {
	rel := "src/orders/orders.service.test.ts"
	existing := strings.Replace(existingJestSuite, "describe('OrdersService.getAllOrdersLegacy'", "const validDto = () => ({ sku: 'B', qty: 2 });\n\ndescribe('OrdersService.getAllOrdersLegacy'", 1)
	repo := writeTemp(t, rel, existing)
	n, _, skips := Write(repo, []Item{{Path: rel, Content: twoSuitePayload, ExtendExisting: true, SourceSymbolFile: "src/orders/orders.service.ts"}})
	if n != 0 || len(skips) != 1 || !strings.Contains(skips[0], "redeclares a binding") {
		t.Fatalf("wrote %d, skips=%v; want a refusal naming the redeclared binding", n, skips)
	}
	if got := readRepoFile(t, repo, rel); got != existing {
		t.Error("a refused append modified the file")
	}
}

func TestJSParseImports_shapes(t *testing.T) {
	src := "import React from 'react';\nimport * as fs from 'fs';\nimport { a, b as c, type D } from './m';\nimport type { E } from './e';\nimport def, { x } from 'y';\nimport 'side';\nimport {\n  multi,\n  line,\n} from './multi';\n"
	names, side := jsImportedBindings(src)
	for _, want := range []string{"React", "fs", "a", "c", "D", "E", "def", "x", "multi", "line"} {
		if !names[want] {
			t.Errorf("binding %q not parsed from:\n%s", want, src)
		}
	}
	if !side["side"] {
		t.Error("side-effect import not recorded")
	}
	if got := jsParseImports("import { a } from './m'")[0].render(); got != "import { a } from './m';" {
		t.Errorf("render = %q", got)
	}
}

func TestJSAppendModulePayload_partialNamedImportKeepsOnlyNewBindings(t *testing.T) {
	existing := "import { test, expect } from '@playwright/test';\n\ntest.describe('a', () => {\n  test('x', async () => {});\n});\n"
	payload := "import { test, expect, Page } from '@playwright/test';\nimport { stubJson } from './support/api';\n\ntest.describe('b', () => {\n  test('y', async ({ page }: { page: Page }) => { await stubJson(page); });\n});\n\ntest.describe('c', () => {\n  test('z', async () => {});\n});\n"
	got, ok := jsAppendModulePayload(existing, payload)
	if !ok {
		t.Fatal("append refused")
	}
	if !strings.Contains(got, "import { Page } from '@playwright/test';") || !strings.Contains(got, "import { stubJson } from './support/api';") {
		t.Errorf("new bindings not imported:\n%s", got)
	}
	if strings.Count(got, "from '@playwright/test'") != 2 || strings.Contains(got, "import { test, expect, Page }") {
		t.Errorf("existing bindings re-imported:\n%s", got)
	}
	if !strings.Contains(got, "test.describe('b'") || !strings.Contains(got, "test.describe('c'") || !strings.Contains(got, "test.describe('a'") {
		t.Errorf("suites missing:\n%s", got)
	}
}

// asqs-go run api-5a67a414d4ba22496fcc23e1143076fa: OrderControllerE2EIT.java reached disk with `\d` in a
// string and failed the containerised compile. The write repairs the escape instead.
func TestWriteGeneratedFiles_repairsIllegalEscapesBeforeWriting(t *testing.T) {
	repo := t.TempDir()
	rel := "src/test/java/com/example/javatest/api/OrderControllerE2EIT.java"
	content := "package com.example.javatest.api;\n\nimport org.junit.jupiter.api.Test;\n\nclass OrderControllerE2EIT {\n    @Test\n    void summary_hasId() {\n        String id = body.replaceAll(\"[^\\d]\", \"\");\n    }\n}\n"
	n, _, skips := Write(repo, []Item{{Path: rel, Content: content}})
	if n != 1 || len(skips) != 0 {
		t.Fatalf("wrote %d, skips=%v", n, skips)
	}
	got := readRepoFile(t, repo, rel)
	if !strings.Contains(got, `"[^\\d]"`) || strings.Contains(got, `"[^\d]"`) {
		t.Fatalf("escape not repaired:\n%s", got)
	}
}

func TestWrapJavaMembersAsTestClass_infersFrameworkImportsAndTolerates_package(t *testing.T) {
	payload := "package com.example.javatest.service;\n\n@Test\nvoid cancelOrder_marksCancelled() {\n    Order o = new Order();\n    when(orderRepository.findById(1L)).thenReturn(Optional.of(o));\n    assertEquals(\"CANCELLED\", o.status());\n    verify(orderRepository).save(any(Order.class));\n}\n"
	out, declined, ok := wrapJavaMembersAsTestClass("src/test/java/com/example/javatest/service/OrderServiceTest.java", payload)
	if !ok {
		t.Fatalf("declined: %s", declined)
	}
	for _, want := range []string{
		"package com.example.javatest.service;",
		"import org.junit.jupiter.api.Test;",
		"import static org.junit.jupiter.api.Assertions.assertEquals;",
		"import static org.mockito.Mockito.verify;",
		"import static org.mockito.Mockito.when;",
		"import static org.mockito.ArgumentMatchers.any;",
		"import java.util.Optional;",
		"class OrderServiceTest {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("wrapped file lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "package ") != 1 {
		t.Errorf("package line duplicated:\n%s", out)
	}
	// Declared imports are not duplicated by inference.
	withImport := "import org.junit.jupiter.api.Test;\n\n@Test\nvoid t() {\n    assertTrue(true);\n}\n"
	out, _, ok = wrapJavaMembersAsTestClass("src/test/java/p/FooTest.java", withImport)
	if !ok || strings.Count(out, "org.junit.jupiter.api.Test;") != 1 || !strings.Contains(out, "Assertions.assertTrue;") {
		t.Fatalf("declared import handling:\n%s", out)
	}
}

func TestWrapJavaMembersAsTestClass_declineNamesTheCause(t *testing.T) {
	_, declined, ok := wrapJavaMembersAsTestClass("src/test/java/p/FooTest.java", "package a;\nclass FooTest { @Test void t() {} }")
	if ok || !strings.Contains(declined, "already declares a type") {
		t.Fatalf("ok=%v declined=%q", ok, declined)
	}
	_, declined, ok = wrapJavaMembersAsTestClass("src/test/java/p/FooTest.java", "void helper() {}")
	if ok || !strings.Contains(declined, "empty Java test file") {
		t.Fatalf("ok=%v declined=%q", ok, declined)
	}
}

func TestWrapCSharpMembersAsTestClass(t *testing.T) {
	payload := "namespace Petclinic.Tests;\n\n[Fact]\npublic async Task Ping_ReturnsOk()\n{\n    var client = new Mock<IHttpClient>();\n    var svc = new BillingService(client.Object);\n    Assert.True(await svc.PingAsync());\n}\n"
	out, declined, ok := wrapCSharpMembersAsTestClass("tests/Petclinic.Tests/BillingServiceTests.cs", payload)
	if !ok {
		t.Fatalf("declined: %s", declined)
	}
	for _, want := range []string{"using Xunit;", "using Moq;", "using System.Threading.Tasks;", "namespace Petclinic.Tests;", "public class BillingServiceTests", "    [Fact]"} {
		if !strings.Contains(out, want) {
			t.Errorf("wrapped file lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "namespace ") < strings.Index(out, "using Xunit;") {
		t.Errorf("usings must precede the namespace:\n%s", out)
	}
	if evaluator.SyntacticShellReason("x.cs", out) != "" || evaluator.EmptyTestFileReason("x.cs", out) != "" {
		t.Fatalf("wrapped C# fails the gates:\n%s", out)
	}
	if _, declined, ok := wrapCSharpMembersAsTestClass("tests/P/FooTests.cs", "public class FooTests { [Fact] public void T() {} }"); ok || declined == "" {
		t.Fatal("a full compilation unit must be declined with a reason")
	}
}

func TestWriteGeneratedFiles_wrapsCSharpFragmentForNewFile(t *testing.T) {
	repo := t.TempDir()
	rel := "tests/Petclinic.Tests/OwnerControllerTests.cs"
	n, _, skips := Write(repo, []Item{{Path: rel, Content: "[TestMethod]\npublic void Index_ReturnsView()\n{\n    Assert.IsNotNull(new OwnerController());\n}\n"}})
	if n != 1 || len(skips) != 0 {
		t.Fatalf("wrote %d, skips=%v", n, skips)
	}
	got := readRepoFile(t, repo, rel)
	if !strings.Contains(got, "using Microsoft.VisualStudio.TestTools.UnitTesting;") || !strings.Contains(got, "public class OwnerControllerTests") {
		t.Fatalf("C# fragment not wrapped:\n%s", got)
	}
}

func TestWriteGeneratedFiles_skipReasonsNameTheRecoveryCause(t *testing.T) {
	repo := t.TempDir()
	// Java: a truncated fragment reaches the wrap, which cannot produce a file that passes the
	// gates; the reason must carry both the gate's verdict and the wrap's.
	n, _, skips := Write(repo, []Item{{Path: "src/test/java/p/FooTest.java", Content: "@Test\nvoid t() {\n    assertTrue(true);\n"}})
	if n != 0 || len(skips) != 1 || !strings.Contains(skips[0], "wrap declined: wrapped file fails the syntactic gate") {
		t.Fatalf("java: wrote %d, skips=%v", n, skips)
	}
	// JS: an extend payload the unwrap cannot reduce must say what shape it had.
	rel := "src/orders/orders.service.test.ts"
	repo2 := writeTemp(t, rel, existingJestSuite)
	payload := "import { OrdersService } from './orders.service';\nconst validDto = () => ({ sku: 'A', qty: 1 });\n"
	n, _, skips = Write(repo2, []Item{{Path: rel, Content: payload, ExtendExisting: true, SourceSymbolFile: "src/orders/orders.service.ts"}})
	if n != 0 || len(skips) != 1 || !strings.Contains(skips[0], "no top-level suite or test") {
		t.Fatalf("js: wrote %d, skips=%v", n, skips)
	}
}

// Module-level tests without a suite, and a single suite the scanner could not reduce, are both
// valid at module level and are appended rather than lost.
func TestWriteGeneratedFiles_appendsModuleLevelTestsToExistingSuite(t *testing.T) {
	rel := "src/orders/orders.service.test.ts"
	repo := writeTemp(t, rel, existingJestSuite)
	payload := "import { describe, it, expect } from '@jest/globals';\nimport { OrdersService } from './orders.service';\n\nit('createOrder assigns an id', () => {\n  expect(new OrdersService().createOrder({ sku: 'A', qty: 1 }).id).toBeDefined();\n});\n"
	n, _, skips := Write(repo, []Item{{Path: rel, Content: payload, ExtendExisting: true, SourceSymbolFile: "src/orders/orders.service.ts"}})
	if n != 1 {
		t.Fatalf("wrote %d, skips=%v", n, skips)
	}
	got := readRepoFile(t, repo, rel)
	if !strings.Contains(got, "it('createOrder assigns an id'") || strings.Count(got, "from './orders.service'") != 1 {
		t.Fatalf("append result:\n%s", got)
	}
}
