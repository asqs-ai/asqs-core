package dotnetproj

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSolutionReferencedCsprojRelPaths(t *testing.T) {
	dir := t.TempDir()
	sln := filepath.Join(dir, "App.sln")
	body := `Microsoft Visual Studio Solution File, Format Version 12.00
Project("{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}") = "Api", "src\Api\Api.csproj", "{11111111-1111-1111-1111-111111111111}"
EndProject
Project("{2150E333-8FDC-42A3-9474-1A3956D46DE8}") = "Solution Items", "Solution Items", "{33333333-3333-3333-3333-333333333333}"
EndProject
Project("{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}") = "Tests", "tests\Unit\Unit.csproj", "{22222222-2222-2222-2222-222222222222}"
EndProject
`
	if err := os.WriteFile(sln, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	paths, err := solutionReferencedCsprojRelPaths(sln)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != `src\Api\Api.csproj` || paths[1] != `tests\Unit\Unit.csproj` {
		t.Fatalf("got %#v", paths)
	}
}

func TestParseSlnxReferencedCsprojRelPaths(t *testing.T) {
	dir := t.TempDir()
	slnx := filepath.Join(dir, "App.slnx")
	body := `<Solution>
  <Configurations>
    <Platform Name="Any CPU" />
  </Configurations>
  <Folder Name="/src/">
    <Project Path="src/Core/Core.csproj" />
  </Folder>
  <Project Path="tests/Unit/Unit.csproj" />
</Solution>`
	if err := os.WriteFile(slnx, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	paths, err := slnxReferencedCsprojRelPaths(slnx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("want 2 projects, got %#v", paths)
	}
	if paths[0] != "src/Core/Core.csproj" || paths[1] != "tests/Unit/Unit.csproj" {
		t.Fatalf("order or values: %#v", paths)
	}
}
