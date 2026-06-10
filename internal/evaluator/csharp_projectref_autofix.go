package evaluator

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// cryptoReader reads cryptographically strong random bytes; separated so unit tests can
// swap it out deterministically if ever needed.
var cryptoReader = func(p []byte) (int, error) { return crand.Read(p) }

var (
	reCS0234MissingNamespace = regexp.MustCompile(`(?m)^(.+?)\(\d+,\d+\):\s*error\s+CS0234:.*?'([^']+)'.*?namespace\s+'([^']+)'.*?(?:\[(.+?\.csproj)\])?\s*$`)
	reNamespaceDecl          = regexp.MustCompile(`(?m)^\s*namespace\s+([A-Za-z_][A-Za-z0-9_.]*)\s*[;{]`)
	reItemGroupBlockEval     = regexp.MustCompile(`(?is)<ItemGroup\b([^>]*)>(.*?)</ItemGroup>`)
	reProjectReferenceTag    = regexp.MustCompile(`(?is)<ProjectReference\b([^>]*)/?>`)
	reIncludeAttrEval        = regexp.MustCompile(`(?is)\bInclude\s*=\s*["']([^"']+)["']`)
	reConditionAttrEval      = regexp.MustCompile(`(?is)\bCondition\s*=`)
)

func tryAutoFixCSharpMissingProjectReferences(ctx context.Context, opts EvalOptions, errorOutput string, audit Auditor) bool {
	lang := strings.ToLower(strings.TrimSpace(opts.Lang))
	hasCS0234 := strings.Contains(strings.ToUpper(errorOutput), "CS0234")
	if strings.TrimSpace(errorOutput) == "" || strings.TrimSpace(opts.RepoPath) == "" {
		return false
	}
	// Trigger for C# runs, and also for mixed-language runs when compile output clearly contains CS0234.
	if (lang != "csharp" && lang != "cs") && !hasCS0234 {
		return false
	}
	repoScanRoot := discoverRepoScanRoot(opts.RepoPath, errorOutput)
	providers, err := buildCSharpNamespaceProjectMap(repoScanRoot)
	if err != nil {
		if audit != nil {
			audit.LogError(ctx, "evaluator.auto_project_reference_error", map[string]interface{}{
				"message": fmt.Sprintf("Auto ProjectReference scan failed: %v", err),
				"error":   err.Error(),
			})
		}
		return false
	}
	type depSet map[string]bool
	targetToDeps := make(map[string]depSet)
	matches := reCS0234MissingNamespace.FindAllStringSubmatch(errorOutput, -1)
	var unresolvedTargetCount int
	var noDepCandidateCount int
	if audit != nil {
		audit.Log(ctx, "evaluator.auto_project_reference_probe", map[string]interface{}{
			"message":        "CS0234 auto ProjectReference probe executed.",
			"lang":           lang,
			"has_cs0234":     hasCS0234,
			"matches":        len(matches),
			"repo_scan_root": repoScanRoot,
		})
	}
	if hasCS0234 && len(matches) == 0 {
		if audit != nil {
			audit.Log(ctx, "evaluator.auto_project_reference_no_candidate", map[string]interface{}{
				"message": "CS0234 detected but parser could not extract source/project from compiler output.",
			})
		}
		return false
	}
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		srcToolPath := strings.TrimSpace(m[1])
		missingLeaf := strings.TrimSpace(m[2])
		missingNS := strings.TrimSpace(m[3]) + "." + missingLeaf
		projectToolPath := ""
		if len(m) >= 5 {
			projectToolPath = strings.TrimSpace(m[4])
		}
		targetProjRel := ""
		if projectToolPath != "" {
			if p := resolveRepoRelPathFromToolPath(repoScanRoot, projectToolPath); p != "" {
				targetProjRel = p
			}
		}
		if targetProjRel == "" {
			sourceRel := resolveRepoRelPathFromToolPath(repoScanRoot, srcToolPath)
			if sourceRel == "" {
				unresolvedTargetCount++
				continue
			}
			p, ok := dotnetproj.NearestCsprojRel(repoScanRoot, sourceRel)
			if !ok {
				unresolvedTargetCount++
				continue
			}
			targetProjRel = p
		}
		targetProjRel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(targetProjRel)), "/")
		candidates := providers[missingNS]
		if len(candidates) == 0 {
			candidates = namespaceProviderCandidates(providers, missingNS)
		}
		depProj := chooseBestNamespaceProvider(candidates, targetProjRel, missingLeaf)
		if depProj == "" {
			if byName := findBestCsprojByLeaf(repoScanRoot, missingLeaf, targetProjRel); byName != "" {
				depProj = byName
			}
		}
		if depProj == "" {
			noDepCandidateCount++
			if audit != nil {
				audit.Log(ctx, "evaluator.auto_project_reference_no_candidate", map[string]interface{}{
					"message":          "No candidate dependency project found for CS0234 namespace miss.",
					"source_file":      srcToolPath,
					"project_file":     projectToolPath,
					"target_project":   targetProjRel,
					"missing_ns":       missingNS,
					"missing_leaf":     missingLeaf,
					"provider_matches": len(candidates),
				})
			}
			continue
		}
		if depProj == targetProjRel {
			continue
		}
		if targetToDeps[targetProjRel] == nil {
			targetToDeps[targetProjRel] = make(depSet)
		}
		targetToDeps[targetProjRel][depProj] = true
	}
	if len(targetToDeps) == 0 {
		if audit != nil {
			audit.Log(ctx, "evaluator.auto_project_reference_probe_result", map[string]interface{}{
				"message":                       "CS0234 probe found no actionable project-reference patch.",
				"lang":                          lang,
				"matches":                       len(matches),
				"unresolved_target_count":       unresolvedTargetCount,
				"no_dependency_candidate_count": noDepCandidateCount,
				"repo_scan_root":                repoScanRoot,
			})
		}
		return false
	}
	changedAny := false
	for targetProj, depSet := range targetToDeps {
		var deps []string
		for d := range depSet {
			deps = append(deps, d)
		}
		sort.Strings(deps)
		targetCsprojAbs := filepath.Join(repoScanRoot, filepath.FromSlash(targetProj))
		result, err := ensureProjectReferencesInCsprojEvalDetailed(targetCsprojAbs, deps, repoScanRoot)
		if err != nil {
			if audit != nil {
				audit.LogError(ctx, "evaluator.auto_project_reference_error", map[string]interface{}{
					"message":        fmt.Sprintf("Auto ProjectReference patch failed for %s: %v", targetProj, err),
					"target_project": targetProj,
					"error":          err.Error(),
				})
			}
			continue
		}
		if audit != nil {
			audit.Log(ctx, "evaluator.auto_project_reference_target_considered", map[string]interface{}{
				"message":               "CS0234 target considered for ProjectReference patch.",
				"target_project":        targetProj,
				"target_csproj_abs":     targetCsprojAbs,
				"dependency_candidates": deps,
				"already_unconditional": result.AlreadyUnconditional,
				"skipped_same_path":     result.SkippedSamePath,
				"skipped_rel_error":     result.SkippedRelError,
				"added":                 result.Added,
				"changed":               result.Changed,
			})
		}
		if !result.Changed {
			// The reference is already in place (or filtered out) but CS0234 persists.
			// First, try the high-value auto-fix: add the missing dependency project(s) to
			// the .sln that contains the consumer. When `dotnet build <sln>` is the compile
			// entry and the sln's project list misses the dep, MSBuild won't produce the
			// dep's assembly; bare CS0234 without MSB3245 is the canonical symptom.
			diag := diagnoseAlreadyReferencedButUnresolved(repoScanRoot, targetProj, result.AlreadyUnconditional, errorOutput)
			slnPatched := tryAutoAddMissingSlnProjects(repoScanRoot, diag.ActiveSlns)
			// If deps are listed in the sln but have "Build" disabled for the target's
			// active configuration, insert the missing ProjectConfigurationPlatforms
			// Build.0 rows. This is the precise fix for the "bare CS0234, no MSB3245"
			// fingerprint we see when MSBuild silently skips building the dep.
			slnBuildEnabled := tryAutoEnableSlnBuildForDeps(repoScanRoot, diag.ActiveSlns)
			if audit != nil && len(result.AlreadyUnconditional) > 0 {
				audit.LogError(ctx, "evaluator.auto_project_reference_unresolved_despite_ref", map[string]interface{}{
					"message":                 fmt.Sprintf("CS0234 persists for %s despite unconditional ProjectReference(s); not a missing-ref problem.", targetProj),
					"target_project":          targetProj,
					"target_target_framework": diag.TargetTargetFramework,
					"already_referenced":      result.AlreadyUnconditional,
					"dep_file_checks":         diag.DepFileChecks,
					"dep_package_references":  diag.DepPackageReferences,
					"nuget_config_feeds":      diag.NuGetConfigFeeds,
					"private_feed_detected":   diag.PrivateFeedDetected,
					"conditional_hints":       diag.ConditionalHints,
					"compile_output_signals":  diag.CompileOutputSignals,
					"active_slns":             diag.ActiveSlns,
					"sln_auto_patched":        slnPatched,
					"sln_build_enabled":       slnBuildEnabled,
					"likely_causes":           diag.LikelyCauses,
					"remediation":             diag.Remediation,
				})
			}
			if len(slnPatched) > 0 {
				changedAny = true
				if audit != nil {
					audit.Log(ctx, "evaluator.auto_project_reference_sln_patched", map[string]interface{}{
						"message":        "Added missing dependency project(s) to sln(s) containing the consumer.",
						"target_project": targetProj,
						"patches":        slnPatched,
					})
				}
			}
			if len(slnBuildEnabled) > 0 {
				changedAny = true
				if audit != nil {
					audit.Log(ctx, "evaluator.auto_project_reference_sln_build_enabled", map[string]interface{}{
						"message":        "Enabled Build.0 for dependency project(s) in sln(s) where the target's configuration was active but the dependency's was not.",
						"target_project": targetProj,
						"patches":        slnBuildEnabled,
					})
				}
			}
			continue
		}
		changedAny = true
		if audit != nil {
			audit.Log(ctx, "evaluator.auto_project_reference_applied", map[string]interface{}{
				"message":            "Applied missing ProjectReference from CS0234 compile diagnostics.",
				"target_project":     targetProj,
				"added_dependencies": result.Added,
			})
		}
	}
	if audit != nil {
		audit.Log(ctx, "evaluator.auto_project_reference_probe_result", map[string]interface{}{
			"message":                       "CS0234 probe finished.",
			"lang":                          lang,
			"matches":                       len(matches),
			"unresolved_target_count":       unresolvedTargetCount,
			"no_dependency_candidate_count": noDepCandidateCount,
			"changed_any":                   changedAny,
			"targets_considered":            len(targetToDeps),
			"repo_scan_root":                repoScanRoot,
		})
	}
	return changedAny
}

func discoverRepoScanRoot(repoPath, errorOutput string) string {
	cur := filepath.Clean(strings.TrimSpace(repoPath))
	if cur == "" {
		return repoPath
	}
	absFromErrors := extractAbsoluteToolPaths(errorOutput)
	if len(absFromErrors) > 0 {
		paths := make([]string, 0, len(absFromErrors)+1)
		paths = append(paths, cur)
		paths = append(paths, absFromErrors...)
		if anc := commonExistingAncestor(paths); anc != "" && ancestorLooksUsableForRepo(cur, anc) {
			return anc
		}
	}
	for {
		gitPath := filepath.Join(cur, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(repoPath)
		}
		cur = parent
	}
}

func ancestorLooksUsableForRepo(repoPath, anc string) bool {
	anc = filepath.Clean(strings.TrimSpace(anc))
	repoPath = filepath.Clean(strings.TrimSpace(repoPath))
	if anc == "" || anc == string(filepath.Separator) {
		return false
	}
	rel, err := filepath.Rel(anc, repoPath)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}

func extractAbsoluteToolPaths(errorOutput string) []string {
	if strings.TrimSpace(errorOutput) == "" {
		return nil
	}
	set := make(map[string]bool)
	add := func(p string) {
		p = strings.TrimSpace(strings.Trim(p, "[]"))
		if !filepath.IsAbs(p) {
			return
		}
		set[filepath.Clean(p)] = true
	}
	for _, m := range reCS0234MissingNamespace.FindAllStringSubmatch(errorOutput, -1) {
		if len(m) >= 2 {
			add(m[1])
		}
		if len(m) >= 5 {
			add(m[4])
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func commonExistingAncestor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	clean := make([]string, 0, len(paths))
	for _, p := range paths {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return ""
	}
	ancestor := clean[0]
	for _, p := range clean[1:] {
		ancestor = pairCommonAncestor(ancestor, p)
		if ancestor == "" {
			break
		}
	}
	for ancestor != "" {
		if st, err := os.Stat(ancestor); err == nil && st.IsDir() {
			return ancestor
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	return ""
}

func pairCommonAncestor(a, b string) string {
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	abs := filepath.IsAbs(ca) && filepath.IsAbs(cb)
	aa := strings.Split(ca, string(filepath.Separator))
	bb := strings.Split(cb, string(filepath.Separator))
	n := 0
	for n < len(aa) && n < len(bb) {
		if aa[n] != bb[n] {
			break
		}
		n++
	}
	if n == 0 {
		return ""
	}
	joined := filepath.Join(aa[:n]...)
	if abs && !filepath.IsAbs(joined) {
		return string(filepath.Separator) + joined
	}
	return joined
}

func findBestCsprojByLeaf(repoRoot, missingLeaf, targetProjRel string) string {
	leaf := strings.ToLower(strings.TrimSpace(missingLeaf))
	if leaf == "" {
		return ""
	}
	type scored struct {
		rel   string
		score int
	}
	best := scored{}
	_ = filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".csproj") {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "/")
		if rel == "" || rel == targetProjRel {
			return nil
		}
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)))
		if !strings.Contains(base, leaf) {
			return nil
		}
		score := 100
		score += commonPathPrefixLen(strings.ToLower(filepath.Dir(filepath.ToSlash(targetProjRel))), strings.ToLower(filepath.Dir(filepath.ToSlash(rel))))
		if score > best.score || best.rel == "" {
			best = scored{rel: rel, score: score}
		}
		return nil
	})
	return best.rel
}

func namespaceProviderCandidates(providers map[string][]string, missingNS string) []string {
	if len(providers) == 0 || strings.TrimSpace(missingNS) == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	if exact := providers[missingNS]; len(exact) > 0 {
		for _, p := range exact {
			add(p)
		}
	}
	prefix := missingNS + "."
	for ns, ps := range providers {
		if !strings.HasPrefix(ns, prefix) {
			continue
		}
		for _, p := range ps {
			add(p)
		}
	}
	sort.Strings(out)
	return out
}

func chooseBestNamespaceProvider(candidates []string, targetProjRel, missingLeaf string) string {
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	missingLeaf = strings.ToLower(strings.TrimSpace(missingLeaf))
	type scored struct {
		proj  string
		score int
	}
	best := scored{}
	for _, c := range candidates {
		score := 0
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(c), filepath.Ext(c)))
		if missingLeaf != "" && strings.Contains(base, missingLeaf) {
			score += 100
		}
		targetDir := strings.ToLower(filepath.Dir(filepath.ToSlash(targetProjRel)))
		cDir := strings.ToLower(filepath.Dir(filepath.ToSlash(c)))
		if targetDir != "" && cDir != "" {
			common := commonPathPrefixLen(targetDir, cDir)
			score += common
		}
		if score > best.score || best.proj == "" {
			best = scored{proj: c, score: score}
		}
	}
	if best.score <= 0 {
		return ""
	}
	return best.proj
}

func commonPathPrefixLen(a, b string) int {
	pa := strings.Split(strings.Trim(a, "/"), "/")
	pb := strings.Split(strings.Trim(b, "/"), "/")
	n := 0
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			break
		}
		n++
	}
	return n
}

func resolveRepoRelPathFromToolPath(repoRoot, toolPath string) string {
	p := filepath.ToSlash(strings.TrimSpace(toolPath))
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "/workspace/") {
		return strings.TrimPrefix(p, "/workspace/")
	}
	if strings.HasPrefix(p, "workspace/") {
		return strings.TrimPrefix(p, "workspace/")
	}
	rootSlash := filepath.ToSlash(filepath.Clean(repoRoot))
	if strings.HasPrefix(p, rootSlash+"/") {
		return strings.TrimPrefix(p, rootSlash+"/")
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i := 0; i < len(parts); i++ {
		cand := strings.Join(parts[i:], "/")
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(cand))); err == nil {
			return cand
		}
	}
	return ""
}

func buildCSharpNamespaceProjectMap(repoRoot string) (map[string][]string, error) {
	seen := make(map[string]map[string]bool)
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".cs") {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}
		projRel, ok := dotnetproj.NearestCsprojRel(repoRoot, filepath.ToSlash(rel))
		if !ok {
			return nil
		}
		projRel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(projRel)), "/")
		if projRel == "" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range reNamespaceDecl.FindAllStringSubmatch(string(b), -1) {
			if len(m) < 2 {
				continue
			}
			ns := strings.TrimSpace(m[1])
			if ns == "" {
				continue
			}
			if seen[ns] == nil {
				seen[ns] = make(map[string]bool)
			}
			seen[ns][projRel] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(seen))
	for ns, projs := range seen {
		var list []string
		for p := range projs {
			list = append(list, p)
		}
		sort.Strings(list)
		out[ns] = list
	}
	return out, nil
}

func ensureProjectReferencesInCsprojEval(targetCsprojAbs string, dependencyProjectRels []string, repoRoot string) (bool, []string, error) {
	res, err := ensureProjectReferencesInCsprojEvalDetailed(targetCsprojAbs, dependencyProjectRels, repoRoot)
	return res.Changed, res.Added, err
}

type ensureProjectRefResult struct {
	Changed              bool
	Added                []string
	AlreadyUnconditional []string
	SkippedSamePath      []string
	SkippedRelError      []string
}

func ensureProjectReferencesInCsprojEvalDetailed(targetCsprojAbs string, dependencyProjectRels []string, repoRoot string) (ensureProjectRefResult, error) {
	var res ensureProjectRefResult
	b, err := os.ReadFile(targetCsprojAbs)
	if err != nil {
		return res, err
	}
	src := string(b)
	lower := strings.ToLower(src)
	idx := strings.LastIndex(lower, "</project>")
	if idx < 0 {
		return res, fmt.Errorf("invalid csproj: missing </Project>")
	}
	targetDir := filepath.Dir(targetCsprojAbs)
	existingAny, existingUnconditional := existingProjectReferenceStateEval(src, targetDir)
	var toAdd []string
	for _, depRel := range dependencyProjectRels {
		depAbs := filepath.Clean(filepath.Join(repoRoot, filepath.FromSlash(depRel)))
		if depAbs == "" || depAbs == targetCsprojAbs {
			res.SkippedSamePath = append(res.SkippedSamePath, depRel)
			continue
		}
		if existingUnconditional[depAbs] {
			res.AlreadyUnconditional = append(res.AlreadyUnconditional, depRel)
			continue
		}
		rel, err := filepath.Rel(targetDir, depAbs)
		if err != nil {
			res.SkippedRelError = append(res.SkippedRelError, depRel)
			continue
		}
		toAdd = append(toAdd, filepath.ToSlash(rel))
		existingAny[depAbs] = true
		existingUnconditional[depAbs] = true
	}
	if len(toAdd) == 0 {
		return res, nil
	}
	sort.Strings(toAdd)
	var block strings.Builder
	block.WriteString("  <ItemGroup>\n")
	for _, inc := range toAdd {
		block.WriteString(fmt.Sprintf("    <ProjectReference Include=\"%s\" />\n", inc))
	}
	block.WriteString("  </ItemGroup>\n")
	out := src[:idx] + block.String() + src[idx:]
	if err := os.WriteFile(targetCsprojAbs, []byte(out), 0o644); err != nil {
		return res, err
	}
	res.Changed = true
	res.Added = toAdd
	return res, nil
}

// diagnoseAlreadyReferencedButUnresolved emits structured hints for the common
// "reference is there but CS0234 still fires" scenarios so the user gets an
// actionable signal in audit logs instead of a silent no-op.
type alreadyReferencedDiag struct {
	DepFileChecks         []map[string]interface{} // per-dep existence and basic metadata (now also: cs_file_count, declared_namespaces, target_framework, has_compile_sources, in_active_sln)
	ConditionalHints      []string                 // hints we detected about conditional wrappers (e.g. Choose/When)
	DepPackageReferences  []map[string]interface{} // per-dep: csproj -> list of PackageReference ids/versions
	NuGetConfigFeeds      []map[string]interface{} // discovered NuGet.config files + their feed URLs
	PrivateFeedDetected   bool                     // at least one feed URL looks private (non-nuget.org, non-localhost)
	TargetTargetFramework string                   // TargetFramework(s) of the consumer csproj
	CompileOutputSignals  map[string]interface{}   // error-code counts + first non-CS0234 lines from compile output
	ActiveSlns            []map[string]interface{} // slns found that include the target project: path + contains_target + contains_each_dep
	LikelyCauses          []string
	Remediation           []string
}

var (
	rePackageRefAttrs    = regexp.MustCompile(`(?is)<PackageReference\b([^>]*)/?>`)
	rePackageRefInclude  = regexp.MustCompile(`(?is)\bInclude\s*=\s*["']([^"']+)["']`)
	rePackageRefVersion  = regexp.MustCompile(`(?is)\bVersion\s*=\s*["']([^"']+)["']`)
	reNuGetConfigAddURL  = regexp.MustCompile(`(?is)<add\b[^>]*\bvalue\s*=\s*["']([^"']+)["'][^>]*/?>`)
	reNuGetConfigAddName = regexp.MustCompile(`(?is)\bkey\s*=\s*["']([^"']+)["']`)
	reCsprojTargetFW     = regexp.MustCompile(`(?is)<TargetFrameworks?>([^<]+)</TargetFrameworks?>`)
	reCsprojCompileInc   = regexp.MustCompile(`(?is)<Compile\b[^>]*\bInclude\s*=`)
	reCsprojCompileRem   = regexp.MustCompile(`(?is)<Compile\b[^>]*\bRemove\s*=`)
	reCsprojSDKAttr      = regexp.MustCompile(`(?is)<Project\b[^>]*\bSdk\s*=`)
	// Error-code grouping (we count occurrences and capture a short sample line per code).
	reMSCErrorCode = regexp.MustCompile(`(?m)\b((?:CS|MSB|NETSDK|NU)\d{3,5})\b`)
)

func diagnoseAlreadyReferencedButUnresolved(repoScanRoot, targetProjRel string, alreadyRefs []string, compileOutput string) alreadyReferencedDiag {
	var out alreadyReferencedDiag
	// Check each already-referenced dependency is physically present on disk.
	missingCount := 0
	emptyLikeDepCount := 0
	namespaceMissMatchCount := 0
	// Resolve the consumer namespace we need the dep to declare. For "Upper.ChromeRemote"
	// we infer from the dep project's path leaf as the sub-namespace; combined with the
	// parent namespace known by the target source. We approximate by taking the leaf as
	// a required substring of any declared namespace in the dep's .cs files.
	for _, depRel := range alreadyRefs {
		depAbs := filepath.Clean(filepath.Join(repoScanRoot, filepath.FromSlash(depRel)))
		check := map[string]interface{}{
			"dependency": depRel,
			"abs_path":   depAbs,
		}
		st, err := os.Stat(depAbs)
		if err != nil {
			check["exists"] = false
			check["stat_error"] = err.Error()
			missingCount++
		} else {
			check["exists"] = true
			check["size"] = st.Size()
			// Inspect dep csproj content for SDK-style + TargetFramework + explicit <Compile> entries.
			if b, err := os.ReadFile(depAbs); err == nil {
				src := string(b)
				check["is_sdk_style"] = reCsprojSDKAttr.MatchString(src)
				if m := reCsprojTargetFW.FindStringSubmatch(src); len(m) >= 2 {
					check["target_framework"] = strings.TrimSpace(m[1])
				}
				check["has_explicit_compile_include"] = reCsprojCompileInc.MatchString(src)
				check["has_compile_remove"] = reCsprojCompileRem.MatchString(src)
			}
			// Scan .cs siblings to detect an empty-like project or namespace mismatch.
			csFiles, namespaces := scanCsFilesAndNamespaces(filepath.Dir(depAbs))
			check["cs_file_count"] = len(csFiles)
			check["declared_namespaces"] = namespaces
			if len(csFiles) == 0 {
				emptyLikeDepCount++
			}
			// If the dep project leaf is e.g. "Upper.ChromeRemote", at least one declared
			// namespace should be exactly that or start with that prefix. If not, a consumer
			// using that namespace will see CS0234 even with a perfect ProjectReference.
			depLeaf := strings.TrimSuffix(filepath.Base(depAbs), filepath.Ext(depAbs))
			if len(namespaces) > 0 && !anyNamespaceMatchesLeaf(namespaces, depLeaf) {
				namespaceMissMatchCount++
				check["namespace_mismatch_hint"] = fmt.Sprintf("no declared namespace matches project leaf %q; consumers using 'using %s;' or 'namespace %s' will see CS0234", depLeaf, depLeaf, depLeaf)
			}
			if pkgs := readPackageReferences(depAbs); len(pkgs) > 0 {
				out.DepPackageReferences = append(out.DepPackageReferences, map[string]interface{}{
					"dependency": depRel,
					"packages":   pkgs,
				})
			}
			for _, cfg := range discoverNuGetConfigsFrom(depAbs, repoScanRoot) {
				out.NuGetConfigFeeds = append(out.NuGetConfigFeeds, cfg)
				if feeds, ok := cfg["feeds"].([]map[string]interface{}); ok {
					for _, f := range feeds {
						if priv, _ := f["private_likely"].(bool); priv {
							out.PrivateFeedDetected = true
						}
					}
				}
			}
		}
		out.DepFileChecks = append(out.DepFileChecks, check)
	}
	// Look for <Choose>/<When>/<Otherwise> in the target csproj; we don't parse those,
	// so a ProjectReference inside them could behave as conditional at build time.
	targetAbs := filepath.Join(repoScanRoot, filepath.FromSlash(targetProjRel))
	if b, err := os.ReadFile(targetAbs); err == nil {
		src := string(b)
		if m := reCsprojTargetFW.FindStringSubmatch(src); len(m) >= 2 {
			out.TargetTargetFramework = strings.TrimSpace(m[1])
		}
		if strings.Contains(strings.ToLower(src), "<choose") || strings.Contains(strings.ToLower(src), "<when") {
			out.ConditionalHints = append(out.ConditionalHints, "target csproj uses <Choose>/<When> blocks; ProjectReferences inside may behave conditionally at build time")
		}
		// Narrow: only flag MSBuild property references ($(...)) when they appear inside
		// a ProjectReference Include= attribute (where they actually affect reference
		// resolution). Generic $(...) elsewhere is present in nearly every csproj and
		// would be a false positive.
		for _, pr := range reProjectReferenceTag.FindAllStringSubmatch(src, -1) {
			if len(pr) < 2 {
				continue
			}
			m := reIncludeAttrEval.FindStringSubmatch(pr[1])
			if len(m) < 2 {
				continue
			}
			if strings.Contains(m[1], "$(") {
				out.ConditionalHints = append(out.ConditionalHints, "a ProjectReference Include path contains MSBuild property references ($(...)); evaluated path may not match on disk")
				break
			}
		}
	}
	// Compile-output signals: classify by known MS/NuGet codes, skipping CS0234 itself
	// so the user sees the *other* errors driving the failure.
	out.CompileOutputSignals = summarizeCompileOutputCodes(compileOutput)

	// .sln inclusion analysis: for every already-referenced dependency, check whether the
	// sln(s) that include the target project also list the dependency. When `dotnet build`
	// is invoked with a .sln, MSBuild's build graph is anchored on the projects declared in
	// that sln. If the dependency isn't listed, its assembly may never be produced in the
	// build workspace, which manifests as CS0234 on the consumer with no accompanying
	// MSB3245 (because the ProjectReference itself is syntactically resolvable and present).
	slnEntries := findSlnsIncludingTarget(repoScanRoot, targetProjRel, alreadyRefs)
	out.ActiveSlns = slnEntries
	// Annotate each dep check with whether it is listed in any active sln, and aggregate
	// a "missing from active sln" count to drive a targeted cause/remediation.
	depInSln := map[string]bool{}
	anyActiveSln := len(slnEntries) > 0
	for _, entry := range slnEntries {
		m, _ := entry["deps_in_sln"].(map[string]interface{})
		for depRel, v := range m {
			if b, ok := v.(bool); ok && b {
				depInSln[depRel] = true
			} else if _, present := depInSln[depRel]; !present {
				depInSln[depRel] = false
			}
		}
	}
	slnMissingDepCount := 0
	for i, check := range out.DepFileChecks {
		depRel, _ := check["dependency"].(string)
		if !anyActiveSln {
			out.DepFileChecks[i]["in_active_sln"] = nil // no sln found that includes target
			continue
		}
		inSln, known := depInSln[depRel]
		if !known {
			out.DepFileChecks[i]["in_active_sln"] = false
		} else {
			out.DepFileChecks[i]["in_active_sln"] = inSln
		}
		if known && !inSln {
			slnMissingDepCount++
		}
	}

	if missingCount > 0 {
		out.LikelyCauses = append(out.LikelyCauses, "one or more already-referenced dependency csproj files are missing from the build workspace (Docker mount/sync gap or sparse checkout)")
		out.Remediation = append(out.Remediation, "ensure the build container mounts the full repo (including services/base/Sources/<DepProj>/) or widen mono_repo_workspace scope so the dependency .csproj is present")
	} else {
		if emptyLikeDepCount > 0 {
			out.LikelyCauses = append(out.LikelyCauses, "dependency project contains no .cs source files in its directory; its compiled assembly exports no types, so any `using <namespace>;` against it fails with CS0234")
			out.Remediation = append(out.Remediation, "verify the dependency project is actually populated with sources in the build workspace (sync / mount / sparse-checkout excluded its .cs files), or remove the consumer's `using` if it was referenced by mistake")
		}
		if namespaceMissMatchCount > 0 {
			out.LikelyCauses = append(out.LikelyCauses, "no namespace declared in the dependency project matches the expected namespace; the ProjectReference is correct but the types live under a different namespace")
			out.Remediation = append(out.Remediation, "align the dependency's `namespace` declarations with the consumer's `using` statement, or change the consumer's `using` to a namespace actually declared by the dependency")
		}
		if emptyLikeDepCount == 0 && namespaceMissMatchCount == 0 {
			out.LikelyCauses = append(out.LikelyCauses, "dependency csproj is present on disk but its types are not produced at build time (likely transitive build failure, e.g. restore/auth error for its NuGet feed, or a TFM mismatch)")
			out.Remediation = append(out.Remediation, "inspect earlier build/restore output for the dependency project (NU1301/NU1101 on its feed, or NETSDK1005/NU1201/MSB3245 for TFM mismatch); the CS0234 is a downstream symptom, not a ProjectReference gap")
		}
	}
	// Compile-output classification hints.
	if sigs, _ := out.CompileOutputSignals["codes"].(map[string]int); sigs != nil {
		if sigs["MSB3245"] > 0 {
			out.LikelyCauses = append(out.LikelyCauses, "MSB3245 present in compile output: MSBuild could not resolve a referenced assembly (ProjectReference output not produced or not on the reference path)")
			out.Remediation = append(out.Remediation, "verify the build command walks transitive ProjectReferences (no `--no-dependencies`); build the dependency project directly to confirm it produces an assembly")
		}
		if sigs["NETSDK1005"] > 0 || sigs["NU1201"] > 0 {
			out.LikelyCauses = append(out.LikelyCauses, "TargetFramework compatibility error between consumer and dependency (NETSDK1005/NU1201)")
			out.Remediation = append(out.Remediation, "align <TargetFramework> between consumer and dependency, or add multi-targeting so the dependency produces a compatible TFM")
		}
	}
	if out.PrivateFeedDetected {
		out.LikelyCauses = append(out.LikelyCauses, "a NuGet.config reachable from the dependency project declares a private feed (e.g. Azure DevOps / GitHub Packages / JFrog); in the build container, restore will fail without credentials and types will not be produced")
		out.Remediation = append(out.Remediation,
			"authenticate the private NuGet feed via ASQS config: for Azure DevOps set `vcs.azure_devops.token` + `runner.azure_devops_nuget_feed_endpoints`; for any other HTTPS NuGet source (Artifactory, ProGet, BaGet, MyGet) add a `- {type: nuget, endpoint, username, password}` entry under `runner.private_registry_credentials` (the same unified list also covers Maven / npm with `type: maven` / `type: npm`). ASQS injects all paths via `VSS_NUGET_EXTERNAL_FEED_ENDPOINTS` on docker eval + bootstrap containers — no code change needed, just configuration. VSS_NUGET_ACCESSTOKEN / Credential Provider also work if you prefer to configure them at the container level.",
			"alternatively, vendor the required packages to a public or internal feed reachable from the build environment and remove the private source from NuGet.config",
		)
	}
	if len(out.ConditionalHints) > 0 {
		out.LikelyCauses = append(out.LikelyCauses, "the ProjectReference may be inside an MSBuild conditional block that doesn't match the active build configuration")
		out.Remediation = append(out.Remediation, "add an unconditional <ProjectReference Include=\"...\"/> at top-level <ItemGroup> (outside any <Choose>/<When>) for the missing dependency")
	}
	// Sln-inclusion analysis: strongest explanation when compile output has only bare
	// CS0234 (no MSB3245, no NU/NETSDK codes) and the consumer's ProjectReference is
	// unconditional on disk.
	if anyActiveSln && slnMissingDepCount > 0 {
		out.LikelyCauses = append(out.LikelyCauses, "the .sln that contains the consumer project does NOT list the already-referenced dependency as a project entry; when MSBuild builds the sln it anchors the build graph on the sln's project list, and a cross-sln ProjectReference can leave the dependency's assembly unbuilt in the workspace (explains bare CS0234 without MSB3245)")
		out.Remediation = append(out.Remediation,
			"add the missing dependency project(s) to the sln with `dotnet sln <sln> add <dep.csproj>` (or have ASQS pre-generate this alongside ProjectReference fixes) so MSBuild produces the dependency's assembly during the sln build",
			"alternatively, build the dependency project explicitly before the sln (`dotnet build <dep.csproj>` or add it to a solution filter), or switch the compile command from the .sln to building the consumer .csproj directly so ProjectReference traversal is not gated by sln membership",
		)
	}
	// Build.0 analysis: dep listed in sln but "Build" checkbox is off for configurations
	// where the target's Build.0 is on. Same symptom as "missing from sln" (bare CS0234,
	// no MSB3245) because MSBuild treats the dep as present-for-reference but not
	// present-for-build in that configuration.
	slnBuildDisabledCount := 0
	for _, entry := range slnEntries {
		if c, ok := entry["deps_build_disabled_count"].(int); ok {
			slnBuildDisabledCount += c
		}
	}
	if slnBuildDisabledCount > 0 {
		out.LikelyCauses = append(out.LikelyCauses, "the dependency project is listed in the sln but has no ProjectConfigurationPlatforms Build.0 row for one or more solution configurations where the target project is built (classic VS 'Build' checkbox unchecked for the dep); MSBuild skips building the dep in that configuration, its assembly is not produced in bin/, and the consumer's ProjectReference resolves to a non-existent DLL — CS0234 without MSB3245 is the canonical symptom")
		out.Remediation = append(out.Remediation,
			"enable Build.0 for the dependency in the active sln configuration(s) — either via Visual Studio's Configuration Manager (tick the 'Build' checkbox) or by adding the missing {DEP-GUID}.<Config>.Build.0 rows to GlobalSection(ProjectConfigurationPlatforms); ASQS can patch this automatically in the same run",
		)
	}
	return out
}

// scanCsFilesAndNamespaces returns the list of .cs file paths (relative to projDir) and
// the set of distinct namespaces declared across those files.
func scanCsFilesAndNamespaces(projDir string) ([]string, []string) {
	var csFiles []string
	nsSet := map[string]bool{}
	_ = filepath.WalkDir(projDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			// Skip build outputs to stay fast and avoid noise.
			lname := strings.ToLower(d.Name())
			if lname == "bin" || lname == "obj" || lname == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".cs") {
			return nil
		}
		rel, err := filepath.Rel(projDir, path)
		if err != nil {
			return nil
		}
		csFiles = append(csFiles, filepath.ToSlash(rel))
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range reNamespaceDecl.FindAllStringSubmatch(string(b), -1) {
			if len(m) >= 2 {
				ns := strings.TrimSpace(m[1])
				if ns != "" {
					nsSet[ns] = true
				}
			}
		}
		return nil
	})
	nss := make([]string, 0, len(nsSet))
	for k := range nsSet {
		nss = append(nss, k)
	}
	sort.Strings(nss)
	sort.Strings(csFiles)
	return csFiles, nss
}

// anyNamespaceMatchesLeaf returns true when at least one namespace either equals the leaf
// or has the leaf as its last segment, or starts with "<leaf>." — covering the usual
// project-leaf-as-namespace convention (e.g. leaf "Upper.ChromeRemote" matches ns
// "Upper.ChromeRemote" or "Upper.ChromeRemote.Client"). Case-insensitive.
func anyNamespaceMatchesLeaf(namespaces []string, leaf string) bool {
	l := strings.ToLower(strings.TrimSpace(leaf))
	if l == "" {
		return true
	}
	for _, ns := range namespaces {
		n := strings.ToLower(strings.TrimSpace(ns))
		if n == l {
			return true
		}
		if strings.HasPrefix(n, l+".") {
			return true
		}
		// Match when the leaf appears at the end of the namespace chain (e.g. leaf
		// "ChromeRemote" matches "Upper.ChromeRemote").
		parts := strings.Split(n, ".")
		if len(parts) > 0 && parts[len(parts)-1] == l {
			return true
		}
	}
	return false
}

// reSlnProjectLine captures (relative project path, project GUID) from a sln "Project(...)" line:
//
//	Project("{TYPE-GUID}") = "Name", "relative\path\To.csproj", "{PROJECT-GUID}"
var reSlnProjectLine = regexp.MustCompile(`(?im)^\s*Project\s*\(\s*"\{[0-9A-Fa-f\-]+\}"\s*\)\s*=\s*"[^"]*"\s*,\s*"([^"]+\.(?:csproj|vcxproj|vbproj|fsproj))"\s*,\s*"(\{[0-9A-Fa-f\-]+\})"\s*$`)

// reSlnGlobalSection finds a "GlobalSection(<Name>) = <when>\n...EndGlobalSection" block.
var reSlnGlobalSection = regexp.MustCompile(`(?is)GlobalSection\s*\(\s*([A-Za-z]+)\s*\)\s*=\s*\w+\s*\r?\n(.*?)EndGlobalSection`)

// reSlnSolutionConfigLine matches a SolutionConfigurationPlatforms entry:
//
//	Debug|Any CPU = Debug|Any CPU
var reSlnSolutionConfigLine = regexp.MustCompile(`(?m)^\s*([^=]+?)\s*=\s*([^\r\n]+?)\s*$`)

// reSlnProjectBuildRow matches a ProjectConfigurationPlatforms entry for Build.0:
//
//	{GUID}.Debug|Any CPU.Build.0 = Debug|Any CPU
var reSlnProjectBuildRow = regexp.MustCompile(`(?im)^\s*\{([0-9A-Fa-f\-]+)\}\.([^.]+)\.Build\.0\s*=\s*([^\r\n]+?)\s*$`)

// reSlnProjectActiveCfgRow matches a ProjectConfigurationPlatforms entry for ActiveCfg.
// The RHS is the project-level config the sln config maps to (which may legitimately
// differ from the sln config name, e.g. "Release|Any CPU.ActiveCfg = Release|x86").
var reSlnProjectActiveCfgRow = regexp.MustCompile(`(?im)^\s*\{([0-9A-Fa-f\-]+)\}\.([^.]+)\.ActiveCfg\s*=\s*([^\r\n]+?)\s*$`)

// slnProjectEntry is the parsed metadata for a project listed in a sln file.
type slnProjectEntry struct {
	Rel     string // repo-relative slash path
	RawPath string // raw path exactly as written in the sln (usually backslashes)
	GUID    string // uppercase, with braces
}

// slnParsed is the distilled inclusion + build-activation graph for one .sln.
type slnParsed struct {
	SlnAbs             string
	SlnDir             string
	LineEnding         string
	ProjectsByNorm     map[string]slnProjectEntry // norm path -> entry
	ProjectsByGUID     map[string]slnProjectEntry // GUID (uppercase braces) -> entry
	SolutionConfigs    []string                   // raw config strings, e.g. "Debug|Any CPU"
	BuildEnabledByGUID map[string]map[string]bool // GUID -> {config -> Build.0 row present}
	// ActiveCfgByGUID maps GUID -> sln-config -> project-config RHS. The RHS is what the
	// sln maps that particular solution configuration to on the referenced project, and it
	// is the value Build.0 must carry on its RHS for MSBuild to build the project in that
	// sln config. When a sln has "Release|Any CPU.ActiveCfg = Release|x86" for a dep and
	// we add a Build.0 row, the RHS must be "Release|x86" — NOT "Release|Any CPU" — or
	// MSBuild silently skips the build with no diagnostic.
	ActiveCfgByGUID map[string]map[string]string // GUID -> {sln-config -> project-config RHS}
}

// parseSln reads a .sln and returns inclusion/configuration metadata. Returns nil on read
// errors. Silently treats .slnx as unsupported here (XML schema).
func parseSln(slnAbs, repoScanRoot string) *slnParsed {
	if strings.EqualFold(filepath.Ext(slnAbs), ".slnx") {
		return nil
	}
	b, err := os.ReadFile(slnAbs)
	if err != nil {
		return nil
	}
	src := string(b)
	eol := "\n"
	if strings.Contains(src, "\r\n") {
		eol = "\r\n"
	}
	slnDir := filepath.Dir(slnAbs)
	p := &slnParsed{
		SlnAbs:             slnAbs,
		SlnDir:             slnDir,
		LineEnding:         eol,
		ProjectsByNorm:     map[string]slnProjectEntry{},
		ProjectsByGUID:     map[string]slnProjectEntry{},
		BuildEnabledByGUID: map[string]map[string]bool{},
		ActiveCfgByGUID:    map[string]map[string]string{},
	}
	// Project entries.
	for _, m := range reSlnProjectLine.FindAllStringSubmatch(src, -1) {
		if len(m) < 3 {
			continue
		}
		raw := strings.TrimSpace(m[1])
		guid := strings.ToUpper(strings.TrimSpace(m[2]))
		rawOS := strings.ReplaceAll(raw, "\\", string(filepath.Separator))
		absRef := filepath.Clean(filepath.Join(slnDir, rawOS))
		relToRepo, relErr := filepath.Rel(repoScanRoot, absRef)
		if relErr != nil {
			continue
		}
		norm := strings.ToLower(filepath.ToSlash(relToRepo))
		e := slnProjectEntry{Rel: filepath.ToSlash(relToRepo), RawPath: raw, GUID: guid}
		p.ProjectsByNorm[norm] = e
		p.ProjectsByGUID[guid] = e
	}
	// Global sections.
	for _, m := range reSlnGlobalSection.FindAllStringSubmatch(src, -1) {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[1])
		body := m[2]
		switch name {
		case "SolutionConfigurationPlatforms":
			for _, lm := range reSlnSolutionConfigLine.FindAllStringSubmatch(body, -1) {
				if len(lm) < 3 {
					continue
				}
				lhs := strings.TrimSpace(lm[1])
				if lhs != "" && !strings.EqualFold(lhs, "EndGlobalSection") {
					p.SolutionConfigs = append(p.SolutionConfigs, lhs)
				}
			}
		case "ProjectConfigurationPlatforms":
			for _, lm := range reSlnProjectBuildRow.FindAllStringSubmatch(body, -1) {
				if len(lm) < 4 {
					continue
				}
				guid := strings.ToUpper("{" + lm[1] + "}")
				cfg := strings.TrimSpace(lm[2])
				if _, ok := p.BuildEnabledByGUID[guid]; !ok {
					p.BuildEnabledByGUID[guid] = map[string]bool{}
				}
				p.BuildEnabledByGUID[guid][cfg] = true
			}
			for _, lm := range reSlnProjectActiveCfgRow.FindAllStringSubmatch(body, -1) {
				if len(lm) < 4 {
					continue
				}
				guid := strings.ToUpper("{" + lm[1] + "}")
				cfg := strings.TrimSpace(lm[2])
				rhs := strings.TrimSpace(lm[3])
				if _, ok := p.ActiveCfgByGUID[guid]; !ok {
					p.ActiveCfgByGUID[guid] = map[string]string{}
				}
				p.ActiveCfgByGUID[guid][cfg] = rhs
			}
		}
	}
	return p
}

// builtConfigsForGUID returns the sorted list of solution configurations for which the
// project identified by guid has a Build.0 row present in ProjectConfigurationPlatforms.
func (p *slnParsed) builtConfigsForGUID(guid string) []string {
	if p == nil {
		return nil
	}
	m := p.BuildEnabledByGUID[strings.ToUpper(guid)]
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// findSlnsIncludingTarget walks repoScanRoot (bounded) looking for .sln files that
// reference targetProjRel. For each such sln, returns the sln path plus whether it
// references each of the given depRels, and — crucially — whether each dep has a
// Build.0 row for every SolutionConfiguration in which the target itself has Build.0.
// A project can be listed in the sln (satisfying deps_in_sln) yet have "Build" unchecked
// for the active configuration: MSBuild then skips building it, Proposals' ProjectReference
// resolves to a non-existent DLL, and CS0234 fires with no MSB3245. This is the precise
// fingerprint we surface here so the evaluator can auto-patch the missing Build.0 rows.
func findSlnsIncludingTarget(repoScanRoot, targetProjRel string, depRels []string) []map[string]interface{} {
	if strings.TrimSpace(repoScanRoot) == "" || strings.TrimSpace(targetProjRel) == "" {
		return nil
	}
	targetNorm := strings.ToLower(filepath.ToSlash(filepath.Clean(targetProjRel)))
	depNorms := make(map[string]string, len(depRels))
	for _, d := range depRels {
		depNorms[d] = strings.ToLower(filepath.ToSlash(filepath.Clean(d)))
	}
	var results []map[string]interface{}
	_ = filepath.WalkDir(repoScanRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			lname := strings.ToLower(d.Name())
			if lname == "bin" || lname == "obj" || lname == "node_modules" || lname == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".sln" && ext != ".slnx" {
			return nil
		}
		parsed := parseSln(path, repoScanRoot)
		if parsed == nil || len(parsed.ProjectsByNorm) == 0 {
			return nil
		}
		targetEntry, ok := parsed.ProjectsByNorm[targetNorm]
		if !ok {
			// This sln doesn't include the target project: not an "active" sln for it.
			return nil
		}
		slnRel, _ := filepath.Rel(repoScanRoot, path)
		entry := map[string]interface{}{
			"sln_path":             filepath.ToSlash(slnRel),
			"contains_target":      true,
			"listed_project_count": len(parsed.ProjectsByNorm),
			"target_guid":          targetEntry.GUID,
			"solution_configs":     parsed.SolutionConfigs,
		}
		// target build-enabled configs
		targetBuiltIn := parsed.builtConfigsForGUID(targetEntry.GUID)
		entry["target_build_configs"] = targetBuiltIn
		// Per-dep analysis: inclusion + build-enabled configs + where target builds but dep does not.
		depsContains := map[string]interface{}{}
		depsBuild := map[string]interface{}{}
		depsBuildDisabled := map[string]interface{}{}
		missingFromSln := []string{}
		depBuildDisabledCount := 0
		for depRel, depNorm := range depNorms {
			entryDep, inSln := parsed.ProjectsByNorm[depNorm]
			if !inSln {
				depsContains[depRel] = false
				missingFromSln = append(missingFromSln, depRel)
				continue
			}
			depsContains[depRel] = true
			depBuiltIn := parsed.builtConfigsForGUID(entryDep.GUID)
			depsBuild[depRel] = depBuiltIn
			disabled := diffConfigs(targetBuiltIn, depBuiltIn)
			if len(disabled) > 0 {
				depsBuildDisabled[depRel] = disabled
				depBuildDisabledCount++
			}
		}
		entry["deps_in_sln"] = depsContains
		sort.Strings(missingFromSln)
		entry["deps_missing_from_sln"] = missingFromSln
		entry["deps_build_configs"] = depsBuild
		entry["deps_build_disabled_in_configs"] = depsBuildDisabled
		entry["deps_build_disabled_count"] = depBuildDisabledCount
		results = append(results, entry)
		return nil
	})
	sort.SliceStable(results, func(i, j int) bool {
		pi, _ := results[i]["sln_path"].(string)
		pj, _ := results[j]["sln_path"].(string)
		return pi < pj
	})
	return results
}

// diffConfigs returns configs present in `a` but missing from `b` (case-insensitive,
// whitespace-tolerant). Used to find solution configurations where the target has
// Build.0 but the dependency does not — i.e. "Build" checkbox unchecked for the dep
// in a configuration that actually runs during this sln build.
func diffConfigs(a, b []string) []string {
	if len(a) == 0 {
		return nil
	}
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "")) }
	bs := map[string]bool{}
	for _, v := range b {
		bs[norm(v)] = true
	}
	var out []string
	for _, v := range a {
		if !bs[norm(v)] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// newGUIDUpper returns a new GUID string in uppercase, with braces, without hyphen normalization
// (standard 8-4-4-4-12 form). Uses crypto/rand-quality if available, falling back to a stable
// hash of the input path + entropy; for .sln consumption any unique GUID works.
func newGUIDUpper() string {
	var b [16]byte
	// Read from /dev/urandom equivalent via filepath-free approach: use nanoseconds + monotonic
	// hash. MSBuild only cares about uniqueness within the sln.
	n := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		b[i] = byte(n >> (uint(i) * 8))
	}
	_, _ = cryptoRandRead(b[8:])
	// RFC 4122 v4 bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08X-%04X-%04X-%04X-%012X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// cryptoRandRead wraps crypto/rand.Read to isolate imports; returns the bytes written.
func cryptoRandRead(p []byte) (int, error) {
	return cryptoReader(p)
}

// tryAutoAddMissingSlnProjects patches each sln entry to include every dep listed under
// deps_missing_from_sln. Returns a summary of what was patched. On any error we skip that
// sln silently (the diag still fires). For each patched sln we:
//  1. Pick a valid C# project-type GUID ({FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}).
//  2. Generate a fresh project GUID.
//  3. Compute relative path from sln dir to dep csproj using Windows-style backslashes
//     (sln files historically expect backslashes, and MSBuild on all platforms accepts them).
//  4. Insert a Project(...)...EndProject block immediately before "Global".
//  5. Add GlobalSection/ProjectConfigurationPlatforms entries for Debug|Any CPU and
//     Release|Any CPU (ActiveCfg + Build.0). For sdk-style projects with multiple TFMs,
//     MSBuild auto-resolves the platform; "Any CPU" is the safe default.
func tryAutoAddMissingSlnProjects(repoScanRoot string, slnEntries []map[string]interface{}) []map[string]interface{} {
	var patched []map[string]interface{}
	const csProjectTypeGUID = "{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}"
	for _, entry := range slnEntries {
		slnRel, _ := entry["sln_path"].(string)
		missingRaw, _ := entry["deps_missing_from_sln"].([]string)
		if slnRel == "" || len(missingRaw) == 0 {
			continue
		}
		slnAbs := filepath.Join(repoScanRoot, filepath.FromSlash(slnRel))
		slnDir := filepath.Dir(slnAbs)
		b, err := os.ReadFile(slnAbs)
		if err != nil {
			continue
		}
		src := string(b)
		// .slnx is XML; we don't attempt to patch it here (different schema). Skip.
		if strings.EqualFold(filepath.Ext(slnAbs), ".slnx") {
			continue
		}
		// Locate "Global" (introducing solution-level sections) — our insertion point for
		// new Project(...) blocks goes immediately before it. If not found, skip (malformed).
		globalIdx := strings.Index(src, "\nGlobal\n")
		if globalIdx < 0 {
			globalIdx = strings.Index(src, "\nGlobal\r\n")
			if globalIdx < 0 {
				continue
			}
		}
		// Detect line ending used in the file.
		eol := "\n"
		if strings.Contains(src[:globalIdx+7], "\r\n") {
			eol = "\r\n"
		}
		// Locate ProjectConfigurationPlatforms section to append configuration rows.
		pcpStart := strings.Index(src, "GlobalSection(ProjectConfigurationPlatforms) = postSolution")
		pcpEnd := -1
		if pcpStart >= 0 {
			pcpEnd = strings.Index(src[pcpStart:], "EndGlobalSection")
			if pcpEnd >= 0 {
				pcpEnd += pcpStart
			}
		}
		var insertedProjects []map[string]interface{}
		var projectBlocks strings.Builder
		var cfgRows strings.Builder
		for _, depRel := range missingRaw {
			depAbs := filepath.Clean(filepath.Join(repoScanRoot, filepath.FromSlash(depRel)))
			depName := strings.TrimSuffix(filepath.Base(depAbs), filepath.Ext(depAbs))
			rel, relErr := filepath.Rel(slnDir, depAbs)
			if relErr != nil {
				continue
			}
			// Use Windows-style backslashes for sln compatibility.
			relWin := strings.ReplaceAll(filepath.ToSlash(rel), "/", `\`)
			projGUID := "{" + newGUIDUpper() + "}"
			projectBlocks.WriteString(fmt.Sprintf(
				`Project("%s") = "%s", "%s", "%s"%sEndProject%s`,
				csProjectTypeGUID, depName, relWin, projGUID, eol, eol,
			))
			// Standard config rows for the 4 common sln configurations.
			for _, cfg := range []string{"Debug|Any CPU", "Release|Any CPU"} {
				cfgRows.WriteString(fmt.Sprintf("\t\t%s.%s.ActiveCfg = %s%s", projGUID, cfg, cfg, eol))
				cfgRows.WriteString(fmt.Sprintf("\t\t%s.%s.Build.0 = %s%s", projGUID, cfg, cfg, eol))
			}
			insertedProjects = append(insertedProjects, map[string]interface{}{
				"dependency": depRel,
				"name":       depName,
				"rel_path":   relWin,
				"guid":       projGUID,
			})
		}
		if len(insertedProjects) == 0 {
			continue
		}
		// Build the new sln content.
		var newSrc strings.Builder
		newSrc.WriteString(src[:globalIdx+1])
		newSrc.WriteString(projectBlocks.String())
		tail := src[globalIdx+1:]
		if pcpEnd >= 0 {
			// Reindex pcpEnd relative to tail.
			pcpEndInTail := pcpEnd - (globalIdx + 1)
			if pcpEndInTail > 0 && pcpEndInTail < len(tail) {
				newSrc.WriteString(tail[:pcpEndInTail])
				newSrc.WriteString(cfgRows.String())
				newSrc.WriteString(tail[pcpEndInTail:])
			} else {
				newSrc.WriteString(tail)
			}
		} else {
			// No ProjectConfigurationPlatforms section: leave config rows out (MSBuild will
			// still build the project with default config from the csproj).
			newSrc.WriteString(tail)
		}
		if err := os.WriteFile(slnAbs, []byte(newSrc.String()), 0o644); err != nil {
			continue
		}
		patched = append(patched, map[string]interface{}{
			"sln_path":          slnRel,
			"inserted_projects": insertedProjects,
		})
	}
	return patched
}

// tryAutoEnableSlnBuildForDeps inserts missing ProjectConfigurationPlatforms "Build.0"
// rows for each dependency that is listed in a sln but whose "Build" checkbox is off
// in configurations where the target project's "Build" is on. This is the canonical
// fix for the "bare CS0234 with no MSB3245" pattern: the dep is in the sln so
// ProjectReference resolution sees it, but MSBuild doesn't build it for the active
// configuration and the consumer's compile sees no assembly with the expected namespace.
//
// For each affected (sln, dep, config) we ensure:
//   - an ActiveCfg row exists (if missing, we add one pointing at the sln config mapping
//     onto the dep — using the sln's own config name so MSBuild picks a matching TFM via
//     the csproj's multi-targeting defaults);
//   - a Build.0 row exists (inserted if absent).
//
// Idempotent: if both rows are already present for a (dep, config) pair, nothing changes.
func tryAutoEnableSlnBuildForDeps(repoScanRoot string, slnEntries []map[string]interface{}) []map[string]interface{} {
	var patched []map[string]interface{}
	for _, entry := range slnEntries {
		slnRel, _ := entry["sln_path"].(string)
		disabled, _ := entry["deps_build_disabled_in_configs"].(map[string]interface{})
		if slnRel == "" || len(disabled) == 0 {
			continue
		}
		slnAbs := filepath.Join(repoScanRoot, filepath.FromSlash(slnRel))
		if strings.EqualFold(filepath.Ext(slnAbs), ".slnx") {
			continue
		}
		parsed := parseSln(slnAbs, repoScanRoot)
		if parsed == nil {
			continue
		}
		b, err := os.ReadFile(slnAbs)
		if err != nil {
			continue
		}
		src := string(b)
		eol := parsed.LineEnding
		// Locate ProjectConfigurationPlatforms body to insert config rows before EndGlobalSection.
		pcpStart := strings.Index(src, "GlobalSection(ProjectConfigurationPlatforms) = postSolution")
		if pcpStart < 0 {
			continue
		}
		// Find the EndGlobalSection that closes this specific block.
		relEnd := strings.Index(src[pcpStart:], "EndGlobalSection")
		if relEnd < 0 {
			continue
		}
		pcpEnd := pcpStart + relEnd
		var rowsToInsert strings.Builder
		var inserts []map[string]interface{}
		for depRel, cfgsV := range disabled {
			cfgs, _ := cfgsV.([]string)
			if len(cfgs) == 0 {
				continue
			}
			depNorm := strings.ToLower(filepath.ToSlash(filepath.Clean(depRel)))
			depEntry, ok := parsed.ProjectsByNorm[depNorm]
			if !ok {
				continue
			}
			enabledMap := parsed.BuildEnabledByGUID[depEntry.GUID]
			activeMap := parsed.ActiveCfgByGUID[depEntry.GUID]
			addedCfgs := []string{}
			for _, cfg := range cfgs {
				if enabledMap != nil && enabledMap[cfg] {
					continue
				}
				// MSBuild requires Build.0's RHS to match the project-level config name
				// the sln has already mapped this sln config to via ActiveCfg. If Build.0
				// names a project config the referenced project does not define, MSBuild
				// silently skips the build (no MSB3245) — exactly the symptom we've been
				// hunting. We therefore:
				//   1. Read the existing ActiveCfg RHS for this (GUID, slnCfg) if present.
				//   2. If absent, pick the first ActiveCfg RHS defined for the dep on any
				//      solution config (its "natural" project config); if none, fall back
				//      to cfg itself (SDK-style projects accept the sln config name).
				//   3. Mirror that RHS in Build.0 (and insert a matching ActiveCfg when
				//      missing so the two rows are consistent).
				rhs := ""
				if activeMap != nil {
					if v, ok := activeMap[cfg]; ok {
						rhs = v
					}
				}
				if rhs == "" {
					if activeMap != nil {
						// Deterministic fallback: pick a stable RHS from existing mappings.
						keys := make([]string, 0, len(activeMap))
						for k := range activeMap {
							keys = append(keys, k)
						}
						sort.Strings(keys)
						for _, k := range keys {
							if v := activeMap[k]; v != "" {
								rhs = v
								break
							}
						}
					}
				}
				if rhs == "" {
					rhs = cfg
				}
				// Insert ActiveCfg if the (GUID, slnCfg) mapping is not already present.
				needleActive := fmt.Sprintf("%s.%s.ActiveCfg", depEntry.GUID, cfg)
				if !strings.Contains(src[pcpStart:pcpEnd], needleActive) {
					rowsToInsert.WriteString(fmt.Sprintf("\t\t%s.%s.ActiveCfg = %s%s", depEntry.GUID, cfg, rhs, eol))
				}
				// Insert Build.0 with the matching RHS.
				needleBuild := fmt.Sprintf("%s.%s.Build.0", depEntry.GUID, cfg)
				if !strings.Contains(src[pcpStart:pcpEnd], needleBuild) {
					rowsToInsert.WriteString(fmt.Sprintf("\t\t%s.%s.Build.0 = %s%s", depEntry.GUID, cfg, rhs, eol))
				}
				addedCfgs = append(addedCfgs, cfg+" -> "+rhs)
			}
			if len(addedCfgs) > 0 {
				sort.Strings(addedCfgs)
				inserts = append(inserts, map[string]interface{}{
					"dependency": depRel,
					"guid":       depEntry.GUID,
					"enabled_in": addedCfgs,
				})
			}
		}
		if rowsToInsert.Len() == 0 || len(inserts) == 0 {
			continue
		}
		// Insert the rows immediately before EndGlobalSection of ProjectConfigurationPlatforms.
		var newSrc strings.Builder
		newSrc.WriteString(src[:pcpEnd])
		newSrc.WriteString(rowsToInsert.String())
		newSrc.WriteString(src[pcpEnd:])
		if err := os.WriteFile(slnAbs, []byte(newSrc.String()), 0o644); err != nil {
			continue
		}
		patched = append(patched, map[string]interface{}{
			"sln_path":      slnRel,
			"build_enabled": inserts,
		})
	}
	return patched
}

// summarizeCompileOutputCodes counts occurrences of MS/NuGet error codes in the compile
// output (CS/MSB/NETSDK/NU####) and captures a short sample line for each distinct code.
func summarizeCompileOutputCodes(compileOutput string) map[string]interface{} {
	if strings.TrimSpace(compileOutput) == "" {
		return nil
	}
	counts := map[string]int{}
	samples := map[string]string{}
	for _, line := range strings.Split(compileOutput, "\n") {
		lower := line
		for _, m := range reMSCErrorCode.FindAllStringSubmatch(lower, -1) {
			if len(m) < 2 {
				continue
			}
			code := m[1]
			counts[code]++
			if _, ok := samples[code]; !ok {
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 400 {
					trimmed = trimmed[:400] + "..."
				}
				samples[code] = trimmed
			}
		}
	}
	if len(counts) == 0 {
		return nil
	}
	out := map[string]interface{}{
		"codes":   counts,
		"samples": samples,
	}
	return out
}

// readPackageReferences returns a list of {include, version} entries for the given csproj.
func readPackageReferences(csprojAbs string) []map[string]interface{} {
	b, err := os.ReadFile(csprojAbs)
	if err != nil {
		return nil
	}
	src := string(b)
	var out []map[string]interface{}
	for _, m := range rePackageRefAttrs.FindAllStringSubmatch(src, -1) {
		if len(m) < 2 {
			continue
		}
		attrs := m[1]
		inc := rePackageRefInclude.FindStringSubmatch(attrs)
		if len(inc) < 2 {
			continue
		}
		entry := map[string]interface{}{"include": strings.TrimSpace(inc[1])}
		if ver := rePackageRefVersion.FindStringSubmatch(attrs); len(ver) >= 2 {
			entry["version"] = strings.TrimSpace(ver[1])
		}
		out = append(out, entry)
	}
	return out
}

// discoverNuGetConfigsFrom walks up from the given csproj file looking for
// NuGet.config / nuget.config / NuGet.Config files (case-insensitive) until it
// reaches repoRoot. For each found config it extracts declared feed URLs and
// heuristically flags private feeds.
func discoverNuGetConfigsFrom(csprojAbs, repoRoot string) []map[string]interface{} {
	var out []map[string]interface{}
	cur := filepath.Dir(filepath.Clean(csprojAbs))
	repoRoot = filepath.Clean(repoRoot)
	seen := map[string]bool{}
	for {
		entries, _ := os.ReadDir(cur)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := strings.ToLower(e.Name())
			if n != "nuget.config" {
				continue
			}
			p := filepath.Join(cur, e.Name())
			if seen[p] {
				continue
			}
			seen[p] = true
			if parsed := parseNuGetConfig(p); parsed != nil {
				out = append(out, parsed)
			}
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
	return out
}

func parseNuGetConfig(absPath string) map[string]interface{} {
	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	src := string(b)
	var feeds []map[string]interface{}
	for _, m := range reNuGetConfigAddURL.FindAllStringSubmatch(src, -1) {
		if len(m) < 2 {
			continue
		}
		url := strings.TrimSpace(m[1])
		if url == "" {
			continue
		}
		name := ""
		if nm := reNuGetConfigAddName.FindStringSubmatch(m[0]); len(nm) >= 2 {
			name = strings.TrimSpace(nm[1])
		}
		entry := map[string]interface{}{
			"name":           name,
			"url":            url,
			"private_likely": feedLooksPrivate(url),
		}
		feeds = append(feeds, entry)
	}
	if len(feeds) == 0 {
		return nil
	}
	return map[string]interface{}{
		"path":  absPath,
		"feeds": feeds,
	}
}

// feedLooksPrivate flags feed URLs that typically require authentication.
// This is deliberately conservative: we flag well-known private-feed hosts and
// anything that isn't nuget.org, localhost, or a file path.
func feedLooksPrivate(url string) bool {
	u := strings.ToLower(strings.TrimSpace(url))
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "file:") || strings.HasPrefix(u, "./") || strings.HasPrefix(u, "../") || strings.HasPrefix(u, "/") || strings.HasPrefix(u, "\\") {
		return false
	}
	if strings.Contains(u, "api.nuget.org") || strings.Contains(u, "//nuget.org") || strings.Contains(u, "://nuget.org") {
		return false
	}
	if strings.Contains(u, "localhost") || strings.Contains(u, "127.0.0.1") {
		return false
	}
	privateHosts := []string{
		"pkgs.dev.azure.com",
		"pkgs.visualstudio.com",
		"nuget.pkg.github.com",
		"jfrog.io",
		"artifactory",
		"myget.org",
		"gemfury.com",
		"cloudsmith.io",
		"packagecloud.io",
	}
	for _, h := range privateHosts {
		if strings.Contains(u, h) {
			return true
		}
	}
	// Unknown HTTPS host that isn't nuget.org — flag as likely-private for safety.
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return true
	}
	return false
}

func existingProjectReferenceStateEval(csprojContent, targetCsprojDir string) (map[string]bool, map[string]bool) {
	any := make(map[string]bool)
	unconditional := make(map[string]bool)
	mark := func(include string, isConditional bool) {
		include = strings.TrimSpace(include)
		if include == "" {
			return
		}
		abs := filepath.Clean(filepath.Join(targetCsprojDir, filepath.FromSlash(strings.ReplaceAll(include, "\\", "/"))))
		any[abs] = true
		if !isConditional {
			unconditional[abs] = true
		}
	}
	for _, g := range reItemGroupBlockEval.FindAllStringSubmatch(csprojContent, -1) {
		if len(g) < 3 {
			continue
		}
		groupAttrs := g[1]
		groupBody := g[2]
		groupConditional := reConditionAttrEval.MatchString(groupAttrs)
		for _, pr := range reProjectReferenceTag.FindAllStringSubmatch(groupBody, -1) {
			if len(pr) < 2 {
				continue
			}
			attrs := pr[1]
			m := reIncludeAttrEval.FindStringSubmatch(attrs)
			if len(m) < 2 {
				continue
			}
			mark(m[1], groupConditional || reConditionAttrEval.MatchString(attrs))
		}
	}
	return any, unconditional
}
