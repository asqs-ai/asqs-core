package evaluator

import "testing"

// Shape of the jest output in the asqs-core audit.log of 2026-09-03 (Angular fixture): the first
// `path:line:col` token in the whole log is a jest-internal frame under node_modules/, printed
// beneath "Your test suite must contain at least one test". The parser blamed
// TestScheduler.js:133 for five consecutive rounds — a file no fix round can write.
const jestVendoredFirstOutput = `FAIL src/app/features/checkout/checkout.component.test.ts
  ● Test suite failed to run

    Your test suite must contain at least one test.

      at onResult (node_modules/@jest/core/build/TestScheduler.js:133:18)
      at node_modules/@jest/core/build/TestScheduler.js:254:19
      at node_modules/emittery/index.js:363:13

PASS src/app/app.routes.test.ts
FAIL src/app/legacy/services/legacy-invoice-bridge.service.test.ts
  ● LegacyInvoiceBridgeService › should be created

    Property ` + "`constructor`" + ` does not have access type get

      at ModuleMocker._spyOnProperty (node_modules/jest-mock/build/index.js:819:13)
      at src/app/legacy/services/legacy-invoice-bridge.service.test.ts:18:10
      at _ZoneDelegate.invoke (node_modules/zone.js/bundles/zone.umd.js:410:32)
`

func TestParsePrimaryFailureSite_skipsVendoredFrames(t *testing.T) {
	site := ParsePrimaryFailureSite(jestVendoredFirstOutput)
	if !site.OK {
		t.Fatal("expected a primary site")
	}
	if site.Path != "src/app/legacy/services/legacy-invoice-bridge.service.test.ts" || site.Line != 18 {
		t.Fatalf("primary site = %s:%d, want the first non-vendored location legacy-invoice-bridge.service.test.ts:18", site.Path, site.Line)
	}
}

// Output whose only locations are vendored frames yields no site at all rather than a node_modules
// path; the callers treat !OK as "nothing to attribute".
func TestParsePrimaryFailureSite_onlyVendoredFramesIsNoSite(t *testing.T) {
	out := "  ● Test suite failed to run\n\n      at onResult (node_modules/@jest/core/build/TestScheduler.js:133:18)\n      at node_modules/emittery/index.js:363:13\n"
	if site := ParsePrimaryFailureSite(out); site.OK {
		t.Fatalf("expected no primary site, got %s:%d", site.Path, site.Line)
	}
}

// Round 4 of the same run: jest printed a frame in the production service (unwritable) before the
// frame in the failing test. With the writable artifacts as preference the test file wins; without
// a preference the first location still wins, so callers that pass nothing see no change.
func TestParsePrimaryFailureSiteAmong_prefersWritableArtifact(t *testing.T) {
	out := `FAIL src/app/legacy/services/legacy-invoice-bridge.service.test.ts
  ● LegacyInvoiceBridgeService › syncLegacyInvoice › should call getHttpClient when invoked

    TypeError: Cannot read properties of undefined (reading 'getHttpClient')

      at LegacyInvoiceBridgeService.syncLegacyInvoice (src/app/legacy/services/legacy-invoice-bridge.service.ts:22:23)
      at Object.<anonymous> (src/app/legacy/services/legacy-invoice-bridge.service.test.ts:41:15)
`
	artifacts := []string{"src/app/features/catalog/catalog.service.test.ts", "src/app/legacy/services/legacy-invoice-bridge.service.test.ts"}
	got := ParsePrimaryFailureSiteAmong(out, artifacts)
	if got.Path != "src/app/legacy/services/legacy-invoice-bridge.service.test.ts" || got.Line != 41 {
		t.Fatalf("preferred site = %s:%d, want legacy-invoice-bridge.service.test.ts:41", got.Path, got.Line)
	}
	plain := ParsePrimaryFailureSite(out)
	if plain.Path != "src/app/legacy/services/legacy-invoice-bridge.service.ts" || plain.Line != 22 {
		t.Fatalf("unpreferred site = %s:%d, want the first location legacy-invoice-bridge.service.ts:22", plain.Path, plain.Line)
	}
	// A preference that matches nothing falls back to the first location.
	none := ParsePrimaryFailureSiteAmong(out, []string{"src/unrelated.test.ts"})
	if none.Path != plain.Path || none.Line != plain.Line {
		t.Fatalf("unmatched preference changed the site to %s:%d", none.Path, none.Line)
	}
}

// Container-absolute diagnostics still match a repo-relative preference (suffix match, the same rule
// sameDiagnosticFile applies everywhere else).
func TestParsePrimaryFailureSiteAmong_matchesContainerAbsolutePath(t *testing.T) {
	out := "src/main/Foo.java:[3,1] error: a\n/workspace/src/test/java/p/T.java:[9,17] cannot find symbol\n"
	got := ParsePrimaryFailureSiteAmong(out, []string{"src/test/java/p/T.java"})
	if got.Path != "workspace/src/test/java/p/T.java" || got.Line != 9 {
		t.Fatalf("site = %s:%d, want workspace/src/test/java/p/T.java:9 (normalizePathForFix strips the leading slash)", got.Path, got.Line)
	}
}
