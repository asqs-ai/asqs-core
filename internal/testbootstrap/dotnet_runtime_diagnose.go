package testbootstrap

import (
	"fmt"
	"regexp"
	"strings"
)

// The .NET host prints this when the shared runtime matching a project's TargetFramework is absent.
// It is an environment fact, not a package problem, and it deserves its own remediation: the raw
// output buries the cause under a stack of URLs.
var (
	// Matches both the raw host output and the prose this file generates from it, so callers can ask
	// the same question of a wrapped error as of the original stdout.
	reDotnetMissingRuntime  = regexp.MustCompile(`(?i)you must install or update \.net to run this application|shared runtime it needs is not installed`)
	reDotnetWantedFramework = regexp.MustCompile(`(?i)Framework:\s*'([\w.]+)',\s*version\s*'([\d.]+)'`)
	reDotnetFoundFramework  = regexp.MustCompile(`(?m)^\s{2,}([\d.]+) at \[`)
)

// dotnetRuntimeMissing reports whether output shows a shared-runtime mismatch.
func dotnetRuntimeMissing(output string) bool {
	return reDotnetMissingRuntime.MatchString(output)
}

// dotnetRuntimeRemediation names the runtime the test host wanted, what is installed, and the two
// ways out. Returns "" when the output is not a runtime mismatch.
//
// Deliberately NOT auto-corrected by setting DOTNET_ROLL_FORWARD: silently running a net8.0 test
// suite on a .NET 10 runtime changes what is being verified, and the same mismatch would hit the
// evaluator later anyway. Surfacing it while the bootstrap is the only thing that has run is the
// cheapest possible moment to find out.
func dotnetRuntimeRemediation(output, targetFramework string) string {
	if !dotnetRuntimeMissing(output) {
		return ""
	}
	wanted := ""
	if m := reDotnetWantedFramework.FindStringSubmatch(output); m != nil {
		wanted = m[1] + " " + m[2]
	}
	var found []string
	seen := map[string]bool{}
	for _, m := range reDotnetFoundFramework.FindAllStringSubmatch(output, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			found = append(found, m[1])
		}
	}

	var b strings.Builder
	b.WriteString("The test project builds, but the .NET shared runtime it needs is not installed on this machine")
	if wanted != "" {
		fmt.Fprintf(&b, " (test host requested %s)", wanted)
	}
	if len(found) > 0 {
		fmt.Fprintf(&b, "; only %s is present", strings.Join(found, ", "))
	}
	b.WriteString(". ")
	if tf := strings.TrimSpace(targetFramework); tf != "" {
		fmt.Fprintf(&b, "The test project targets %s because that is the highest TargetFramework among the production projects. ", tf)
	}
	b.WriteString("Either install the matching .NET runtime on the host, or run evaluation in Docker " +
		"(general.sandbox.type: docker with an image matching the TargetFramework) so the runtime is guaranteed to match. " +
		"Evaluation would fail the same way, so this is not specific to bootstrap.")
	return b.String()
}
