package testbootstrap

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxBootstrapWalkDepth = 12

// bootstrapSkipDir reports whether a directory entry should be skipped during repo walks.
func bootstrapSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "node_modules", ".git", "dist", "build", "bin", "obj", "target", "packages",
		".vs", "venv", "__pycache__", "vendor", "coverage", "playwright-report",
		"test-results", ".gradle", ".idea":
		return true
	default:
		if len(name) > 0 && name[0] == '.' && name != "." && name != ".." {
			return true
		}
		return false
	}
}

func repoRelDepth(repo, absPath string) int {
	repo = filepath.Clean(repo)
	absPath = filepath.Clean(absPath)
	rel, err := filepath.Rel(repo, absPath)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator))
}

// csprojRelForDotnet returns a path suitable for `dotnet build|test` with working directory repoDir.
func csprojRelForDotnet(repoDir, csprojAbs string) (string, error) {
	repoDir = filepath.Clean(repoDir)
	csprojAbs = filepath.Clean(csprojAbs)
	rel, err := filepath.Rel(repoDir, csprojAbs)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf(".csproj %q is outside repo %q", csprojAbs, repoDir)
	}
	return filepath.ToSlash(rel), nil
}

// primaryCsprojAbs picks a .csproj for C# bootstrap and test verification.
//  1. If one or more *.sln / *.slnx exist at the repo root, only projects referenced by those solutions are considered:
//     prefer one that already has Microsoft.NET.Test.Sdk / xUnit / NUnit / MSTest; otherwise prefer a test-shaped
//     project (*.Tests, under tests/) so xUnit is not injected into an arbitrary app project.
//  2. Otherwise prefers root-level SDK-style .csproj (skipping MAUI/mobile TFMs when possible), then nested projects
//     (workload-heavy and non-test-shaped sort last).
func primaryCsprojAbs(repo string) (string, error) {
	repo = filepath.Clean(repo)
	fromSln, err := collectCsprojPathsFromRootSolutions(repo)
	if err != nil {
		return "", err
	}
	if len(fromSln) > 0 {
		if picked := pickPrimaryCsprojFromSolutionCsprojs(fromSln); picked != "" {
			return picked, nil
		}
	}
	if abs, err := pickRootCsprojForBootstrap(repo); err != nil {
		return "", err
	} else if abs != "" {
		return abs, nil
	}
	paths, err := discoverSDKStyleCsprojPaths(repo)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	workload := make(map[string]bool, len(paths))
	for _, p := range paths {
		workload[p] = csprojRequiresOptionalDotnetWorkload(p)
	}
	sort.SliceStable(paths, func(i, j int) bool {
		wi, wj := workload[paths[i]], workload[paths[j]]
		if wi != wj {
			return !wi && wj
		}
		si, sj := csprojBootstrapScore(paths[i]), csprojBootstrapScore(paths[j])
		if si != sj {
			return si < sj
		}
		return paths[i] < paths[j]
	})
	return paths[0], nil
}

// pickPrimaryCsprojFromSolutionCsprojs chooses the best bootstrap target among projects listed in root .sln / .slnx files.
func pickPrimaryCsprojFromSolutionCsprojs(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	type meta struct {
		abs       string
		workload  bool
		hasTests  bool
		likeScore int
		bootScore int
	}
	ms := make([]meta, 0, len(paths))
	for _, abs := range paths {
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		s := string(b)
		ms = append(ms, meta{
			abs:       abs,
			workload:  csprojRequiresOptionalDotnetWorkloadContent(s),
			hasTests:  csprojHasDotNetTestFrameworkContent(s),
			likeScore: csprojTestProjectLikenessScore(abs),
			bootScore: csprojBootstrapScore(abs),
		})
	}
	if len(ms) == 0 {
		return ""
	}
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if a.hasTests != b.hasTests {
			return a.hasTests && !b.hasTests
		}
		if a.workload != b.workload {
			return !a.workload && b.workload
		}
		if !a.hasTests && a.likeScore != b.likeScore {
			return a.likeScore > b.likeScore
		}
		if a.bootScore != b.bootScore {
			return a.bootScore < b.bootScore
		}
		return a.abs < b.abs
	})
	return ms[0].abs
}

func csprojBootstrapScore(abs string) int {
	p := strings.ToLower(filepath.ToSlash(abs))
	base := strings.ToLower(filepath.Base(abs))
	score := strings.Count(p, "/") * 3
	if strings.Contains(base, "test") {
		score -= 30
	}
	if strings.Contains(p, "/tests/") || strings.Contains(p, "/test/") {
		score -= 15
	}
	return score
}

func discoverSDKStyleCsprojPaths(repo string) ([]string, error) {
	all, err := discoverCsprojPaths(repo)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range all {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if isSdkStyleCsproj(string(b)) {
			out = append(out, p)
		}
	}
	return out, nil
}

func discoverCsprojPaths(repo string) ([]string, error) {
	var out []string
	repo = filepath.Clean(repo)
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == repo {
				return nil
			}
			if bootstrapSkipDir(d.Name()) {
				return fs.SkipDir
			}
			if repoRelDepth(repo, path) > maxBootstrapWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ".csproj") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

// --- Java ---

type javaBuildKind int

const (
	javaBuildNone javaBuildKind = iota
	javaBuildMaven
	javaBuildGradleGroovy
	javaBuildGradleKotlin
)

type javaBuildPick struct {
	Abs  string
	Kind javaBuildKind
}

// primaryJavaBuildFile selects pom.xml or Gradle build file for bootstrap (skips obvious Maven aggregators when possible).
func primaryJavaBuildFile(repo string) (javaBuildPick, error) {
	repo = filepath.Clean(repo)
	poms, err := discoverPomXMLPaths(repo)
	if err != nil {
		return javaBuildPick{}, err
	}
	gradles, err := discoverGradlePaths(repo)
	if err != nil {
		return javaBuildPick{}, err
	}
	if len(poms) == 0 && len(gradles) == 0 {
		return javaBuildPick{}, nil
	}
	pickPom, err := selectPrimaryPomPath(repo, poms)
	if err != nil {
		return javaBuildPick{}, err
	}
	pickGradle, kind := selectPrimaryGradlePath(repo, gradles)
	switch {
	case pickPom != "" && pickGradle != "":
		depP := repoRelDepth(repo, pickPom)
		depG := repoRelDepth(repo, pickGradle)
		if depP < depG {
			return javaBuildPick{Abs: pickPom, Kind: javaBuildMaven}, nil
		}
		if depG < depP {
			return javaBuildPick{Abs: pickGradle, Kind: kind}, nil
		}
		// Same depth: prefer Maven when both exist (common in mixed templates).
		return javaBuildPick{Abs: pickPom, Kind: javaBuildMaven}, nil
	case pickPom != "":
		return javaBuildPick{Abs: pickPom, Kind: javaBuildMaven}, nil
	case pickGradle != "":
		return javaBuildPick{Abs: pickGradle, Kind: kind}, nil
	}
	return javaBuildPick{}, nil
}

func discoverPomXMLPaths(repo string) ([]string, error) {
	var out []string
	repo = filepath.Clean(repo)
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != repo && strings.EqualFold(d.Name(), "target") {
				return fs.SkipDir
			}
			if path == repo {
				return nil
			}
			if bootstrapSkipDir(d.Name()) {
				return fs.SkipDir
			}
			if repoRelDepth(repo, path) > maxBootstrapWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(d.Name(), "pom.xml") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func discoverGradlePaths(repo string) ([]string, error) {
	var out []string
	repo = filepath.Clean(repo)
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == repo {
				return nil
			}
			if bootstrapSkipDir(d.Name()) {
				return fs.SkipDir
			}
			if repoRelDepth(repo, path) > maxBootstrapWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		n := d.Name()
		if n == "build.gradle" || n == "build.gradle.kts" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func selectPrimaryPomPath(repo string, poms []string) (string, error) {
	if len(poms) == 0 {
		return "", nil
	}
	type scored struct {
		path  string
		depth int
		agg   bool
	}
	var rows []scored
	for _, p := range poms {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rows = append(rows, scored{
			path:  p,
			depth: repoRelDepth(repo, p),
			agg:   isLikelyMavenAggregator(string(b)),
		})
	}
	if len(rows) == 0 {
		return "", nil
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].agg != rows[j].agg {
			return !rows[i].agg && rows[j].agg
		}
		if rows[i].depth != rows[j].depth {
			return rows[i].depth < rows[j].depth
		}
		return rows[i].path < rows[j].path
	})
	return rows[0].path, nil
}

func isLikelyMavenAggregator(pom string) bool {
	low := strings.ToLower(pom)
	return strings.Contains(low, "<modules>") && strings.Contains(low, "<packaging>pom</packaging>")
}

func selectPrimaryGradlePath(repo string, gradles []string) (abs string, kind javaBuildKind) {
	if len(gradles) == 0 {
		return "", javaBuildNone
	}
	sort.SliceStable(gradles, func(i, j int) bool {
		di := repoRelDepth(repo, gradles[i])
		dj := repoRelDepth(repo, gradles[j])
		if di != dj {
			return di < dj
		}
		return gradles[i] < gradles[j]
	})
	g := gradles[0]
	if strings.HasSuffix(g, ".kts") {
		return g, javaBuildGradleKotlin
	}
	return g, javaBuildGradleGroovy
}

// --- JS / npm ---

// resolveJSPackageDirForBootstrap returns the directory whose package.json should be patched.
// Prefers repo root when package.json exists there; otherwise discovers a nested package.json.
func resolveJSPackageDirForBootstrap(repo string) (string, error) {
	repo = filepath.Clean(repo)
	rootPkg := filepath.Join(repo, "package.json")
	if st, err := os.Stat(rootPkg); err == nil && !st.IsDir() {
		return repo, nil
	}
	dirs, err := discoverPackageJSONDirs(repo)
	if err != nil {
		return "", err
	}
	if len(dirs) == 0 {
		return "", fmt.Errorf("no package.json under repo (searched nested paths)")
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		si, sj := jsPackageDirScore(repo, dirs[i]), jsPackageDirScore(repo, dirs[j])
		if si != sj {
			return si < sj
		}
		return dirs[i] < dirs[j]
	})
	return dirs[0], nil
}

func jsPackageDirScore(repo, abs string) int {
	p := strings.ToLower(filepath.ToSlash(abs))
	score := repoRelDepth(repo, abs) * 2
	if strings.Contains(p, "/e2e") || strings.Contains(p, "/apps/") {
		score -= 5
	}
	if strings.Contains(p, "playwright") {
		score -= 8
	}
	return score
}

func discoverPackageJSONDirs(repo string) ([]string, error) {
	var out []string
	repo = filepath.Clean(repo)
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == repo {
				return nil
			}
			if bootstrapSkipDir(d.Name()) {
				return fs.SkipDir
			}
			if repoRelDepth(repo, path) > maxBootstrapWalkDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "package.json" {
			out = append(out, filepath.Dir(path))
		}
		return nil
	})
	return out, err
}

// npmInstallWorkdir returns the directory to run npm/pnpm/yarn install from: nearest ancestor of
// pkgDir (up to repoRoot) that contains a lockfile, or pkgDir if none.
func npmInstallWorkdir(repoRoot, pkgDir string) string {
	repoRoot = filepath.Clean(repoRoot)
	pkgDir = filepath.Clean(pkgDir)
	cur := pkgDir
	for {
		if hasAnyNPMLockfile(cur) {
			return cur
		}
		if cur == repoRoot {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return pkgDir
}

func hasAnyNPMLockfile(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "yarn.lock")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err == nil {
		return true
	}
	return false
}

// jsPackageRootsForDetection returns package directories to inspect for E2E / test detection.
func jsPackageRootsForDetection(repo string) ([]string, error) {
	repo = filepath.Clean(repo)
	rootPkg := filepath.Join(repo, "package.json")
	if st, err := os.Stat(rootPkg); err == nil && !st.IsDir() {
		var roots []string
		roots = append(roots, repo)
		ws, err := npmWorkspacePackageDirs(repo)
		if err != nil {
			return nil, err
		}
		roots = append(roots, ws...)
		return dedupeSortedStrings(roots), nil
	}
	return discoverPackageJSONDirs(repo)
}

func dedupeSortedStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = filepath.Clean(s)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func npmWorkspacePackageDirs(repo string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(repo, "package.json"))
	if err != nil {
		return nil, err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	patterns := collectWorkspacePatterns(root)
	var out []string
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if strings.Contains(pat, "*") {
			g, err := filepath.Glob(filepath.Join(repo, filepath.FromSlash(pat)))
			if err != nil {
				continue
			}
			for _, d := range g {
				if st, e := os.Stat(filepath.Join(d, "package.json")); e == nil && !st.IsDir() {
					out = append(out, filepath.Clean(d))
				}
			}
			continue
		}
		abs := filepath.Join(repo, filepath.FromSlash(pat))
		if st, e := os.Stat(filepath.Join(abs, "package.json")); e == nil && !st.IsDir() {
			out = append(out, filepath.Clean(abs))
		}
	}
	return out, nil
}

func relPathForBootstrap(repo, abs string) string {
	repo = filepath.Clean(repo)
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(repo, abs)
	if err != nil {
		return filepath.Base(abs)
	}
	return filepath.ToSlash(rel)
}

func collectWorkspacePatterns(root map[string]interface{}) []string {
	var out []string
	if ws, ok := root["workspaces"].([]interface{}); ok {
		for _, v := range ws {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if m, ok := root["workspaces"].(map[string]interface{}); ok {
		if pkgs, ok := m["packages"].([]interface{}); ok {
			for _, v := range pkgs {
				if s, ok := v.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
