package javaproj

import "testing"

const bootParentPom = `<?xml version="1.0"?>
<project>
  <parent>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-starter-parent</artifactId>
    <version>3.2.5</version>
  </parent>
  <properties>
    <java.version>17</java.version>
  </properties>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-starter-test</artifactId>
      <scope>test</scope>
    </dependency>
    <!-- <dependency><artifactId>should-be-ignored</artifactId><scope>test</scope></dependency> -->
  </dependencies>
</project>`

// The parent's <version> IS the Boot version — the single most valuable fact, obtainable with no
// POM resolution at all.
func TestParseSpringBootVersion_fromStarterParent(t *testing.T) {
	if got := ParseSpringBootVersion(bootParentPom, nil); got != "3.2.5" {
		t.Fatalf("ParseSpringBootVersion = %q, want 3.2.5", got)
	}
}

func TestParseJavaVersion_precedence(t *testing.T) {
	tests := []struct {
		name string
		pom  string
		want string
	}{
		{"java.version property", `<project><properties><java.version>21</java.version></properties></project>`, "21"},
		{"maven.compiler.release", `<project><properties><maven.compiler.release>17</maven.compiler.release></properties></project>`, "17"},
		{"maven.compiler.source", `<project><properties><maven.compiler.source>11</maven.compiler.source></properties></project>`, "11"},
		{"release element", `<project><build><plugins><plugin><configuration><release>20</release></configuration></plugin></plugins></build></project>`, "20"},
		{"absent", `<project></project>`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseJavaVersion(tc.pom, nil); got != tc.want {
				t.Fatalf("ParseJavaVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

// An unresolved ${...} must never be emitted: it would state a version that does not exist.
func TestResolveProperty_indirection(t *testing.T) {
	props := map[string]string{"spring-boot.version": "${revision}", "revision": "3.3.0", "loop": "${loop}"}
	tests := []struct{ in, want string }{
		{"3.2.5", "3.2.5"},
		{"${revision}", "3.3.0"},
		{"${spring-boot.version}", "3.3.0"}, // one extra hop
		{"${missing}", ""},
		{"${loop}", ""}, // must not emit a literal ${...}
	}
	for _, tc := range tests {
		if got := ResolveProperty(tc.in, props); got != tc.want {
			t.Errorf("ResolveProperty(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseTestDependencies(t *testing.T) {
	deps := ParseTestDependencies(bootParentPom, nil)
	if len(deps) != 1 {
		t.Fatalf("got %d deps, want 1 (the commented-out block must be ignored): %v", len(deps), deps)
	}
	if got := deps[0].String(); got != "org.springframework.boot:spring-boot-starter-test" {
		t.Fatalf("dep = %q", got)
	}
}

func TestParseTestDependencies_includesKnownTestArtifactsWithoutExplicitScope(t *testing.T) {
	pom := `<project><dependencies>
	  <dependency><groupId>org.mockito</groupId><artifactId>mockito-core</artifactId></dependency>
	  <dependency><groupId>com.acme</groupId><artifactId>app-core</artifactId></dependency>
	</dependencies></project>`
	deps := ParseTestDependencies(pom, nil)
	if len(deps) != 1 || deps[0].ArtifactID != "mockito-core" {
		t.Fatalf("got %v, want only mockito-core", deps)
	}
}

func TestStripXMLComments(t *testing.T) {
	if got := StripXMLComments("a<!-- b -->c"); got != "ac" {
		t.Fatalf("StripXMLComments = %q", got)
	}
}

func TestMajorVersion(t *testing.T) {
	for in, want := range map[string]string{"4.0.0": "4", "3.2.5": "3", "21": "21", "3.0.0-SNAPSHOT": "3", "": ""} {
		if got := MajorVersion(in); got != want {
			t.Errorf("MajorVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
