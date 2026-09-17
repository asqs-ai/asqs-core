package runner

import (
	"os"
	"path/filepath"
	"testing"
)

// `dotnet test --collect "XPlat Code Coverage"` produces a report only when a test project
// references coverlet.collector; without it the collector is not registered and the step just
// re-runs the whole suite for nothing, then reports "coverage report not found".
//
// Java has had the mirror-image gate since JaCoCo: no plugin declared, no coverage step. The C#
// side had none, so every C# run paid for a second full test execution and reported no coverage —
// which is what the validation fixture did.
func TestDotnetCoverageAvailable(t *testing.T) {
	cases := []struct {
		name        string
		files       map[string]string
		wantOK      bool
		wantPackage string
	}{
		{
			name: "coverlet.collector on a test project",
			files: map[string]string{
				"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="coverlet.collector" Version="6.0.2" /></ItemGroup>
</Project>`,
			},
			wantOK:      true,
			wantPackage: "coverlet.collector",
		},
		{
			name: "coverlet.msbuild counts too",
			files: map[string]string{
				"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="coverlet.msbuild" Version="6.0.2" /></ItemGroup>
</Project>`,
			},
			wantOK:      true,
			wantPackage: "coverlet.msbuild",
		},
		{
			name: "through central package management, where the reference carries no version",
			files: map[string]string{
				"Directory.Packages.props": `<Project><ItemGroup>
  <PackageVersion Include="coverlet.collector" Version="6.0.2" />
</ItemGroup></Project>`,
				"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="coverlet.collector" /></ItemGroup>
</Project>`,
			},
			wantOK:      true,
			wantPackage: "coverlet.collector",
		},
		{
			name: "no coverlet anywhere",
			files: map[string]string{
				"src/App/App.csproj": `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
				"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="xunit" Version="2.9.2" /></ItemGroup>
</Project>`,
			},
			wantOK: false,
		},
		{
			name:   "no projects at all",
			files:  map[string]string{"README.md": "hi"},
			wantOK: false,
		},
		{
			name: "commented-out reference does not count",
			files: map[string]string{
				"tests/App.Tests/App.Tests.csproj": `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><!-- <PackageReference Include="coverlet.collector" Version="6.0.2" /> --></ItemGroup>
</Project>`,
			},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			for rel, body := range tc.files {
				p := filepath.Join(repo, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			pkg, ok := dotnetCoverageCollectorDeclared(repo)
			if ok != tc.wantOK {
				t.Fatalf("declared = %v (%q), want %v", ok, pkg, tc.wantOK)
			}
			if tc.wantOK && pkg != tc.wantPackage {
				t.Fatalf("package = %q, want %q", pkg, tc.wantPackage)
			}
		})
	}
}

// The skip reason has to name the package, because the operator's fix is to add it.
func TestDotnetCoverageSkipReason(t *testing.T) {
	reason := dotnetCoverageSkipReason()
	for _, want := range []string{"coverlet.collector", "skip"} {
		if !containsFold(reason, want) {
			t.Fatalf("skip reason %q does not mention %q", reason, want)
		}
	}
}

func containsFold(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexFold(haystack, needle) >= 0)
}

func indexFold(h, n string) int {
	hl, nl := toLowerASCII(h), toLowerASCII(n)
	for i := 0; i+len(nl) <= len(hl); i++ {
		if hl[i:i+len(nl)] == nl {
			return i
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
