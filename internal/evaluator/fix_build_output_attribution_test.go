package evaluator

import (
	"strings"
	"testing"
)

// The E2E output of asqs-go run api-f246dd9ba0642ba07187f9207c44680a, shortened: the application
// now actually boots for the E2E pass, so its own runtime frames — compiled files under dist/ —
// reach the failure text ahead of the spec that failed.
const builtAppFailure = `[WebServer] [Nest] WARN [LoggingInterceptor] POST /orders failed in 1ms — BadRequestException
[WebServer]     at OrdersController.createOrder (/workspace/dist/main.js:41:11)
    Received: undefined
    at Object.<anonymous> (/workspace/e2e/api/orders.controllerRoute.e2e-spec.ts:57:33)
`

// A streak or enforcement decision anchored on a compiled file is anchored on a line no repair can
// touch.
func TestParsePrimaryFailureSite_skipsBuildOutputFrames(t *testing.T) {
	got := ParsePrimaryFailureSite(builtAppFailure)
	if !got.OK {
		t.Fatalf("no primary site found: %+v", got)
	}
	if strings.Contains(got.Path, "dist/") {
		t.Errorf("primary site = %q, want the spec rather than the build output", got.Path)
	}
	if !strings.Contains(got.Path, "orders.controllerRoute.e2e-spec.ts") || got.Line != 57 {
		t.Errorf("primary site = %q:%d, want the spec frame", got.Path, got.Line)
	}
}

// When every frame is build output there is no site to anchor on, and saying so beats naming one
// the fixer cannot act on.
func TestParsePrimaryFailureSite_buildOutputOnlyYieldsNoSite(t *testing.T) {
	if got := ParsePrimaryFailureSite("at f (/workspace/dist/main.js:41:11)\nat g (/workspace/build/server.js:3:1)\n"); got.OK {
		t.Errorf("primary site = %+v, want none", got)
	}
}

// A source path that merely contains the word dist under src/ is not build output.
func TestParsePrimaryFailureSite_keepsSourceUnderSrc(t *testing.T) {
	got := ParsePrimaryFailureSite("at Object.<anonymous> (/workspace/src/dist/helper.test.ts:9:1)\n")
	if !got.OK || !strings.Contains(got.Path, "src/dist/helper.test.ts") {
		t.Errorf("primary site = %+v, want the src file kept", got)
	}
}
