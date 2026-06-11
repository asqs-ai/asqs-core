package testbootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asqs/asqs-core/internal/runner"
)

// Block namespace + explicit LaunchAsync options: repos often pin LangVersion 8.0 while targeting net6.0;
// file-scoped namespaces and target-typed new() require C# 10 / 9.
const csharpPlaywrightSmoke = `using System.Threading.Tasks;
using Microsoft.Playwright;
using Xunit;

namespace Asqs.E2E
{
    /// <summary>ASQS e2e_framework_bootstrap smoke test (Playwright .NET).</summary>
    public class AsqsPlaywrightSmokeE2E
    {
        [Fact]
        public async Task LaunchChromium()
        {
            using var playwright = await Playwright.CreateAsync();
            await using var browser = await playwright.Chromium.LaunchAsync(new BrowserTypeLaunchOptions { Headless = true });
            var page = await browser.NewPageAsync();
            await page.GotoAsync("data:text/html,<html><title>asqs</title></html>");
            var title = await page.TitleAsync();
            Assert.Contains("asqs", title);
        }
    }
}
`

// applyPlaywrightDotNetBootstrap adds Microsoft.Playwright + xUnit test SDK if needed, smoke test, build, playwright install, dotnet test.
// When ed is non-nil, restore/build/test/install run in ephemeral Docker (git root at /workspace; cwd is the mono workspace when configured).
func applyPlaywrightDotNetBootstrap(ctx context.Context, p E2EParams, audit Auditor, repo, gitRoot string, ed *EphemeralDocker) error {
	logAudit(audit, ctx, "e2e_bootstrap.apply_start", map[string]interface{}{
		"message": "Installing Playwright for .NET (PackageReference + smoke test)",
		"stack":   "playwright-dotnet",
	})
	fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: installing Microsoft.Playwright (.NET)…")

	// Target a DEDICATED e2e/ Playwright project (created from the production projects) instead of
	// polluting a production .csproj; reuse an existing E2E project when present. Kept under e2e/ so it
	// never collides with the unit test project under tests/.
	csproj, createdFiles, err := ensureCSharpE2EProjectForBootstrap(repo, gitRoot, dotnetTFMFallbackFromRunner(p.Runner))
	if err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: ensure e2e project: %w", err)
	}
	if csproj == "" {
		return fmt.Errorf("e2e_framework_bootstrap: no SDK-style .csproj found under repo")
	}
	if len(createdFiles) > 0 {
		addCSharpTestProjectToSolutions(ctx, ed, repo, csproj, audit)
	}

	xuFiles, err := applyCSharpXUnit(repo, csproj, gitRoot)
	if err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Ensure xUnit / test SDK: %v", err), "step": "csproj_xunit", "error": err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap csproj xunit: %w", err)
	}

	pwFiles, err := applyCSharpPlaywrightPackage(repo, csproj, gitRoot)
	if err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.apply_failed", map[string]interface{}{
			"message": fmt.Sprintf("Failed to add Microsoft.Playwright: %v", err), "step": "csproj_playwright", "error": err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap csproj playwright: %w", err)
	}

	var filesChanged []string
	seen := map[string]bool{}
	for _, abs := range append(append(append([]string(nil), createdFiles...), xuFiles...), pwFiles...) {
		rel := relPathForBootstrap(repo, abs)
		if !seen[rel] {
			seen[rel] = true
			filesChanged = append(filesChanged, rel)
		}
	}

	e2eDir := filepath.Join(filepath.Dir(csproj), "E2E")
	if err := os.MkdirAll(e2eDir, 0755); err != nil {
		return err
	}
	smokePath := filepath.Join(e2eDir, "AsqsPlaywrightSmokeE2E.cs")
	if _, err := os.Stat(smokePath); os.IsNotExist(err) {
		if err := atomicWrite(smokePath, []byte(csharpPlaywrightSmoke)); err != nil {
			return fmt.Errorf("e2e_framework_bootstrap write smoke test: %w", err)
		}
		if rel, e := filepath.Rel(repo, smokePath); e == nil {
			filesChanged = append(filesChanged, filepath.ToSlash(rel))
		} else {
			filesChanged = append(filesChanged, "E2E/AsqsPlaywrightSmokeE2E.cs")
		}
	}

	if len(filesChanged) > 0 {
		logAudit(audit, ctx, "e2e_bootstrap.patched", map[string]interface{}{
			"message": fmt.Sprintf("Patched: %s", strings.Join(filesChanged, ", ")), "files_changed": filesChanged,
		})
	}

	timeout := installTimeout(p.RunnerTimeout)
	csprojRel, err := csprojRelForDotnet(repo, csproj)
	if err != nil {
		return fmt.Errorf("e2e_framework_bootstrap: csproj path: %w", err)
	}

	fallbackTFM := dotnetTFMFallbackFromRunner(p.Runner)
	// Each EphemeralDocker command is a separate `docker run --rm`; /usr/share/dotnet changes do not persist
	// across steps. Prepend runtime/SDK install to every dotnet-related container command when needed.
	dotNetInstallShell := ""
	if ed != nil {
		dotNetInstallShell = runner.PlaywrightDotnetDockerInstallShell(repo, csproj, fallbackTFM)
		if strings.TrimSpace(dotNetInstallShell) != "" {
			logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
				"message": "Playwright .NET Docker: prepending side-by-side .NET install to each container step (ephemeral docker run --rm)",
			})
		}
	}

	vCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
		"message": "dotnet build (Playwright .NET)", "command": "dotnet build " + csprojRel,
	})
	buildArgv := []string{"dotnet", "build", csprojRel, "-c", "Release", "--verbosity", "quiet", "-nologo"}
	buildArgv = runner.AppendDotnetMultiTargetFrameworkArgv(buildArgv, csproj, fallbackTFM)
	buildArgv = appendDotnetCLIArgsTFMFallback(buildArgv, csproj, fallbackTFM)
	buildArgv = runner.ApplyDotnetTestFrameworkBootstrapMSBuildProps(buildArgv)
	out, err := RunArgvWithShellPrefix(vCtx, ed, repo, buildArgv, nil, dotNetInstallShell)
	if err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("dotnet build failed: %v", err), "output": truncate(string(out), 8000), "error": err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap dotnet build: %w\n%s", err, truncate(string(out), 4000))
	}

	script := findPlaywrightDotnetScript(repo, csproj)
	_, _, _, bundledOK := findPlaywrightDotnetBundledNodeCLI(repo, csproj, ed != nil)
	if script == "" && !bundledOK {
		logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
			"message": "Could not find playwright.ps1, playwright.sh, or bundled .playwright/package/cli.js under bin/ after build",
		})
		return fmt.Errorf("e2e_framework_bootstrap: playwright CLI script not found under bin/")
	}

	installMeta := map[string]interface{}{"message": "Playwright CLI install chromium"}
	if script != "" {
		installMeta["script"] = script
	} else {
		installMeta["method"] = "bundled_node_cli"
	}
	logAudit(audit, ctx, "e2e_bootstrap.install", installMeta)
	if err := runPlaywrightDotnetInstall(vCtx, ed, repo, csproj, script, dotNetInstallShell); err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("playwright install failed: %v", err), "error": err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap playwright install: %w", err)
	}

	logAudit(audit, ctx, "e2e_bootstrap.install", map[string]interface{}{
		"message": "dotnet test smoke E2E", "command": "dotnet test --filter FullyQualifiedName~AsqsPlaywrightSmokeE2E",
	})
	testOut, err := runDotnetTestWithFilter(vCtx, ed, repo, csproj, "FullyQualifiedName~AsqsPlaywrightSmokeE2E", fallbackTFM, dotNetInstallShell)
	if err != nil {
		logAuditError(audit, ctx, "e2e_bootstrap.verify_failed", map[string]interface{}{
			"message": fmt.Sprintf("dotnet test failed: %v", err), "output": truncate(string(testOut), 8000), "error": err.Error(),
		})
		return fmt.Errorf("e2e_framework_bootstrap dotnet test: %w\n%s", err, truncate(string(testOut), 4000))
	}

	logAudit(audit, ctx, "e2e_bootstrap.apply_ok", map[string]interface{}{
		"message": "Playwright .NET bootstrap complete", "files_changed": filesChanged, "stack": "playwright-dotnet",
	})
	fmt.Fprintln(os.Stderr, "  e2e_framework_bootstrap: Playwright .NET ok (build + browsers + smoke test)")
	return nil
}

// findPlaywrightDotnetShellScript returns the first playwright.sh under the project's bin/, then repo bin/ (for Linux / Docker).
func findPlaywrightDotnetShellScript(repo, csprojAbs string) string {
	if sh := findPlaywrightShellUnderBin(filepath.Join(filepath.Dir(csprojAbs), "bin")); sh != "" {
		return sh
	}
	return findPlaywrightShellUnderBin(filepath.Join(repo, "bin"))
}

// findBundledPlaywrightNodeCLIInOutDir finds the Playwright JS CLI and bundled Node under a single TFM output folder.
// Microsoft.Playwright 1.40+ removed playwright.sh from the package; Docker images often lack pwsh, so we run
// `node cli.js install` using the copies MSBuild places under .playwright/.
func findBundledPlaywrightNodeCLIInOutDir(outDir string, forDockerLinux bool) (node string, cli string, ok bool) {
	cli = filepath.Join(outDir, ".playwright", "package", "cli.js")
	if !fileExists(cli) {
		return "", "", false
	}
	var platforms []string
	if forDockerLinux {
		platforms = []string{"linux-x64", "linux-arm64"}
	} else {
		switch runtime.GOOS {
		case "linux":
			if runtime.GOARCH == "arm64" {
				platforms = []string{"linux-arm64", "linux-x64"}
			} else {
				platforms = []string{"linux-x64", "linux-arm64"}
			}
		case "darwin":
			if runtime.GOARCH == "arm64" {
				platforms = []string{"darwin-arm64", "darwin-x64"}
			} else {
				platforms = []string{"darwin-x64", "darwin-arm64"}
			}
		case "windows":
			platforms = []string{"win32_x64"}
		default:
			platforms = []string{"linux-x64", "linux-arm64"}
		}
	}
	for _, plat := range platforms {
		base := filepath.Join(outDir, ".playwright", "node", plat)
		n := filepath.Join(base, "node")
		if runtime.GOOS == "windows" && !forDockerLinux {
			n = filepath.Join(base, "node.exe")
		}
		if fileExists(n) {
			return n, cli, true
		}
	}
	return "", "", false
}

// findPlaywrightDotnetBundledNodeCLI searches the test project's bin/ for a Playwright build output (cli.js + node).
func findPlaywrightDotnetBundledNodeCLI(repo, csprojAbs string, linuxContainer bool) (outDir, node, cli string, ok bool) {
	projBin := filepath.Join(filepath.Dir(csprojAbs), "bin")
	if !dirExists(projBin) {
		return "", "", "", false
	}
	var candidates []string
	_ = filepath.Walk(projBin, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "cli.js" {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "package" {
			return nil
		}
		candidates = append(candidates, path)
		return nil
	})
	if len(candidates) == 0 {
		return "", "", "", false
	}
	pick := candidates[0]
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(filepath.ToSlash(c)), "/release/") {
			pick = c
			break
		}
	}
	out := filepath.Dir(filepath.Dir(filepath.Dir(pick)))
	nb, cj, ok2 := findBundledPlaywrightNodeCLIInOutDir(out, linuxContainer)
	if !ok2 {
		return "", "", "", false
	}
	return out, nb, cj, true
}

func findPlaywrightShellUnderBin(binRoot string) string {
	if !dirExists(binRoot) {
		return ""
	}
	var sh string
	_ = filepath.Walk(binRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.ToLower(info.Name()) == "playwright.sh" && sh == "" {
			sh = path
		}
		return nil
	})
	return sh
}

func findPlaywrightDotnetScript(repo, csprojAbs string) string {
	projBin := filepath.Join(filepath.Dir(csprojAbs), "bin")
	repoBin := filepath.Join(repo, "bin")
	var ps1, sh string
	walk := func(root string) {
		if !dirExists(root) {
			return
		}
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			n := strings.ToLower(info.Name())
			if n == "playwright.ps1" && ps1 == "" {
				ps1 = path
			}
			if n == "playwright.sh" && sh == "" {
				sh = path
			}
			return nil
		})
	}
	walk(projBin)
	if ps1 == "" && sh == "" {
		walk(repoBin)
	}
	if runtime.GOOS == "windows" && ps1 != "" {
		return ps1
	}
	if sh != "" {
		return sh
	}
	return ps1
}

func runPlaywrightDotnetInstall(ctx context.Context, ed *EphemeralDocker, repo, csprojAbs, script, dockerDotNetInstallShell string) error {
	linuxContainer := ed != nil
	if strings.TrimSpace(script) == "" {
		outDir, node, cli, ok := findPlaywrightDotnetBundledNodeCLI(repo, csprojAbs, linuxContainer)
		if !ok {
			return fmt.Errorf("Playwright .NET: no shell script and no bundled .playwright CLI in build output")
		}
		if ed == nil {
			return runPlaywrightDotnetInstallBundledLocal(ctx, outDir, node, cli)
		}
		return runPlaywrightDotnetInstallBundledDocker(ctx, ed, outDir, node, cli, dockerDotNetInstallShell)
	}
	if ed == nil {
		return runPlaywrightDotnetInstallLocal(ctx, script)
	}
	shPath := script
	if strings.HasSuffix(strings.ToLower(shPath), ".ps1") {
		outDir := filepath.Dir(shPath)
		if node, cli, ok := findBundledPlaywrightNodeCLIInOutDir(outDir, true); ok {
			return runPlaywrightDotnetInstallBundledDocker(ctx, ed, outDir, node, cli, dockerDotNetInstallShell)
		}
		if alt := findPlaywrightDotnetShellScript(repo, csprojAbs); alt != "" {
			shPath = alt
		} else {
			inPath, err := workspaceScriptPath(ed.hostAbs, shPath)
			if err != nil {
				return err
			}
			line := "pwsh -NoProfile -File " + shellQuoteArg(inPath) + " install chromium"
			_, err = ed.sh(ctx, shellWithDotNetDockerPrep(dockerDotNetInstallShell, line), nil)
			return err
		}
	}
	inPath, err := workspaceScriptPath(ed.hostAbs, shPath)
	if err != nil {
		return err
	}
	line := "bash " + shellQuoteArg(inPath) + " install chromium"
	_, err = ed.sh(ctx, shellWithDotNetDockerPrep(dockerDotNetInstallShell, line), nil)
	return err
}

func runPlaywrightDotnetInstallBundledLocal(ctx context.Context, outDir, node, cli string) error {
	cmd := exec.CommandContext(ctx, node, cli, "install", "chromium")
	cmd.Dir = outDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bundled node playwright install: %w\n%s", err, truncate(string(out), 4000))
	}
	return nil
}

func runPlaywrightDotnetInstallBundledDocker(ctx context.Context, ed *EphemeralDocker, outDir, node, cli string, dockerDotNetInstallShell string) error {
	relOut, err := filepath.Rel(ed.hostAbs, outDir)
	if err != nil {
		return fmt.Errorf("bootstrap: output dir not under repo: %w", err)
	}
	wsOut := "/workspace/" + filepath.ToSlash(relOut)
	relNode, err := filepath.Rel(outDir, node)
	if err != nil {
		return err
	}
	relCli, err := filepath.Rel(outDir, cli)
	if err != nil {
		return err
	}
	inner := joinShellArgs([]string{"./" + filepath.ToSlash(relNode), "./" + filepath.ToSlash(relCli), "install", "chromium"})
	line := "cd " + shellQuoteArg(wsOut) + " && " + inner
	_, err = ed.sh(ctx, shellWithDotNetDockerPrep(dockerDotNetInstallShell, line), nil)
	return err
}

func runPlaywrightDotnetInstallLocal(ctx context.Context, script string) error {
	if strings.HasSuffix(strings.ToLower(script), ".ps1") {
		var name string
		var args []string
		if runtime.GOOS == "windows" {
			name = "powershell.exe"
			args = []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "install", "chromium"}
		} else {
			name = "pwsh"
			args = []string{"-NoProfile", "-File", script, "install", "chromium"}
		}
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = filepath.Dir(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w\n%s", name, err, truncate(string(out), 4000))
		}
		return nil
	}
	cmd := exec.CommandContext(ctx, "bash", script, "install", "chromium")
	cmd.Dir = filepath.Dir(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bash playwright.sh: %w\n%s", err, truncate(string(out), 4000))
	}
	return nil
}

func shellWithDotNetDockerPrep(prep, inner string) string {
	prep = strings.TrimSpace(prep)
	inner = strings.TrimSpace(inner)
	if prep == "" {
		return inner
	}
	return prep + " && " + inner
}
