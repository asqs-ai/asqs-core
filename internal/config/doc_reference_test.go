package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// pendingDocs are doc paths sources may cite before the document exists, each naming the bundle that
// creates it. This is a DEBT LEDGER, not an exemption list: an entry is a promise with an owner, and
// the guard below turns it back into a failure the moment that owner lands without keeping it.
//
// The agent-session engine's reference document is deliberately NOT here. That engine is
// enterprise-excluded, so its document can never exist in the open core; the three citations of it
// were rewritten in CP36 rather than parked.
var pendingDocs = map[string]string{
	"docs/DOCUMENTATION.md": "CP56 creates core's behaviour reference; these citations predate it",
}

// reDocRef finds docs/<NAME>.md paths cited anywhere in sources.
//
// The first character must be uppercase, which is what separates a citation of one of THIS
// repository's documents from a path belonging to a repository under test. Core's docs are named in
// caps (DOCUMENTATION.md, TEST-FRAMEWORK-BOOTSTRAP.md); the lowercase docs/readme.md and
// docs/conventions.md that appear in projectintel's scanner and its fixtures are strings describing
// somebody else's tree, and flagging those would make the guard cry wolf on its first run.
var reDocRef = regexp.MustCompile(`docs/[A-Z][A-Za-z0-9_.-]*\.md`)

// Core's sources cite documents that do not exist in this repository — 21 references at CP36, debris
// from the original strip that no earlier check could catch: CP40's doc-path guard inspects CONFIG
// paths, not doc links, so nothing has ever looked at these.
//
// The cost is not cosmetic. A comment saying "see docs/DOCUMENTATION.md — Static micro-gate" is read
// as evidence that a described behaviour is real and documented; several of those citations survived
// the deletion of the very feature they described.
func TestNoDanglingDocReferences(t *testing.T) {
	root := repoRootFromConfigPkg(t)

	type ref struct{ where, doc string }
	var dangling []ref
	pendingSeen := map[string]bool{}

	for _, sub := range []string{"internal", "cmd", "tools", "docs", "."} {
		base := filepath.Join(root, sub)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// The plan documents dangling paths as data — it is a record of what needs fixing,
				// not a claim that they resolve.
				if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(path) {
			case ".go", ".sql", ".md", ".yaml", ".yml":
			default:
				return nil
			}
			// Test files carry fixture paths describing OTHER repositories — projectintel's relevance
			// tests pass "docs/README.md" as scanner input, which is data, not a citation. A dangling
			// link in a test comment also misleads nobody the way one in production code or a README
			// does, so the guard covers what a reader would actually follow.
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "docs/IMPLEMENTATION-PLAN") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			for _, doc := range reDocRef.FindAllString(string(b), -1) {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(doc))); err == nil {
					continue
				}
				if _, pending := pendingDocs[doc]; pending {
					pendingSeen[doc] = true
					continue
				}
				dangling = append(dangling, ref{where: rel, doc: doc})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", base, err)
		}
		if sub == "." {
			break
		}
	}

	if len(dangling) > 0 {
		var lines []string
		for _, d := range dangling {
			lines = append(lines, d.where+" cites "+d.doc)
		}
		sort.Strings(lines)
		t.Errorf("these sources cite documents that do not exist:\n  %s\n\n"+
			"Either write the document, repoint the citation at something real, or delete it. Add to "+
			"pendingDocs only when a named bundle is committed to creating the file.",
			strings.Join(unique(lines), "\n  "))
	}

	// A pending entry whose document now exists is debt that was paid; the ledger must shrink or it
	// decays into the permanent exemption list this bundle spent its time deleting.
	for doc, owner := range pendingDocs {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(doc))); err == nil {
			t.Errorf("%s exists now (%s) — remove it from pendingDocs so the guard covers it", doc, owner)
			continue
		}
		if !pendingSeen[doc] {
			t.Errorf("%s is in pendingDocs (%s) but nothing cites it any more — drop the entry", doc, owner)
		}
	}
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
