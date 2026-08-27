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
// KNOWN BLIND SPOT. The check matches identifiers, not types, so a field counts as read when any
// package declares a field of the same name. It has missed two keys for exactly that reason:
// runner.prefer_default_test_suffix (orchestrator.Config also has a PreferDefaultTestSuffix) and
// general.git.ship.allow_partial (session.ShipDecision also has an AllowPartial). Both were
// eventually caught by review, not by this test. Closing the gap needs go/types to resolve each
// selector to its declaring type; until then, a config field whose name is duplicated elsewhere in
// the tree is worth confirming by hand.
var exemptConfigFields = map[string]string{
	// TRIAGE: llm.max_concurrent has zero readers because core's gap loop is sequential — the
	// concurrency-limited completer that consumes it is upstream orchestrator machinery. CP60
	// decides wire-or-delete alongside runner.gap_concurrency (D15).
	// TRIAGE: the private-registry credential seam is compile-only in core (CP33 / §10.4); the
	// runtime that reads endpoint/scope is on the enterprise side of the seam.
	"PrivateRegistryCredential.Endpoint": "TRIAGE: compile-only credential seam (CP33)",
	"PrivateRegistryCredential.Scope":    "TRIAGE: compile-only credential seam (CP33)",
	// TRIAGE: consumed by the multi-turn fixer wave (CP50–CP53); the key predates its feature.
	"RunnerConfig.DisableMultiTurnFixer": "TRIAGE: fixer-hardening wave (CP50–CP53) brings the reader",
	// TRIAGE: read by the generator merge's suffix policy; arrives with CP49's llm_generator
	// reconciliation. Note upstream's lint has a KNOWN BLIND SPOT on exactly this name.
	"RunnerConfig.PreferDefaultTestSuffix": "TRIAGE: CP49 generator merge brings the reader",
	// TRIAGE: pre-generation seam refactor is a later wave (P7); key shipped ahead of it.
	"RunnerConfig.AutoSeamRefactorPreGenerate": "TRIAGE: seam-refactor bundle brings the reader",
	// TRIAGE: the CLI pipeline is one-shot; the interval belongs to the serve-mode scheduler,
	// which is enterprise-excluded. Candidate for deletion in CP38's re-key.
	"RunnerConfig.SchedulerInterval": "TRIAGE: serve-mode scheduler excluded from core; CP38 candidate delete",
	// TRIAGE: the post-generate static micro-gate (language lint before evaluation) is CP50's;
	// the whole block waits for its feature.
	"RunnerConfig.PostGenerateStaticCheck":        "TRIAGE: post-generate static gate arrives with CP50",
	"PostGenerateStaticCheckConfig.FailStopsEval": "TRIAGE: post-generate static gate arrives with CP50",
	"PostGenerateStaticCheckConfig.JavaCommand":   "TRIAGE: post-generate static gate arrives with CP50",
	"PostGenerateStaticCheckConfig.NodeCommand":   "TRIAGE: post-generate static gate arrives with CP50",
	"PostGenerateStaticCheckConfig.CSharpCommand": "TRIAGE: post-generate static gate arrives with CP50",
	// TRIAGE: mono-repo extra scan roots; the scanner consuming them is part of the mono-repo
	// workspace wave. mono_repo_workspace itself IS wired (buildPlanOptions).
	"IndexerConfig.MonoRepoExtraPaths": "TRIAGE: mono-repo scan wave brings the reader",
	// TRIAGE: the persist half of the failure-hint loop (write .asqs/last-eval-failure.log after
	// evaluation) is the fix-loop wave's; the READ half (failure_hint_file) is wired.
	"RetrievalConfig.PersistLastEvalFailure": "TRIAGE: fix-loop wave brings the writer (read half is wired)",
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
