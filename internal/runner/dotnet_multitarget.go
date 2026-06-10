package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

// PlaywrightDotnetImageBundledRuntimeMajor is the Microsoft.NETCore.App major bundled with
// mcr.microsoft.com/playwright/dotnet (SDK 8 → shared runtime 8). Older TFMs (net6, net7, …)
// need a side-by-side runtime install to run testhost.
const PlaywrightDotnetImageBundledRuntimeMajor = 8

// PlaywrightDotnetBundledSDKMajor is the SDK major bundled with official Playwright/dotnet images.
// Repos targeting a newer SDK (global.json / rollForward) may need a side-by-side SDK install.
const PlaywrightDotnetBundledSDKMajor = 8

var (
	reCsprojMultitargetTF            = regexp.MustCompile(`(?i)<TargetFramework>\s*([^<]*?)\s*</TargetFramework>`)
	reCsprojMultitargetTFs           = regexp.MustCompile(`(?i)<TargetFrameworks>\s*([^<]*?)\s*</TargetFrameworks>`)
	reDotnetInstallNetTFMChannel     = regexp.MustCompile(`(?i)^net(\d+)\.(\d+)`)
	reDotnetInstallNetCoreAppChannel = regexp.MustCompile(`(?i)^netcoreapp(\d+)\.(\d+)`)
)

// DotnetSDKRunnableTestTFM is true for TFMs that `dotnet test` can run on Linux without Mono
// (excludes net472/net48, netstandard, etc.).
func DotnetSDKRunnableTestTFM(tfm string) bool {
	t := strings.TrimSpace(strings.ToLower(tfm))
	if t == "" || strings.HasPrefix(t, "$(") {
		return false
	}
	if strings.HasPrefix(t, "netstandard") {
		return false
	}
	if strings.HasPrefix(t, "netcoreapp") {
		return true
	}
	if strings.HasPrefix(t, "net10.") || strings.HasPrefix(t, "net11.") || strings.HasPrefix(t, "net12.") {
		return true
	}
	if strings.HasPrefix(t, "net2") || strings.HasPrefix(t, "net3") || strings.HasPrefix(t, "net4") {
		return false
	}
	return strings.HasPrefix(t, "net5.") || strings.HasPrefix(t, "net6.") ||
		strings.HasPrefix(t, "net7.") || strings.HasPrefix(t, "net8.") ||
		strings.HasPrefix(t, "net9.")
}

// ParseCsprojTargetFrameworksList returns TFMs from TargetFramework or TargetFrameworks (split on ';').
func ParseCsprojTargetFrameworksList(csprojPath string) ([]string, error) {
	b, err := os.ReadFile(csprojPath)
	if err != nil {
		return nil, err
	}
	s := string(b)
	if m := reCsprojMultitargetTF.FindStringSubmatch(s); len(m) >= 2 {
		v := strings.TrimSpace(m[1])
		if v != "" && !strings.HasPrefix(v, "$(") {
			return []string{v}, nil
		}
	}
	if m := reCsprojMultitargetTFs.FindStringSubmatch(s); len(m) >= 2 {
		v := strings.TrimSpace(m[1])
		if v == "" || strings.HasPrefix(v, "$(") {
			return nil, nil
		}
		var out []string
		for _, p := range strings.Split(v, ";") {
			if t := strings.TrimSpace(p); t != "" && !strings.HasPrefix(t, "$(") {
				out = append(out, t)
			}
		}
		return out, nil
	}
	return nil, nil
}

// PickDotnetMultiTargetTestTargetFramework picks a TFM for `dotnet test`/`dotnet build` in Linux Docker when the project
// multi-targets and mixes .NET Framework (net48, …) with modern TFMs. Returns ("", false) when no pinning is needed.
func PickDotnetMultiTargetTestTargetFramework(csprojAbs, preferTFM string) (string, bool) {
	tfms, err := ParseCsprojTargetFrameworksList(csprojAbs)
	if err != nil || len(tfms) <= 1 {
		return "", false
	}
	var portable []string
	var hasNonPortable bool
	for _, t := range tfms {
		if DotnetSDKRunnableTestTFM(t) {
			portable = append(portable, strings.TrimSpace(t))
		} else {
			hasNonPortable = true
		}
	}
	if !hasNonPortable || len(portable) == 0 {
		return "", false
	}
	preferTFM = strings.TrimSpace(preferTFM)
	if preferTFM != "" {
		for _, t := range portable {
			if strings.EqualFold(strings.TrimSpace(t), preferTFM) {
				return t, true
			}
		}
	}
	return portable[0], true
}

// DotnetInstallChannelForTFM returns the dotnet-install.sh --channel value for shared runtime installs
// (e.g. net6.0 → "6.0", netcoreapp3.1 → "3.1"). Empty when the TFM is not a .NET Core-style moniker.
func DotnetInstallChannelForTFM(tfm string) string {
	t := strings.TrimSpace(strings.ToLower(tfm))
	if t == "" {
		return ""
	}
	if m := reDotnetInstallNetCoreAppChannel.FindStringSubmatch(t); len(m) == 3 {
		return m[1] + "." + m[2]
	}
	if m := reDotnetInstallNetTFMChannel.FindStringSubmatch(t); len(m) == 3 {
		return m[1] + "." + m[2]
	}
	return ""
}

func dotnetChannelMajor(channel string) int {
	channel = strings.TrimSpace(channel)
	parts := strings.SplitN(channel, ".", 2)
	if len(parts) == 0 || parts[0] == "" {
		return 0
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return n
}

// PlaywrightDotnetMissingRuntimeChannels lists dotnet-install.sh channels for Microsoft.NETCore.App runtimes
// required to execute tests for csprojAbs in the Playwright/dotnet image (bundled runtime 8 only).
// preferTFM is the configured dotnet fallback TFM when multi-targeting mixes Framework + Core.
func PlaywrightDotnetMissingRuntimeChannels(csprojAbs, preferTFM string) []string {
	tfms, err := ParseCsprojTargetFrameworksList(csprojAbs)
	if err != nil || len(tfms) == 0 {
		return nil
	}
	var portable []string
	hasNonPortable := false
	for _, t := range tfms {
		t = strings.TrimSpace(t)
		if DotnetSDKRunnableTestTFM(t) {
			portable = append(portable, t)
		} else {
			hasNonPortable = true
		}
	}
	var scan []string
	if hasNonPortable && len(portable) > 0 {
		pick, ok := PickDotnetMultiTargetTestTargetFramework(csprojAbs, preferTFM)
		if !ok || pick == "" {
			return nil
		}
		scan = []string{pick}
	} else {
		scan = portable
	}
	seen := map[string]struct{}{}
	var out []string
	for _, t := range scan {
		ch := DotnetInstallChannelForTFM(t)
		if ch == "" {
			continue
		}
		maj := dotnetChannelMajor(ch)
		if maj <= 0 || maj >= PlaywrightDotnetImageBundledRuntimeMajor {
			continue
		}
		if _, ok := seen[ch]; ok {
			continue
		}
		seen[ch] = struct{}{}
		out = append(out, ch)
	}
	return out
}

// AppendDotnetMultiTargetFrameworkArgv pins /p:TargetFramework=<tfm> when TargetFrameworks mixes net4xx with net5+
// so `dotnet test` on Linux does not invoke the Mono-based .NET Framework host.
func AppendDotnetMultiTargetFrameworkArgv(argv []string, csprojAbs, preferTFM string) []string {
	if len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv
	}
	if argvAlreadyHasTargetFrameworkMSBuildProp(argv) {
		return argv
	}
	pick, ok := PickDotnetMultiTargetTestTargetFramework(csprojAbs, preferTFM)
	if !ok || pick == "" {
		return argv
	}
	prop := "/p:TargetFramework=" + pick
	verb := strings.ToLower(strings.TrimSpace(argv[1]))
	switch verb {
	case "build", "test", "restore", "publish", "pack", "run", "msbuild", "format", "add", "remove", "list", "clean", "watch":
		out := make([]string, 0, len(argv)+1)
		out = append(out, argv[0], argv[1], prop)
		out = append(out, argv[2:]...)
		return out
	default:
		out := append([]string(nil), argv...)
		return append(out, prop)
	}
}

// PlaywrightDotnetDockerInstallShell returns a shell snippet (no trailing "&&") that installs extra
// Microsoft.NETCore.App and Microsoft.AspNetCore.App runtimes (and/or a newer SDK) into /usr/share/dotnet.
// AspNetCore is required for testhost when the project references the shared ASP.NET Core framework
// (Sdk Web, FrameworkReference Microsoft.AspNetCore.App, etc.). Empty when the default Playwright/dotnet
// image is sufficient. Must run in the same `docker run` as dotnet commands.
// repoRoot is the git checkout root on the host (for global.json / SDK roll-forward scan).
func PlaywrightDotnetDockerInstallShell(repoRoot, csprojAbs, fallbackTFM string) string {
	chs := PlaywrightDotnetMissingRuntimeChannels(csprojAbs, fallbackTFM)
	maj := profile.MaxDotNetSdkMajorRequiredByRepo(repoRoot)
	needSDK := maj > PlaywrightDotnetBundledSDKMajor
	if len(chs) == 0 && !needSDK {
		return ""
	}
	var b strings.Builder
	b.WriteString("set -eu; curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/di.sh && chmod +x /tmp/di.sh")
	for _, ch := range chs {
		b.WriteString(" && /tmp/di.sh --install-dir /usr/share/dotnet --channel ")
		b.WriteString(ch)
		b.WriteString(" --runtime dotnet --no-path")
		b.WriteString(" && /tmp/di.sh --install-dir /usr/share/dotnet --channel ")
		b.WriteString(ch)
		b.WriteString(" --runtime aspnetcore --no-path")
	}
	if needSDK {
		b.WriteString(fmt.Sprintf(" && /tmp/di.sh --install-dir /usr/share/dotnet --channel %d.0 --no-path", maj))
	}
	return b.String()
}

// ResolveCsprojAbsForDotnetDockerEval picks a primary .csproj for multitarget / Playwright runtime heuristics
// given the eval working directory and patched argv (exec-form dotnet or sh -c).
func ResolveCsprojAbsForDotnetDockerEval(cwdAbs string, argv []string) (string, error) {
	cwdAbs = filepath.Clean(cwdAbs)
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		abs, ok, err := shellScriptResolvePrimaryCsprojAbs(argv[2], cwdAbs)
		if err != nil || !ok {
			return "", err
		}
		return abs, nil
	}
	for i := len(argv) - 1; i >= 0; i-- {
		a := argv[i]
		if strings.EqualFold(filepath.Ext(a), ".csproj") {
			p := a
			if !filepath.IsAbs(p) {
				p = filepath.Join(cwdAbs, filepath.FromSlash(p))
			}
			return filepath.Clean(p), nil
		}
	}
	rel, err := resolveDotnetEntryRel(cwdAbs)
	if err != nil {
		return "", err
	}
	if rel == "" {
		return "", nil
	}
	if strings.HasSuffix(strings.ToLower(rel), ".csproj") {
		return filepath.Join(cwdAbs, filepath.FromSlash(rel)), nil
	}
	paths, err := discoverCsprojPathsForDotnet(cwdAbs)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	sort.Strings(paths)
	return paths[0], nil
}

func applyDotnetShellScriptMultiTargetPin(script, cwdAbs, csprojAbs, preferTFM string) string {
	script = strings.TrimSpace(script)
	if shellScriptHasTargetFrameworkMSBuildProp(script) {
		return script
	}
	p := strings.TrimSpace(csprojAbs)
	if p == "" {
		abs, ok, err := shellScriptResolvePrimaryCsprojAbs(script, cwdAbs)
		if err != nil || !ok {
			return script
		}
		p = abs
	}
	pick, ok := PickDotnetMultiTargetTestTargetFramework(p, preferTFM)
	if !ok || pick == "" {
		return script
	}
	return applyDotnetShellScriptInsertMSBuildProps(script, []string{"/p:TargetFramework=" + pick})
}

// ApplyDotnetDockerMultiTargetFramework pins a runnable TFM for multitarget csproj (exec argv or sh -c dotnet).
func ApplyDotnetDockerMultiTargetFramework(argv []string, cwdAbs, csprojAbs, preferTFM string) []string {
	if len(argv) == 3 && argv[0] == "sh" && argv[1] == "-c" {
		return []string{"sh", "-c", applyDotnetShellScriptMultiTargetPin(argv[2], cwdAbs, csprojAbs, preferTFM)}
	}
	return AppendDotnetMultiTargetFrameworkArgv(argv, csprojAbs, preferTFM)
}

// PrependShellSnippetToDockerCommand prepends a POSIX shell snippet so ephemeral `docker run --rm` jobs
// (restore + main) each run install/setup before the original command.
func PrependShellSnippetToDockerCommand(command []string, prep string) []string {
	prep = strings.TrimSpace(prep)
	if prep == "" {
		return command
	}
	if len(command) >= 3 && command[0] == "sh" && command[1] == "-c" {
		inner := strings.TrimSpace(command[2])
		if inner == "" {
			return []string{"sh", "-c", prep}
		}
		return []string{"sh", "-c", prep + " && " + inner}
	}
	return []string{"sh", "-c", prep + " && " + argvToShellSingleCommandLine(command)}
}
