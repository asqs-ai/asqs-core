package testbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Build excludes per bootstrap: what each one writes outside src/, plus the generated test globs.
var (
	unitBuildExcludes       = []string{jsSmokeDir, "**/*.test.ts", "**/*.spec.ts"}
	playwrightBuildExcludes = []string{"e2e", "playwright.config.ts"}
	cypressBuildExcludes    = []string{"cypress", "cypress.config.ts"}
)

// excludeBootstrapPathsFromBuild applies ensureBuildTSConfigExcludes for one bootstrap and audits
// the write under "<prefix>.build_tsconfig_excluded". Returns the repo-relative path written, or
// nothing when the package has no build tsconfig or already excludes the patterns.
func excludeBootstrapPathsFromBuild(ctx context.Context, audit Auditor, prefix, repo, pkgDir string, patterns []string) []string {
	rel, changed, err := ensureBuildTSConfigExcludes(pkgDir, patterns)
	if err != nil {
		logAuditError(audit, ctx, prefix+".build_tsconfig_exclude_failed", map[string]interface{}{
			"message":  fmt.Sprintf("Could not keep the bootstrap's files out of %s: %v. The production build may emit them and shift its output root.", tsBuildConfigName, err),
			"patterns": patterns,
			"error":    err.Error(),
		})
		return nil
	}
	if !changed {
		return nil
	}
	full := relPathForBootstrap(repo, filepath.Join(pkgDir, rel))
	logAudit(audit, ctx, prefix+".build_tsconfig_excluded", map[string]interface{}{
		"message": fmt.Sprintf("Excluded %s from %s so the production build keeps its emit root (a file outside src/ moves it and `node dist/main.js` then fails).",
			joinPatterns(patterns), full),
		"path":     full,
		"patterns": patterns,
	})
	return []string{full}
}

func joinPatterns(patterns []string) string {
	out := ""
	for i, p := range patterns {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// tsBuildConfigName is the build-only tsconfig NestJS generates (`nest build` compiles against it)
// and other TypeScript services adopt for the same purpose.
const tsBuildConfigName = "tsconfig.build.json"

// ensureBuildTSConfigExcludes keeps what the bootstrap adds out of the package's production build.
//
// tsc derives the emit root from the files it compiles. A build tsconfig with no `include` compiles
// every .ts file under the package, so the bootstrap's own additions outside src/ — __tests__/,
// e2e/, playwright.config.ts — move that root from src/ to the package root and every emitted path
// gains a src/ segment: run api-7425be21b83f608e66318fdd99528522 built dist/src/main.js where the
// repository's `npm run start` expects dist/main.js, the E2E web server died on "Cannot find module
// '/workspace/dist/main.js'", and a shipped PR would have broken the repository's own start script
// the same way. Generated tests inside src/ do not move the root but do land in dist/ as
// production output, so the test-file globs go too.
//
// Only tsconfig.build.json is patched. tsconfig.json is what ts-jest and the type-check gate read,
// and excluding test files from it would blind both. A build tsconfig with an explicit `include`
// already scopes the build and is left alone. Returns the repo-relative path when it wrote.
func ensureBuildTSConfigExcludes(pkgDir string, patterns []string) (rel string, changed bool, err error) {
	path := filepath.Join(pkgDir, tsBuildConfigName)
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		return "", false, nil
	}
	stripped := stripTrailingJSONCommas(stripTSConfigJSONComments(raw))
	var root map[string]json.RawMessage
	if json.Unmarshal(stripped, &root) != nil {
		return "", false, nil
	}
	if inc, ok := root["include"]; ok && !bytes.Equal(bytes.TrimSpace(inc), []byte("null")) {
		return "", false, nil
	}
	var exclude []string
	if ex, ok := root["exclude"]; ok {
		if json.Unmarshal(ex, &exclude) != nil {
			return "", false, nil
		}
	}
	have := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		have[e] = true
	}
	added := 0
	for _, p := range patterns {
		if p == "" || have[p] {
			continue
		}
		have[p] = true
		exclude = append(exclude, p)
		added++
	}
	if added == 0 {
		return "", false, nil
	}
	ex, merr := json.Marshal(exclude)
	if merr != nil {
		return "", false, merr
	}
	root["exclude"] = ex
	out, merr := marshalTSConfigInOrder(stripped, root)
	if merr != nil {
		return "", false, merr
	}
	if werr := atomicWrite(path, out); werr != nil {
		return "", false, werr
	}
	return tsBuildConfigName, true, nil
}

// marshalTSConfigInOrder renders root with the top-level keys in the order the original file had
// them (a new key goes last), so a patched tsconfig still reads like the one the repository wrote.
func marshalTSConfigInOrder(original []byte, root map[string]json.RawMessage) ([]byte, error) {
	var order []string
	dec := json.NewDecoder(bytes.NewReader(original))
	if tok, err := dec.Token(); err == nil && tok == json.Delim('{') {
		depth := 0
		for dec.More() || depth > 0 {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch v := tok.(type) {
			case json.Delim:
				switch v {
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
			case string:
				if depth == 0 {
					order = append(order, v)
					// Skip the value; a nested object or array is consumed as one token stream.
					var skip json.RawMessage
					if err := dec.Decode(&skip); err != nil {
						break
					}
				}
			}
			if depth < 0 {
				break
			}
		}
	}
	seen := make(map[string]bool, len(order))
	var buf bytes.Buffer
	buf.WriteString("{\n")
	first := true
	write := func(k string) {
		v, ok := root[k]
		if !ok || seen[k] {
			return
		}
		seen[k] = true
		if !first {
			buf.WriteString(",\n")
		}
		first = false
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, v, "  ", "  "); err != nil {
			pretty.Reset()
			pretty.Write(v)
		}
		kb, _ := json.Marshal(k)
		buf.WriteString("  ")
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(pretty.Bytes())
	}
	for _, k := range order {
		write(k)
	}
	// Keys the walk did not see (a new "exclude" on a file that had none), in a stable order.
	rest := make([]string, 0, len(root))
	for k := range root {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		write(k)
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}
