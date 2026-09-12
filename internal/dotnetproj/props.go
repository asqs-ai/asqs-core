package dotnetproj

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Facts is what a .csproj really declares once MSBuild's file-level inheritance is accounted for.
//
// Reading the .csproj alone was wrong for any repository that factors shared properties out, which
// is the recommended layout: the validation fixture's Directory.Build.props carries LangVersion,
// Nullable and ImplicitUsings that no consumer ever saw, and a repo that puts TargetFramework there
// too (the common case once projects multi-target) read as "declares no TFM" — the generated test
// project fell back to net8.0, the eval image fell back to the default tag, and the multi-target pin
// had nothing to pin.
//
// This is file-level evaluation, not MSBuild: only unconditional PropertyGroups are read, because a
// conditioned one depends on properties that are not known without running MSBuild, and guessing
// which branch it takes would produce a confident wrong answer instead of an honest blank.
type Facts struct {
	// ProjectPath is the .csproj these facts describe (absolute).
	ProjectPath string
	// TFMs are the concrete target framework monikers, in document order. Empty when nothing in the
	// project or its ancestors declares one.
	TFMs []string
	// LangVersion, Nullable: the property values as written ("12.0", "latest", "enable").
	LangVersion string
	Nullable    string
	// ImplicitUsings is true only for an explicit enable. C# test generation reads it to avoid
	// emitting `using System;` into a project that already has it.
	ImplicitUsings bool
	// SDK is the project's SDK identifier, IsWebSDK its ASP.NET Core specialisation.
	SDK        string
	IsWebSDK   bool
	IsSDKStyle bool
	// PropsPaths lists the Directory.Build.props / .targets files that contributed, nearest last.
	PropsPaths []string
	// CentralPackageManagement is true when a Directory.Packages.props opts in.
	CentralPackageManagement bool
	// CPMPath is that file, when found.
	CPMPath string

	packages map[string]string // lower-cased package id -> version ("" = referenced, version unknown)
}

var (
	rePropertyGroup   = regexp.MustCompile(`(?is)<PropertyGroup([^>]*)>(.*?)</PropertyGroup>`)
	reItemGroupBlock  = regexp.MustCompile(`(?is)<ItemGroup([^>]*)>(.*?)</ItemGroup>`)
	reConditionAttr   = regexp.MustCompile(`(?i)\bCondition\s*=`)
	rePackageRefOpen  = regexp.MustCompile(`(?is)<PackageReference\b([^>]*?)(/?)>`)
	rePackageVersion  = regexp.MustCompile(`(?is)<PackageVersion\b([^>]*?)/?>`)
	reAttrInclude     = regexp.MustCompile(`(?i)\bInclude\s*=\s*["']([^"']+)["']`)
	reAttrUpdate      = regexp.MustCompile(`(?i)\bUpdate\s*=\s*["']([^"']+)["']`)
	reAttrVersion     = regexp.MustCompile(`(?i)\bVersion\s*=\s*["']([^"']+)["']`)
	reChildVersion    = regexp.MustCompile(`(?is)<Version>\s*([^<]*?)\s*</Version>`)
	reManageCentrally = regexp.MustCompile(`(?is)<ManagePackageVersionsCentrally>\s*([^<]*?)\s*</ManagePackageVersionsCentrally>`)
	reNullable        = regexp.MustCompile(`(?i)<Nullable>\s*([^<]*?)\s*</Nullable>`)
	reImplicitUsings  = regexp.MustCompile(`(?i)<ImplicitUsings>\s*([^<]*?)\s*</ImplicitUsings>`)
)

// maxPropsWalkDepth bounds the ancestor walk. MSBuild itself stops at the first Directory.Build.props
// unless one imports its parent; ASQS reads every ancestor inside the repo instead, because the
// import is the common convention and missing an inherited TFM is the failure this exists to prevent.
const maxPropsWalkDepth = 24

// ResolveFacts reads csprojAbs plus every Directory.Build.props / Directory.Build.targets between it
// and repoRoot (exclusive of anything above repoRoot — a props file outside the checkout belongs to
// somebody else), and returns the effective unconditional facts. Nearer files win.
func ResolveFacts(repoRoot, csprojAbs string) (Facts, error) {
	csprojAbs = filepath.Clean(strings.TrimSpace(csprojAbs))
	body, err := os.ReadFile(csprojAbs)
	if err != nil {
		return Facts{}, fmt.Errorf("dotnetproj: read %s: %w", csprojAbs, err)
	}
	projectXML := StripXMLComments(string(body))

	f := Facts{
		ProjectPath: csprojAbs,
		SDK:         SDKName(projectXML),
		IsWebSDK:    IsWebSDK(projectXML),
		IsSDKStyle:  IsSDKStyle(projectXML),
		packages:    map[string]string{},
	}

	// Ancestors first (farthest to nearest), then the project itself, so the nearest declaration wins.
	for _, path := range ancestorPropsFiles(repoRoot, csprojAbs) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		f.PropsPaths = append(f.PropsPaths, path)
		f.applyProperties(StripXMLComments(string(b)))
	}
	f.applyProperties(projectXML)

	f.readPackageReferences(projectXML)
	f.resolveCentralPackageVersions(repoRoot, csprojAbs)
	return f, nil
}

// applyProperties overlays one file's unconditional property values onto the facts.
func (f *Facts) applyProperties(xml string) {
	for _, g := range unconditionalPropertyGroups(xml) {
		if tfms := ParseTargetFrameworkMonikers(g); len(tfms) > 0 {
			f.TFMs = tfms
		}
		if v := ParseLangVersion(g); v != "" {
			f.LangVersion = v
		}
		if m := reNullable.FindStringSubmatch(g); len(m) > 1 {
			if v := strings.TrimSpace(m[1]); v != "" && !strings.HasPrefix(v, "$(") {
				f.Nullable = v
			}
		}
		if m := reImplicitUsings.FindStringSubmatch(g); len(m) > 1 {
			v := strings.ToLower(strings.TrimSpace(m[1]))
			if v != "" && !strings.HasPrefix(v, "$(") {
				f.ImplicitUsings = v == "enable" || v == "true"
			}
		}
	}
}

// unconditionalPropertyGroups returns the bodies of PropertyGroups with no Condition attribute.
func unconditionalPropertyGroups(xml string) []string {
	var out []string
	for _, m := range rePropertyGroup.FindAllStringSubmatch(xml, -1) {
		if reConditionAttr.MatchString(m[1]) {
			continue
		}
		out = append(out, m[2])
	}
	return out
}

// ancestorPropsFiles lists Directory.Build.props / .targets from repoRoot down to the project's own
// directory, farthest first. A project outside repoRoot yields nothing.
func ancestorPropsFiles(repoRoot, csprojAbs string) []string {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	dir := filepath.Dir(csprojAbs)
	var dirs []string
	for i := 0; i < maxPropsWalkDepth; i++ {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			break // above the checkout
		}
		dirs = append(dirs, dir)
		if rel == "." {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	var out []string
	for i := len(dirs) - 1; i >= 0; i-- { // farthest first
		for _, name := range []string{"Directory.Build.props", "Directory.Build.targets"} {
			p := filepath.Join(dirs[i], name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				out = append(out, p)
			}
		}
	}
	return out
}

// readPackageReferences records every PackageReference with the version it declares inline, whether
// as an attribute or a child element. A reference with neither is recorded with an empty version:
// the package IS referenced, and a caller asking "does this project use coverlet" must get yes.
func (f *Facts) readPackageReferences(xml string) {
	for _, g := range reItemGroupBlock.FindAllStringSubmatch(xml, -1) {
		body := g[2]
		locs := rePackageRefOpen.FindAllStringSubmatchIndex(body, -1)
		for i, loc := range locs {
			attrs := body[loc[2]:loc[3]]
			id := packageIDFromAttrs(attrs)
			if id == "" {
				continue
			}
			version := ""
			if m := reAttrVersion.FindStringSubmatch(attrs); len(m) > 1 {
				version = strings.TrimSpace(m[1])
			} else if body[loc[4]:loc[5]] != "/" {
				// Element form: <PackageReference Include="x"><Version>1.2.3</Version></PackageReference>.
				// Scan up to the next PackageReference so a sibling's child cannot be misread as this one's.
				end := len(body)
				if i+1 < len(locs) {
					end = locs[i+1][0]
				}
				if m := reChildVersion.FindStringSubmatch(body[loc[1]:end]); len(m) > 1 {
					version = strings.TrimSpace(m[1])
				}
			}
			key := strings.ToLower(id)
			if prev, ok := f.packages[key]; !ok || (prev == "" && version != "") {
				f.packages[key] = version
			}
		}
	}
}

func packageIDFromAttrs(attrs string) string {
	if m := reAttrInclude.FindStringSubmatch(attrs); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	// <PackageReference Update="x" Version="…"/> pins a version without adding a reference; it is
	// still a version fact for a package the SDK brought in implicitly.
	if m := reAttrUpdate.FindStringSubmatch(attrs); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// resolveCentralPackageVersions fills in versions for versionless references from the nearest
// Directory.Packages.props. Central Package Management is the reason a modern .csproj can carry
// `<PackageReference Include="xunit" />` with no version anywhere in the file.
func (f *Facts) resolveCentralPackageVersions(repoRoot, csprojAbs string) {
	path := findCentralPackagesProps(repoRoot, csprojAbs)
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	xml := StripXMLComments(string(b))
	f.CPMPath = path
	if m := reManageCentrally.FindStringSubmatch(xml); len(m) > 1 {
		f.CentralPackageManagement = strings.EqualFold(strings.TrimSpace(m[1]), "true")
	}
	for _, m := range rePackageVersion.FindAllStringSubmatch(xml, -1) {
		id := packageIDFromAttrs(m[1])
		if id == "" {
			continue
		}
		v := ""
		if vm := reAttrVersion.FindStringSubmatch(m[1]); len(vm) > 1 {
			v = strings.TrimSpace(vm[1])
		}
		key := strings.ToLower(id)
		if cur, referenced := f.packages[key]; referenced && cur == "" {
			f.packages[key] = v
		}
	}
}

// findCentralPackagesProps walks from the project's directory up to repoRoot looking for
// Directory.Packages.props, nearest first.
func findCentralPackagesProps(repoRoot, csprojAbs string) string {
	root := filepath.Clean(strings.TrimSpace(repoRoot))
	dir := filepath.Dir(csprojAbs)
	for i := 0; i < maxPropsWalkDepth; i++ {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ""
		}
		p := filepath.Join(dir, "Directory.Packages.props")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
		if rel == "." {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// PackageVersion returns the version this project resolves for a package id, and whether the project
// references it at all. A referenced package with no resolvable version yields ("", true).
func (f Facts) PackageVersion(id string) (string, bool) {
	v, ok := f.packages[strings.ToLower(strings.TrimSpace(id))]
	return v, ok
}

// ReferencesPackage is the gate predicate: does this project reference the package at all?
func (f Facts) ReferencesPackage(id string) bool {
	_, ok := f.packages[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

// ReferencesAnyPackage reports whether any of the ids is referenced, for gates like "coverlet is
// available under either of its two package names".
func (f Facts) ReferencesAnyPackage(ids ...string) (string, bool) {
	for _, id := range ids {
		if f.ReferencesPackage(id) {
			return id, true
		}
	}
	return "", false
}

// PackageIDs returns every referenced package id, lower-cased, for rendering a dependency block.
func (f Facts) PackageIDs() []string {
	out := make([]string, 0, len(f.packages))
	for id := range f.packages {
		out = append(out, id)
	}
	return out
}

// MaxNetMajor returns the highest netX.Y major among the TFMs, or 0 when none is a .NET (Core) one.
// netstandard and netcoreapp contribute nothing: neither names an SDK major.
func (f Facts) MaxNetMajor() int {
	max := 0
	for _, tfm := range f.TFMs {
		if n := NetMajorFromTFM(tfm); n > max {
			max = n
		}
	}
	return max
}

// PrimaryTFM is the moniker a single-target consumer should use: the highest netX.0, else the first
// declared moniker, else "".
func (f Facts) PrimaryTFM() string {
	best, bestMajor := "", 0
	for _, tfm := range f.TFMs {
		if n := NetMajorFromTFM(tfm); n > bestMajor {
			best, bestMajor = tfm, n
		}
	}
	if best != "" {
		return best
	}
	if len(f.TFMs) > 0 {
		return f.TFMs[0]
	}
	return ""
}

// NetMajorFromTFM returns the major version of a netX.Y moniker (net8.0 -> 8, net8.0-windows -> 8),
// or 0 for netstandard / netcoreapp / net48 and anything unrecognised.
func NetMajorFromTFM(tfm string) int {
	low := strings.ToLower(strings.TrimSpace(tfm))
	if !strings.HasPrefix(low, "net") || strings.HasPrefix(low, "netstandard") || strings.HasPrefix(low, "netcoreapp") {
		return 0
	}
	rest := strings.TrimPrefix(low, "net")
	i := strings.IndexByte(rest, '.')
	if i <= 0 {
		return 0 // net48 and friends carry no dot: .NET Framework, not a Core major
	}
	n, err := strconv.Atoi(rest[:i])
	if err != nil {
		return 0
	}
	return n
}
