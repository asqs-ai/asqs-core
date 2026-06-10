// Package profile: toolchain profiles for Docker evaluation (one profile per build stack).
package profile

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ToolchainID names a built-in profile.
type ToolchainID string

const (
	JavaMaven         ToolchainID = "java-maven" // JDK 21 (default Maven docker eval)
	JavaMaven11       ToolchainID = "java-maven-11"
	JavaMaven21       ToolchainID = "java-maven-21" // explicit JDK 21, same image default as java-maven
	JavaGradle        ToolchainID = "java-gradle"   // JDK 21
	JavaGradle11      ToolchainID = "java-gradle-11"
	JavaGradle21      ToolchainID = "java-gradle-21"
	TypeScriptNPM     ToolchainID = "typescript-npm"
	TypeScriptPNPM    ToolchainID = "typescript-pnpm"
	TypeScriptYarn    ToolchainID = "typescript-yarn"
	CSharpDotnet      ToolchainID = "csharp-dotnet"
	UnsupportedDocker ToolchainID = ""
)

// ToolchainProfile defines argv and image for Docker eval jobs.
type ToolchainProfile struct {
	ID               ToolchainID
	Image            string
	Restore          []string
	Compile          []string
	Test             []string
	Coverage         []string
	CacheTargetPaths []string
	ArtifactPaths    []string
}

const DefaultMavenImage = "maven:3.9-eclipse-temurin-21"
const DefaultMavenImageJDK11 = "maven:3.9-eclipse-temurin-11"
const DefaultGradleImage = "gradle:8.11-jdk21"
const DefaultGradleImageJDK11 = "gradle:8.11-jdk11"
const DefaultNodeImage = "node:20-bookworm"

// DefaultNodeLTSImage tracks the current Node.js LTS release (Docker Hub official node:lts).
const DefaultNodeLTSImage = "node:lts"
const DefaultDotNetImage = "mcr.microsoft.com/dotnet/sdk:10.0"

// BuiltinToolchain returns the default profile for an ID with resolved images.
// For csharp-dotnet, .NET SDK Docker image follows imageDotNet when set; otherwise DefaultDotNetImage (no csproj scan — pass via ResolveToolchain for TFM-based selection).
func BuiltinToolchain(id ToolchainID, imageJavaMaven, imageJavaGradle, imageNode, imageDotNet string) ToolchainProfile {
	return builtinToolchain(id, imageJavaMaven, imageJavaGradle, imageNode, imageDotNet, DefaultNodeImage, "")
}

func mavenToolchainProfile(id ToolchainID, img string) ToolchainProfile {
	return ToolchainProfile{
		ID:               id,
		Image:            img,
		Restore:          []string{"mvn", "-q", "-B", "dependency:go-offline"},
		Compile:          []string{"mvn", "-q", "-B", "-DskipTests", "compile"},
		Test:             []string{"mvn", "-q", "-B", "test"},
		Coverage:         []string{"mvn", "-q", "-B", "test", "jacoco:report"},
		CacheTargetPaths: []string{"/root/.m2"},
		ArtifactPaths:    []string{"target/surefire-reports", "target/site/jacoco"},
	}
}

func gradleToolchainProfile(id ToolchainID, img string) ToolchainProfile {
	return ToolchainProfile{
		ID:               id,
		Image:            img,
		Restore:          []string{"./gradlew", "--no-daemon", "-q", "dependencies"},
		Compile:          []string{"./gradlew", "--no-daemon", "-q", "compileJava"},
		Test:             []string{"./gradlew", "--no-daemon", "-q", "test"},
		Coverage:         []string{"./gradlew", "--no-daemon", "-q", "test", "jacocoTestReport"},
		CacheTargetPaths: []string{"/root/.gradle"},
		ArtifactPaths:    []string{"build/reports/tests", "build/reports/jacoco"},
	}
}

func builtinToolchain(id ToolchainID, imageJavaMaven, imageJavaGradle, imageNode, imageDotNet, nodeImageIfUnset, repoPath string) ToolchainProfile {
	switch id {
	case JavaGradle:
		img := strings.TrimSpace(imageJavaGradle)
		if img == "" {
			img = DefaultGradleImage
		}
		return gradleToolchainProfile(JavaGradle, img)
	case JavaGradle21:
		img := strings.TrimSpace(imageJavaGradle)
		if img == "" {
			img = DefaultGradleImage
		}
		return gradleToolchainProfile(JavaGradle21, img)
	case JavaGradle11:
		img := strings.TrimSpace(imageJavaGradle)
		if img == "" {
			img = DefaultGradleImageJDK11
		}
		return gradleToolchainProfile(JavaGradle11, img)
	case JavaMaven:
		img := strings.TrimSpace(imageJavaMaven)
		if img == "" {
			img = DefaultMavenImage
		}
		return mavenToolchainProfile(JavaMaven, img)
	case JavaMaven21:
		img := strings.TrimSpace(imageJavaMaven)
		if img == "" {
			img = DefaultMavenImage
		}
		return mavenToolchainProfile(JavaMaven21, img)
	case JavaMaven11:
		img := strings.TrimSpace(imageJavaMaven)
		if img == "" {
			img = DefaultMavenImageJDK11
		}
		return mavenToolchainProfile(JavaMaven11, img)
	case TypeScriptPNPM:
		img := strings.TrimSpace(imageNode)
		if img == "" {
			img = nodeImageIfUnset
		}
		return ToolchainProfile{
			ID:      TypeScriptPNPM,
			Image:   img,
			Restore: []string{"sh", "-c", "corepack enable && pnpm install --frozen-lockfile"},
			// Prepend node_modules/.bin so scripts that call `tsc` find the local typescript package (common with node:lts / slim images).
			Compile:          []string{"sh", "-c", "corepack enable && export PATH=\"${PWD}/node_modules/.bin:${PATH}\" && pnpm run build"},
			Test:             []string{"sh", "-c", "corepack enable && CI=true pnpm test --if-present"},
			Coverage:         []string{"sh", "-c", "corepack enable && CI=true pnpm run test:coverage --if-present || CI=true pnpm test --if-present"},
			CacheTargetPaths: []string{"/root/.npm", "/root/.local/share/pnpm/store"},
			ArtifactPaths:    []string{"coverage/lcov.info"},
		}
	case TypeScriptYarn:
		img := strings.TrimSpace(imageNode)
		if img == "" {
			img = nodeImageIfUnset
		}
		return ToolchainProfile{
			ID:               TypeScriptYarn,
			Image:            img,
			Restore:          []string{"yarn", "install", "--frozen-lockfile"},
			Compile:          []string{"sh", "-c", "export PATH=\"${PWD}/node_modules/.bin:${PATH}\" && yarn run build"},
			Test:             []string{"sh", "-c", "CI=true yarn test"},
			Coverage:         []string{"sh", "-c", "CI=true yarn run coverage || CI=true yarn test"},
			CacheTargetPaths: []string{"/root/.npm", "/usr/local/share/.cache/yarn"},
			ArtifactPaths:    []string{"coverage/lcov.info"},
		}
	case CSharpDotnet:
		img := resolveDotNetDockerImage(imageDotNet, repoPath)
		return ToolchainProfile{
			ID:      CSharpDotnet,
			Image:   img,
			Restore: []string{"dotnet", "restore"},
			Compile: []string{"dotnet", "build", "-c", "Release", "--no-restore"},
			Test:    []string{"dotnet", "test", "-c", "Release", "--no-build"},
			// Two argv elements: a single --collect:"…" token can be misparsed so MSBuild sees a /p: name with spaces (MSB4177).
			Coverage:         []string{"dotnet", "test", "-c", "Release", "--no-build", "--collect", "XPlat Code Coverage"},
			CacheTargetPaths: []string{"/root/.nuget/packages"},
			ArtifactPaths:    []string{"TestResults"},
		}
	case TypeScriptNPM:
		fallthrough
	default:
		img := strings.TrimSpace(imageNode)
		if img == "" {
			img = nodeImageIfUnset
		}
		return ToolchainProfile{
			ID:               TypeScriptNPM,
			Image:            img,
			Restore:          []string{"npm", "install"},
			Compile:          []string{"sh", "-c", "export PATH=\"${PWD}/node_modules/.bin:${PATH}\" && npm run build"},
			Test:             []string{"sh", "-c", "CI=true npm test"},
			Coverage:         []string{"sh", "-c", "CI=true npm run coverage --if-present || CI=true npm test"},
			CacheTargetPaths: []string{"/root/.npm"},
			ArtifactPaths:    []string{"coverage/lcov.info"},
		}
	}
}

// DetectToolchainID picks a profile from repo layout and language.
func DetectToolchainID(repoPath, lang string) ToolchainID {
	lang = strings.ToLower(strings.TrimSpace(lang))
	dir := filepath.Clean(repoPath)
	switch lang {
	case "csharp", "cs":
		return CSharpDotnet
	case "java":
		hasPom := fileExists(filepath.Join(dir, "pom.xml"))
		hasGradle := fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts"))
		if hasGradle && !hasPom {
			return JavaGradle
		}
		if hasPom {
			return JavaMaven
		}
		if hasGradle {
			return JavaGradle
		}
		// Workflow may still report "java" for mixed or mis-counted repos; do not default to Maven if this is a .NET tree.
		// Regression: TestDetectToolchainID_javaNoMavenGradle_csprojRegistry / TestResolveToolchain_auto_javaLang_csprojUsesDotnet.
		if repoHasCsproj(dir) {
			return CSharpDotnet
		}
		return JavaMaven
	case "javascript", "typescript", "js", "ts":
		if !fileExists(filepath.Join(dir, "package.json")) {
			return UnsupportedDocker
		}
		if fileExists(filepath.Join(dir, "pnpm-lock.yaml")) {
			return TypeScriptPNPM
		}
		if fileExists(filepath.Join(dir, "yarn.lock")) {
			return TypeScriptYarn
		}
		return TypeScriptNPM
	default:
		return UnsupportedDocker
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// repoHasCsproj reports whether root contains a *.csproj under common noise directories skipped.
func repoHasCsproj(root string) bool {
	root = filepath.Clean(root)
	var found bool
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			switch name {
			case ".git", "node_modules", "bin", "obj", "out", "build", "dist", "target", "packages", ".nuget", "__pycache__", ".next":
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".csproj") {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// ResolveToolchain returns the profile: explicit profile name wins, else auto-detect.
func ResolveToolchain(repoPath, lang, explicitProfile string, imageJavaMaven, imageJavaGradle, imageNode, imageDotNet string) (ToolchainProfile, error) {
	raw := strings.TrimSpace(strings.ToLower(explicitProfile))
	var id ToolchainID
	nodeImg := DefaultNodeImage
	switch raw {
	case "", "auto":
		id = DetectToolchainID(repoPath, lang)
	case "java-maven":
		id = JavaMaven
	case "java-maven-11", "maven-11", "mvn-11":
		id = JavaMaven11
	case "java-maven-21", "maven-21", "mvn-21":
		id = JavaMaven21
	case "java-gradle":
		id = JavaGradle
	case "java-gradle-11", "gradle-11":
		id = JavaGradle11
	case "java-gradle-21", "gradle-21":
		id = JavaGradle21
	case "typescript-npm", "ts-npm":
		id = TypeScriptNPM
	case "typescript-pnpm", "ts-pnpm":
		id = TypeScriptPNPM
	case "typescript-yarn":
		id = TypeScriptYarn
	case "nodejs-lts", "node-lts", "typescript-lts", "ts-lts":
		id = DetectToolchainID(repoPath, "typescript")
		if id != TypeScriptNPM && id != TypeScriptPNPM && id != TypeScriptYarn {
			return ToolchainProfile{}, ErrUnsupportedToolchain
		}
		nodeImg = DefaultNodeLTSImage
	case "typescript-npm-lts", "ts-npm-lts":
		id = TypeScriptNPM
		nodeImg = DefaultNodeLTSImage
	case "typescript-pnpm-lts", "ts-pnpm-lts":
		id = TypeScriptPNPM
		nodeImg = DefaultNodeLTSImage
	case "typescript-yarn-lts", "ts-yarn-lts":
		id = TypeScriptYarn
		nodeImg = DefaultNodeLTSImage
	case "csharp-dotnet", "dotnet":
		id = CSharpDotnet
	default:
		id = DetectToolchainID(repoPath, lang)
	}
	// Evaluation language wins over runner.eval_profile: shared configs often pin java-maven while the
	// workflow correctly sets Lang=csharp — do not run mvn in Docker for C# runs.
	// When adding java-* eval_profile values, extend javaDockerEvalProfiles in eval_toolchain_contract_test.go.
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "csharp", "cs":
		id = CSharpDotnet
	}
	if id == UnsupportedDocker {
		return ToolchainProfile{}, ErrUnsupportedToolchain
	}
	p := builtinToolchain(id, imageJavaMaven, imageJavaGradle, imageNode, imageDotNet, nodeImg, repoPath)
	if p.Image == "" {
		return ToolchainProfile{}, ErrUnsupportedToolchain
	}
	return p, nil
}

// ApplyCommandOverrides merges runner config compile_command / test_command into the profile.
// Non-empty values replace the built-in argv for that step (Docker runs them as sh -c "<line>").
// When testCommand is set, both Test and Coverage use it so a single override controls all test-like steps.
//
// For typescript-pnpm / typescript-yarn, official Node OCI images do not place pnpm/yarn on PATH until
// Corepack shims are enabled; bare overrides like "pnpm test" would fail with "pnpm: not found".
// We prepend corepack enable (and node_modules/.bin on PATH) to match the built-in profile behavior.
func ApplyCommandOverrides(p ToolchainProfile, compileCommand, testCommand string) ToolchainProfile {
	if c := strings.TrimSpace(compileCommand); c != "" {
		p.Compile = []string{"sh", "-c", wrapDockerNodeToolchainShell(p.ID, c)}
	}
	if c := strings.TrimSpace(testCommand); c != "" {
		wrapped := wrapDockerNodeToolchainShell(p.ID, c)
		p.Test = []string{"sh", "-c", wrapped}
		p.Coverage = []string{"sh", "-c", wrapped}
	}
	return p
}

func wrapDockerNodeToolchainShell(id ToolchainID, script string) string {
	script = strings.TrimSpace(script)
	switch id {
	case TypeScriptPNPM, TypeScriptYarn:
		return wrapCorepackManagedCLI(script)
	default:
		return script
	}
}

func wrapCorepackManagedCLI(script string) string {
	if script == "" {
		return script
	}
	if strings.Contains(script, "corepack enable") {
		return ensureNodeModulesBinInPath(script)
	}
	return "corepack enable && " + ensureNodeModulesBinInPath(script)
}

func ensureNodeModulesBinInPath(script string) string {
	if strings.Contains(script, "node_modules/.bin") {
		return script
	}
	return "export PATH=\"${PWD}/node_modules/.bin:${PATH}\" && " + script
}

// ErrUnsupportedToolchain means Docker eval cannot run this lang/repo combo.
var ErrUnsupportedToolchain = errUnsupported{}

type errUnsupported struct{}

func (errUnsupported) Error() string { return "unsupported toolchain for docker sandbox" }
