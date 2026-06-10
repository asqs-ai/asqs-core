package evaluator

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// NuGet restore diagnostics. These errors are NOT fixable by ASQS (they require
// container credentials or feed configuration) but they silently cascade into
// CS0234/CS0246 compile errors on consumers of the unresolved assemblies. We
// surface them as a distinct, actionable audit event so the real root cause
// stops being buried behind downstream symptoms.

var (
	// Matches: error NU1301: Unable to load the service index for source <url>.
	reNuGetServiceIndex = regexp.MustCompile(`(?mi)error\s+(NU1301)\b[^\n]*?source\s+([^\s\.]+(?:\.[^\s\.]+)*)`)
	// Matches: error NU1101: Unable to find package <pkg>. No packages exist with this id ...
	reNuGetNotFound = regexp.MustCompile(`(?mi)error\s+(NU1101|NU1102|NU1103)\b[^\n]*?(?:package|packages)\s+'?([A-Za-z0-9_.\-]+)'?`)
	// Matches: error NU1403: Package content hash validation failed ...
	reNuGetAuthFail = regexp.MustCompile(`(?mi)error\s+(NU1403|NU5036)\b`)
	// Generic: "Unable to load the service index" (no code prefix on some outputs)
	reNuGetServiceIndexGeneric = regexp.MustCompile(`(?mi)Unable to load the service index for source\s+(\S+)`)
)

// reportNuGetRestoreFailure scans compile/restore output for NuGet feed/restore
// errors and, when found, emits evaluator.nuget_restore_failure so the user can
// see the real root cause instead of chasing downstream CS0234/CS0246 noise.
func reportNuGetRestoreFailure(ctx context.Context, opts EvalOptions, errorOutput string, audit Auditor) {
	if audit == nil || strings.TrimSpace(errorOutput) == "" {
		return
	}
	codes := map[string]bool{}
	feedURLs := map[string]bool{}
	missingPkgs := map[string]bool{}

	for _, m := range reNuGetServiceIndex.FindAllStringSubmatch(errorOutput, -1) {
		if len(m) >= 3 {
			codes[m[1]] = true
			feedURLs[strings.TrimRight(m[2], ".")] = true
		}
	}
	for _, m := range reNuGetServiceIndexGeneric.FindAllStringSubmatch(errorOutput, -1) {
		if len(m) >= 2 {
			codes["NU1301"] = true
			feedURLs[strings.TrimRight(m[1], ".")] = true
		}
	}
	for _, m := range reNuGetNotFound.FindAllStringSubmatch(errorOutput, -1) {
		if len(m) >= 3 {
			codes[m[1]] = true
			missingPkgs[strings.TrimRight(m[2], ".")] = true
		}
	}
	for _, m := range reNuGetAuthFail.FindAllStringSubmatch(errorOutput, -1) {
		if len(m) >= 2 {
			codes[m[1]] = true
		}
	}

	if len(codes) == 0 && len(feedURLs) == 0 && len(missingPkgs) == 0 {
		return
	}

	payload := map[string]interface{}{
		"message":          "NuGet restore/feed failure detected; downstream CS0234/CS0246 errors are likely symptoms, not code issues.",
		"codes":            sortedKeys(codes),
		"feed_urls":        sortedKeys(feedURLs),
		"missing_packages": sortedKeys(missingPkgs),
		"remediation":      nuGetRestoreRemediation(sortedKeys(feedURLs)),
	}
	audit.LogError(ctx, "evaluator.nuget_restore_failure", payload)
}

// nuGetRestoreRemediation produces the operator-facing remediation list for evaluator.nuget_restore_failure
// and the unrecoverable-env audit. It names the ASQS config keys operators actually need to set (not
// generic env-var hints) and, when the failing feed is recognised as an Azure DevOps Artifacts feed
// (pkgs.dev.azure.com or *.pkgs.visualstudio.com), points at the PAT-based path that is already wired
// through to docker eval + bootstrap containers via VSS_NUGET_EXTERNAL_FEED_ENDPOINTS. For any other
// private feed it points at the unified runner.private_registry_credentials path (cross-ecosystem, also
// covers Maven/npm) so non-Azure feeds (Artifactory, ProGet, BaGet, MyGet) can be authenticated without
// code changes.
func nuGetRestoreRemediation(failingFeedURLs []string) []string {
	azure := false
	for _, u := range failingFeedURLs {
		lu := strings.ToLower(u)
		if strings.Contains(lu, "pkgs.dev.azure.com") || strings.Contains(lu, ".pkgs.visualstudio.com") {
			azure = true
			break
		}
	}
	out := make([]string, 0, 4)
	if azure {
		sample := ""
		for _, u := range failingFeedURLs {
			lu := strings.ToLower(u)
			if strings.Contains(lu, "pkgs.dev.azure.com") || strings.Contains(lu, ".pkgs.visualstudio.com") {
				sample = u
				break
			}
		}
		line := "Provide an Azure DevOps PAT with Packaging (Read): set `vcs.azure_devops.token` (or env `ASQS_AZURE_DEVOPS_TOKEN`) AND list every failing feed's v3 index URL under `runner.azure_devops_nuget_feed_endpoints`"
		if sample != "" {
			line += " (e.g. `- \"" + sample + "\"`)"
		}
		line += ". ASQS already merges this into `VSS_NUGET_EXTERNAL_FEED_ENDPOINTS` and injects it into every docker eval and bootstrap container, so no code change is needed — just configuration."
		out = append(out, line)
	}
	out = append(out,
		"For non-Azure-DevOps private feeds, set `runner.private_registry_credentials` with a `- {type: nuget, endpoint, username, password}` entry; ASQS merges these into the same `VSS_NUGET_EXTERNAL_FEED_ENDPOINTS` envelope read by dotnet's Artifacts Credential Provider (works for Artifactory / ProGet / BaGet / MyGet / any HTTPS NuGet source). The same unified list also configures Maven (settings.xml) and npm (.npmrc) with `type: maven` / `type: npm` entries.",
		"Alternatively, vendor the required packages into a public or reachable internal feed and remove the private source from `NuGet.config` — this is the only option when credentials cannot be supplied.",
		"Until restore succeeds for every transitively referenced project, CS0234/CS0246 on consumers are symptoms of the restore failure and cannot be repaired by edits to tests or source — the LLM fixer is deliberately skipped in this case to avoid degrading generated tests.",
	)
	return out
}

func sortedKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
