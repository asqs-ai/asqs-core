package evaluator

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Scoped-compile NuGet helpers.
//
// The scoped-compile fallback (see csharp_scoped_compile.go) retries `dotnet build <csproj>` after an
// initial sln-level compile fails with a NuGet restore error (NU1301 etc.) on a sibling project. In
// practice this retry is almost always defeated by a *shared* repo-level `nuget.config`: even when we
// scope the build to a single project, `dotnet restore` still walks every source configured in the
// effective NuGet config chain, including the failing private feed, and surfaces the same NU1301.
//
// The functions here discover the repo's configured NuGet sources, extract the failing feed URLs from
// the compile output, and compose a `/p:RestoreSources="..."` argument that restricts the scoped retry
// to sources that are known to be reachable. They also walk transitive <ProjectReference> edges so the
// audit can show whether the scoped target's graph still includes a failing sibling (in which case
// feed exclusion alone can't rescue the build and the operator needs to supply credentials).

// failingNuGetFeedURLs extracts NuGet feed URLs that appear in NU1301 / "Unable to load the service
// index" lines in the compile output. The set is normalised (lowercased, trailing dot/slash stripped)
// so callers can diff it against sources parsed from nuget.config files.
func failingNuGetFeedURLs(output string) map[string]struct{} {
	out := map[string]struct{}{}
	if strings.TrimSpace(output) == "" {
		return out
	}
	for _, m := range reNuGetServiceIndex.FindAllStringSubmatch(output, -1) {
		if len(m) >= 3 {
			u := normaliseFeedURL(m[2])
			if u != "" {
				out[u] = struct{}{}
			}
		}
	}
	for _, m := range reNuGetServiceIndexGeneric.FindAllStringSubmatch(output, -1) {
		if len(m) >= 2 {
			u := normaliseFeedURL(m[1])
			if u != "" {
				out[u] = struct{}{}
			}
		}
	}
	return out
}

// normaliseFeedURL lowercases and trims trailing punctuation from a feed URL so two different string
// renderings of the same source compare equal. It deliberately preserves the scheme and path so hosts
// with the same name but different paths (e.g. two Azure DevOps organisations) don't collide.
func normaliseFeedURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, ".,;)\" ")
	raw = strings.TrimRight(raw, "/")
	return strings.ToLower(raw)
}

// findNuGetConfigFiles returns every nuget.config encountered while walking up from the target csproj
// to the repo root (case-insensitive file-name match). Files are returned in lookup order — nearest
// first — mirroring MSBuild's effective-config resolution. Missing parents are tolerated (the walk
// simply skips them). Absolute paths are returned so the caller can read them directly.
func findNuGetConfigFiles(repoAbs, csprojRel string) []string {
	repoAbs = strings.TrimRight(filepath.Clean(repoAbs), string(os.PathSeparator))
	if repoAbs == "" {
		return nil
	}
	var out []string
	start := filepath.Dir(filepath.Join(repoAbs, filepath.FromSlash(strings.TrimSpace(csprojRel))))
	if strings.TrimSpace(csprojRel) == "" {
		start = repoAbs
	}
	dir := start
	for {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if strings.EqualFold(e.Name(), "nuget.config") {
					out = append(out, filepath.Join(dir, e.Name()))
				}
			}
		}
		if dir == repoAbs {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if !strings.HasPrefix(parent, repoAbs) && parent != repoAbs {
			break
		}
		dir = parent
	}
	return out
}

// nuGetConfigSources returns the absolute/URL values of every `<packageSources><add value="..."/>`
// entry in the given nuget.config. `<clear/>` is respected so sources defined above a `<clear/>` are
// dropped, matching NuGet's real evaluation order. Relative filesystem paths are left as-is (MSBuild
// resolves them relative to the config file); callers that need fully-qualified absolute paths can
// resolve them against filepath.Dir(configPath) themselves.
func nuGetConfigSources(configPath string) ([]string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	type addElem struct {
		XMLName xml.Name `xml:"add"`
		Key     string   `xml:"key,attr"`
		Value   string   `xml:"value,attr"`
	}
	type clearElem struct {
		XMLName xml.Name `xml:"clear"`
	}
	type packageSources struct {
		XMLName xml.Name      `xml:"packageSources"`
		Clears  []clearElem   `xml:"clear"`
		Adds    []addElem     `xml:"add"`
		Raw     []interface{} `xml:",any"`
	}
	type configuration struct {
		XMLName        xml.Name       `xml:"configuration"`
		PackageSources packageSources `xml:"packageSources"`
	}
	var cfg configuration
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// We can't preserve child ordering with the simple struct above, so reparse via a token decoder
	// only when <clear/> is present (the common case has none and we can fast-path).
	if len(cfg.PackageSources.Clears) == 0 {
		out := make([]string, 0, len(cfg.PackageSources.Adds))
		for _, a := range cfg.PackageSources.Adds {
			if v := strings.TrimSpace(a.Value); v != "" {
				out = append(out, v)
			}
		}
		return out, nil
	}
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	inPackageSources := false
	var out []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if strings.EqualFold(t.Name.Local, "packageSources") {
				inPackageSources = true
				continue
			}
			if !inPackageSources {
				continue
			}
			if strings.EqualFold(t.Name.Local, "clear") {
				out = nil
				continue
			}
			if strings.EqualFold(t.Name.Local, "add") {
				for _, a := range t.Attr {
					if strings.EqualFold(a.Name.Local, "value") {
						if v := strings.TrimSpace(a.Value); v != "" {
							out = append(out, v)
						}
					}
				}
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "packageSources") {
				inPackageSources = false
			}
		}
	}
	return out, nil
}

// discoverRepoNuGetSources returns the de-duplicated union of all sources parsed from every
// nuget.config in the effective chain for the given scope target. Errors on individual files are
// tolerated (we return what we could parse) so a malformed config deep in the tree doesn't defeat the
// fallback entirely.
func discoverRepoNuGetSources(repoAbs, csprojRel string) []string {
	configs := findNuGetConfigFiles(repoAbs, csprojRel)
	seen := map[string]struct{}{}
	var out []string
	for _, c := range configs {
		srcs, err := nuGetConfigSources(c)
		if err != nil {
			continue
		}
		for _, s := range srcs {
			key := normaliseFeedURL(s)
			if key == "" {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// passingNuGetSources returns the sources from discoverRepoNuGetSources that are NOT in failingURLs.
// When every configured source is failing (or no config was found), it falls back to the public
// nuget.org feed so the scoped retry at least has a chance of resolving common test packages.
func passingNuGetSources(repoAbs, csprojRel string, failingURLs map[string]struct{}) []string {
	all := discoverRepoNuGetSources(repoAbs, csprojRel)
	var out []string
	for _, s := range all {
		if _, bad := failingURLs[normaliseFeedURL(s)]; bad {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return []string{"https://api.nuget.org/v3/index.json"}
	}
	return out
}

// walkTransitiveProjectRefs returns the set of repo-relative, normalised .csproj paths that are
// transitively referenced by the given csproj (including the csproj itself). The walk is bounded by
// maxDepth to guard against reference cycles in malformed project files. Errors reading any single
// csproj are tolerated.
func walkTransitiveProjectRefs(repoAbs, csprojRel string, maxDepth int) map[string]struct{} {
	out := map[string]struct{}{}
	if strings.TrimSpace(csprojRel) == "" || strings.TrimSpace(repoAbs) == "" {
		return out
	}
	if maxDepth <= 0 {
		maxDepth = 32
	}
	type frame struct {
		rel   string
		depth int
	}
	queue := []frame{{rel: filepath.ToSlash(strings.TrimSpace(csprojRel)), depth: 0}}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		key := normRepoRel(f.rel)
		if _, done := out[key]; done {
			continue
		}
		out[key] = struct{}{}
		if f.depth >= maxDepth {
			continue
		}
		abs := filepath.Join(repoAbs, filepath.FromSlash(f.rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		for _, ref := range extractProjectReferenceIncludes(string(data)) {
			resolved := resolveProjectRefPath(f.rel, ref)
			if resolved == "" {
				continue
			}
			queue = append(queue, frame{rel: resolved, depth: f.depth + 1})
		}
	}
	return out
}

// extractProjectReferenceIncludes parses `<ProjectReference Include="..."/>` entries out of a csproj
// body. We use a small XML tokeniser instead of a regex so attribute order and whitespace variations
// don't trip us up.
func extractProjectReferenceIncludes(csprojBody string) []string {
	var out []string
	dec := xml.NewDecoder(strings.NewReader(csprojBody))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !strings.EqualFold(se.Name.Local, "ProjectReference") {
			continue
		}
		for _, a := range se.Attr {
			if strings.EqualFold(a.Name.Local, "Include") {
				v := strings.TrimSpace(a.Value)
				if v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// resolveProjectRefPath resolves a `<ProjectReference Include="...">` value against the owning
// csproj's directory and returns a repo-relative, forward-slash path. Windows-style separators are
// normalised. Returns "" on any error so callers can tolerate malformed references.
func resolveProjectRefPath(ownerCsprojRel, includeValue string) string {
	owner := filepath.ToSlash(strings.TrimSpace(ownerCsprojRel))
	inc := strings.ReplaceAll(strings.TrimSpace(includeValue), "\\", "/")
	if owner == "" || inc == "" {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(inc), ".csproj") {
		return ""
	}
	joined := filepath.ToSlash(filepath.Join(filepath.Dir(owner), inc))
	return strings.TrimPrefix(joined, "./")
}

// sortedSourceList returns a deterministic-order copy of sources for audit emission.
func sortedSourceList(sources []string) []string {
	out := make([]string, len(sources))
	copy(out, sources)
	sort.Strings(out)
	return out
}
