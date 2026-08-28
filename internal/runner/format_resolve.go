package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/asqs/asqs-core/internal/buildtool"
)

// FormatResolveResult is the outcome of resolving which formatter to run after generation.
type FormatResolveResult struct {
	Command, Source, SkipReason string
	PerFile                     bool
}

// ResolveFormatCommand picks the effective format command from explicit config or repo
// auto-detection.
//
// When onlyAdded is true, auto-detection returns only per-file formatters, never a repo-wide
// Maven/Gradle goal. An explicitly CONFIGURED command is always honoured, but it is only treated as
// per-file when appending a path to it would actually format that path — see
// formatCommandIsPerFileCapable. A configured `mvn spring-javaformat:apply` therefore runs
// repo-wide even under only_added, rather than being handed a file it cannot accept.
//
// target says WHERE the resolved command will run. It matters because availability was probed on
// the host unconditionally: under runner.type: docker on a host without mvn/gradle/dotnet, the
// format step was silently skipped even though the toolchain image supplies exactly those
// binaries. See formatBinaryAvailable.
// WithPerFile returns a copy with PerFile overridden. Used where a caller has already narrowed the
// decision for one invocation — the post-generate step formats only the paths it just wrote — while
// keeping the resolver's command, source and skip reason intact.
func (r FormatResolveResult) WithPerFile(perFile bool) FormatResolveResult {
	r.PerFile = perFile
	return r
}

func ResolveFormatCommand(repoPath, lang, configuredCmd, buildTool string, onlyAdded bool, target Target) FormatResolveResult {
	configuredCmd = strings.TrimSpace(configuredCmd)
	if configuredCmd != "" {
		perFile := onlyAdded && formatCommandIsPerFileCapable(configuredCmd)
		r := FormatResolveResult{
			Command: configuredCmd,
			Source:  "config",
			PerFile: perFile,
		}
		if reason := formatAvailabilitySkipReason(repoPath, r, target); reason != "" {
			r.Command = ""
			r.SkipReason = reason
		}
		return r
	}

	lang = strings.ToLower(strings.TrimSpace(lang))
	if onlyAdded {
		return resolveFormatOnlyAdded(repoPath, lang, target)
	}
	return resolveFormatRepoWide(repoPath, lang, buildTool, target)
}

func resolveFormatOnlyAdded(repoPath, lang string, target Target) FormatResolveResult {
	switch lang {
	case "java":
		if _, err := exec.LookPath("google-java-format"); err == nil {
			return FormatResolveResult{
				Command: "google-java-format -i",
				Source:  "auto_google_java_format",
				PerFile: true,
			}
		}
		return FormatResolveResult{
			Source:     "none",
			SkipReason: "no per-file formatter for java (google-java-format not on PATH)",
		}
	case "go", "golang":
		if _, err := exec.LookPath("gofmt"); err != nil {
			return FormatResolveResult{
				Source:     "none",
				SkipReason: "formatter_not_available:gofmt",
			}
		}
		return FormatResolveResult{
			Command: "gofmt -w",
			Source:  "auto_gofmt",
			PerFile: true,
		}
	case "csharp", "cs":
		if !dotnetOnPATH() {
			return FormatResolveResult{
				Source:     "none",
				SkipReason: "formatter_not_available:dotnet",
			}
		}
		return FormatResolveResult{
			Command: "dotnet format",
			Source:  "auto_dotnet",
			PerFile: true,
		}
	case "javascript", "js", "typescript", "ts":
		cmd, ok := prettierPerFileCommand(repoPath, target)
		if !ok {
			return FormatResolveResult{
				Source:     "none",
				SkipReason: "no per-file formatter for js/ts (prettier not available)",
			}
		}
		return FormatResolveResult{
			Command: cmd,
			Source:  "auto_prettier",
			PerFile: true,
		}
	default:
		return FormatResolveResult{
			Source:     "none",
			SkipReason: "no formatter configured or auto-detected for language " + lang,
		}
	}
}

func resolveFormatRepoWide(repoPath, lang, buildTool string, target Target) FormatResolveResult {
	switch lang {
	case "java":
		if cmd, source, ok := javaRepoWideFormatCommand(repoPath, buildTool); ok {
			r := FormatResolveResult{Command: cmd, Source: source, PerFile: false}
			if reason := formatAvailabilitySkipReason(repoPath, r, target); reason != "" {
				r.Command = ""
				r.SkipReason = reason
			}
			return r
		}
		return FormatResolveResult{
			Source:     "none",
			SkipReason: "no java formatter detected in repo (spotless, spring-javaformat, or google-java-format plugin)",
		}
	case "go", "golang":
		if _, err := exec.LookPath("gofmt"); err != nil {
			return FormatResolveResult{
				Source:     "none",
				SkipReason: "formatter_not_available:gofmt",
			}
		}
		return FormatResolveResult{
			Command: "gofmt -w .",
			Source:  "auto_gofmt",
			PerFile: false,
		}
	case "csharp", "cs":
		if !dotnetOnPATH() {
			return FormatResolveResult{
				Source:     "none",
				SkipReason: "formatter_not_available:dotnet",
			}
		}
		return FormatResolveResult{
			Command: "dotnet format",
			Source:  "auto_dotnet",
			PerFile: false,
		}
	case "javascript", "js", "typescript", "ts":
		if cmd, ok := prettierRepoWideCommand(repoPath, target); ok {
			return FormatResolveResult{Command: cmd, Source: "auto_prettier", PerFile: false}
		}
		return FormatResolveResult{
			Source:     "none",
			SkipReason: "no js/ts formatter detected (prettier not available)",
		}
	default:
		return FormatResolveResult{
			Source:     "none",
			SkipReason: "no formatter configured or auto-detected for language " + lang,
		}
	}
}

func javaRepoWideFormatCommand(repoPath, buildTool string) (cmd, source string, ok bool) {
	dir := filepath.Clean(strings.TrimSpace(repoPath))
	pomPath := filepath.Join(dir, "pom.xml")
	gradlePaths := []string{
		filepath.Join(dir, "build.gradle"),
		filepath.Join(dir, "build.gradle.kts"),
	}
	pomContent, hasPom := readFileLower(pomPath)
	var gradleContent string
	hasGradle := false
	for _, gp := range gradlePaths {
		if c, ok := readFileLower(gp); ok {
			gradleContent = c
			hasGradle = true
			break
		}
	}
	if !hasPom && !hasGradle {
		return "", "", false
	}

	prefix, err := javaBuildPrefix(dir, buildTool, hasPom, hasGradle)
	if err != nil {
		return "", "", false
	}

	switch {
	case hasPom && (strings.Contains(pomContent, "spring-javaformat-maven-plugin") || strings.Contains(pomContent, "spring-javaformat")):
		return prefix + " spring-javaformat:apply -q", "auto_spring_javaformat", true
	case hasPom && strings.Contains(pomContent, "spotless-maven-plugin"):
		return prefix + " spotless:apply -q", "auto_spotless", true
	case hasPom && (strings.Contains(pomContent, "fmt-maven-plugin") || strings.Contains(pomContent, "google-java-format")):
		if strings.Contains(pomContent, "com.spotify.fmt") {
			return prefix + " com.spotify.fmt:fmt-maven-plugin:2.27:format -q", "auto_google_java_format", true
		}
		return prefix + " com.spotify.fmt:fmt-maven-plugin:format -q", "auto_google_java_format", true
	case hasGradle && strings.Contains(gradleContent, "spotless"):
		if strings.HasPrefix(prefix, "./") || strings.HasPrefix(prefix, "gradlew") {
			return prefix + " spotlessApply", "auto_spotless", true
		}
		return prefix + " spotlessApply", "auto_spotless", true
	default:
		return "", "", false
	}
}

// javaBuildPrefix returns the build-tool invocation the repo-wide Java format command is built on.
//
// It used to carry its own copy of the auto-detect logic, complete with a runtime.GOOS branch that
// emitted "mvnw.cmd" — a Windows batch file — into a command that, under runner.type: docker, runs
// inside a Linux container. Availability is no longer decided here either: the caller knows which
// target will execute, and formatBinaryAvailable answers for that target.
func javaBuildPrefix(dir, buildTool string, hasPom, hasGradle bool) (string, error) {
	tool, err := buildtool.Resolve(dir, buildTool)
	if err != nil {
		return "", err
	}
	switch tool.Kind {
	case buildtool.Maven:
		if !hasPom {
			return "", errNoJavaBuildFile
		}
		return tool.Binary, nil
	case buildtool.Gradle:
		if !hasGradle {
			return "", errNoJavaBuildFile
		}
		return tool.Binary, nil
	}
	return "", errNoJavaBuildFile
}

var (
	errNoJavaBuildFile      = errors.New("no java build file")
	errUnsupportedBuildTool = errors.New("unsupported build tool")
)

func prettierPerFileCommand(repoPath string, target Target) (string, bool) {
	dir := filepath.Clean(strings.TrimSpace(repoPath))
	if bin := localNodeBin(dir, "prettier", target); bin != "" {
		return bin + " --write", true
	}
	if _, err := exec.LookPath("npx"); err == nil {
		return "npx --no-install prettier --write", true
	}
	if _, err := exec.LookPath("prettier"); err == nil {
		return "prettier --write", true
	}
	return "", false
}

func prettierRepoWideCommand(repoPath string, target Target) (string, bool) {
	dir := filepath.Clean(strings.TrimSpace(repoPath))
	if !pathExists(filepath.Join(dir, "package.json")) {
		return "", false
	}
	if bin := localNodeBin(dir, "prettier", target); bin != "" {
		return bin + " --write .", true
	}
	if _, err := exec.LookPath("npx"); err == nil {
		return "npx --no-install prettier --write .", true
	}
	if _, err := exec.LookPath("prettier"); err == nil {
		return "prettier --write .", true
	}
	return "", false
}

// localNodeBin returns the repo-local node binary to invoke, or "" when it is not installed.
//
// The shape depends on where the command will run, and getting that wrong was a live bug. For the
// local target it is an absolute HOST path with the Windows ".cmd" suffix. For the Docker target an
// absolute host path does not exist inside the container — the repository is mounted at /workspace
// — so the command must be repo-relative, and it must never carry a ".cmd" suffix derived from the
// host's OS, because the container is Linux. Existence is still checked on the host: node_modules
// is in the bind-mounted repository, so what the host sees is what the container gets.
func localNodeBin(repoPath, name string, target Target) string {
	rel := filepath.Join("node_modules", ".bin", name)
	hostPath := filepath.Join(filepath.Clean(repoPath), rel)
	if target == TargetDocker {
		if pathExists(hostPath) {
			return filepath.ToSlash(rel)
		}
		return ""
	}
	if runtime.GOOS == "windows" {
		hostPath += ".cmd"
	}
	if pathExists(hostPath) {
		return hostPath
	}
	return ""
}

func readFileLower(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.ToLower(string(b)), true
}

func formatAvailabilitySkipReason(repoPath string, r FormatResolveResult, target Target) string {
	cmd := strings.TrimSpace(r.Command)
	if cmd == "" {
		return ""
	}
	if formatCommandNeedsShell(cmd) {
		return shellFormatAvailabilitySkipReason(repoPath, cmd, target)
	}
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	bin := parts[0]
	if strings.EqualFold(bin, "dotnet") {
		if !dotnetOnPATH() {
			return "formatter_not_available:dotnet"
		}
		return ""
	}
	if strings.HasPrefix(bin, "./") || strings.HasPrefix(bin, ".\\") {
		if pathExists(filepath.Join(filepath.Clean(repoPath), filepath.FromSlash(bin))) {
			return ""
		}
		return "formatter_not_available:" + bin
	}
	if !formatBinaryAvailable(bin, target) {
		return "formatter_not_available:" + bin
	}
	return ""
}

// imageProvidedFormatBinaries are supplied by the toolchain image, so probing the HOST's PATH for
// them answers the wrong question on the Docker target. This was a silent capability loss: a host
// without Maven skipped the format step even though every eval container had `mvn`.
//
// Deliberately narrow. Binaries that come from neither the image nor the repository — a standalone
// google-java-format, say — keep the host probe: it is the wrong question there too, but assuming
// an arbitrary binary exists inside the container would turn a silent skip into a hard failure,
// and that call belongs to U10 along with the rest of the format-step parity work.
var imageProvidedFormatBinaries = map[string]bool{
	"mvn": true, "gradle": true, "dotnet": true,
	"npx": true, "npm": true, "node": true, "yarn": true, "pnpm": true,
}

func formatBinaryAvailable(bin string, target Target) bool {
	if target == TargetDocker && imageProvidedFormatBinaries[bin] {
		return true
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

func shellFormatAvailabilitySkipReason(repoPath, cmd string, target Target) string {
	low := strings.ToLower(cmd)
	dir := filepath.Clean(strings.TrimSpace(repoPath))
	switch {
	// The presence of a repo wrapper used to count as "Maven is available". Since D3 (U3b) nothing
	// invokes ./mvnw, so a repo that ships one says nothing about whether the command can run —
	// keeping that branch let a host with no Maven resolve `mvn spotless:apply` and fail later.
	case strings.Contains(low, "mvn") || strings.Contains(low, "mvnw"):
		if !formatBinaryAvailable("mvn", target) {
			return "formatter_not_available:mvn"
		}
	case strings.Contains(low, "gradle") || strings.Contains(low, "gradlew"):
		if !formatBinaryAvailable("gradle", target) {
			return "formatter_not_available:gradle"
		}
	case strings.Contains(low, "prettier"):
		if _, ok := prettierPerFileCommand(dir, target); !ok {
			if _, ok := prettierRepoWideCommand(dir, target); !ok {
				return "formatter_not_available:prettier"
			}
		}
	case strings.Contains(low, "dotnet"):
		if !dotnetOnPATH() {
			return "formatter_not_available:dotnet"
		}
	}
	return ""
}

// formatCommandIsPerFileCapable reports whether appending a file path to a format command formats
// that file.
//
// This used to be inferred from "the command contains no shell operators", which is not the same
// question and gets build tools wrong: `mvn spring-javaformat:apply -q src/Foo.java` makes Maven
// read the path as a lifecycle phase and fail with "Unknown lifecycle phase". A repo-wide goal is
// repo-wide however it is invoked.
func formatCommandIsPerFileCapable(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// `dotnet format` takes paths through --include rather than as a trailing argument, and the
	// dedicated helper handles that shape.
	if IsDotNetFormatCommand(cmd) {
		return true
	}
	// With a shell operator the path would land after the LAST command in the chain, not on the
	// formatter.
	if formatCommandNeedsShell(cmd) {
		return false
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	return !isBuildToolBinary(fields[0])
}

// isBuildToolBinary reports whether a command's first token is a Maven or Gradle invocation, in any
// of the spellings that have appeared in configs: a bare binary, a repo wrapper, or a Windows
// wrapper script. Wrappers are no longer produced by ASQS (D3) but an operator may still configure
// one by hand.
func isBuildToolBinary(bin string) bool {
	b := strings.ToLower(filepath.Base(strings.TrimSpace(bin)))
	b = strings.TrimSuffix(strings.TrimSuffix(b, ".cmd"), ".bat")
	b = strings.TrimSuffix(b, ".exe")
	switch b {
	case "mvn", "mvnw", "gradle", "gradlew":
		return true
	}
	return false
}
