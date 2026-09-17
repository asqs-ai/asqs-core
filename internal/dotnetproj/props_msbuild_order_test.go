package dotnetproj

import (
	"reflect"
	"testing"
)

// MSBuild imports Directory.Build.props at the TOP of a project and Directory.Build.targets at the
// BOTTOM, so an unconditional property in .targets overrides the project's own. Setting a property
// in .targets specifically to win over projects is the reason people reach for .targets at all.
func TestResolveFacts_targetsOverrideTheProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.targets", `<Project><PropertyGroup>
  <TargetFramework>net8.0</TargetFramework><Nullable>enable</Nullable>
</PropertyGroup></Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup>
  <TargetFramework>net6.0</TargetFramework><Nullable>disable</Nullable>
</PropertyGroup></Project>`)

	f, err := ResolveFacts(root, csproj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.TFMs, []string{"net8.0"}) {
		t.Errorf("TFMs = %v, want [net8.0]: Directory.Build.targets is imported after the project", f.TFMs)
	}
	if f.Nullable != "enable" {
		t.Errorf("Nullable = %q, want enable from .targets", f.Nullable)
	}
}

// .props still loses to the project, which is the other half of the same rule.
func TestResolveFacts_propsLoseToTheProject(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project><PropertyGroup><TargetFramework>net6.0</TargetFramework></PropertyGroup></Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)

	f, _ := ResolveFacts(root, csproj)
	if !reflect.DeepEqual(f.TFMs, []string{"net8.0"}) {
		t.Errorf("TFMs = %v, want [net8.0]: the project wins over .props", f.TFMs)
	}
}

// MSBuild stops at the FIRST Directory.Build.props walking up; a nearer file that does not import
// its parent shadows everything above it. Reading every ancestor invents properties that do not
// apply, and C# generation reads Nullable and ImplicitUsings to decide what to emit.
func TestResolveFacts_walkStopsAtTheNearestProps(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project><PropertyGroup>
  <Nullable>enable</Nullable><ImplicitUsings>enable</ImplicitUsings>
</PropertyGroup></Project>`)
	write(t, root, "legacy/Directory.Build.props", `<Project><PropertyGroup><LangVersion>7.3</LangVersion></PropertyGroup></Project>`)
	csproj := write(t, root, "legacy/Old/Old.csproj", `<Project Sdk="Microsoft.NET.Sdk"></Project>`)

	f, _ := ResolveFacts(root, csproj)
	if f.Nullable != "" {
		t.Errorf("Nullable = %q, want empty: legacy/Directory.Build.props shadows the root one", f.Nullable)
	}
	if f.ImplicitUsings {
		t.Error("ImplicitUsings = true, want false: the root props file does not apply here")
	}
	if f.LangVersion != "7.3" {
		t.Errorf("LangVersion = %q, want 7.3 from the nearest props", f.LangVersion)
	}
	if len(f.PropsPaths) != 1 {
		t.Errorf("PropsPaths = %v, want only the nearest file", f.PropsPaths)
	}
}

// A props file that chains to its parent the standard way keeps the whole chain.
func TestResolveFacts_walkFollowsAnExplicitParentImport(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project><PropertyGroup><Nullable>enable</Nullable></PropertyGroup></Project>`)
	write(t, root, "src/Directory.Build.props", `<Project>
  <Import Project="$([MSBuild]::GetPathOfFileAbove($(MSBuildThisFile), $(MSBuildThisFileDirectory)..))" />
  <PropertyGroup><LangVersion>12.0</LangVersion></PropertyGroup>
</Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk"></Project>`)

	f, _ := ResolveFacts(root, csproj)
	if f.Nullable != "enable" {
		t.Errorf("Nullable = %q, want enable: src props imports its parent", f.Nullable)
	}
	if f.LangVersion != "12.0" {
		t.Errorf("LangVersion = %q, want 12.0", f.LangVersion)
	}
	if len(f.PropsPaths) != 2 {
		t.Errorf("PropsPaths = %v, want both files", f.PropsPaths)
	}
}

// Items are inherited too. Factoring the test-harness PackageReferences into a shared
// tests/Directory.Build.props is the layout ResolveFacts exists to support, and the coverage gate
// reported "no coverlet referenced by any project" for exactly this shape.
func TestResolveFacts_packageReferencesFromAncestorProps(t *testing.T) {
	root := t.TempDir()
	write(t, root, "tests/Directory.Build.props", `<Project><ItemGroup>
  <PackageReference Include="coverlet.collector" Version="6.0.2" />
  <PackageReference Include="Microsoft.NET.Test.Sdk" Version="17.11.1" />
</ItemGroup></Project>`)
	csproj := write(t, root, "tests/App.Tests/App.Tests.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="xunit" Version="2.9.2" /></ItemGroup>
</Project>`)

	f, _ := ResolveFacts(root, csproj)
	for id, want := range map[string]string{
		"coverlet.collector":     "6.0.2",
		"Microsoft.NET.Test.Sdk": "17.11.1",
		"xunit":                  "2.9.2",
	} {
		got, ok := f.PackageVersion(id)
		if !ok {
			t.Errorf("PackageVersion(%q): not referenced", id)
			continue
		}
		if got != want {
			t.Errorf("PackageVersion(%q) = %q, want %q", id, got, want)
		}
	}
}

// The project's own version wins over an inherited one, the same way properties do.
func TestResolveFacts_projectPackageVersionWins(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Directory.Build.props", `<Project><ItemGroup>
  <PackageReference Include="Moq" Version="4.18.0" />
</ItemGroup></Project>`)
	csproj := write(t, root, "src/App/App.csproj", `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup><PackageReference Include="Moq" Version="4.20.72" /></ItemGroup>
</Project>`)

	f, _ := ResolveFacts(root, csproj)
	if v, _ := f.PackageVersion("Moq"); v != "4.20.72" {
		t.Errorf("PackageVersion(Moq) = %q, want the project's 4.20.72", v)
	}
}
