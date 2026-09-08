package testbootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalPom = `<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>demo</artifactId>
  <version>1.0</version>
  <dependencies>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>5.11.3</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
  <build>
    <plugins>
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-surefire-plugin</artifactId>
        <version>3.5.2</version>
      </plugin>
    </plugins>
  </build>
</project>
`

// The evaluator's Java E2E pass invokes Failsafe (evaluator/e2e_command.go). The bootstrap must
// declare it so the version is pinned and *E2EIT classes are included, but without lifecycle
// executions so the repository's own `mvn verify` keeps its previous behaviour.
func TestApplyMavenPlaywrightE2E_declaresFailsafeWithoutExecutions(t *testing.T) {
	dir := t.TempDir()
	pom := filepath.Join(dir, "pom.xml")
	if err := os.WriteFile(pom, []byte(minimalPom), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := applyMavenPlaywrightE2E(pom)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(pom)
	s := string(b)
	for _, want := range []string{
		"<artifactId>maven-failsafe-plugin</artifactId>",
		"<version>" + VersionMavenFailsafePlugin + "</version>",
		"<include>**/*E2EIT.java</include>",
		"com.microsoft.playwright",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("pom lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "<executions>") {
		t.Errorf("Failsafe must not be bound to the lifecycle:\n%s", s)
	}
	if strings.Count(s, "maven-surefire-plugin") != 1 {
		t.Errorf("surefire declared more than once:\n%s", s)
	}
	if !strings.Contains(s, "</plugins>\n  </build>") {
		t.Errorf("plugins block no longer well-formed:\n%s", s)
	}
	// Idempotent: a second pass changes nothing.
	changed, err = applyMavenPlaywrightE2E(pom)
	if err != nil || changed {
		t.Fatalf("second pass: changed=%v err=%v", changed, err)
	}
}

func TestInsertMavenBuildPlugin_createsBuildAndPluginsWhenAbsent(t *testing.T) {
	pom := "<project>\n  <artifactId>x</artifactId>\n</project>\n"
	out, err := insertMavenFailsafePlugin(pom)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<build>", "<plugins>", "maven-failsafe-plugin", "</plugins>", "</build>", "</project>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "</build>") > strings.Index(out, "</project>") {
		t.Errorf("build block landed after </project>:\n%s", out)
	}
}
