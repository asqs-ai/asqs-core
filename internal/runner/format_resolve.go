package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FormatResolveResult is the outcome of resolving which formatter to run after generation.
type FormatResolveResult struct {
	Command, Source, SkipReason string
	PerFile                     bool
}

// ResolveFormatCommand picks the effective format command from explicit config or repo auto-detection.
// When onlyAdded is true, only per-file formatters are returned (never repo-wide Maven/Gradle plugin goals).
func ResolveFormatCommand(repoPath, lang, configuredCmd, buildTool string, onlyAdded bool) FormatResolveResult {
	configuredCmd = strings.TrimSpace(configuredCmd)
	if configuredCmd != "" {
		perFile := onlyAdded && (IsDotNetFormatCommand(configuredCmd) || !formatCommandNeedsShell(configuredCmd))
		r := FormatResolveResult{
			Command: configuredCmd,
			Source:  "config",
			PerFile: perFile,
		}
		if reason := formatAvailabilitySkipReason(repoPath, r); reason != "" {
			r.Command = ""
			r.SkipReason = reason
		}
		return r
	}

	lang = strings.ToLower(strings.TrimSpace(lang))
	if onlyAdded {
		return resolveFormatOnlyAdded(repoPath, lang)
	}
	return resolveFormatRepoWide(repoPath, lang, buildTool)
}

func resolveFormatOnlyAdded(repoPath, lang string) FormatResolveResult {
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
		cmd, ok := prettierPerFileCommand(repoPath)
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

func resolveFormatRepoWide(repoPath, lang, buildTool string) FormatResolveResult {
	switch lang {
	case "java":
		if cmd, source, ok := javaRepoWideFormatCommand(repoPath, buildTool); ok {
			r := FormatResolveResult{Command: cmd, Source: source, PerFile: false}
			if reason := formatAvailabilitySkipReason(repoPath, r); reason != "" {
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
		if cmd, ok := prettierRepoWideCommand(repoPath); ok {
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

func javaBuildPrefix(dir, buildTool string, hasPom, hasGradle bool) (string, error) {
	tool := strings.ToLower(strings.TrimSpace(buildTool))
	if tool == "" {
		tool = "auto"
	}
	hasMvnw := pathExists(filepath.Join(dir, "mvnw")) || pathExists(filepath.Join(dir, "mvnw.cmd"))
	hasGradlew := pathExists(filepath.Join(dir, "gradlew")) || pathExists(filepath.Join(dir, "gradlew.bat"))

	if tool == "auto" {
		switch {
		case hasPom:
			if hasMvnw {
				tool = "mvnw"
			} else {
				tool = "mvn"
			}
		case hasGradle:
			if hasGradlew {
				tool = "gradlew"
			} else {
				tool = "gradle"
			}
		default:
			return "", errNoJavaBuildFile
		}
	}

	switch tool {
	case "mvn", "mvnw":
		if !hasPom {
			return "", errNoJavaBuildFile
		}
		if tool == "mvnw" {
			if !hasMvnw {
				return "", errNoJavaBuildFile
			}
			if runtime.GOOS == "windows" && pathExists(filepath.Join(dir, "mvnw.cmd")) {
				return "mvnw.cmd", nil
			}
			return "./mvnw", nil
		}
		if _, err := exec.LookPath("mvn"); err != nil && !hasMvnw {
			return "", err
		}
		return "mvn", nil
	case "gradle", "gradlew":
		if !hasGradle {
			return "", errNoJavaBuildFile
		}
		if tool == "gradlew" {
			if !hasGradlew {
				return "", errNoJavaBuildFile
			}
			if runtime.GOOS == "windows" && pathExists(filepath.Join(dir, "gradlew.bat")) {
				return "gradlew.bat", nil
			}
			return "./gradlew", nil
		}
		if _, err := exec.LookPath("gradle"); err != nil && !hasGradlew {
			return "", err
		}
		return "gradle", nil
	default:
		return "", errUnsupportedBuildTool
	}
}

var (
	errNoJavaBuildFile      = errors.New("no java build file")
	errUnsupportedBuildTool = errors.New("unsupported build tool")
)

func prettierPerFileCommand(repoPath string) (string, bool) {
	dir := filepath.Clean(strings.TrimSpace(repoPath))
	if bin := localNodeBin(dir, "prettier"); bin != "" {
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

func prettierRepoWideCommand(repoPath string) (string, bool) {
	dir := filepath.Clean(strings.TrimSpace(repoPath))
	if !pathExists(filepath.Join(dir, "package.json")) {
		return "", false
	}
	if bin := localNodeBin(dir, "prettier"); bin != "" {
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

func localNodeBin(repoPath, name string) string {
	bin := filepath.Join(filepath.Clean(repoPath), "node_modules", ".bin", name)
	if runtime.GOOS == "windows" {
		bin += ".cmd"
	}
	if pathExists(bin) {
		return bin
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

func formatAvailabilitySkipReason(repoPath string, r FormatResolveResult) string {
	cmd := strings.TrimSpace(r.Command)
	if cmd == "" {
		return ""
	}
	if formatCommandNeedsShell(cmd) {
		return shellFormatAvailabilitySkipReason(repoPath, cmd)
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
	if _, err := exec.LookPath(bin); err != nil {
		return "formatter_not_available:" + bin
	}
	return ""
}

func shellFormatAvailabilitySkipReason(repoPath, cmd string) string {
	low := strings.ToLower(cmd)
	dir := filepath.Clean(strings.TrimSpace(repoPath))
	switch {
	case strings.Contains(low, "mvn") || strings.Contains(low, "mvnw"):
		if pathExists(filepath.Join(dir, "mvnw")) || pathExists(filepath.Join(dir, "mvnw.cmd")) {
			return ""
		}
		if _, err := exec.LookPath("mvn"); err != nil {
			return "formatter_not_available:mvn"
		}
	case strings.Contains(low, "gradle") || strings.Contains(low, "gradlew"):
		if pathExists(filepath.Join(dir, "gradlew")) || pathExists(filepath.Join(dir, "gradlew.bat")) {
			return ""
		}
		if _, err := exec.LookPath("gradle"); err != nil {
			return "formatter_not_available:gradle"
		}
	case strings.Contains(low, "prettier"):
		if _, ok := prettierPerFileCommand(dir); !ok {
			if _, ok := prettierRepoWideCommand(dir); !ok {
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
