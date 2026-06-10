package indexer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// OpenAPISpecRelPaths is a flattened list of JSON spec paths (historical); merge uses stem order
// JSON → YAML → `.yml` per directory. Kept for callers that need a conservative file list.
var OpenAPISpecRelPaths = []string{
	"openapi.json",
	"swagger.json",
	"api/openapi.json",
	"api/swagger.json",
	"docs/openapi.json",
	"docs/swagger.json",
	"src/openapi.json",
	"src/main/resources/openapi.json",
	"src/main/resources/swagger.json",
}

// openAPISpecDirs: common repo layouts for contract-first APIs (extended over time).
var openAPISpecDirs = []string{
	"", "api", "docs", "src", "src/main/resources",
	"openapi", "spec", "specs", "contracts", "rest-api",
}
var openAPISpecNames = []string{"openapi", "swagger"}
var openAPISpecExts = []string{".json", ".yaml", ".yml"}

var openapiHTTPVerbs = map[string]struct{}{
	"get": {}, "post": {}, "put": {}, "patch": {}, "delete": {},
	"options": {}, "head": {}, "trace": {},
}

type openapiOp struct {
	method, path, operationID string
}

func openAPISpecRelPath(dir, name, ext string) string {
	base := name + ext
	if dir == "" {
		return filepath.ToSlash(base)
	}
	return filepath.ToSlash(filepath.Join(dir, base))
}

func openAPIRootMapFromBytes(data []byte) map[string]interface{} {
	trim := bytes.TrimSpace(data)
	if len(trim) == 0 {
		return nil
	}
	if trim[0] == '{' || trim[0] == '[' {
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err == nil && m != nil {
			return m
		}
	}
	var y map[string]interface{}
	if err := yaml.Unmarshal(data, &y); err == nil && y != nil {
		return y
	}
	return nil
}

// parseOpenAPIOperationsJSON parses JSON OpenAPI/Swagger (with in-document $ref expansion on paths).
func parseOpenAPIOperationsJSON(data []byte) []openapiOp {
	return parseOpenAPIOperationsFromBytes(data)
}

// parseOpenAPIOperationsYAML parses YAML OpenAPI/Swagger (same extraction as JSON).
func parseOpenAPIOperationsYAML(data []byte) []openapiOp {
	return parseOpenAPIOperationsFromBytes(data)
}

// parseOpenAPIOperationsFromBytes loads the document as a generic map, expands internal Path Item and
// Operation $ref (JSON Pointer, RFC 6901), then extracts HTTP operations from paths.
func parseOpenAPIOperationsFromBytes(data []byte) []openapiOp {
	root := openAPIRootMapFromBytes(data)
	if root == nil {
		return nil
	}
	pathsRaw, ok := root["paths"].(map[string]interface{})
	if !ok || pathsRaw == nil {
		return nil
	}
	pathsExp := expandOpenAPIPathRefs(pathsRaw, root)
	return openapiPathsMapToOps(pathsExp)
}

func openAPIDeclarationAnchor(relPOSIX, method, apiPath string) string {
	return "openapi:" + relPOSIX + "#" + method + "_" + apiPath
}

func parsedFileFromOpenAPIOps(relSlash string, source []byte, ops []openapiOp) *ParsedFile {
	moduleFQ := pathToModuleFQ(relSlash)
	pf := &ParsedFile{
		Path:    relSlash,
		Lang:    "openapi",
		Module:  moduleFQ,
		IsTest:  false,
		Source:  string(source),
		Symbols: nil,
		Edges:   nil,
	}
	pf.Symbols = append(pf.Symbols, ParsedSymbol{
		Kind: "MODULE", FQName: moduleFQ, StartLine: 1, EndLine: 1,
	})
	line := 2
	for _, op := range ops {
		anchor := openAPIDeclarationAnchor(relSlash, op.method, op.path)
		fq := "API_ROUTE:" + op.method + ":" + op.path + "@" + anchor
		sig := map[string]string{
			"framework":   "openapi",
			"spec":        relSlash,
			"http_method": op.method,
			"path":        op.path,
		}
		if op.operationID != "" {
			sig["operation_id"] = op.operationID
		}
		sigJSON, _ := json.Marshal(sig)
		pf.Symbols = append(pf.Symbols, ParsedSymbol{
			Kind: "API_ROUTE", FQName: fq, StartLine: line, EndLine: line,
			SignatureJSON: sigJSON,
		})
		pf.Edges = append(pf.Edges, ParsedEdge{
			CallerFQName: moduleFQ, CalleeFQName: fq, EdgeType: "CONTAINS",
		})
		line++
	}
	return pf
}

// ParsedFileFromOpenAPISource builds a ParsedFile for OpenAPI/Swagger JSON or YAML (used by Run when scan tags the file as openapi).
func ParsedFileFromOpenAPISource(relSlash string, source []byte) *ParsedFile {
	ops := parseOpenAPIOperationsFromBytes(source)
	return parsedFileFromOpenAPIOps(relSlash, source, ops)
}

// MergeOpenAPISpecFilesIntoMap adds synthetic MODULE + API_ROUTE symbols for OpenAPI/Swagger specs
// (JSON or YAML) not already present in byPath. For each directory × basename (`openapi` / `swagger`),
// the first existing file among `.json`, `.yaml`, `.yml` with at least one operation wins (JSON preferred).
// Returns the number of new files merged.
func MergeOpenAPISpecFilesIntoMap(repoPath string, byPath map[string]*ParsedFile) int {
	repoPath = filepath.Clean(repoPath)
	added := 0
	for _, dir := range openAPISpecDirs {
		for _, name := range openAPISpecNames {
			for _, ext := range openAPISpecExts {
				rel := openAPISpecRelPath(dir, name, ext)
				relSlash := filepath.ToSlash(rel)
				if _, exists := byPath[relSlash]; exists {
					break
				}
				full := filepath.Join(repoPath, rel)
				st, err := os.Stat(full)
				if err != nil || st.IsDir() {
					continue
				}
				data, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				ops := parseOpenAPIOperationsFromBytes(data)
				if len(ops) == 0 {
					continue
				}
				pf := parsedFileFromOpenAPIOps(relSlash, data, ops)
				putOpenAPIVariants(byPath, relSlash, pf)
				added++
				break
			}
		}
	}
	return added
}

func putOpenAPIVariants(byPath map[string]*ParsedFile, relSlash string, pf *ParsedFile) {
	byPath[relSlash] = pf
	byPath[strings.TrimPrefix(relSlash, "/")] = pf
}
