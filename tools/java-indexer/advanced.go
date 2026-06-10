// Advanced Java indexer: run the JavaParser JAR and decode JSONL into the same ParsedFile
// contract as the minimal indexer so the pipeline can use either without change.

package javaindexer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asqs/asqs-core/internal/intelligence/indexer"
)

// JSONL record types produced by the Java indexer JAR (one line per class/interface/record/enum).
// Lines with kind "java_meta" (Phase-1 discovery) are skipped when building ParsedFile maps.

type javaFieldDetail struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Line        int    `json:"line"`
	StartColumn int    `json:"startColumn"`
	EndColumn   int    `json:"endColumn"`
}

type javaSpringRoute struct {
	HTTPMethod    string `json:"httpMethod"`
	ClassMapping  string `json:"classMapping"`
	MethodMapping string `json:"methodMapping"`
	HandlerMethod string `json:"handlerMethod"`
	Line          int    `json:"line"`
	StartColumn   int    `json:"startColumn"`
	EndColumn     int    `json:"endColumn"`
}

type javaClassRecord struct {
	ID              string            `json:"id"`
	Kind            string            `json:"kind"` // "class", "interface", "record"
	FQName          string            `json:"fqName"`
	FilePath        string            `json:"filePath"`
	PackageName     string            `json:"packageName"`
	IsTest          bool              `json:"isTest"`
	StartLine       int               `json:"startLine"`
	EndLine         int               `json:"endLine"`
	StartColumn     int               `json:"startColumn"`
	EndColumn       int               `json:"endColumn"`
	JavadocSummary  string            `json:"javadocSummary"`
	Imports         []string          `json:"imports"`
	ExtendsTypes    []string          `json:"extendsTypes"`
	ImplementsTypes []string          `json:"implementsTypes"`
	Fields          []string          `json:"fields"`
	FieldDetails    []javaFieldDetail `json:"fieldDetails"`
	Methods         []javaMethodRec   `json:"methods"`
	Annotations     []string          `json:"annotations"`
	SpringRoutes    []javaSpringRoute `json:"springRoutes"`
	DIInjectedTypes []string          `json:"diInjectedTypes"`
	DIRegisters     []string          `json:"diRegisteredServices"`
	DIImplements    []string          `json:"diImplementsServices"`
}

type javaMethodRec struct {
	ID          string   `json:"id"`
	FQName      string   `json:"fqName"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // "method" or "constructor"
	Signature   string   `json:"signature"`
	StartLine   int      `json:"startLine"`
	EndLine     int      `json:"endLine"`
	StartColumn int      `json:"startColumn"`
	EndColumn   int      `json:"endColumn"`
	Visibility  string   `json:"visibility"`
	IsStatic    bool     `json:"isStatic"`
	Calls       []string `json:"calls"`
}

type javaE2ESpecBlock struct {
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Framework string `json:"framework"`
}

type javaTestSelectorRec struct {
	TestID  string `json:"testId"`
	Line    int    `json:"line"`
	EndLine int    `json:"endLine"`
}

type javaAPIClientRec struct {
	HTTPMethod     string `json:"httpMethod"`
	Path           string `json:"path"`
	Line           int    `json:"line"`
	EndLine        int    `json:"endLine"`
	Framework      string `json:"framework"`
	CallerMethodFQ string `json:"callerMethodFq"`
}

type javaFileEnrichment struct {
	Kind              string                `json:"kind"`
	FilePath          string                `json:"filePath"`
	PackageName       string                `json:"packageName"`
	IsTest            bool                  `json:"isTest"`
	E2ESpec           *javaE2ESpecBlock     `json:"e2eSpec"`
	TestSelectors     []javaTestSelectorRec `json:"testSelectors"`
	APIClientRequests []javaAPIClientRec    `json:"apiClientRequests"`
}

type javaHTMLHookRec struct {
	Line         int    `json:"line"`
	SelectorKind string `json:"selectorKind"`
	Value        string `json:"value"`
	Framework    string `json:"framework"`
}

type javaHTMLHooksLine struct {
	Kind     string            `json:"kind"`
	FilePath string            `json:"filePath"`
	Hooks    []javaHTMLHookRec `json:"hooks"`
}

type javaEnumRecord struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"` // "enum"
	FQName         string   `json:"fqName"`
	FilePath       string   `json:"filePath"`
	PackageName    string   `json:"packageName"`
	IsTest         bool     `json:"isTest"`
	StartLine      int      `json:"startLine"`
	EndLine        int      `json:"endLine"`
	StartColumn    int      `json:"startColumn"`
	EndColumn      int      `json:"endColumn"`
	JavadocSummary string   `json:"javadocSummary"`
	Annotations    []string `json:"annotations"`
	Members        []string `json:"members"`
}

// intPtrColumn returns a pointer to v when v is a positive 1-based column; nil otherwise (unknown / omitted in JSON).
func intPtrColumn(v int) *int {
	if v < 1 {
		return nil
	}
	x := v
	return &x
}

// RunJARConfig controls how RunJAR invokes the advanced Java indexer JAR.
type RunJARConfig struct {
	Timeout time.Duration
	// Docker non-nil: run inside ephemeral `docker run --rm` (no host java required).
	Docker *JavaDockerConfig
}

// RunJAR runs the advanced Java indexer JAR, parses JSONL stdout, and aggregates by file path.
// Local mode: java -jar jarPath repoPath. Docker mode: see RunJARConfig.Docker and BuildJavaDockerRunArgs.
// Paths in the map use forward slashes (repo-relative). Source is not set (caller fills from LangIndexerFromMap).
func RunJAR(ctx context.Context, repoPath, jarPath string, cfg RunJARConfig) (map[string]*indexer.ParsedFile, error) {
	jarPath = strings.TrimSpace(jarPath)
	if jarPath == "" {
		return nil, fmt.Errorf("java-indexer: empty JAR path")
	}
	if !filepath.IsAbs(jarPath) {
		absJar, err := filepath.Abs(jarPath)
		if err != nil {
			return nil, fmt.Errorf("java-indexer: resolve JAR path: %w", err)
		}
		jarPath = absJar
	}
	if _, err := os.Stat(jarPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("java-indexer: JAR not found at %s (use an absolute path, or run from the directory that contains tools/java-indexer)", jarPath)
		}
		return nil, fmt.Errorf("java-indexer: JAR path: %w", err)
	}
	repoPath = filepath.Clean(repoPath)
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("java-indexer: repo path: %w", err)
	}
	if cfg.Docker != nil {
		return runJARDocker(ctx, absRepo, jarPath, cfg)
	}
	return runJARLocal(ctx, absRepo, jarPath, cfg.Timeout)
}

func runJARLocal(ctx context.Context, absRepo, jarPath string, timeout time.Duration) (map[string]*indexer.ParsedFile, error) {
	runCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "java", "-jar", jarPath, absRepo)
	cmd.Dir = absRepo
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("java-indexer: stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("java-indexer: start: %w", err)
	}
	byPath, scanErr := parseJARJSONLFromReader(stdout, nil, &stderr)
	if scanErr != nil {
		_ = cmd.Wait()
		return nil, scanErr
	}
	indexer.MergeOpenAPISpecFilesIntoMap(absRepo, byPath)
	waitErr := cmd.Wait()
	if len(byPath) == 0 {
		msg := "java-indexer: JAR produced no parsed files (check repo path and that the JAR runs: java -jar " + jarPath + " <repoPath>)"
		if stderr.Len() > 0 {
			msg += "; JAR stderr: " + strings.TrimSpace(stderr.String())
		}
		if waitErr != nil {
			msg += "; exit: " + waitErr.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return byPath, nil
}

func runJARDocker(ctx context.Context, absRepo, jarPath string, cfg RunJARConfig) (map[string]*indexer.ParsedFile, error) {
	dargs, err := BuildJavaDockerRunArgs(absRepo, jarPath, cfg.Docker)
	if err != nil {
		return nil, err
	}
	runCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	cli := cfg.Docker.cli()
	fmt.Fprintf(os.Stderr, "  java-indexer: %s %s\n", cli, strings.Join(dargs, " "))
	cmd := exec.CommandContext(runCtx, cli, dargs...)
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("java-indexer docker: stdout pipe: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("java-indexer docker: start %s: %w", cli, err)
	}
	norm := &PathNormalizer{HostRepoAbs: absRepo, ContainerWorkDir: cfg.Docker.workdir()}
	byPath, scanErr := parseJARJSONLFromReader(stdout, norm, &stderr)
	if scanErr != nil {
		_ = cmd.Wait()
		return nil, scanErr
	}
	indexer.MergeOpenAPISpecFilesIntoMap(absRepo, byPath)
	waitErr := cmd.Wait()
	if len(byPath) == 0 {
		msg := "java-indexer: docker JAR produced no parsed files (check repo mount and image; docker " + strings.Join(dargs, " ") + ")"
		if stderr.Len() > 0 {
			msg += "; stderr: " + strings.TrimSpace(stderr.String())
		}
		if waitErr != nil {
			msg += "; exit: " + waitErr.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return byPath, nil
}

func parseJARJSONLFromReader(stdout io.Reader, norm *PathNormalizer, stderr *strings.Builder) (map[string]*indexer.ParsedFile, error) {
	byPath := make(map[string]*indexer.ParsedFile)
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var kindOnly struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal([]byte(line), &kindOnly); err != nil {
			continue
		}
		if kindOnly.Kind == "java_meta" {
			continue
		}
		var classRec javaClassRecord
		if err := json.Unmarshal([]byte(line), &classRec); err == nil &&
			(classRec.Kind == "class" || classRec.Kind == "interface" || classRec.Kind == "record") {
			mergeClassRecord(classRec, byPath, norm)
			continue
		}
		var enumRec javaEnumRecord
		if err := json.Unmarshal([]byte(line), &enumRec); err == nil && enumRec.Kind == "enum" {
			mergeEnumRecord(enumRec, byPath, norm)
			continue
		}
		var fileEnr javaFileEnrichment
		if err := json.Unmarshal([]byte(line), &fileEnr); err == nil && fileEnr.Kind == "java_file_enrichment" {
			mergeFileEnrichment(fileEnr, byPath, norm)
			continue
		}
		var htmlHooks javaHTMLHooksLine
		if err := json.Unmarshal([]byte(line), &htmlHooks); err == nil && htmlHooks.Kind == "java_html_hooks" {
			mergeJavaHTMLHooks(htmlHooks, byPath, norm)
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("java-indexer: read stdout: %w", err)
	}
	return byPath, nil
}

func mergeClassRecord(rec javaClassRecord, byPath map[string]*indexer.ParsedFile, norm *PathNormalizer) {
	path := NormalizeJavaIndexerPath(rec.FilePath, norm)
	if path == "" {
		return
	}
	if _, ok := byPath[path]; !ok {
		pf := &indexer.ParsedFile{
			Path:    path,
			Lang:    "java",
			Module:  javaPathToModule(path),
			IsTest:  rec.IsTest,
			Symbols: nil,
			Edges:   nil,
		}
		putByPathVariants(byPath, path, pf)
	}
	pf := byPath[path]
	pf.IsTest = pf.IsTest || rec.IsTest

	pkg := strings.TrimSpace(rec.PackageName)
	if pkg != "" {
		if !hasModuleSymbol(pf) {
			pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
				Kind:      "MODULE",
				FQName:    pkg,
				StartLine: 1,
				EndLine:   1,
			})
		}
		appendImportsEdges(pf, rec.FQName, rec.Imports)
	}

	start, end := rec.StartLine, rec.EndLine
	if start < 1 {
		start = 1
	}
	if end < start {
		end = start
	}
	classSig := classSignatureJSON(rec.JavadocSummary, rec.Annotations)
	pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
		Kind:          rec.Kind,
		FQName:        rec.FQName,
		StartLine:     start,
		EndLine:       end,
		StartColumn:   intPtrColumn(rec.StartColumn),
		EndColumn:     intPtrColumn(rec.EndColumn),
		SignatureJSON: classSig,
	})
	emitJavaClassDIEdges(pf, rec)

	for _, ext := range rec.ExtendsTypes {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: rec.FQName,
			CalleeFQName: ext,
			EdgeType:     "EXTENDS",
		})
	}
	for _, im := range rec.ImplementsTypes {
		im = strings.TrimSpace(im)
		if im == "" {
			continue
		}
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: rec.FQName,
			CalleeFQName: im,
			EdgeType:     "IMPLEMENTS",
		})
	}

	for _, fd := range rec.FieldDetails {
		if fd.Name == "" {
			continue
		}
		ln := fd.Line
		if ln < 1 {
			ln = start
		}
		fq := rec.FQName + "#" + fd.Name
		fsig, _ := json.Marshal(map[string]string{"type": fd.Type})
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          "field",
			FQName:        fq,
			StartLine:     ln,
			EndLine:       ln,
			StartColumn:   intPtrColumn(fd.StartColumn),
			EndColumn:     intPtrColumn(fd.EndColumn),
			SignatureJSON: fsig,
		})
	}

	for _, m := range rec.Methods {
		mk := strings.ToLower(strings.TrimSpace(m.Kind))
		if mk == "" {
			mk = "method"
		}
		sigObj := map[string]interface{}{
			"signature": m.Signature,
			"exported":  strings.EqualFold(strings.TrimSpace(m.Visibility), "public"),
			"static":    m.IsStatic,
		}
		if m.Visibility != "" {
			sigObj["visibility"] = m.Visibility
		}
		sigJSON, _ := json.Marshal(sigObj)
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          mk,
			FQName:        m.FQName,
			StartLine:     m.StartLine,
			EndLine:       m.EndLine,
			StartColumn:   intPtrColumn(m.StartColumn),
			EndColumn:     intPtrColumn(m.EndColumn),
			SignatureJSON: sigJSON,
		})
		for _, callee := range m.Calls {
			if callee == "" || strings.HasPrefix(callee, "UNRESOLVED:") {
				continue
			}
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: m.FQName,
				CalleeFQName: callee,
				EdgeType:     "calls",
			})
		}
	}

	for _, sr := range rec.SpringRoutes {
		if sr.HandlerMethod == "" || sr.HTTPMethod == "" {
			continue
		}
		handlerFq := rec.FQName + "#" + sr.HandlerMethod
		if !methodSymbolExists(pf.Symbols, handlerFq) {
			continue
		}
		fullPath := normalizeWebPath(sr.ClassMapping, sr.MethodMapping)
		apiFq := springAPIRouteFQName(sr.HTTPMethod, fullPath, handlerFq)
		ln := sr.Line
		if ln < 1 {
			ln = start
		}
		sig, _ := json.Marshal(map[string]string{
			"framework":    "spring_web",
			"http_method":  strings.ToUpper(strings.TrimSpace(sr.HTTPMethod)),
			"path_pattern": fullPath,
			"handler_fq":   handlerFq,
			"class":        rec.FQName,
			"class_fq":     rec.FQName,
		})
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          "API_ROUTE",
			FQName:        apiFq,
			StartLine:     ln,
			EndLine:       ln,
			StartColumn:   intPtrColumn(sr.StartColumn),
			EndColumn:     intPtrColumn(sr.EndColumn),
			SignatureJSON: sig,
		})
		pf.Edges = append(pf.Edges,
			indexer.ParsedEdge{CallerFQName: rec.FQName, CalleeFQName: apiFq, EdgeType: "CONTAINS"},
			indexer.ParsedEdge{CallerFQName: apiFq, CalleeFQName: handlerFq, EdgeType: "ROUTE_TO_HANDLER"},
		)
	}
}

func emitJavaClassDIEdges(pf *indexer.ParsedFile, rec javaClassRecord) {
	if pf == nil {
		return
	}
	usedJARNative := false
	// Prefer DI facts emitted directly by the Java extractor when present.
	for _, dep := range rec.DIInjectedTypes {
		dep = strings.TrimSpace(dep)
		if dep == "" {
			continue
		}
		usedJARNative = true
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: rec.FQName,
			CalleeFQName: dep,
			EdgeType:     "INJECTS",
		})
	}
	for _, svc := range rec.DIRegisters {
		svc = strings.TrimSpace(svc)
		if svc == "" {
			continue
		}
		usedJARNative = true
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: rec.FQName,
			CalleeFQName: svc,
			EdgeType:     "REGISTERS_SERVICE",
		})
	}
	for _, itf := range rec.DIImplements {
		itf = strings.TrimSpace(itf)
		if itf == "" {
			continue
		}
		usedJARNative = true
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: rec.FQName,
			CalleeFQName: itf,
			EdgeType:     "IMPLEMENTS_SERVICE",
		})
	}
	if usedJARNative {
		pf.Edges = dedupeJavaParsedEdges(pf.Edges)
		return
	}

	// Constructor injection edges: class -> constructor parameter type.
	for _, m := range rec.Methods {
		if !strings.EqualFold(strings.TrimSpace(m.Kind), "constructor") {
			continue
		}
		for _, dep := range javaParamTypesFromSignature(m.Signature) {
			if dep == "" {
				continue
			}
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: rec.FQName,
				CalleeFQName: dep,
				EdgeType:     "INJECTS",
			})
		}
	}

	if !javaLooksLikeServiceClass(rec.Annotations) {
		return
	}
	// Service registration: package/module contains this service class for DI lookup context.
	if pkg := strings.TrimSpace(rec.PackageName); pkg != "" {
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: pkg,
			CalleeFQName: rec.FQName,
			EdgeType:     "REGISTERS_SERVICE",
		})
	}
	// Service mapping: class implements interface abstraction.
	for _, itf := range rec.ImplementsTypes {
		itf = strings.TrimSpace(itf)
		if itf == "" {
			continue
		}
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: rec.FQName,
			CalleeFQName: itf,
			EdgeType:     "IMPLEMENTS_SERVICE",
		})
	}
	// @Configuration-style bean factory heuristic: register non-void, non-primitive method return types.
	if javaHasAnnotation(rec.Annotations, "Configuration") {
		for _, m := range rec.Methods {
			if strings.EqualFold(strings.TrimSpace(m.Kind), "constructor") {
				continue
			}
			ret := javaReturnTypeFromMethodSignature(m.Signature)
			if ret == "" {
				continue
			}
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: rec.FQName,
				CalleeFQName: ret,
				EdgeType:     "REGISTERS_SERVICE",
			})
		}
	}
	pf.Edges = dedupeJavaParsedEdges(pf.Edges)
}

func javaLooksLikeServiceClass(annotations []string) bool {
	return javaHasAnnotation(annotations, "Service") ||
		javaHasAnnotation(annotations, "Component") ||
		javaHasAnnotation(annotations, "Repository") ||
		javaHasAnnotation(annotations, "Controller") ||
		javaHasAnnotation(annotations, "RestController")
}

func javaHasAnnotation(annotations []string, simpleName string) bool {
	simpleName = strings.TrimSpace(simpleName)
	if simpleName == "" {
		return false
	}
	for _, a := range annotations {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		// Handles values like "@Service", "@org.springframework.stereotype.Service", "@Service(...)"
		if strings.Contains(a, "@"+simpleName) || strings.Contains(a, "."+simpleName) {
			return true
		}
	}
	return false
}

func javaParamTypesFromSignature(sig string) []string {
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return nil
	}
	lp := strings.Index(sig, "(")
	rp := strings.LastIndex(sig, ")")
	if lp < 0 || rp <= lp+1 {
		return nil
	}
	params := strings.TrimSpace(sig[lp+1 : rp])
	if params == "" {
		return nil
	}
	raw := strings.Split(params, ",")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fields := strings.Fields(p)
		if len(fields) == 0 {
			continue
		}
		// Parameter form: "<Type> <name>" (possible annotations/modifiers before).
		typ := fields[0]
		if len(fields) >= 2 {
			typ = fields[len(fields)-2]
		}
		typ = strings.TrimSuffix(strings.TrimSpace(typ), "...")
		if typ == "" || typ == "?" {
			continue
		}
		out = append(out, typ)
	}
	return out
}

func javaReturnTypeFromMethodSignature(sig string) string {
	sig = strings.TrimSpace(sig)
	if sig == "" {
		return ""
	}
	lp := strings.Index(sig, "(")
	if lp <= 0 {
		return ""
	}
	head := strings.TrimSpace(sig[:lp])
	parts := strings.Fields(head)
	if len(parts) < 2 {
		return ""
	}
	ret := strings.TrimSpace(parts[len(parts)-2])
	switch strings.ToLower(ret) {
	case "", "void", "int", "long", "short", "byte", "double", "float", "boolean", "char":
		return ""
	default:
		return ret
	}
}

func dedupeJavaParsedEdges(in []indexer.ParsedEdge) []indexer.ParsedEdge {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]indexer.ParsedEdge, 0, len(in))
	for _, e := range in {
		k := strings.TrimSpace(e.CallerFQName) + "\x00" + strings.TrimSpace(e.CalleeFQName) + "\x00" + strings.ToUpper(strings.TrimSpace(e.EdgeType))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// appendImportsEdges links the compilation unit's host type to each imported type (cross-file graph).
// Caller is the class/record FQName (not the package) so the edge resolves to a symbol in this file.
// Callee strings are normalized: JavaParser emits "import pkg.Type;", not "pkg.Type".
func appendImportsEdges(pf *indexer.ParsedFile, callerClassFQ string, imports []string) {
	if callerClassFQ == "" {
		return
	}
	seen := make(map[string]bool)
	for _, e := range pf.Edges {
		if strings.EqualFold(e.EdgeType, "imports") && e.CallerFQName == callerClassFQ {
			seen[e.CalleeFQName] = true
		}
	}
	for _, raw := range imports {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		q := indexer.NormalizeJavaImportDecl(raw)
		if q == "" {
			continue
		}
		if seen[q] {
			continue
		}
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: callerClassFQ,
			CalleeFQName: q,
			EdgeType:     "IMPORTS",
		})
		seen[q] = true
	}
}

func hasModuleSymbol(pf *indexer.ParsedFile) bool {
	for _, s := range pf.Symbols {
		if strings.EqualFold(s.Kind, "module") {
			return true
		}
	}
	return false
}

func classSignatureJSON(javadoc string, annotations []string) []byte {
	obj := make(map[string]interface{})
	if strings.TrimSpace(javadoc) != "" {
		obj["javadoc"] = strings.TrimSpace(javadoc)
	}
	if len(annotations) > 0 {
		obj["annotations"] = annotations
	}
	if len(obj) == 0 {
		return nil
	}
	b, _ := json.Marshal(obj)
	return b
}

// putByPathVariants stores pf under path and under normalized variants so lookup matches scan vs JAR path format.
func putByPathVariants(byPath map[string]*indexer.ParsedFile, path string, pf *indexer.ParsedFile) {
	byPath[path] = pf
	byPath[strings.TrimPrefix(path, "/")] = pf
	byPath[filepath.ToSlash(filepath.Clean(path))] = pf
}

// javaFileToModuleDots matches JS filePathToModuleId style (path segments joined by dots, no extension).
func javaFileToModuleDots(path string) string {
	p := filepath.ToSlash(strings.TrimSpace(path))
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, ".java")
	if p == "" {
		return "root"
	}
	parts := strings.Split(p, "/")
	return strings.Join(parts, ".")
}

func mergeFileEnrichment(rec javaFileEnrichment, byPath map[string]*indexer.ParsedFile, norm *PathNormalizer) {
	path := NormalizeJavaIndexerPath(rec.FilePath, norm)
	if path == "" {
		return
	}
	if _, ok := byPath[path]; !ok {
		pf := &indexer.ParsedFile{
			Path:    path,
			Lang:    "java",
			Module:  javaPathToModule(path),
			IsTest:  rec.IsTest,
			Symbols: nil,
			Edges:   nil,
		}
		putByPathVariants(byPath, path, pf)
	}
	pf := byPath[path]
	pf.IsTest = pf.IsTest || rec.IsTest

	pkg := strings.TrimSpace(rec.PackageName)
	if pkg != "" && !hasModuleSymbol(pf) {
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:      "MODULE",
			FQName:    pkg,
			StartLine: 1,
			EndLine:   1,
		})
	}

	modDots := javaFileToModuleDots(path)
	e2eFq := ""
	if rec.E2ESpec != nil {
		e2eFq = "E2E_SPEC:" + path
		es, ee := rec.E2ESpec.StartLine, rec.E2ESpec.EndLine
		if es < 1 {
			es = 1
		}
		if ee < es {
			ee = es
		}
		sig, _ := json.Marshal(map[string]string{
			"framework": strings.TrimSpace(rec.E2ESpec.Framework),
			"spec_path": path,
		})
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          "E2E_SPEC",
			FQName:        e2eFq,
			StartLine:     es,
			EndLine:       ee,
			SignatureJSON: sig,
		})
		if pkg != "" {
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: pkg,
				CalleeFQName: e2eFq,
				EdgeType:     "CONTAINS",
			})
		}
	}

	for _, sel := range rec.TestSelectors {
		tid := strings.TrimSpace(sel.TestID)
		if tid == "" {
			continue
		}
		line := sel.Line
		if line < 1 {
			line = 1
		}
		end := sel.EndLine
		if end < line {
			end = line
		}
		selFq := fmt.Sprintf("TEST_SELECTOR:testid:%s@%s:L%d", tid, modDots, line)
		selSig, _ := json.Marshal(map[string]string{
			"selector_kind": "testid",
			"value":         tid,
			"framework":     "playwright_java",
		})
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          "TEST_SELECTOR",
			FQName:        selFq,
			StartLine:     line,
			EndLine:       end,
			SignatureJSON: selSig,
		})
		if pkg != "" {
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: pkg,
				CalleeFQName: selFq,
				EdgeType:     "CONTAINS",
			})
		}
		if e2eFq != "" {
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: e2eFq,
				CalleeFQName: selFq,
				EdgeType:     "USES_SELECTOR",
			})
		}
	}

	for _, api := range rec.APIClientRequests {
		pNorm := normalizeWebPath("", strings.TrimSpace(api.Path))
		if pNorm == "" {
			continue
		}
		m := strings.ToUpper(strings.TrimSpace(api.HTTPMethod))
		if m == "" {
			m = "GET"
		}
		line := api.Line
		if line < 1 {
			line = 1
		}
		end := api.EndLine
		if end < line {
			end = line
		}
		symFq := fmt.Sprintf("API_CLIENT_REQUEST:%s:%s@%s:L%d", m, pNorm, modDots, line)
		fw := strings.TrimSpace(api.Framework)
		if fw == "" {
			fw = "java_http"
		}
		apiSig, _ := json.Marshal(map[string]string{
			"framework":    fw,
			"http_method":  m,
			"path_pattern": pNorm,
		})
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          "API_CLIENT_REQUEST",
			FQName:        symFq,
			StartLine:     line,
			EndLine:       end,
			SignatureJSON: apiSig,
		})
		caller := strings.TrimSpace(api.CallerMethodFQ)
		if caller != "" {
			pf.Edges = append(pf.Edges, indexer.ParsedEdge{
				CallerFQName: caller,
				CalleeFQName: symFq,
				EdgeType:     "CALLS_API",
			})
		}
	}
}

func htmlPathToModuleFq(path string) string {
	p := filepath.ToSlash(strings.TrimSpace(path))
	p = strings.TrimPrefix(p, "/")
	lower := strings.ToLower(p)
	for _, ext := range []string{".html", ".htm"} {
		if strings.HasSuffix(lower, ext) {
			p = p[:len(p)-len(ext)]
			break
		}
	}
	if p == "" {
		return "template.content.root"
	}
	return "template.content." + strings.ReplaceAll(p, "/", ".")
}

func uiHookValueToken(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '@', ':', ' ', '\t', '\n', '\r', '/', '\\':
			return '_'
		default:
			return r
		}
	}, s)
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// mergeJavaHTMLHooks merges Thymeleaf/static HTML testability hooks into a ParsedFile (lang html).
func mergeJavaHTMLHooks(rec javaHTMLHooksLine, byPath map[string]*indexer.ParsedFile, norm *PathNormalizer) {
	path := NormalizeJavaIndexerPath(rec.FilePath, norm)
	if path == "" || len(rec.Hooks) == 0 {
		return
	}
	modFq := htmlPathToModuleFq(path)
	tplFq := "STATIC_TEMPLATE:" + path
	maxLine := 1
	for _, h := range rec.Hooks {
		if h.Line > maxLine {
			maxLine = h.Line
		}
	}
	lowPath := strings.ToLower(path)
	isTestRes := strings.Contains(lowPath, "/test/") || strings.Contains(lowPath, "src/test/")
	if _, ok := byPath[path]; !ok {
		modShort := strings.TrimPrefix(modFq, "template.content.")
		pf := &indexer.ParsedFile{
			Path:    path,
			Lang:    "html",
			Module:  modShort,
			IsTest:  isTestRes,
			Symbols: nil,
			Edges:   nil,
		}
		putByPathVariants(byPath, path, pf)
	}
	pf := byPath[path]
	pf.Lang = "html"
	pf.IsTest = pf.IsTest || isTestRes
	if !hasModuleSymbol(pf) {
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:      "MODULE",
			FQName:    modFq,
			StartLine: 1,
			EndLine:   maxLine,
		})
	}
	tplSeen := false
	for _, s := range pf.Symbols {
		if s.Kind == "STATIC_TEMPLATE" && s.FQName == tplFq {
			tplSeen = true
			break
		}
	}
	if !tplSeen {
		tplSig, _ := json.Marshal(map[string]string{
			"template_path": path,
			"facet":         "server_template",
		})
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          "STATIC_TEMPLATE",
			FQName:        tplFq,
			StartLine:     1,
			EndLine:       maxLine,
			SignatureJSON: tplSig,
		})
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: modFq,
			CalleeFQName: tplFq,
			EdgeType:     "CONTAINS",
		})
	}
	for _, h := range rec.Hooks {
		val := strings.TrimSpace(h.Value)
		if val == "" {
			continue
		}
		sk := strings.TrimSpace(h.SelectorKind)
		if sk == "" {
			sk = "hook"
		}
		line := h.Line
		if line < 1 {
			line = 1
		}
		fw := strings.TrimSpace(h.Framework)
		if fw == "" {
			fw = "html"
		}
		hookFq := fmt.Sprintf("UI_TEST_HOOK:%s:%s@%s:L%d", sk, uiHookValueToken(val), path, line)
		hookSig, _ := json.Marshal(map[string]string{
			"selector_kind": sk,
			"value":         val,
			"framework":     fw,
			"template_path": path,
		})
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:          "UI_TEST_HOOK",
			FQName:        hookFq,
			StartLine:     line,
			EndLine:       line,
			SignatureJSON: hookSig,
		})
		pf.Edges = append(pf.Edges, indexer.ParsedEdge{
			CallerFQName: tplFq,
			CalleeFQName: hookFq,
			EdgeType:     "CONTAINS",
		})
	}
}

func mergeEnumRecord(rec javaEnumRecord, byPath map[string]*indexer.ParsedFile, norm *PathNormalizer) {
	path := NormalizeJavaIndexerPath(rec.FilePath, norm)
	if path == "" {
		return
	}
	if _, ok := byPath[path]; !ok {
		pf := &indexer.ParsedFile{
			Path:    path,
			Lang:    "java",
			Module:  javaPathToModule(path),
			IsTest:  rec.IsTest,
			Symbols: nil,
			Edges:   nil,
		}
		putByPathVariants(byPath, path, pf)
	}
	pf := byPath[path]
	pf.IsTest = pf.IsTest || rec.IsTest

	pkg := strings.TrimSpace(rec.PackageName)
	if pkg != "" && !hasModuleSymbol(pf) {
		pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
			Kind:      "MODULE",
			FQName:    pkg,
			StartLine: 1,
			EndLine:   1,
		})
	}

	es, ee := rec.StartLine, rec.EndLine
	if es < 1 {
		es = 1
	}
	if ee < es {
		ee = es
	}
	enumMeta := make(map[string]interface{})
	if strings.TrimSpace(rec.JavadocSummary) != "" {
		enumMeta["javadoc"] = strings.TrimSpace(rec.JavadocSummary)
	}
	if len(rec.Annotations) > 0 {
		enumMeta["annotations"] = rec.Annotations
	}
	if len(rec.Members) > 0 {
		enumMeta["members"] = rec.Members
	}
	var enumSig []byte
	if len(enumMeta) > 0 {
		enumSig, _ = json.Marshal(enumMeta)
	}
	pf.Symbols = append(pf.Symbols, indexer.ParsedSymbol{
		Kind:          "enum",
		FQName:        rec.FQName,
		StartLine:     es,
		EndLine:       ee,
		StartColumn:   intPtrColumn(rec.StartColumn),
		EndColumn:     intPtrColumn(rec.EndColumn),
		SignatureJSON: enumSig,
	})
}

// LangIndexerFromMap returns a LangIndexer that uses the precomputed map from RunJAR.
// For each path it looks up the ParsedFile (trying path, path without leading slash, and Clean(path) so scan and JAR path formats match), sets Source from the provided source bytes, and returns it.
// If the path is not in the map, it delegates to indexer.StubLangIndexer.
func LangIndexerFromMap(parsedByPath map[string]*indexer.ParsedFile) indexer.LangIndexer {
	return func(ctx context.Context, path string, lang string, source []byte) (*indexer.ParsedFile, error) {
		path = filepath.ToSlash(path)
		tryPaths := []string{path, strings.TrimPrefix(path, "/"), filepath.ToSlash(filepath.Clean(path))}
		for _, k := range tryPaths {
			if pf, ok := parsedByPath[k]; ok {
				out := *pf
				out.Source = string(source)
				return &out, nil
			}
		}
		return indexer.StubLangIndexer(ctx, path, lang, source)
	}
}
