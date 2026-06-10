package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// GlobalJsonSdkMajor returns the major version from repoRoot/global.json → sdk.version (e.g. "10.0.201" → 10).
// Missing file, invalid JSON, or absent version returns 0.
func GlobalJsonSdkMajor(repoRoot string) int {
	path := filepath.Join(filepath.Clean(repoRoot), "global.json")
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return 0
	}
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		b = b[3:]
	}
	var root struct {
		SDK *struct {
			Version string `json:"version"`
		} `json:"sdk"`
	}
	if json.Unmarshal(b, &root) != nil || root.SDK == nil {
		return 0
	}
	ver := strings.TrimSpace(root.SDK.Version)
	if ver == "" {
		return 0
	}
	// "10.0.201" or "10.0.201-preview.1"
	before, _, _ := strings.Cut(ver, "-")
	parts := strings.SplitN(before, ".", 2)
	if len(parts) == 0 || parts[0] == "" {
		return 0
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	return n
}

// MaxDotNetSdkMajorRequiredByRepo is max(MaxNetTFMMajorFromRepo, GlobalJsonSdkMajor): the highest .NET major
// implied by TFMs under the repo or by root global.json sdk.version. Used to decide when a Playwright/dotnet
// Docker image (bundled SDK 8) needs a side-by-side SDK install — e.g. net8.0 projects with global.json pinning SDK 10.
func MaxDotNetSdkMajorRequiredByRepo(repoRoot string) int {
	a := MaxNetTFMMajorFromRepo(repoRoot)
	b := GlobalJsonSdkMajor(repoRoot)
	if b > a {
		return b
	}
	return a
}
