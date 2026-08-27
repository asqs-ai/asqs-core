package apisurface

import (
	"context"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CSharpProvider resolves API surfaces for .NET projects from the XML documentation files NuGet
// packages ship beside their assemblies.
//
// Approach note, because this is not the obvious one. Member lists for .NET live in assembly
// metadata, and reading that properly means a MetadataLoadContext inside a .NET process — which for
// this repo would mean a new mode on tools/csharp-indexer plus a Go bridge. That path also inherits
// a deployment dependency the API surface has no reason to carry: the indexer only runs when
// indexer.csharp.dll_path points at a published DLL, and in Docker deployments it runs in a
// container. An API-surface block that silently disappears when the indexer is unconfigured is
// worse than one with a narrower source.
//
// The XML doc file is a better source for this purpose. NuGet ships it in the package next to the
// DLL, `dotnet restore` puts it in the global packages folder, and every member is listed with a
// full ECMA-334 documentation ID that encodes the declaring type, the member name and every
// parameter type:
//
//	M:Microsoft.Playwright.ILocatorAssertions.ToContainTextAsync(System.String,Microsoft.Playwright.LocatorAssertionsToContainTextOptions)
//
// That is exactly the fact the model gets wrong, in a plain-text file, with no toolchain, no
// subprocess and no container — the same shape as NodeProvider reading .d.ts.
//
// The honest limit: a package that does not ship XML docs resolves to nothing. Every assertion
// library we target does ship them (Microsoft.Playwright lists 40 members for ILocatorAssertions),
// and a miss is audited rather than fatal, so the failure mode is "no block" — the behaviour before
// any of this existed.
type CSharpProvider struct {
	mu sync.Mutex
	// cache maps docFilePath -> declaring type -> member declarations.
	cache map[string]map[string][]string
	// nugetRoot overrides the global packages folder; empty means the default resolution.
	nugetRoot string
}

func NewCSharpProvider() *CSharpProvider {
	return &CSharpProvider{cache: map[string]map[string][]string{}}
}

// nugetPackagesRoot returns the global packages folder, honouring NUGET_PACKAGES.
func (p *CSharpProvider) nugetPackagesRoot() string {
	if strings.TrimSpace(p.nugetRoot) != "" {
		return p.nugetRoot
	}
	if v := strings.TrimSpace(os.Getenv("NUGET_PACKAGES")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".nuget", "packages")
}

// maxDocFileScan bounds how many XML files one lookup will read. A repo's bin/ tree plus the global
// package cache can hold thousands; the assertion docs are found in the first handful, and an
// unbounded walk would turn a prompt-building step into a disk crawl.
const maxDocFileScan = 64

// Lookup implements Provider.
func (p *CSharpProvider) Lookup(ctx context.Context, repoPath string, targets []Target) ([]TypeSurface, error) {
	if strings.TrimSpace(repoPath) == "" || len(targets) == 0 {
		return nil, nil
	}
	docs := p.candidateDocFiles(ctx, repoPath, targets)
	if len(docs) == 0 {
		return nil, fmt.Errorf("apisurface: no NuGet XML documentation found for %s (searched the build output under %s and the packages folder %s)",
			describeTargetNames(targets), repoPath, p.nugetPackagesRoot())
	}

	var out []TypeSurface
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		members, origin := p.membersOf(ctx, docs, t.Name)
		if len(members) == 0 {
			continue
		}
		out = append(out, NewTypeSurface(t.Name, members, t.Member, origin))
	}
	return out, nil
}

func describeTargetNames(targets []Target) string {
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

func (p *CSharpProvider) membersOf(ctx context.Context, docs []string, typeName string) ([]string, string) {
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, ""
		}
		byType, err := p.parseDocFile(doc)
		if err != nil {
			continue
		}
		if members, ok := byType[typeName]; ok && len(members) > 0 {
			return members, filepath.Base(doc)
		}
	}
	return nil, ""
}

// candidateDocFiles returns XML doc files worth reading, nearest-first: the repo's own build output
// (which carries the exact versions the project restored) ahead of the global packages folder.
//
// Files are filtered by assembly name against the targets' namespaces, so resolving
// Microsoft.Playwright.ILocatorAssertions does not read every XML file in a large package cache.
func (p *CSharpProvider) candidateDocFiles(ctx context.Context, repoPath string, targets []Target) []string {
	wanted := map[string]bool{}
	for _, t := range targets {
		// Microsoft.Playwright.ILocatorAssertions -> the doc file is Microsoft.Playwright.xml, but
		// an assembly may be any prefix of the namespace, so match on prefix rather than equality.
		if i := strings.LastIndex(t.Name, "."); i > 0 {
			wanted[strings.ToLower(t.Name[:i])] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	add := func(path string) bool {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(path), ".xml"))
		match := false
		for ns := range wanted {
			if ns == base || strings.HasPrefix(ns, base+".") || strings.HasPrefix(base, ns) {
				match = true
				break
			}
		}
		if !match || seen[path] {
			return false
		}
		seen[path] = true
		out = append(out, path)
		return len(out) >= maxDocFileScan
	}

	// 1. Build output under the repo.
	_ = filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return fs.SkipAll
		}
		if d.IsDir() {
			switch strings.ToLower(d.Name()) {
			case "node_modules", ".git", "obj":
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".xml") && add(path) {
			return fs.SkipAll
		}
		return nil
	})
	if len(out) >= maxDocFileScan {
		return out
	}

	// 2. Global packages folder, restricted to the package directories the namespaces name so the
	// walk cannot wander the whole cache.
	root := p.nugetPackagesRoot()
	if root == "" {
		return out
	}
	for ns := range wanted {
		dir := filepath.Join(root, strings.ToLower(ns))
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil {
				return fs.SkipAll
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".xml") && add(path) {
				return fs.SkipAll
			}
			return nil
		})
	}
	return out
}

// docXML is the subset of a NuGet XML documentation file this needs.
type docXML struct {
	Assembly struct {
		Name string `xml:"name"`
	} `xml:"assembly"`
	Members struct {
		Member []struct {
			Name string `xml:"name,attr"`
		} `xml:"member"`
	} `xml:"members"`
}

// parseDocFile reads one XML doc file into declaring type -> member declarations.
func (p *CSharpProvider) parseDocFile(path string) (map[string][]string, error) {
	p.mu.Lock()
	if v, ok := p.cache[path]; ok {
		p.mu.Unlock()
		return v, nil
	}
	p.mu.Unlock()

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc docXML
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	byType := map[string][]string{}
	for _, m := range doc.Members.Member {
		declType, decl, ok := parseDocMemberID(m.Name)
		if !ok {
			continue
		}
		byType[declType] = append(byType[declType], decl)
	}

	p.mu.Lock()
	p.cache[path] = byType
	p.mu.Unlock()
	return byType, nil
}

// parseDocMemberID decodes an ECMA-334 documentation ID into its declaring type and a readable
// member declaration.
//
//	M:Microsoft.Playwright.ILocatorAssertions.ToContainTextAsync(System.String,Microsoft.Playwright.LocatorAssertionsToContainTextOptions)
//	  -> ("Microsoft.Playwright.ILocatorAssertions", "ToContainTextAsync(string, LocatorAssertionsToContainTextOptions)")
//
// T: entries describe the type itself, not a member, and are skipped. F:/E: (fields, events) are
// rendered like properties.
func parseDocMemberID(id string) (declType, decl string, ok bool) {
	id = strings.TrimSpace(id)
	if len(id) < 3 || id[1] != ':' {
		return "", "", false
	}
	kind := id[0]
	body := id[2:]
	if kind == 'T' || kind == 'N' {
		return "", "", false
	}

	params := ""
	if i := strings.IndexByte(body, '('); i >= 0 {
		if !strings.HasSuffix(body, ")") {
			return "", "", false
		}
		params = body[i+1 : len(body)-1]
		body = body[:i]
	}
	dot := strings.LastIndex(body, ".")
	if dot <= 0 || dot == len(body)-1 {
		return "", "", false
	}
	declType = body[:dot]
	name := body[dot+1:]

	switch kind {
	case 'M':
		var rendered []string
		for _, p := range splitDocParams(params) {
			if p != "" {
				rendered = append(rendered, shortenDocType(p))
			}
		}
		return declType, name + "(" + strings.Join(rendered, ", ") + ");", true
	case 'P', 'F', 'E':
		return declType, name + ";", true
	default:
		return "", "", false
	}
}

// splitDocParams splits a doc-ID parameter list on commas at depth zero, so a generic argument list
// (`System.Collections.Generic.IEnumerable{System.String}`) is not cut in half. Doc IDs use braces
// for generics because angle brackets are not legal in XML attributes.
func splitDocParams(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// docTypeKeywords maps the BCL types that have a C# keyword to it. Rendering `string` rather than
// `System.String` is not decoration: these declarations are read alongside the file under repair,
// which is written in C#, and the keyword form is both shorter and the one a compiler error will
// quote back.
var docTypeKeywords = map[string]string{
	"System.String": "string", "System.Int32": "int", "System.Int64": "long",
	"System.Boolean": "bool", "System.Double": "double", "System.Single": "float",
	"System.Decimal": "decimal", "System.Object": "object", "System.Void": "void",
	"System.Byte": "byte", "System.SByte": "sbyte", "System.Char": "char",
	"System.UInt32": "uint", "System.UInt64": "ulong", "System.Int16": "short",
	"System.UInt16": "ushort",
}

// shortenDocType renders a doc-ID type reference as C#: keywords for BCL primitives, `<>` for
// generics, and the simple name for everything else.
func shortenDocType(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	// Trailing modifiers: @ = by-ref, * = pointer.
	suffix := ""
	for len(t) > 0 && (t[len(t)-1] == '@' || t[len(t)-1] == '*') {
		if t[len(t)-1] == '@' {
			suffix = "&" + suffix
		} else {
			suffix = "*" + suffix
		}
		t = t[:len(t)-1]
	}
	// Arrays.
	for strings.HasSuffix(t, "[]") {
		suffix = "[]" + suffix
		t = strings.TrimSuffix(t, "[]")
	}
	if open := strings.IndexByte(t, '{'); open >= 0 && strings.HasSuffix(t, "}") {
		outer := t[:open]
		args := splitDocParams(t[open+1 : len(t)-1])
		rendered := make([]string, 0, len(args))
		for _, a := range args {
			rendered = append(rendered, shortenDocType(a))
		}
		// A generic type's doc ID carries an arity suffix (IEnumerable`1); it is noise once the
		// argument list is rendered.
		if tick := strings.IndexByte(outer, '`'); tick >= 0 {
			outer = outer[:tick]
		}
		return simpleDocName(outer) + "<" + strings.Join(rendered, ", ") + ">" + suffix
	}
	if kw, ok := docTypeKeywords[t]; ok {
		return kw + suffix
	}
	return simpleDocName(t) + suffix
}

func simpleDocName(t string) string {
	if i := strings.LastIndex(t, "."); i >= 0 && i < len(t)-1 {
		return t[i+1:]
	}
	return t
}
