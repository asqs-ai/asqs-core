package profile

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	reTargetFramework  = regexp.MustCompile(`<TargetFramework>\s*([^<\s]+)\s*</TargetFramework>`)
	reTargetFrameworks = regexp.MustCompile(`<TargetFrameworks>\s*([^<]+)\s*</TargetFrameworks>`)
	reNetMajor         = regexp.MustCompile(`(?i)net(\d+)\.`)
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
	tag, ok := dotNetSDKTagFromRepo(repoPath)
	if !ok {
		return DefaultDotNetImage
	}
	return "mcr.microsoft.com/dotnet/sdk:" + tag
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

func dotnetWalkSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "node_modules", ".git", "bin", "obj", "packages", "dist", "target",
		"build", "coverage", ".vs", "venv", "__pycache__", "vendor",
		"playwright-report", "test-results", ".gradle", ".idea":
		return true
	default:
		return len(name) > 0 && name[0] == '.'
	}
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
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if m := maxNetMajorFromProjectXML(string(b)); m > maxMajor {
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

func maxNetMajorFromProjectXML(s string) int {
	max := 0
	for _, m := range reTargetFramework.FindAllStringSubmatch(s, -1) {
		if len(m) > 1 {
			max = maxInt(max, maxNetMajorFromTFM(strings.TrimSpace(m[1])))
		}
	}
	for _, m := range reTargetFrameworks.FindAllStringSubmatch(s, -1) {
		if len(m) <= 1 {
			continue
		}
		for _, part := range strings.Split(m[1], ";") {
			max = maxInt(max, maxNetMajorFromTFM(strings.TrimSpace(part)))
		}
	}
	return max
}

func maxNetMajorFromTFM(tfm string) int {
	if tfm == "" {
		return 0
	}
	sub := reNetMajor.FindStringSubmatch(tfm)
	if len(sub) < 2 {
		return 0
	}
	n, err := strconv.Atoi(sub[1])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
