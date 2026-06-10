package testbootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// centralPackageManagementSearchCeiling returns the directory up to which we walk to find
// Directory.Packages.props. When gitRepoRoot is set and contains the .csproj directory (typical
// mono-repo: test workspace under full clone), the ceiling is the git root so CPM files outside
// the bootstrap folder are still found. Otherwise the ceiling is workspaceRoot.
func centralPackageManagementSearchCeiling(workspaceRoot, gitRepoRoot, csprojPath string) string {
	w := filepath.Clean(workspaceRoot)
	csprojDir := filepath.Clean(filepath.Dir(csprojPath))
	g := filepath.Clean(strings.TrimSpace(gitRepoRoot))
	if g == "" {
		return w
	}
	rel, err := filepath.Rel(g, csprojDir)
	if err != nil {
		return w
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return w
	}
	return g
}

// findCentralPackageProps returns the path to Directory.Packages.props when walking from startDir
// up to ceilingDir (inclusive). Empty string if none found or startDir is not under ceilingDir.
func findCentralPackageProps(ceilingDir, startDir string) string {
	repoRoot := filepath.Clean(ceilingDir)
	dir := filepath.Clean(startDir)
	for {
		rel, err := filepath.Rel(repoRoot, dir)
		if err != nil || (rel != "." && strings.HasPrefix(rel, "..")) {
			return ""
		}
		p := filepath.Join(dir, "Directory.Packages.props")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
		if dir == repoRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func propsDeclaresPackageVersion(content, include string) bool {
	incLower := strings.ToLower(include)
	for _, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		low := strings.ToLower(trim)
		if !strings.HasPrefix(low, "<packageversion") {
			continue
		}
		if strings.Contains(low, `include="`+incLower+`"`) || strings.Contains(low, `include='`+incLower+`'`) {
			return true
		}
	}
	return false
}

// mergeCentralPackageVersions inserts an ItemGroup of PackageVersion entries for any keys in pkgs
// not already declared. pkgs maps package id -> version string.
func mergeCentralPackageVersions(content string, pkgs map[string]string) (newContent string, changed bool, err error) {
	var missing []string
	for id := range pkgs {
		if !propsDeclaresPackageVersion(content, id) {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return content, false, nil
	}
	var lines []string
	for _, id := range missing {
		ver := pkgs[id]
		if strings.TrimSpace(ver) == "" {
			return "", false, fmt.Errorf("empty version for PackageVersion %q", id)
		}
		lines = append(lines, fmt.Sprintf(`    <PackageVersion Include="%s" Version="%s" />`, id, ver))
	}
	block := "  <ItemGroup>\n" + strings.Join(lines, "\n") + "\n  </ItemGroup>\n"
	lower := strings.ToLower(content)
	idx := strings.LastIndex(lower, "</project>")
	if idx < 0 {
		return "", false, fmt.Errorf("Directory.Packages.props: missing closing </Project>")
	}
	return content[:idx] + block + content[idx:], true, nil
}

// ensureCentralPackageVersions adds missing PackageVersion lines to propsPath.
func ensureCentralPackageVersions(propsPath string, pkgs map[string]string) (bool, error) {
	b, err := os.ReadFile(propsPath)
	if err != nil {
		return false, err
	}
	s := string(b)
	out, changed, err := mergeCentralPackageVersions(s, pkgs)
	if err != nil || !changed {
		return changed, err
	}
	return true, atomicWrite(propsPath, []byte(out))
}
