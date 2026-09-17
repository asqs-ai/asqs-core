package evaluator

import (
	"os"
	"path/filepath"
	"testing"
)

func writeScopeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func scopeIndexOf(hay []string, needle string) int {
	for i, h := range hay {
		if h == needle {
			return i
		}
	}
	return -1
}

// The scope is an ORDER, because the consumer takes the front of it. Artifacts first, then what each
// is written against — the type a generated test cannot resolve is usually declared in the source
// project's imports, not the test project's.
func TestGlobalUsingsScope_artifactsThenWhatTheyAreWrittenAgainst(t *testing.T) {
	root := writeScopeRepo(t, map[string]string{
		"src/App.Core/Widget.cs":         "namespace App.Core;\npublic class Widget { }\n",
		"tests/App.Tests/WidgetTests.cs": "namespace App.Tests;\npublic class WidgetTests { }\n",
		"src/App.Core/Helper.cs":         "namespace App.Core;\npublic class Helper { }\n",
	})
	opts := EvalOptions{
		Lang: "csharp", RepoPath: root,
		ArtifactDependencies: map[string][]string{
			"tests/App.Tests/WidgetTests.cs": {"src/App.Core/Helper.cs"},
		},
	}

	got := globalUsingsScope(opts, []string{"tests/App.Tests/WidgetTests.cs"})
	if len(got) == 0 || got[0] != "tests/App.Tests/WidgetTests.cs" {
		t.Fatalf("the artifact must come first, got %v", got)
	}
	if scopeIndexOf(got, "src/App.Core/Helper.cs") < 0 {
		t.Errorf("a declared dependency must be in scope, got %v", got)
	}
	if scopeIndexOf(got, "src/App.Core/Widget.cs") < 0 {
		t.Errorf("the source under test must be in scope, got %v", got)
	}
}

// Nothing from the prompt at large. In a monorepo that is what put a sibling application's
// namespaces in front of the fixer.
func TestGlobalUsingsScope_isNotThePromptAtLarge(t *testing.T) {
	root := writeScopeRepo(t, map[string]string{
		"src/App.Core/Widget.cs":             "namespace App.Core;\npublic class Widget { }\n",
		"tests/App.Tests/WidgetTests.cs":     "namespace App.Tests;\npublic class WidgetTests { }\n",
		"sample/src/Other.Core/Widget.cs":    "namespace Other.Core;\npublic class Widget { }\n",
		"MinimalClean/src/Min.Web/Widget.cs": "namespace Min;\npublic class Widget { }\n",
	})
	opts := EvalOptions{Lang: "csharp", RepoPath: root}

	got := globalUsingsScope(opts, []string{"tests/App.Tests/WidgetTests.cs"})
	for _, g := range got {
		if g == "sample/src/Other.Core/Widget.cs" || g == "MinimalClean/src/Min.Web/Widget.cs" {
			t.Errorf("a sibling application's file entered the scope: %v", got)
		}
	}
}

// The source mapping is language-aware, so a Java or JS/TS round building the same scope reaches the
// right file too — the ordering rule does not have to be re-derived per ecosystem.
func TestGlobalUsingsScope_resolvesTheSourceForEveryMappedLanguage(t *testing.T) {
	cases := []struct {
		lang, artifact, source string
		files                  map[string]string
	}{
		{
			lang:     "java",
			artifact: "src/test/java/app/WidgetTest.java",
			source:   "src/main/java/app/Widget.java",
			files: map[string]string{
				"src/main/java/app/Widget.java":     "package app;\nclass Widget { }\n",
				"src/test/java/app/WidgetTest.java": "package app;\nclass WidgetTest { }\n",
			},
		},
		{
			lang:     "csharp",
			artifact: "tests/App.Tests/WidgetTests.cs",
			source:   "src/App.Core/Widget.cs",
			files: map[string]string{
				"src/App.Core/Widget.cs":         "namespace App.Core;\npublic class Widget { }\n",
				"tests/App.Tests/WidgetTests.cs": "namespace App.Tests;\npublic class WidgetTests { }\n",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			root := writeScopeRepo(t, c.files)
			got := globalUsingsScope(EvalOptions{Lang: c.lang, RepoPath: root}, []string{c.artifact})
			if scopeIndexOf(got, c.source) < 0 {
				t.Errorf("%s: source %s not in scope, got %v", c.lang, c.source, got)
			}
			if got[0] != c.artifact {
				t.Errorf("%s: artifact must lead, got %v", c.lang, got)
			}
		})
	}
}

// Duplicates cost cap slots, and an artifact that is also someone's dependency must not take two.
func TestGlobalUsingsScope_dedupesAndKeepsFirstPosition(t *testing.T) {
	root := writeScopeRepo(t, map[string]string{
		"src/App.Core/Widget.cs":         "namespace App.Core;\npublic class Widget { }\n",
		"tests/App.Tests/WidgetTests.cs": "namespace App.Tests;\npublic class WidgetTests { }\n",
	})
	opts := EvalOptions{
		Lang: "csharp", RepoPath: root,
		ArtifactDependencies: map[string][]string{
			"tests/App.Tests/WidgetTests.cs": {"src/App.Core/Widget.cs", "src/App.Core/Widget.cs"},
		},
	}
	got := globalUsingsScope(opts, []string{
		"tests/App.Tests/WidgetTests.cs", "tests/App.Tests/WidgetTests.cs",
	})
	seen := map[string]int{}
	for _, g := range got {
		seen[g]++
	}
	for g, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times: %v", g, n, got)
		}
	}
}
