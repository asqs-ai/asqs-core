package indexer

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Manifest parsing and per-ecosystem doc extraction for B55. Everything here is pure file reading
// against the caches the project's own build populated; a dependency whose artifact is not on disk
// is SKIPPED and counted, never fetched.

// ---- Maven ----

type mavenCoord struct{ group, artifact, version string }

func (c mavenCoord) coordinate() string { return c.group + ":" + c.artifact + ":" + c.version }

var (
	pomDepRe  = regexp.MustCompile(`(?s)<dependency>(.*?)</dependency>`)
	pomTagRe  = regexp.MustCompile(`<(groupId|artifactId|version|scope)>\s*([^<]+?)\s*</`)
	pomPropRe = regexp.MustCompile(`(?s)<properties>(.*?)</properties>`)
	propTagRe = regexp.MustCompile(`<([\w.-]+)>\s*([^<]+?)\s*</`)
)

// parsePomDirectDeps reads DIRECT dependencies from the repository's pom.xml files (root plus one
// directory level, covering the common parent/module split). Versions written as ${property} are
// resolved from <properties>; a version that stays unresolved (dependencyManagement in a parent
// outside the repo, BOM imports) skips the dependency rather than guessing — a wrong-version doc
// chunk is worse than none, because it is exact-looking and stale.
func parsePomDirectDeps(repoPath string) []mavenCoord {
	var poms []string
	if p := filepath.Join(repoPath, "pom.xml"); fileExists(p) {
		poms = append(poms, p)
	}
	if entries, err := os.ReadDir(repoPath); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				if p := filepath.Join(repoPath, e.Name(), "pom.xml"); fileExists(p) {
					poms = append(poms, p)
				}
			}
		}
	}
	seen := map[string]bool{}
	var out []mavenCoord
	for _, pom := range poms {
		b, err := os.ReadFile(pom)
		if err != nil {
			continue
		}
		src := string(b)
		props := map[string]string{}
		if m := pomPropRe.FindStringSubmatch(src); m != nil {
			for _, kv := range propTagRe.FindAllStringSubmatch(m[1], -1) {
				props[kv[1]] = kv[2]
			}
		}
		for _, dep := range pomDepRe.FindAllStringSubmatch(src, -1) {
			c := mavenCoord{}
			scope := ""
			for _, kv := range pomTagRe.FindAllStringSubmatch(dep[1], -1) {
				switch kv[1] {
				case "groupId":
					c.group = kv[2]
				case "artifactId":
					c.artifact = kv[2]
				case "version":
					c.version = kv[2]
				case "scope":
					scope = kv[2]
				}
			}
			if strings.HasPrefix(c.version, "${") {
				name := strings.TrimSuffix(strings.TrimPrefix(c.version, "${"), "}")
				c.version = props[name]
			}
			if c.group == "" || c.artifact == "" || c.version == "" || strings.HasPrefix(c.version, "${") {
				continue
			}
			if scope == "system" || scope == "import" {
				continue
			}
			key := c.coordinate()
			if !seen[key] {
				seen[key] = true
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].coordinate() < out[j].coordinate() })
	return out
}

// javaDocsForCoord extracts per-type documentation from the dependency's -sources.jar in the local
// Maven repository. One depDoc per public top-level type: the type's javadoc summary plus its
// public/protected member signatures — signatures are the payload; the CS0122/Pageable failure
// class needs "what is the real signature", not prose.
func javaDocsForCoord(c mavenCoord, m2root string) []depDoc {
	if m2root == "" {
		return nil
	}
	jar := filepath.Join(append(append([]string{m2root}, strings.Split(c.group, ".")...),
		c.artifact, c.version, c.artifact+"-"+c.version+"-sources.jar")...)
	zr, err := zip.OpenReader(jar)
	if err != nil {
		return nil
	}
	defer func() { _ = zr.Close() }()

	var out []depDoc
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".java") ||
			strings.HasSuffix(f.Name, "package-info.java") || strings.HasSuffix(f.Name, "module-info.java") ||
			f.UncompressedSize64 > 512*1024 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		src, err := io.ReadAll(io.LimitReader(rc, 512*1024))
		_ = rc.Close()
		if err != nil {
			continue
		}
		if doc, ok := javaTypeDoc(string(src)); ok {
			doc.Coordinate = c.coordinate()
			doc.Source = "maven"
			out = append(out, doc)
		}
	}
	return out
}

var (
	javaPackageRe = regexp.MustCompile(`(?m)^\s*package\s+([\w.]+)\s*;`)
	javaTypeRe    = regexp.MustCompile(`(?m)^\s*public\s+(?:final\s+|abstract\s+|sealed\s+)*(?:class|interface|enum|record|@interface)\s+(\w+)`)
	javaMemberRe  = regexp.MustCompile(`(?m)^\s{1,8}(public|protected)\s+[^;{=]{0,200}?[;{]`)
	javadocRe     = regexp.MustCompile(`(?s)/\*\*(.*?)\*/`)
)

// javaTypeDoc renders one source file's public API. A scanner, not a parser, on purpose: the input
// is machine-published library source, overwhelmingly conventional in layout, and a javac-grade
// parse buys marginal recall for a large dependency.
func javaTypeDoc(src string) (depDoc, bool) {
	tm := javaTypeRe.FindStringSubmatchIndex(src)
	if tm == nil {
		return depDoc{}, false
	}
	typeName := src[tm[2]:tm[3]]
	pkg := ""
	if m := javaPackageRe.FindStringSubmatch(src); m != nil {
		pkg = m[1]
	}
	fq := typeName
	if pkg != "" {
		fq = pkg + "." + typeName
	}
	var b strings.Builder
	b.WriteString(fq + "\n")
	// Type javadoc: the last /** … */ that ends before the type declaration begins.
	for _, d := range javadocRe.FindAllStringSubmatchIndex(src[:tm[0]], -1) {
		_ = d
	}
	if docs := javadocRe.FindAllStringSubmatch(src[:tm[0]], -1); len(docs) > 0 {
		if sum := javadocSummary(docs[len(docs)-1][1]); sum != "" {
			b.WriteString(sum + "\n")
		}
	}
	b.WriteString("\nPublic API:\n")
	members := javaMemberRe.FindAllString(src[tm[1]:], -1)
	const maxMembers = 30
	n := 0
	for _, m := range members {
		sig := strings.Join(strings.Fields(strings.TrimRight(strings.TrimSpace(m), ";{")), " ")
		if sig == "" || strings.HasPrefix(sig, "public class") || strings.HasPrefix(sig, "public interface") {
			continue
		}
		b.WriteString("  " + sig + "\n")
		n++
		if n >= maxMembers {
			b.WriteString("  … (truncated)\n")
			break
		}
	}
	if n == 0 {
		return depDoc{}, false
	}
	return depDoc{FQName: fq, Content: b.String()}, true
}

// javadocSummary reduces a javadoc body to its first sentence, tags stripped.
func javadocSummary(body string) string {
	lines := strings.Split(body, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "*"))
	}
	text := strings.TrimSpace(strings.Join(lines, " "))
	text = regexp.MustCompile(`<[^>]{1,40}>`).ReplaceAllString(text, "")
	text = regexp.MustCompile(`\{@\w+\s+([^}]*)\}`).ReplaceAllString(text, "$1")
	if i := strings.Index(text, ". "); i > 0 {
		text = text[:i+1]
	}
	if at := strings.Index(text, "@"); at == 0 {
		return ""
	}
	const cap = 400
	if r := []rune(text); len(r) > cap {
		text = string(r[:cap])
	}
	return strings.TrimSpace(text)
}

// ---- NuGet ----

type nugetRef struct{ id, version string }

var csprojPkgRe = regexp.MustCompile(`<PackageReference\s+Include="([^"]+)"\s+Version="([^"]+)"`)

func parseCsprojPackageRefs(repoPath string) []nugetRef {
	seen := map[string]bool{}
	var out []nugetRef
	_ = filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == "bin" || name == "obj" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".csproj") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range csprojPkgRe.FindAllStringSubmatch(string(b), -1) {
			key := m[1] + "@" + m[2]
			if !seen[key] {
				seen[key] = true
				out = append(out, nugetRef{id: m[1], version: m[2]})
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// nugetXMLDoc is the schema NuGet documentation files share: /doc/members/member.
type nugetXMLDoc struct {
	Members []struct {
		Name    string `xml:"name,attr"`
		Summary string `xml:"summary"`
	} `xml:"members>member"`
}

// csharpDocsForPackage reads the XML documentation file shipped inside the package's lib folder
// and renders one depDoc per TYPE: the type summary plus its members' names and summaries.
func csharpDocsForPackage(ref nugetRef, nugetRoot string) []depDoc {
	if nugetRoot == "" {
		return nil
	}
	libDir := filepath.Join(nugetRoot, strings.ToLower(ref.id), strings.ToLower(ref.version), "lib")
	var xmlPath string
	_ = filepath.WalkDir(libDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || xmlPath != "" {
			return nil
		}
		if strings.EqualFold(filepath.Base(path), ref.id+".xml") {
			xmlPath = path
		}
		return nil
	})
	if xmlPath == "" {
		return nil
	}
	b, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil
	}
	var doc nugetXMLDoc
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil
	}
	byType := map[string]*strings.Builder{}
	var order []string
	clean := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for _, m := range doc.Members {
		kind, name, ok := strings.Cut(m.Name, ":")
		if !ok {
			continue
		}
		switch kind {
		case "T":
			if byType[name] == nil {
				byType[name] = &strings.Builder{}
				order = append(order, name)
			}
			byType[name].WriteString(name + "\n" + clean(m.Summary) + "\n\nMembers:\n")
		case "M", "P", "F", "E":
			typeName := name
			if i := strings.Index(name, "("); i > 0 {
				typeName = name[:i]
			}
			if i := strings.LastIndex(typeName, "."); i > 0 {
				typeName = typeName[:i]
			}
			if tb := byType[typeName]; tb != nil {
				tb.WriteString("  " + kind + ": " + name + " — " + clean(m.Summary) + "\n")
			}
		}
	}
	coord := ref.id + "@" + ref.version
	out := make([]depDoc, 0, len(order))
	for _, t := range order {
		out = append(out, depDoc{Coordinate: coord, Source: "nuget", FQName: t, Content: byType[t].String()})
	}
	return out
}

// ---- npm ----

func parsePackageJSONDeps(repoPath string) []string {
	b, err := os.ReadFile(filepath.Join(repoPath, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return nil
	}
	out := make([]string, 0, len(pkg.Dependencies))
	for name := range pkg.Dependencies {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// tsDocsForPackage reads the package's .d.ts declarations — the API surface itself. One depDoc per
// declaration file, capped: .d.ts is already the distilled form, and splitting it per symbol
// multiplies chunks for no retrieval gain.
func tsDocsForPackage(repoPath, name string) (coordinate string, docs []depDoc) {
	root := filepath.Join(repoPath, "node_modules", filepath.FromSlash(name))
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return "", nil
	}
	var pkg struct {
		Version string `json:"version"`
		Types   string `json:"types"`
		Typings string `json:"typings"`
	}
	if json.Unmarshal(b, &pkg) != nil || pkg.Version == "" {
		return "", nil
	}
	coordinate = name + "@" + pkg.Version

	const maxFiles = 8
	const maxRunesPerFile = 8000
	var files []string
	entry := pkg.Types
	if entry == "" {
		entry = pkg.Typings
	}
	if entry != "" && fileExists(filepath.Join(root, filepath.FromSlash(entry))) {
		files = append(files, filepath.Join(root, filepath.FromSlash(entry)))
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(files) >= maxFiles {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".d.ts") && path != "" && !containsString(files, path) {
			files = append(files, path)
		}
		return nil
	})
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		content := string(b)
		if r := []rune(content); len(r) > maxRunesPerFile {
			content = string(r[:maxRunesPerFile]) + "\n… (truncated)"
		}
		rel, _ := filepath.Rel(root, f)
		docs = append(docs, depDoc{
			Coordinate: coordinate,
			Source:     "npm",
			FQName:     name + "/" + filepath.ToSlash(rel),
			Content:    name + "@" + pkg.Version + " — " + filepath.ToSlash(rel) + "\n\n" + content,
		})
	}
	return coordinate, docs
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
