package uitesthooks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// typescriptResolveDir is a directory whose node_modules carries the typescript package: the
// repository's own js-ts-indexer tool. The script resolves typescript from the repository under
// test in production; here the fixture repository has no node_modules.
func typescriptResolveDir(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(here), "..", "..", "tools", "js-ts-indexer")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "typescript")); err != nil {
		t.Skipf("typescript not vendored under %s: %v", dir, err)
	}
	return dir
}

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
}

const homePage = `import { Link } from 'react-router-dom';
import { Panel } from './Panel';

export function HomePage({ items }: { items: string[] }) {
  if (!items) {
    return <p>Nothing yet</p>;
  }
  return (
    <div className="space-y-4">
      <h1 className="text-xl">Welcome to ASQS</h1>
      <Panel title="x" />
      <nav aria-label="Main navigation">
        <ul>
          {items.map((it) => (
            <li key={it}><Link to={'/' + it}>{it}</Link></li>
          ))}
        </ul>
      </nav>
      <button type="button" onClick={() => alert(1)}>Go to orders</button>
      <button data-testid="already">Keep</button>
      <input {...rest} placeholder="Search" />
      <a href="/settings/profile">Profile</a>
    </div>
  );
}

export const Footer = () => <footer>© ASQS</footer>;
`

func TestApply_addsHooksToIntrinsicJSXOnly(t *testing.T) {
	requireNode(t)
	tsDir := typescriptResolveDir(t)
	root := t.TempDir()
	rel := "src/pages/HomePage.tsx"
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(homePage), 0o644); err != nil {
		t.Fatal(err)
	}
	var journalled []string
	plan := PlanTargets(root, Options{})
	res, err := Apply(context.Background(), root, plan, Options{}, func(_ string, rel string) { journalled = append(journalled, rel) }, tsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Rel != rel {
		t.Fatalf("applied = %+v failed = %+v", res.Applied, res.Failed)
	}
	if strings.Join(journalled, ",") != rel {
		t.Fatalf("the caller must be told before the write; journalled=%v", journalled)
	}
	out, _ := os.ReadFile(full)
	src := string(out)
	for _, want := range []string{
		`<p data-testid="home-page-root">Nothing yet</p>`,            // early return is a root too
		`<div data-testid="home-page-root-2" className="space-y-4">`, // second root, unique name
		`<h1 data-testid="home-page-heading-welcome-to-asqs"`,        // literal heading text
		`<nav data-testid="home-page-nav-main-navigation"`,           // aria-label
		`<ul data-testid="home-page-list">`,
		`<li data-testid="home-page-item" key={it}>`, // dynamic text → no slug
		`<button data-testid="home-page-button-go-to-orders" type="button"`,
		`<a data-testid="home-page-link-profile" href="/settings/profile">`,
		`<footer data-testid="home-page-root-3">© ASQS</footer>`, // arrow component root
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in:\n%s", want, src)
		}
	}
	for _, mustNot := range []string{
		`<Panel data-testid`, // components are never touched
		`<Link data-testid`,  // components are never touched
		`<button data-testid="home-page-button-keep" data-testid="already">`, // existing hook kept alone
		`<input data-testid`, // spread attribute → skipped
	} {
		if strings.Contains(src, mustNot) {
			t.Errorf("must not contain %q:\n%s", mustNot, src)
		}
	}
	if res.Applied[0].Skipped != 2 {
		t.Errorf("skipped = %d, want 2 (pre-hooked button, spread input)", res.Applied[0].Skipped)
	}

	// Idempotent: a second run writes nothing.
	again, err := Apply(context.Background(), root, PlanTargets(root, Options{}), Options{}, nil, tsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Applied) != 0 || len(again.Unchanged) != 1 {
		t.Fatalf("second run must be a no-op; applied=%v unchanged=%v", again.Applied, again.Unchanged)
	}
}

func TestApply_perFileCapAndSyntaxErrorRefusal(t *testing.T) {
	requireNode(t)
	tsDir := typescriptResolveDir(t)
	root := t.TempDir()
	many := "export const X = () => (<div>" + strings.Repeat("<button>b</button>", 10) + "</div>);\n"
	broken := "export const Y = () => (<div><button>unclosed</div>);\n"
	for rel, src := range map[string]string{"src/Many.tsx": many, "src/Broken.tsx": broken} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Apply(context.Background(), root, PlanTargets(root, Options{}), Options{MaxPerFile: 3}, nil, tsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Applied {
		if a.Rel == "src/Many.tsx" && len(a.Added) != 3 {
			t.Errorf("cap ignored: %d added", len(a.Added))
		}
	}
	// A file that already fails to parse gets edited only if parsing does not get worse; either
	// way nothing may be made worse, and the outcome must be reported rather than silently dropped.
	out, _ := os.ReadFile(filepath.Join(root, "src/Broken.tsx"))
	if strings.Count(string(out), "data-testid") > 2 {
		t.Errorf("broken file received more hooks than elements: %s", out)
	}
	if len(res.Applied)+len(res.Unchanged)+len(res.Failed) != 2 {
		t.Errorf("every target must be accounted for: %+v", res)
	}
}
