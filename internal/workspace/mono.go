// Package workspace provides helpers for scoping qualitybot to a subdirectory of a git repository (mono-repo).
package workspace

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// NormalizeMonoRepoWorkspace validates and returns a repo-relative path using forward slashes, no leading/trailing slashes, no ".." segments.
// Empty input returns ("", nil). Use for indexer.mono_repo_workspace config.
func NormalizeMonoRepoWorkspace(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	s = filepath.ToSlash(s)
	s = strings.Trim(s, "/")
	if s == "" {
		return "", nil
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("mono_repo_workspace: invalid segment %q in %q (no empty, ., or .. segments)", seg, s)
		}
	}
	return s, nil
}

// JoinGitRootAndWorkspace returns filepath.Join(gitRoot, workspaceRel) with workspaceRel using OS separators.
// workspaceRel must be normalized via NormalizeMonoRepoWorkspace; empty workspaceRel returns gitRoot.
func JoinGitRootAndWorkspace(gitRoot, workspaceRel string) (string, error) {
	gitRoot = filepath.Clean(strings.TrimSpace(gitRoot))
	if gitRoot == "" {
		return "", fmt.Errorf("mono_repo_workspace: empty git root")
	}
	absRoot, err := filepath.Abs(gitRoot)
	if err != nil {
		return "", err
	}
	ws, err := NormalizeMonoRepoWorkspace(workspaceRel)
	if err != nil {
		return "", err
	}
	if ws == "" {
		return absRoot, nil
	}
	out := filepath.Join(absRoot, filepath.FromSlash(ws))
	out, err = filepath.Abs(out)
	if err != nil {
		return "", err
	}
	// Ensure workspace stays under git root (after clean/abs).
	rel, err := filepath.Rel(absRoot, out)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("mono_repo_workspace: resolved path %q escapes git root %q", out, absRoot)
	}
	return out, nil
}

// HasProjectRootMarker is true when dir contains a conventional project root file: pom.xml, package.json,
// build.gradle, build.gradle.kts, or any *.sln / *.slnx / *.csproj in the directory (not recursive).
func HasProjectRootMarker(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		switch n {
		case "pom.xml", "package.json", "build.gradle", "build.gradle.kts":
			return true
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".sln", ".slnx", ".csproj":
			return true
		}
	}
	return false
}

// FileUnderPrefix reports whether repo-relative file path (forward slashes) is exactly prefix or under prefix/.
func FileUnderPrefix(file, prefix string) bool {
	prefix = strings.Trim(filepath.ToSlash(strings.TrimSpace(prefix)), "/")
	if prefix == "" {
		return true
	}
	f := strings.Trim(filepath.ToSlash(strings.TrimSpace(file)), "/")
	if f == "" {
		return false
	}
	if f == prefix {
		return true
	}
	return strings.HasPrefix(f, prefix+"/")
}

// ResolveMonoScanRoots returns repo-relative directory roots to include in indexer.ScanRepoForFiles when using
// mono_repo_workspace plus optional mono_repo_extra_paths (shared libraries, e.g. services/base next to projects/upper).
// Returns (nil, nil) when primary is empty and extras is empty — caller walks the entire git root once.
// When primary is non-empty, the first root is always primary; extras are sibling (or other) trees not already under primary.
func ResolveMonoScanRoots(gitRootAbs, primaryNorm string, rawExtras []string) ([]string, error) {
	gitRootAbs = filepath.Clean(strings.TrimSpace(gitRootAbs))
	var normExtras []string
	for _, e := range rawExtras {
		n, err := NormalizeMonoRepoWorkspace(e)
		if err != nil {
			return nil, fmt.Errorf("mono_repo_extra_paths: %w", err)
		}
		if n != "" {
			normExtras = append(normExtras, n)
		}
	}
	if len(normExtras) > 0 && primaryNorm == "" {
		return nil, fmt.Errorf("mono_repo_extra_paths requires indexer.mono_repo_workspace to be set (extra paths only apply together with a primary workspace)")
	}
	if primaryNorm == "" {
		return nil, nil
	}
	roots := []string{primaryNorm}
	for _, e := range normExtras {
		if e == primaryNorm {
			continue
		}
		if FileUnderPrefix(e, primaryNorm) {
			continue
		}
		if FileUnderPrefix(primaryNorm, e) {
			return nil, fmt.Errorf("mono_repo_workspace %q is under mono_repo_extra_paths entry %q; remove that extra or widen mono_repo_workspace", primaryNorm, e)
		}
		roots = append(roots, e)
	}
	for _, r := range roots {
		p := filepath.Join(gitRootAbs, filepath.FromSlash(r))
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("mono repo scan root %q: %w", r, err)
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("mono repo scan root %q: not a directory", r)
		}
	}
	return roots, nil
}

// MonoRepoExtraRootsRel returns normalized repo-relative scan roots from mono_repo_extra_paths only
// (the primary mono_repo_workspace entry is excluded). Empty/nil when there is no primary workspace,
// validation fails, or extras is empty. Same validation as ResolveMonoScanRoots.
func MonoRepoExtraRootsRel(gitRootAbs, primaryNorm string, rawExtras []string) ([]string, error) {
	roots, err := ResolveMonoScanRoots(gitRootAbs, primaryNorm, rawExtras)
	if err != nil {
		return nil, err
	}
	if len(roots) <= 1 {
		return nil, nil
	}
	out := make([]string, len(roots)-1)
	copy(out, roots[1:])
	return out, nil
}

// MonoDependencyRootsOptions controls auto-expansion of additional mono-repo index roots.
// Task 8 wiring: this starts with manual extras compatibility and can be extended with language-specific resolvers.
type MonoDependencyRootsOptions struct {
	LegacyExtraPaths []string
}

// MonoDependencyRootBreakdown captures root sets by discovery source for telemetry/rollout comparison.
type MonoDependencyRootBreakdown struct {
	Legacy   []string
	CSharp   []string
	Java     []string
	JS       []string
	Fallback []string
	Final    []string
}

// ResolveMonoDependencyRootsRel returns additional repo-relative roots (excluding primary workspace) to scan/index.
// Current behavior:
//   - always honors/validates legacy mono_repo_extra_paths for backward compatibility
//   - optional auto-expand fallback discovers sibling project-root-marker directories from git root
//     when no explicit legacy extras are configured.
func ResolveMonoDependencyRootsRel(gitRootAbs, primaryNorm string, opts MonoDependencyRootsOptions) ([]string, error) {
	b, err := ResolveMonoDependencyRootsWithBreakdown(gitRootAbs, primaryNorm, opts)
	if err != nil {
		return nil, err
	}
	return b.Final, nil
}

// ResolveMonoDependencyRootsWithBreakdown resolves extra roots and returns per-resolver buckets.
func ResolveMonoDependencyRootsWithBreakdown(gitRootAbs, primaryNorm string, opts MonoDependencyRootsOptions) (*MonoDependencyRootBreakdown, error) {
	legacy, err := MonoRepoExtraRootsRel(gitRootAbs, primaryNorm, opts.LegacyExtraPaths)
	if err != nil {
		return nil, err
	}
	out := &MonoDependencyRootBreakdown{Legacy: compressRoots(legacy, 0)}
	roots := append([]string(nil), legacy...)
	if primaryNorm == "" {
		out.Final = compressRoots(roots, 0)
		return out, nil
	}

	csRoots, err := ResolveCSharpProjectDependencyRootsRel(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	out.CSharp = csRoots
	roots = mergeRootSets(roots, csRoots)
	javaRoots, err := ResolveJavaProjectDependencyRootsRel(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	out.Java = javaRoots
	roots = mergeRootSets(roots, javaRoots)
	jsRoots, err := ResolveJSProjectDependencyRootsRel(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	out.JS = jsRoots
	roots = mergeRootSets(roots, jsRoots)
	if len(roots) > 0 {
		out.Final = compressRoots(roots, 0)
		return out, nil
	}
	fallbackRoots, err := discoverProjectMarkerRoots(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	out.Fallback = fallbackRoots
	out.Final = mergeRootSets(roots, fallbackRoots)
	return out, nil
}

func discoverProjectMarkerRoots(gitRootAbs, primaryNorm string) ([]string, error) {
	gitRootAbs = filepath.Clean(strings.TrimSpace(gitRootAbs))
	if gitRootAbs == "" {
		return nil, fmt.Errorf("mono dependency roots: empty git root")
	}
	var candidates []string
	err := filepath.WalkDir(gitRootAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		relRaw, relErr := filepath.Rel(gitRootAbs, path)
		if relErr != nil {
			return relErr
		}
		rel := filepath.ToSlash(relRaw)
		if rel == "." {
			rel = ""
		}
		if rel != "" {
			base := strings.ToLower(filepath.Base(path))
			if strings.HasPrefix(base, ".") || base == "node_modules" || base == "bin" || base == "obj" || base == "dist" || base == "target" {
				return filepath.SkipDir
			}
		}
		if rel != "" && !FileUnderPrefix(rel, primaryNorm) && !FileUnderPrefix(primaryNorm, rel) && HasProjectRootMarker(path) {
			candidates = append(candidates, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		di := strings.Count(candidates[i], "/")
		dj := strings.Count(candidates[j], "/")
		if di != dj {
			return di < dj
		}
		return candidates[i] < candidates[j]
	})
	var roots []string
	for _, c := range candidates {
		covered := false
		for _, r := range roots {
			if FileUnderPrefix(c, r) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		roots = append(roots, c)
	}
	return roots, nil
}

var slnProjPathRe = regexp.MustCompile(`"([^"]+\.csproj)"`)
var gradleProjectPathRe = regexp.MustCompile(`['"](:[A-Za-z0-9_.:-]+)['"]`)
var gradleProjectDirAssignRe = regexp.MustCompile(`project\(\s*['"](:[^'"]+)['"]\s*\)\.projectDir\s*=\s*file\(\s*['"]([^'"]+)['"]\s*\)`)

// ResolveCSharpProjectDependencyRootsRel discovers repo-relative extra roots by traversing C# project references
// from seed projects under mono primary workspace (root .csproj and .sln/.slnx memberships), then returns only
// dependency roots outside primaryNorm.
func ResolveCSharpProjectDependencyRootsRel(gitRootAbs, primaryNorm string) ([]string, error) {
	primaryNorm = strings.Trim(filepath.ToSlash(strings.TrimSpace(primaryNorm)), "/")
	if primaryNorm == "" {
		return nil, nil
	}
	primaryAbs, err := JoinGitRootAndWorkspace(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(primaryAbs)
	if err != nil {
		return nil, err
	}
	var seedProjAbs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".csproj") {
			seedProjAbs = append(seedProjAbs, filepath.Join(primaryAbs, e.Name()))
			continue
		}
		if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".slnx") {
			paths, perr := parseSolutionProjectPaths(filepath.Join(primaryAbs, e.Name()))
			if perr != nil {
				return nil, perr
			}
			for _, p := range paths {
				if p == "" {
					continue
				}
				if !filepath.IsAbs(p) {
					p = filepath.Join(primaryAbs, p)
				}
				seedProjAbs = append(seedProjAbs, filepath.Clean(p))
			}
		}
	}
	seedProjAbs = uniqueExistingAbsPaths(seedProjAbs)
	if len(seedProjAbs) == 0 {
		return nil, nil
	}

	type qitem struct {
		abs string
	}
	queue := make([]qitem, 0, len(seedProjAbs))
	seenProj := make(map[string]struct{}, len(seedProjAbs))
	for _, p := range seedProjAbs {
		absP, aerr := filepath.Abs(p)
		if aerr != nil {
			continue
		}
		queue = append(queue, qitem{abs: absP})
		seenProj[absP] = struct{}{}
	}

	var depRoots []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		refs, rerr := parseProjectReferences(cur.abs)
		if rerr != nil {
			return nil, rerr
		}
		for _, ref := range refs {
			ref = filepath.Clean(ref)
			if !filepath.IsAbs(ref) {
				ref = filepath.Join(filepath.Dir(cur.abs), ref)
			}
			refAbs, aerr := filepath.Abs(ref)
			if aerr != nil {
				continue
			}
			if _, statErr := os.Stat(refAbs); statErr != nil {
				continue
			}
			if _, ok := seenProj[refAbs]; ok {
				continue
			}
			seenProj[refAbs] = struct{}{}
			queue = append(queue, qitem{abs: refAbs})

			relProjDir, relErr := filepath.Rel(filepath.Clean(gitRootAbs), filepath.Dir(refAbs))
			if relErr != nil {
				continue
			}
			relProjDir = filepath.ToSlash(relProjDir)
			if relProjDir == "." || strings.HasPrefix(relProjDir, "..") {
				continue
			}
			if FileUnderPrefix(relProjDir, primaryNorm) {
				continue
			}
			depRoots = append(depRoots, relProjDir)
		}
	}
	depRoots = compressRoots(depRoots, 0)
	return depRoots, nil
}

type mavenCoord struct {
	GroupID    string
	ArtifactID string
}

type mavenPOM struct {
	GroupID      string        `xml:"groupId"`
	ArtifactID   string        `xml:"artifactId"`
	Parent       mavenPOMParen `xml:"parent"`
	Modules      []string      `xml:"modules>module"`
	Dependencies []mavenPOMDep `xml:"dependencies>dependency"`
}

type mavenPOMParen struct {
	GroupID string `xml:"groupId"`
}

type mavenPOMDep struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
}

type mavenPOMInfo struct {
	DirRel       string
	Coord        mavenCoord
	Modules      []string
	Dependencies []mavenCoord
}

type jsPackageJSON struct {
	Name                 string            `json:"name"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// ResolveJavaProjectDependencyRootsRel discovers extra roots for Java monorepos via Maven/Gradle local dependency graphs.
func ResolveJavaProjectDependencyRootsRel(gitRootAbs, primaryNorm string) ([]string, error) {
	mavenRoots, err := resolveMavenDependencyRootsRel(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	gradleRoots, err := resolveGradleDependencyRootsRel(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	return mergeRootSets(mavenRoots, gradleRoots), nil
}

// ResolveJSProjectDependencyRootsRel discovers extra roots for TS/JS monorepos using workspace package closure
// from package dependency names and tsconfig path aliases.
func ResolveJSProjectDependencyRootsRel(gitRootAbs, primaryNorm string) ([]string, error) {
	primaryNorm = strings.Trim(filepath.ToSlash(strings.TrimSpace(primaryNorm)), "/")
	if primaryNorm == "" {
		return nil, nil
	}
	primaryAbs, err := JoinGitRootAndWorkspace(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	pkgByDir, nameToDirs, err := collectRepoPackages(gitRootAbs)
	if err != nil {
		return nil, err
	}
	if len(pkgByDir) == 0 {
		return nil, nil
	}
	var queue []string
	seen := map[string]struct{}{}
	for dirRel := range pkgByDir {
		if FileUnderPrefix(dirRel, primaryNorm) {
			queue = append(queue, dirRel)
			seen[dirRel] = struct{}{}
		}
	}
	// If no package.json under workspace, still try tsconfig path expansion from workspace.
	if len(queue) == 0 {
		pathRoots, perr := resolveTSPathAliasRoots(primaryAbs, gitRootAbs, primaryNorm)
		if perr != nil {
			return nil, perr
		}
		return compressRoots(pathRoots, 0), nil
	}

	var depRoots []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		pkg := pkgByDir[cur]
		if pkg != nil {
			for _, depName := range pkg.Data.allDependencyNames() {
				for _, depDir := range nameToDirs[depName] {
					if _, ok := seen[depDir]; ok {
						continue
					}
					seen[depDir] = struct{}{}
					queue = append(queue, depDir)
					if !FileUnderPrefix(depDir, primaryNorm) {
						depRoots = append(depRoots, depDir)
					}
				}
			}
		}
		curAbs := filepath.Join(gitRootAbs, filepath.FromSlash(cur))
		pathRoots, perr := resolveTSPathAliasRoots(curAbs, gitRootAbs, primaryNorm)
		if perr != nil {
			return nil, perr
		}
		for _, r := range pathRoots {
			if _, ok := seen[r]; !ok {
				seen[r] = struct{}{}
				queue = append(queue, r)
			}
			if !FileUnderPrefix(r, primaryNorm) {
				depRoots = append(depRoots, r)
			}
		}
	}
	return compressRoots(depRoots, 0), nil
}

type jsPkgInfo struct {
	DirRel string
	Data   *jsPackageJSON
}

func (j *jsPackageJSON) allDependencyNames() []string {
	if j == nil {
		return nil
	}
	out := make([]string, 0, len(j.Dependencies)+len(j.DevDependencies)+len(j.PeerDependencies)+len(j.OptionalDependencies))
	appendKeys := func(m map[string]string) {
		for k := range m {
			k = strings.TrimSpace(k)
			if k != "" {
				out = append(out, k)
			}
		}
	}
	appendKeys(j.Dependencies)
	appendKeys(j.DevDependencies)
	appendKeys(j.PeerDependencies)
	appendKeys(j.OptionalDependencies)
	return dedupeStrings(out)
}

func collectRepoPackages(gitRootAbs string) (map[string]*jsPkgInfo, map[string][]string, error) {
	pkgByDir := map[string]*jsPkgInfo{}
	nameToDirs := map[string][]string{}
	err := filepath.WalkDir(gitRootAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := strings.ToLower(d.Name())
			if strings.HasPrefix(base, ".") || base == "node_modules" || base == "dist" || base == "build" || base == "target" || base == "bin" || base == "obj" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(d.Name(), "package.json") {
			return nil
		}
		dirAbs := filepath.Dir(path)
		rel, rerr := filepath.Rel(gitRootAbs, dirAbs)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || strings.HasPrefix(rel, "..") {
			return nil
		}
		data, perr := readJSPackageJSON(path)
		if perr != nil {
			return nil
		}
		info := &jsPkgInfo{DirRel: rel, Data: data}
		pkgByDir[rel] = info
		name := strings.TrimSpace(data.Name)
		if name != "" {
			nameToDirs[name] = append(nameToDirs[name], rel)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for name, dirs := range nameToDirs {
		nameToDirs[name] = compressRoots(dirs, 0)
	}
	return pkgByDir, nameToDirs, nil
}

func readJSPackageJSON(path string) (*jsPackageJSON, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p jsPackageJSON
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func resolveTSPathAliasRoots(baseAbs, gitRootAbs, primaryNorm string) ([]string, error) {
	var roots []string
	for _, cfgName := range []string{"tsconfig.json", "tsconfig.base.json"} {
		p := filepath.Join(baseAbs, cfgName)
		b, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		raw := map[string]interface{}{}
		if err := json.Unmarshal(stripTrailingCommas(stripJSONLineComments(string(b))), &raw); err != nil {
			continue
		}
		co, _ := raw["compilerOptions"].(map[string]interface{})
		if co == nil {
			continue
		}
		paths, _ := co["paths"].(map[string]interface{})
		for _, v := range paths {
			arr, _ := v.([]interface{})
			for _, item := range arr {
				target, _ := item.(string)
				target = strings.TrimSpace(target)
				if target == "" {
					continue
				}
				target = strings.TrimSuffix(target, "/*")
				abs := filepath.Clean(filepath.Join(baseAbs, filepath.FromSlash(target)))
				if _, err := os.Stat(abs); err != nil {
					continue
				}
				rootRel, ok := findNearestPackageDirRel(abs, gitRootAbs)
				if !ok {
					continue
				}
				if !FileUnderPrefix(rootRel, primaryNorm) {
					roots = append(roots, rootRel)
				}
			}
		}
	}
	return compressRoots(roots, 0), nil
}

func findNearestPackageDirRel(abs, gitRootAbs string) (string, bool) {
	cur := abs
	for {
		if _, err := os.Stat(filepath.Join(cur, "package.json")); err == nil {
			rel, rerr := filepath.Rel(gitRootAbs, cur)
			if rerr != nil {
				return "", false
			}
			rel = filepath.ToSlash(rel)
			if rel == "." || strings.HasPrefix(rel, "..") {
				return "", false
			}
			return rel, true
		}
		parent := filepath.Dir(cur)
		if parent == cur || strings.HasPrefix(parent, filepath.Clean(gitRootAbs)+string(filepath.Separator)) == false && parent != filepath.Clean(gitRootAbs) {
			break
		}
		cur = parent
	}
	return "", false
}

func stripJSONLineComments(s string) []byte {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		if strings.HasPrefix(trim, "//") {
			continue
		}
		out = append(out, ln)
	}
	return []byte(strings.Join(out, "\n"))
}

func stripTrailingCommas(b []byte) []byte {
	re := regexp.MustCompile(`,(\s*[}\]])`)
	return re.ReplaceAll(b, []byte("$1"))
}

func resolveMavenDependencyRootsRel(gitRootAbs, primaryNorm string) ([]string, error) {
	primaryNorm = strings.Trim(filepath.ToSlash(strings.TrimSpace(primaryNorm)), "/")
	if primaryNorm == "" {
		return nil, nil
	}
	pomByDir, coordsToDir, err := collectMavenPOMInfos(gitRootAbs)
	if err != nil {
		return nil, err
	}
	if len(pomByDir) == 0 {
		return nil, nil
	}

	var queue []string
	seen := map[string]struct{}{}
	for relDir := range pomByDir {
		if FileUnderPrefix(relDir, primaryNorm) {
			queue = append(queue, relDir)
			seen[relDir] = struct{}{}
		}
	}
	if len(queue) == 0 {
		return nil, nil
	}
	var depRoots []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		info := pomByDir[cur]
		if info == nil {
			continue
		}
		curAbs := filepath.Join(gitRootAbs, filepath.FromSlash(cur))
		for _, m := range info.Modules {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			modAbs := filepath.Clean(filepath.Join(curAbs, filepath.FromSlash(m)))
			modRel, rerr := filepath.Rel(gitRootAbs, modAbs)
			if rerr != nil {
				continue
			}
			modRel = filepath.ToSlash(modRel)
			if modRel == "." || strings.HasPrefix(modRel, "..") {
				continue
			}
			if _, ok := pomByDir[modRel]; !ok {
				continue
			}
			if _, ok := seen[modRel]; ok {
				continue
			}
			seen[modRel] = struct{}{}
			queue = append(queue, modRel)
			if !FileUnderPrefix(modRel, primaryNorm) {
				depRoots = append(depRoots, modRel)
			}
		}
		for _, dep := range info.Dependencies {
			if dep.GroupID == "" || dep.ArtifactID == "" {
				continue
			}
			targetRel := coordsToDir[dep]
			if targetRel == "" {
				continue
			}
			if _, ok := seen[targetRel]; ok {
				continue
			}
			seen[targetRel] = struct{}{}
			queue = append(queue, targetRel)
			if !FileUnderPrefix(targetRel, primaryNorm) {
				depRoots = append(depRoots, targetRel)
			}
		}
	}
	return compressRoots(depRoots, 0), nil
}

func collectMavenPOMInfos(gitRootAbs string) (map[string]*mavenPOMInfo, map[mavenCoord]string, error) {
	pomByDir := map[string]*mavenPOMInfo{}
	coordsToDir := map[mavenCoord]string{}
	err := filepath.WalkDir(gitRootAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := strings.ToLower(d.Name())
			if strings.HasPrefix(base, ".") || base == "node_modules" || base == "bin" || base == "obj" || base == "dist" || base == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(d.Name(), "pom.xml") {
			return nil
		}
		dirAbs := filepath.Dir(path)
		rel, rerr := filepath.Rel(gitRootAbs, dirAbs)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || strings.HasPrefix(rel, "..") {
			return nil
		}
		pi, perr := parseMavenPOM(path)
		if perr != nil {
			return perr
		}
		pi.DirRel = rel
		pomByDir[rel] = pi
		if pi.Coord.GroupID != "" && pi.Coord.ArtifactID != "" {
			if _, exists := coordsToDir[pi.Coord]; !exists {
				coordsToDir[pi.Coord] = rel
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return pomByDir, coordsToDir, nil
}

func parseMavenPOM(pomAbs string) (*mavenPOMInfo, error) {
	b, err := os.ReadFile(pomAbs)
	if err != nil {
		return nil, err
	}
	var p mavenPOM
	if err := xml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	groupID := strings.TrimSpace(p.GroupID)
	if groupID == "" {
		groupID = strings.TrimSpace(p.Parent.GroupID)
	}
	artifactID := strings.TrimSpace(p.ArtifactID)
	var mods []string
	for _, m := range p.Modules {
		m = strings.TrimSpace(m)
		if m != "" {
			mods = append(mods, m)
		}
	}
	var deps []mavenCoord
	for _, d := range p.Dependencies {
		g := strings.TrimSpace(d.GroupID)
		a := strings.TrimSpace(d.ArtifactID)
		if g == "" || a == "" {
			continue
		}
		deps = append(deps, mavenCoord{GroupID: g, ArtifactID: a})
	}
	return &mavenPOMInfo{
		Coord:        mavenCoord{GroupID: groupID, ArtifactID: artifactID},
		Modules:      mods,
		Dependencies: deps,
	}, nil
}

func resolveGradleDependencyRootsRel(gitRootAbs, primaryNorm string) ([]string, error) {
	primaryNorm = strings.Trim(filepath.ToSlash(strings.TrimSpace(primaryNorm)), "/")
	if primaryNorm == "" {
		return nil, nil
	}
	primaryAbs, err := JoinGitRootAndWorkspace(gitRootAbs, primaryNorm)
	if err != nil {
		return nil, err
	}
	projectDirByID, settingsFound, err := parseGradleSettings(primaryAbs, gitRootAbs)
	if err != nil {
		return nil, err
	}
	if !settingsFound {
		return nil, nil
	}
	if _, ok := projectDirByID[":"]; !ok {
		projectDirByID[":"] = primaryNorm
	}
	queue := []string{":"}
	for _, id := range parseGradleProjectDepsFromBuildFiles(primaryAbs) {
		if strings.HasPrefix(id, ":") {
			queue = append(queue, id)
		}
	}
	queue = dedupeStrings(queue)
	visited := map[string]struct{}{}
	var depRoots []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, ok := visited[id]; ok {
			continue
		}
		visited[id] = struct{}{}
		dirRel := projectDirByID[id]
		if dirRel == "" && id != ":" {
			continue
		}
		dirAbs := filepath.Join(gitRootAbs, filepath.FromSlash(dirRel))
		if id == ":" {
			dirAbs = primaryAbs
		}
		for _, depID := range parseGradleProjectDepsFromBuildFiles(dirAbs) {
			if !strings.HasPrefix(depID, ":") {
				continue
			}
			if _, ok := visited[depID]; !ok {
				queue = append(queue, depID)
			}
			depRel := projectDirByID[depID]
			if depRel == "" {
				continue
			}
			if !FileUnderPrefix(depRel, primaryNorm) {
				depRoots = append(depRoots, depRel)
			}
		}
	}
	return compressRoots(depRoots, 0), nil
}

func parseGradleSettings(primaryAbs, gitRootAbs string) (map[string]string, bool, error) {
	settingsPaths := []string{
		filepath.Join(primaryAbs, "settings.gradle"),
		filepath.Join(primaryAbs, "settings.gradle.kts"),
	}
	projectByID := map[string]string{}
	found := false
	for _, p := range settingsPaths {
		b, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, false, err
		}
		found = true
		txt := string(b)
		for _, m := range gradleProjectPathRe.FindAllStringSubmatch(txt, -1) {
			if len(m) < 2 {
				continue
			}
			id := strings.TrimSpace(m[1])
			if id == "" {
				continue
			}
			if _, ok := projectByID[id]; !ok {
				projectByID[id] = gradleProjectIDToRelDir(id)
			}
		}
		for _, m := range gradleProjectDirAssignRe.FindAllStringSubmatch(txt, -1) {
			if len(m) < 3 {
				continue
			}
			id := strings.TrimSpace(m[1])
			dir := strings.Trim(filepath.ToSlash(strings.TrimSpace(m[2])), "/")
			if id == "" || dir == "" {
				continue
			}
			projectByID[id] = dir
		}
	}
	if !found {
		return nil, false, nil
	}
	out := map[string]string{}
	for id, rel := range projectByID {
		abs := primaryAbs
		if rel != "" {
			abs = filepath.Clean(filepath.Join(primaryAbs, filepath.FromSlash(rel)))
		}
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		r, err := filepath.Rel(gitRootAbs, abs)
		if err != nil || r == "." || strings.HasPrefix(r, "..") {
			continue
		}
		out[id] = filepath.ToSlash(r)
	}
	return out, true, nil
}

func parseGradleProjectDepsFromBuildFiles(dirAbs string) []string {
	var out []string
	for _, n := range []string{"build.gradle", "build.gradle.kts"} {
		b, err := os.ReadFile(filepath.Join(dirAbs, n))
		if err != nil {
			continue
		}
		for _, m := range gradleProjectPathRe.FindAllStringSubmatch(string(b), -1) {
			if len(m) < 2 {
				continue
			}
			id := strings.TrimSpace(m[1])
			if strings.HasPrefix(id, ":") {
				out = append(out, id)
			}
		}
	}
	return dedupeStrings(out)
}

func gradleProjectIDToRelDir(id string) string {
	id = strings.TrimSpace(strings.TrimPrefix(id, ":"))
	if id == "" {
		return ""
	}
	return strings.ReplaceAll(id, ":", "/")
}

func dedupeStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func parseSolutionProjectPaths(slnAbs string) ([]string, error) {
	b, err := os.ReadFile(slnAbs)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range slnProjPathRe.FindAllSubmatch(b, -1) {
		if len(m) < 2 {
			continue
		}
		p := strings.TrimSpace(string(m[1]))
		if p == "" {
			continue
		}
		p = filepath.FromSlash(strings.ReplaceAll(p, "\\", "/"))
		out = append(out, p)
	}
	return out, nil
}

func parseProjectReferences(csprojAbs string) ([]string, error) {
	f, err := os.Open(csprojAbs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := xml.NewDecoder(bufio.NewReader(f))
	var out []string
	for {
		tok, terr := dec.Token()
		if terr != nil {
			if terr == io.EOF {
				break
			}
			return nil, terr
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !strings.EqualFold(start.Name.Local, "ProjectReference") {
			continue
		}
		for _, a := range start.Attr {
			if strings.EqualFold(a.Name.Local, "Include") {
				v := strings.TrimSpace(a.Value)
				if v != "" {
					out = append(out, filepath.FromSlash(strings.ReplaceAll(v, "\\", "/")))
				}
			}
		}
	}
	return out, nil
}

func uniqueExistingAbsPaths(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		absP, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if _, err := os.Stat(absP); err != nil {
			continue
		}
		if _, ok := seen[absP]; ok {
			continue
		}
		seen[absP] = struct{}{}
		out = append(out, absP)
	}
	return out
}

func mergeRootSets(left, right []string) []string {
	if len(right) == 0 {
		return compressRoots(left, 0)
	}
	all := append(append([]string(nil), left...), right...)
	return compressRoots(all, 0)
}

func compressRoots(in []string, maxRoots int) []string {
	if len(in) == 0 {
		return nil
	}
	uniq := map[string]struct{}{}
	var cleaned []string
	for _, r := range in {
		r = strings.Trim(filepath.ToSlash(strings.TrimSpace(r)), "/")
		if r == "" {
			continue
		}
		if _, ok := uniq[r]; ok {
			continue
		}
		uniq[r] = struct{}{}
		cleaned = append(cleaned, r)
	}
	sort.Slice(cleaned, func(i, j int) bool {
		di := strings.Count(cleaned[i], "/")
		dj := strings.Count(cleaned[j], "/")
		if di != dj {
			return di < dj
		}
		return cleaned[i] < cleaned[j]
	})
	var out []string
	for _, c := range cleaned {
		covered := false
		for _, r := range out {
			if FileUnderPrefix(c, r) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		out = append(out, c)
		if maxRoots > 0 && len(out) >= maxRoots {
			break
		}
	}
	return out
}

// RemapSuggestedTestPathForMonoTestWorkspace rewrites a repo-relative suggested test path from the code workspace
// tree into the sibling test-project tree when indexer.mono_repo_test_workspace is set.
// codeMono and testMono must be normalized via NormalizeMonoRepoWorkspace; empty testMono leaves suggestedTestRel unchanged (aside from slash normalization).
//
// Rules:
//   - If suggested already starts with testMono/, it is returned unchanged (idempotent / LLM already used test tree).
//   - If suggested starts with codeMono/, that prefix is replaced with testMono/.
//   - Else if sourceRel starts with codeMono/ and suggested does not, suggested is treated as layout relative to the
//     repo root (e.g. e2e/api/Foo.e2e-spec.ts) and the result is testMono/suggested.
func RemapSuggestedTestPathForMonoTestWorkspace(codeMono, testMono, sourceRel, suggestedTestRel string) string {
	testMono = strings.Trim(filepath.ToSlash(strings.TrimSpace(testMono)), "/")
	sug := filepath.ToSlash(strings.TrimSpace(suggestedTestRel))
	sug = strings.TrimPrefix(sug, "/")
	if testMono == "" {
		return sug
	}
	if strings.HasPrefix(sug, testMono+"/") || sug == testMono {
		return sug
	}
	codeMono = strings.Trim(filepath.ToSlash(strings.TrimSpace(codeMono)), "/")
	src := filepath.ToSlash(strings.TrimSpace(sourceRel))
	src = strings.TrimPrefix(src, "/")
	underCode := func(p string) bool {
		return codeMono != "" && p != "" && (p == codeMono || strings.HasPrefix(p, codeMono+"/"))
	}
	// Only rewrite when the symbol source is under the code workspace; avoids remapping inconsistent pairs.
	if underCode(src) && underCode(sug) {
		rest := strings.TrimPrefix(sug, codeMono+"/")
		return filepath.ToSlash(filepath.Join(testMono, rest))
	}
	if underCode(src) && sug != "" && !underCode(sug) {
		return filepath.ToSlash(filepath.Join(testMono, sug))
	}
	return sug
}
