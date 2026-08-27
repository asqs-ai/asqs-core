package testbootstrap

import (
	"strings"
	"testing"
)

// Verbatim host output from running a net8.0 test project on a machine with only the .NET 10 runtime.
const dotnetMissingRuntimeOutput = `Test run for /tmp/x/tests/bin/Debug/net8.0/Asqs.CsharpTest.Tests.dll (.NETCoreApp,Version=v8.0)
A total of 1 test files matched the specified pattern.
Testhost process for source(s) '/tmp/x/tests/bin/Debug/net8.0/Asqs.CsharpTest.Tests.dll' exited with error: You must install or update .NET to run this application.
App: /tmp/x/tests/bin/Debug/net8.0/testhost.dll
Architecture: arm64
Framework: 'Microsoft.NETCore.App', version '8.0.0' (arm64)
.NET location: /usr/local/share/dotnet/
The following frameworks were found:
  10.0.5 at [/usr/local/share/dotnet/shared/Microsoft.NETCore.App]
Learn more:
https://aka.ms/dotnet/app-launch-failed
. Please check the diagnostic logs for more information.

Test Run Aborted.`

func TestDotnetRuntimeRemediation_namesWantedAndInstalledRuntimes(t *testing.T) {
	if !dotnetRuntimeMissing(dotnetMissingRuntimeOutput) {
		t.Fatal("missing-runtime output not recognised")
	}
	got := dotnetRuntimeRemediation(dotnetMissingRuntimeOutput, "net8.0")
	for _, want := range []string{"Microsoft.NETCore.App 8.0.0", "10.0.5", "net8.0", "Docker"} {
		if !strings.Contains(got, want) {
			t.Errorf("remediation should mention %q; got:\n%s", want, got)
		}
	}
}

func TestDotnetRuntimeRemediation_ignoresUnrelatedFailures(t *testing.T) {
	for _, out := range []string{
		"",
		"error CS0246: The type or namespace name 'Moq' could not be found",
		"Failed! - Failed: 1, Passed: 0",
	} {
		if dotnetRuntimeMissing(out) {
			t.Errorf("false positive on %q", out)
		}
		if got := dotnetRuntimeRemediation(out, "net8.0"); got != "" {
			t.Errorf("expected no remediation for %q, got %q", out, got)
		}
	}
}
