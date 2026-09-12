package errloc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realVSTestOutput is the `dotnet test` console output captured from a .NET 10 CLI against a
// throwaway xUnit project (internal/evaluator/testdata/dotnet_output). Driving the patterns from a
// recorded run rather than from memory is the point: the shape has details that are easy to get
// wrong from a description, and every one of them decides whether a location is found.
func realVSTestOutput(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "testdata", "dotnet_output", "vstest_failures.txt")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// No pattern anywhere matched `at Ns.Type.Method(...) in <path>.cs:line <n>`, so every runtime C#
// failure lost its file and line: no code window for the fixer, no scope narrowing, no primary-site
// attribution. The compiler's own `File.cs(12,5)` shape was matched, which is why COMPILE failures
// localised and TEST failures did not.
func TestParseLocations_dotnetFrame(t *testing.T) {
	locs := ParseLocations(realVSTestOutput(t))
	if len(locs) == 0 {
		t.Fatal("no locations parsed from a real dotnet test failure")
	}

	want := map[int]bool{10: false, 21: false, 28: false, 35: false}
	for _, l := range locs {
		if !strings.HasSuffix(l.File, "T.cs") {
			t.Errorf("parsed a non-source file %q", l.File)
		}
		if _, ok := want[l.Line]; ok {
			want[l.Line] = true
		}
	}
	for line, found := range want {
		if !found {
			t.Errorf("line %d was never located; got %v", line, locs)
		}
	}
}

func TestParseLocations_dotnetFrameShapes(t *testing.T) {
	cases := []struct {
		name string
		line string
		want *Location
	}{
		{
			name: "typed parameters",
			line: `   at Asqs.Probe.OrderService.Total(Int32 qty, Decimal unit) in /workspace/src/Core/OrderService.cs:line 10`,
			want: &Location{File: "/workspace/src/Core/OrderService.cs", Line: 10},
		},
		{
			name: "no parameters",
			line: `     at Asqs.Probe.OrderServiceTests.Total_multiplies() in /workspace/T.cs:line 21`,
			want: &Location{File: "/workspace/T.cs", Line: 21},
		},
		{
			name: "generic method",
			line: `   at Shop.Repo` + "`1" + `.GetAsync[TResult](String id) in /workspace/src/Repo.cs:line 88`,
			want: &Location{File: "/workspace/src/Repo.cs", Line: 88},
		},
		{
			name: "async state machine",
			line: `   at Shop.Services.OrderService.<CreateAsync>d__7.MoveNext() in /workspace/src/OrderService.cs:line 42`,
			want: &Location{File: "/workspace/src/OrderService.cs", Line: 42},
		},
		{
			name: "a Razor component frame",
			line: `   at Shop.Components.Counter.Increment() in /workspace/src/Components/Counter.razor:line 14`,
			want: &Location{File: "/workspace/src/Components/Counter.razor", Line: 14},
		},
		{
			name: "a Razor Pages code-behind frame",
			line: `   at Shop.Pages.Orders.IndexModel.OnGet() in /workspace/src/Pages/Orders/Index.cshtml.cs:line 19`,
			want: &Location{File: "/workspace/src/Pages/Orders/Index.cshtml.cs", Line: 19},
		},
		{
			name: "a Windows path",
			line: `   at Shop.Api.Handler.Run() in C:\src\Shop\Api\Handler.cs:line 7`,
			want: &Location{File: `C:\src\Shop\Api\Handler.cs`, Line: 7},
		},
		{
			name: "a path with spaces in a directory name",
			line: `   at Shop.Api.Handler.Run() in /workspace/My Project/Handler.cs:line 7`,
			want: &Location{File: "/workspace/My Project/Handler.cs", Line: 7},
		},
		{
			name: "a framework frame carries no file at all",
			line: `   at System.Reflection.MethodBaseInvoker.InterpretedInvoke_Method(Object obj, IntPtr* args)`,
			want: nil,
		},
		{
			name: "a frame with no line number is not a location",
			line: `   at Shop.Api.Handler.Run() in /workspace/src/Handler.cs`,
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locs := ParseLocations(tc.line)
			if tc.want == nil {
				for _, l := range locs {
					if strings.Contains(l.File, ".cs") || strings.Contains(l.File, ".razor") {
						t.Fatalf("parsed %+v from a line that names no source location", l)
					}
				}
				return
			}
			found := false
			for _, l := range locs {
				if l.File == tc.want.File && l.Line == tc.want.Line {
					found = true
				}
			}
			if !found {
				t.Fatalf("locations = %+v, want %+v", locs, *tc.want)
			}
		})
	}
}

// The compiler shape already worked and must keep working: a regression here would trade a fixed
// test failure for a broken compile failure.
func TestParseLocations_msbuildShapeStillWorks(t *testing.T) {
	const line = `/workspace/Broken.cs(7,11): error CS1061: 'OrderService' does not contain a definition for 'NoSuchMethod'`
	locs := ParseLocations(line)
	found := false
	for _, l := range locs {
		if l.File == "/workspace/Broken.cs" && l.Line == 7 {
			found = true
		}
	}
	if !found {
		t.Fatalf("locations = %+v, want /workspace/Broken.cs:7", locs)
	}
}

// ExtraReadableRepoPaths feeds the fixer's file budget; a path found only in a stack frame has to
// reach it, or the fixer reads everything except the file that threw.
func TestExtraReadableRepoPaths_includesFramePaths(t *testing.T) {
	repo := t.TempDir()
	writeAt(t, filepath.Join(repo, "src", "Core", "OrderService.cs"), "namespace N; public class OrderService {}")

	out := ExtraReadableRepoPaths(
		"   at N.OrderService.Total(Int32 q) in "+filepath.Join(repo, "src", "Core", "OrderService.cs")+":line 10",
		repo, nil, 10)
	if len(out) != 1 || out[0] != "src/Core/OrderService.cs" {
		t.Fatalf("ExtraReadableRepoPaths = %v, want [src/Core/OrderService.cs]", out)
	}
}

func writeAt(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
