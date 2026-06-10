package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/asqs/asqs-core/internal/runner/profile"
)

var (
	reCsprojTargetFramework  = regexp.MustCompile(`(?i)<TargetFramework>\s*([^<]*?)\s*</TargetFramework>`)
	reCsprojTargetFrameworks = regexp.MustCompile(`(?i)<TargetFrameworks>\s*([^<]*?)\s*</TargetFrameworks>`)
	reCsprojXMLComments      = regexp.MustCompile(`(?s)<!--.*?-->`)
)

const maxDotnetWalkDepth = 12

func dotnetEvalSkipDir(name string) bool {
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

func dotnetRepoRelDepth(repo, absPath string) int {
	repo = filepath.Clean(repo)
	absPath = filepath.Clean(absPath)
	rel, err := filepath.Rel(repo, absPath)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator))
}

func isSdkStyleCsprojContent(content string) bool {
	s := strings.ToLower(content)
	return strings.Contains(s, `sdk="microsoft.net.sdk"`) || strings.Contains(s, `sdk='microsoft.net.sdk'`)
}

func rootSlnRel(repo string) (rel string, ok bool, err error) {
	ents, err := os.ReadDir(repo)
	if err != nil {
		return "", false, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".sln", ".slnx":
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", false, nil
	}
	sort.Strings(names)
	return filepath.ToSlash(names[0]), true, nil
}

func firstRootCsprojAbs(repo string) (string, error) {
	ents, err := os.ReadDir(repo)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".csproj") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", nil
	}
	return filepath.Join(repo, names[0]), nil
}

func discoverCsprojPathsForDotnet(repo string) ([]string, error) {
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
			if dotnetEvalSkipDir(d.Name()) {
				return fs.SkipDir
			}
			if dotnetRepoRelDepth(repo, path) > maxDotnetWalkDepth {
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

func discoverSDKStyleCsprojPathsForDotnet(repo string) ([]string, error) {
	all, err := discoverCsprojPathsForDotnet(repo)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range all {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if isSdkStyleCsprojContent(string(b)) {
			out = append(out, p)
		}
	}
	return out, nil
}

func csprojEvalScore(abs string) int {
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

// resolveDotnetEntryRel returns a repo-relative slash path to a root .sln/.slnx, root .csproj, or best nested SDK-style .csproj.
func resolveDotnetEntryRel(repo string) (string, error) {
	repo = filepath.Clean(repo)
	if rel, ok, err := rootSlnRel(repo); err != nil {
		return "", err
	} else if ok {
		return rel, nil
	}
	abs, err := firstRootCsprojAbs(repo)
	if err != nil {
		return "", err
	}
	if abs != "" {
		return relPathUnderRepo(repo, abs)
	}
	paths, err := discoverSDKStyleCsprojPathsForDotnet(repo)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", nil
	}
	sort.SliceStable(paths, func(i, j int) bool {
		si, sj := csprojEvalScore(paths[i]), csprojEvalScore(paths[j])
		if si != sj {
			return si < sj
		}
		return paths[i] < paths[j]
	})
	return relPathUnderRepo(repo, paths[0])
}

func relPathUnderRepo(repo, abs string) (string, error) {
	repo = filepath.Clean(repo)
	abs = filepath.Clean(abs)
	rel, err := filepath.Rel(repo, abs)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf(".NET project path %q is outside repo %q", abs, repo)
	}
	return filepath.ToSlash(rel), nil
}

// argvDotNetFormatHasIncludeOption is true when argv contains dotnet format's --include flag.
// The dotnet-format CLI only accepts trailing MSBuild /p: tokens after --include and the paths that follow;
// inserting /p: immediately after `format` consumes the workspace slot and the next token (.csproj) errors as
// "Unrecognized command or argument". Whole-repo `dotnet format <proj>` has no supported position for these props.
func argvDotNetFormatHasIncludeOption(argv []string) bool {
	for _, a := range argv {
		if strings.EqualFold(strings.TrimSpace(a), "--include") {
			return true
		}
	}
	return false
}

func argvHasDotnetProjectFile(argv []string) bool {
	for _, a := range argv {
		la := strings.ToLower(a)
		if strings.HasSuffix(la, ".csproj") || strings.HasSuffix(la, ".sln") || strings.HasSuffix(la, ".slnx") {
			return true
		}
		// `dotnet format .` targets the workspace; do not append another project path.
		if la == "." || la == ".." {
			return true
		}
	}
	return false
}

func argvAlreadyHasTargetFrameworkMSBuildProp(argv []string) bool {
	for _, a := range argv {
		la := strings.ToLower(strings.TrimSpace(a))
		if strings.HasPrefix(la, "/p:targetframework=") || strings.HasPrefix(la, "-p:targetframework=") {
			return true
		}
	}
	return false
}

// CsprojDeclaresConcreteTargetFramework is true when the file has a non-empty TargetFramework / TargetFrameworks value that is not an MSBuild property reference like $(Foo).
func CsprojDeclaresConcreteTargetFramework(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	s := reCsprojXMLComments.ReplaceAllString(string(b), "")
	for _, m := range reCsprojTargetFramework.FindAllStringSubmatch(s, -1) {
		v := strings.TrimSpace(m[1])
		if v != "" && !strings.HasPrefix(v, "$(") {
			return true, nil
		}
	}
	for _, m := range reCsprojTargetFrameworks.FindAllStringSubmatch(s, -1) {
		v := strings.TrimSpace(m[1])
		if v != "" && !strings.HasPrefix(v, "$(") {
			return true, nil
		}
	}
	return false, nil
}

// insertDotnetTargetFrameworkMSBuildProp adds /p:TargetFramework=… immediately after the dotnet CLI verb for most commands.
// For `dotnet format`, props are appended at the end when --include is present; otherwise the argv is unchanged.
func insertDotnetTargetFrameworkMSBuildProp(argv []string, fallback string) []string {
	prop := "/p:TargetFramework=" + strings.TrimSpace(fallback)
	if len(argv) < 2 {
		return append(append([]string(nil), argv...), prop)
	}
	verb := strings.ToLower(strings.TrimSpace(argv[1]))
	if verb == "format" {
		if !argvDotNetFormatHasIncludeOption(argv) {
			return argv
		}
		out := append([]string(nil), argv...)
		return append(out, prop)
	}
	switch verb {
	case "build", "test", "restore", "publish", "pack", "run", "msbuild", "add", "remove", "list", "clean", "watch":
		out := make([]string, 0, len(argv)+1)
		out = append(out, argv[0], argv[1], prop)
		out = append(out, argv[2:]...)
		return out
	default:
		out := append([]string(nil), argv...)
		return append(out, prop)
	}
}

// applyDotnetTargetFrameworkFallbackArgv inserts /p:TargetFramework=<fallback> when fallback is set and the entry .csproj
// does not declare a concrete TFM in the project file itself (no repo edits). cwdAbs is the dotnet working directory.
func applyDotnetTargetFrameworkFallbackArgv(argv []string, cwdAbs, fallback string) ([]string, error) {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" || len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv, nil
	}
	if argvAlreadyHasTargetFrameworkMSBuildProp(argv) {
		return argv, nil
	}
	cwdAbs = filepath.Clean(cwdAbs)
	var projArg string
	for i := len(argv) - 1; i >= 0; i-- {
		a := argv[i]
		if strings.EqualFold(filepath.Ext(a), ".csproj") {
			projArg = a
			break
		}
	}
	if projArg == "" && len(argv) >= 2 && strings.EqualFold(argv[1], "format") {
		rel, err := resolveDotnetEntryRel(cwdAbs)
		if err != nil || rel == "" || !strings.HasSuffix(strings.ToLower(rel), ".csproj") {
			return argv, nil
		}
		projArg = rel
	}
	if projArg == "" {
		return argv, nil
	}
	absProj := projArg
	if !filepath.IsAbs(projArg) {
		absProj = filepath.Join(cwdAbs, filepath.FromSlash(projArg))
	}
	absProj = filepath.Clean(absProj)
	st, err := os.Stat(absProj)
	if err != nil || st.IsDir() {
		return argv, nil
	}
	ok, err := CsprojDeclaresConcreteTargetFramework(absProj)
	if err == nil && ok {
		return argv, nil
	}
	return insertDotnetTargetFrameworkMSBuildProp(argv, fallback), nil
}

// pickDotnetWorkspace returns preferredWorkspaceRel when it points to an existing .csproj/.sln/.slnx under repoAbs;
// otherwise resolveDotnetEntryRel(repoAbs) (root solution first, then best SDK-style project).
func pickDotnetWorkspace(repoAbs, preferredWorkspaceRel string) (string, error) {
	preferredWorkspaceRel = strings.TrimSpace(filepath.ToSlash(preferredWorkspaceRel))
	if preferredWorkspaceRel != "" {
		abs := filepath.Join(repoAbs, filepath.FromSlash(preferredWorkspaceRel))
		st, err := os.Stat(abs)
		if err == nil && !st.IsDir() {
			low := strings.ToLower(preferredWorkspaceRel)
			if strings.HasSuffix(low, ".csproj") || strings.HasSuffix(low, ".sln") || strings.HasSuffix(low, ".slnx") {
				return preferredWorkspaceRel, nil
			}
		}
	}
	return resolveDotnetEntryRel(repoAbs)
}

// ensureDotnetProjectArg appends a discovered solution/project path for bare `dotnet` CLI argv (Docker / execve).
func ensureDotnetProjectArg(p profile.ToolchainProfile, argv []string, repoAbs string) ([]string, error) {
	return ensureDotnetProjectArgPreferred(p, argv, repoAbs, "")
}

// ensureDotnetProjectArgPreferred is like ensureDotnetProjectArg but uses pickDotnetWorkspace(repoAbs, preferredWorkspaceRel)
// when inserting the format/build workspace (so dotnet format --include can target the .csproj that owns the listed files).
func ensureDotnetProjectArgPreferred(p profile.ToolchainProfile, argv []string, repoAbs, preferredWorkspaceRel string) ([]string, error) {
	if p.ID != profile.CSharpDotnet || len(argv) < 2 || !dotnetFirstArgIsCLI(argv) {
		return argv, nil
	}
	// `dotnet format .` (or `..`) uses the directory as the workspace; with several root .sln/.slnx the tool errors.
	// Treat `.`/`..` as missing an explicit workspace and substitute pickDotnetWorkspace (same as omitting the path).
	if strings.EqualFold(strings.TrimSpace(argv[1]), "format") && len(argv) >= 3 {
		w := strings.TrimSpace(argv[2])
		if w == "." || w == ".." {
			rel, err := pickDotnetWorkspace(repoAbs, preferredWorkspaceRel)
			if err != nil {
				return nil, err
			}
			if rel == "" {
				return nil, fmt.Errorf("no .sln/.slnx or SDK-style .csproj found under %q (dotnet needs a project or solution file)", repoAbs)
			}
			argv = append([]string{argv[0], argv[1], rel}, argv[3:]...)
		}
	}
	if argvHasDotnetProjectFile(argv) {
		return argv, nil
	}
	rel, err := pickDotnetWorkspace(repoAbs, preferredWorkspaceRel)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, fmt.Errorf("no .sln/.slnx or SDK-style .csproj found under %q (dotnet needs a project or solution file)", repoAbs)
	}
	// `dotnet format [<PROJECT|SLN>] [options]` — the workspace must be the first argument after `format`.
	// If it is only appended after `--include`, the CLI treats the repo directory as the workspace and fails with
	// "Multiple MSBuild solution files found" when several .sln/.slnx sit at the root.
	if strings.EqualFold(strings.TrimSpace(argv[1]), "format") {
		out := make([]string, 0, len(argv)+1)
		out = append(out, argv[0], argv[1], rel)
		out = append(out, argv[2:]...)
		return out, nil
	}
	out := append([]string(nil), argv...)
	out = append(out, rel)
	return out, nil
}

// dotnetShellLineWithProject appends a quoted repo-relative project/solution path for `sh -c` local runs (cwd = repo).
// When fallbackTFM is set and the entry is a .csproj without a concrete in-file TFM, inserts /p:TargetFramework=… before the project path.
func dotnetShellLineWithProject(repo, commandPrefix string, fallbackTFM string) (string, error) {
	repo = filepath.Clean(repo)
	rel, err := resolveDotnetEntryRel(repo)
	if err != nil {
		return "", err
	}
	if rel == "" {
		return "", fmt.Errorf("no .sln/.slnx or SDK-style .csproj found under %q", repo)
	}
	line := strings.TrimSpace(commandPrefix)
	fallbackTFM = strings.TrimSpace(fallbackTFM)
	if fallbackTFM != "" && strings.HasSuffix(strings.ToLower(rel), ".csproj") {
		abs := filepath.Join(repo, filepath.FromSlash(rel))
		ok, err := CsprojDeclaresConcreteTargetFramework(abs)
		if err != nil || !ok {
			line += " /p:TargetFramework=" + fallbackTFM
		}
	}
	line += " " + strconv.Quote(rel)
	return line, nil
}
