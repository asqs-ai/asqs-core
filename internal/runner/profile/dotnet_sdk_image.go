package profile

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// resolveDotNetDockerImage returns the Docker image for csharp-dotnet eval.
// When configured is non-empty it is returned as-is.
// When empty and repoPath is set, scans *.csproj under the repo (recursive, bounded depth, common
// build-artifact dirs skipped) for <TargetFramework> / <TargetFrameworks> and picks
// mcr.microsoft.com/dotnet/sdk:{major}.0 for the maximum net{major}.* TFM found.
// When nothing matches, DefaultDotNetImage is used.
func resolveDotNetDockerImage(configured, repoPath string) string {
	if s := strings.TrimSpace(configured); s != "" {
		return s
	}
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return DefaultDotNetImage
	}
	// The image must satisfy BOTH constraints the repo states: the highest net{major} its projects
	// target (including monikers inherited from Directory.Build.props) and the SDK its global.json
	// pins. Only the first was consulted, so a net8.0 solution with global.json pinning SDK 10 was
	// evaluated in sdk:8.0 and every step failed at "A compatible .NET SDK was not found".
	major := MaxDotNetSdkMajorRequiredByRepo(repoPath)
	if major <= 0 {
		return DefaultDotNetImage
	}
	return "mcr.microsoft.com/dotnet/sdk:" + strconv.Itoa(major) + ".0"
}

const maxDotNetCsprojWalkDepth = 12

// MaxNetTFMMajorFromRepo returns the largest net{major}.* major version found in any .csproj under
// repoRoot, or 0 when no TFMs are discovered.
func MaxNetTFMMajorFromRepo(repoRoot string) int {
	tag, ok := dotNetSDKTagFromRepo(repoRoot)
	if !ok {
		return 0
	}
	parts := strings.SplitN(tag, ".", 2)
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return n
}

// dotnetWalkSkipDir delegates to dotnetproj.WalkSkipDir: there were five of these lists and they
// disagreed, so two walks over the same tree descended into different build output.
func dotnetWalkSkipDir(name string) bool {
	return dotnetproj.WalkSkipDir(name)
}

func repoWalkDepth(root, abs string) int {
	root = filepath.Clean(root)
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator))
}

// dotNetSDKTagFromRepo returns an SDK image tag like "8.0" when TFMs are found.
func dotNetSDKTagFromRepo(repoRoot string) (string, bool) {
	dir := filepath.Clean(repoRoot)
	maxMajor := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			if dotnetWalkSkipDir(d.Name()) {
				return fs.SkipDir
			}
			if repoWalkDepth(dir, path) > maxDotNetCsprojWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			// Through ResolveFacts, so a TargetFramework factored into Directory.Build.props counts.
			facts, ferr := dotnetproj.ResolveFacts(dir, path)
			if ferr != nil {
				return nil
			}
			if m := facts.MaxNetMajor(); m > maxMajor {
				maxMajor = m
			}
		}
		return nil
	})
	if maxMajor <= 0 {
		return "", false
	}
	return strconv.Itoa(maxMajor) + ".0", true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
