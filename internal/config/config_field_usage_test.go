package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// exemptConfigFields lists config fields with no consumer anywhere. It is EMPTY, and that is the
// point: every field of Config is now referenced by production code.
//
// History, because an empty list is easy to mistake for a disabled check. This lint was written
// after the deep system review found five inert config surfaces; its first run reported 28, so
// every one was parked here with a "TRIAGE" note. Bundle C1 of the config restructure cleared all
// 28 two ways. Roughly twenty were never inert at all — they are read through the accessor methods
// that live inside internal/config (ActiveShip, ActiveGating, ActiveWebhook*,
// ActiveDefaultOwnerRepo, poolMaxConns, EffectiveCachePath), which the old scan deliberately
// skipped, so the lint could not see their consumers and the "never consumed" notes were simply
// wrong; collectIdentifiersOutsideConfig now scans those files. The genuinely dead remainder —
// vcs.github.upload_url, runner.docker_endpoint, copilot.permissions.allow_write_globs and
// allow_urls, plus the superseded websearch.cache_dir — was deleted outright, struct field, env tag,
// docs and example lines together (docs/IMPLEMENTATION-PLAN-CONFIG-RESTRUCTURE.md §2.1, §2.4).
//
// Keep it empty. Adding an entry is an admission that a documented YAML key does nothing; wire the
// field or delete it instead. If a field is read only from inside internal/config, name the
// accessor in a comment rather than exempting it — the scan can see those now.
//
// KNOWN BLIND SPOT, and CP36 measured how deep it goes. The check matches identifiers, not types,
// so a field counts as read when ANY package declares a field of the same name. Three keys have now
// slipped through for exactly that reason — runner.prefer_default_test_suffix (orchestrator.Config
// also has a PreferDefaultTestSuffix), general.git.ship.allow_partial (session.ShipDecision also has
// an AllowPartial), and retrieval.failure_hint_file, which reached PlanOptions.FailureHintFile and
// stopped there because nothing opened the file. All three were caught by review, not by this test.
//
// Two cheap closures were tried at CP36 and BOTH fail; do not spend the afternoon rediscovering it:
//
//  1. "Flag fields whose name is declared elsewhere and confirm those by hand." 148 of 266 config
//     fields are ambiguous that way. Ambiguity is the NORM here — config values legitimately mirror
//     into each consumer's own options struct, which is how configuration flows through this
//     codebase — so the list is noise, not a review queue.
//  2. "Require the read to be a selector chain ending in <Parent>.<Field>." 108 of 266 fields fail
//     it, because so many are read through an accessor (ActiveShip, EffectiveProjectIntel) or a
//     bound sub-struct value, where the Config root is no longer in the expression.
//
// The gap really does need go/types to resolve each selector to its declaring type, which needs
// golang.org/x/tools — not a dependency worth adding for a test. Until then this stays a smoke
// alarm: it catches a field NOTHING anywhere mentions, and cannot catch a field shadowed by a
// same-named field elsewhere. The golden resolved-config fixtures are the other half of the
// defence, and they are type-blind by construction.
// CP36 emptied the TRIAGE backlog. Thirteen entries were resolved by deciding each one, which was
// the point of baselining them rather than deleting the check:
//
//   - llm.max_concurrent — WIRED by CP60 (BuildStepCompleters). Its note outlived its problem.
//   - runner.disable_multi_turn_fixer — DELETED. It had zero readers UPSTREAM too, so no wave was
//     ever going to bring one; the fixer-hardening bundles came and went without touching it.
//   - runner.auto_seam_refactor_pre_generate, runner.scheduler_interval,
//     runner.post_generate_static_check.* (5), indexer.mono_repo_extra_paths — DELETED. Every
//     consumer is in an excluded package (orchestrator, workflow, session, upstream's own CLI), and
//     no bundle in the port plan brings one. mono_repo_extra_paths is additionally DEPRECATED
//     upstream in favour of mono dependency auto-expansion.
//   - retrieval.persist_last_eval_failure — WIRED (internal/pipeline/failure_hint.go), together
//     with a defect the old note asserted away: it claimed "the READ half is wired", and the read
//     half was NOT. retrieval.failure_hint_file reached PlanOptions.FailureHintFile and stopped
//     there — nothing opened the file. Another instance of the blind spot below.
//
// Only the credential seam remains, and it is structural rather than deferred: see its note.
var exemptConfigFields = map[string]string{
	// The private-registry credential seam is compile-only in core (CP33 / §10.4). These two fields
	// exist to hold the seam open so copied callers compile; the runtime that reads endpoint and
	// scope is on the enterprise side. This is NOT a deferral — no core bundle will ever wire them,
	// and deleting them would break the seam. It is the one shape of exemption that is legitimate.
	"PrivateRegistryCredential.Endpoint": "structural: compile-only credential seam (CP33 / §10.4)",
	"PrivateRegistryCredential.Scope":    "structural: compile-only credential seam (CP33 / §10.4)",
}

// TestEveryConfigFieldIsRead walks config.Config reflectively and asserts each field name is
// referenced somewhere outside internal/config and outside _test.go files.
//
// This is the general fix for a defect class the system review found five instances of:
// documented, configurable, unit-tested, and inert. `retrieval.context_compact.*` had 475 lines
// behind it and no caller; `MaxContextChunks` / `MaxConfigChunks` / `DependencyMaxDepth` were
// settable only through runner.policy; `GapPolicy.Temperature` had zero production call sites.
// A user reading the exhaustively-commented config.example.yaml reasonably assumes the knobs work.
//
// The check is deliberately name-based rather than type-aware: it is a smoke alarm, not a compiler.
// A field referenced only in a comment would pass, but a field nothing mentions anywhere cannot.
func TestEveryConfigFieldIsRead(t *testing.T) {
	root := repoRootFromConfigPkg(t)
	referenced := collectIdentifiersOutsideConfig(t, root)

	var missing []string
	walkStructFields(reflect.TypeOf(Config{}), map[reflect.Type]bool{}, func(owner, field string) {
		if _, ok := exemptConfigFields[owner+"."+field]; ok {
			return
		}
		if _, ok := exemptConfigFields[field]; ok {
			return
		}
		if _, ok := referenced[field]; !ok {
			missing = append(missing, owner+"."+field)
		}
	})

	if len(missing) > 0 {
		t.Fatalf("these config fields are never referenced outside internal/config, so setting them "+
			"in YAML or env does nothing:\n  %s\n\nEither wire the field, delete it (and its "+
			"config.example.yaml entry), or add it to exemptConfigFields with a reason.",
			strings.Join(missing, "\n  "))
	}
}

// walkStructFields visits every exported field of t and its nested structs, reporting
// (ownerTypeName, fieldName). Slices, maps and pointers are followed to their element type.
func walkStructFields(t reflect.Type, seen map[reflect.Type]bool, visit func(owner, field string)) {
	for t != nil && (t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Map) {
		if t.Kind() == reflect.Map {
			t = t.Elem()
			continue
		}
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		visit(t.Name(), f.Name)
		walkStructFields(f.Type, seen, visit)
	}
}

func repoRootFromConfigPkg(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate the module root")
	return ""
}

// configStructDefFile is the one production file inside internal/config that is excluded from the
// scan: it holds the struct definitions themselves, so counting it would make every field trivially
// "referenced" by its own declaration and the lint would never fire.
const configStructDefFile = "config.go"

// collectIdentifiersOutsideConfig returns every identifier and selector name appearing in
// production Go files under internal/, cmd/ and tools/.
//
// internal/config's OWN production files are included (all but configStructDefFile). Before C1 the
// whole package was skipped, which made the check blind to the accessor methods that live there —
// ActiveShip, ActiveGating, ActiveWebhook*, ActiveDefaultOwnerRepo, poolMaxConns,
// EffectiveProjectIntel — and so reported ~20 genuinely-consumed VCS and pool fields as inert. The
// exemption list grew a "TRIAGE: never consumed" entry for each, which was simply wrong.
func collectIdentifiersOutsideConfig(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	configStructDef := filepath.Join(root, "internal", "config", configStructDefFile)
	for _, sub := range []string{"internal", "cmd", "tools"} {
		base := filepath.Join(root, sub)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			if path == configStructDef {
				// The struct-definition file declares every field, so a whole-file scan would make
				// each field self-referencing. Core keeps its accessor methods (EffectiveProjectIntel,
				// ActiveShip, …) in the same file, so instead of skipping the file — which made
				// accessor-read fields false-flag — scan only its FUNCTION BODIES: declarations do
				// not count, reads inside accessors do.
				for _, d := range f.Decls {
					fd, ok := d.(*ast.FuncDecl)
					if !ok || fd.Body == nil {
						continue
					}
					ast.Inspect(fd.Body, func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok {
							out[id.Name] = struct{}{}
						}
						return true
					})
				}
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.Ident:
					out[v.Name] = struct{}{}
				case *ast.SelectorExpr:
					if v.Sel != nil {
						out[v.Sel.Name] = struct{}{}
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", base, err)
		}
	}
	return out
}
