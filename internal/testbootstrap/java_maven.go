package testbootstrap

import (
	"fmt"
	"os"
	"strings"
)

const mavenJUnitDep = `
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter</artifactId>
      <version>` + VersionJUnitJupiter + `</version>
      <scope>test</scope>
    </dependency>`

// renderMavenDep emits a test-scoped <dependency>. A dep with no Version is rendered without a
// <version> element so the framework's parent POM or BOM supplies it.
func renderMavenDep(d javaDep) string {
	var b strings.Builder
	b.WriteString("\n    <dependency>")
	b.WriteString("\n      <groupId>" + d.GroupID + "</groupId>")
	b.WriteString("\n      <artifactId>" + d.ArtifactID + "</artifactId>")
	if d.Version != "" {
		b.WriteString("\n      <version>" + d.Version + "</version>")
	}
	b.WriteString("\n      <scope>test</scope>")
	b.WriteString("\n    </dependency>")
	return b.String()
}

const mavenSurefirePlugin = `
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-surefire-plugin</artifactId>
        <version>` + VersionMavenSurefirePlugin + `</version>
      </plugin>`

// applyMavenTestDeps merges the profile's required test dependencies (and Surefire, where the
// profile asks for it) into pom.xml.
//
// This replaced a hardcoded junit-jupiter insert. The old behaviour patched every Java project
// identically, so a Spring Boot module got bare JUnit and every generated test that touched
// @SpringBootTest, Mockito or AssertJ failed to compile against a manifest the fix loop may not
// write. What goes in now is decided by javaTestProfile, from the framework actually detected.
func applyMavenTestDeps(pomPath string, prof javaTestProfile) (changed bool, added []javaDep, err error) {
	b, err := os.ReadFile(pomPath)
	if err != nil {
		return false, nil, err
	}
	s := string(b)
	orig := s

	for _, d := range prof.missingDeps(s, true) {
		s, err = insertMavenDependency(s, d)
		if err != nil {
			return false, nil, err
		}
		added = append(added, d)
	}

	if prof.NeedsSurefirePlugin && !strings.Contains(s, "maven-surefire-plugin") {
		s, err = insertMavenSurefirePlugin(s)
		if err != nil {
			return false, nil, err
		}
	}

	if s == orig {
		return false, nil, nil
	}
	return true, added, atomicWrite(pomPath, []byte(s))
}

// insertMavenDependency places one rendered dependency inside <dependencies>, creating the section
// when the POM has none.
func insertMavenDependency(pom string, d javaDep) (string, error) {
	const open = "<dependencies>"
	const closeTag = "</dependencies>"
	block := renderMavenDep(d)
	start := strings.Index(pom, open)
	if start < 0 {
		return insertBeforeClosingProject(pom, "  <dependencies>"+block+"\n  </dependencies>\n\n"), nil
	}
	afterOpen := start + len(open)
	if closeIdx := strings.Index(pom[afterOpen:], closeTag); closeIdx < 0 {
		return "", fmt.Errorf("pom.xml: unclosed <dependencies>")
	}
	return pom[:afterOpen] + block + "\n" + pom[afterOpen:], nil
}

func insertMavenJUnitDependency(pom string) (string, error) {
	const open = "<dependencies>"
	const close = "</dependencies>"
	if strings.Contains(pom, "junit-jupiter") {
		return pom, nil
	}
	start := strings.Index(pom, open)
	if start < 0 {
		// No dependencies section: insert before </project>
		block := "  <dependencies>" + mavenJUnitDep + "\n  </dependencies>\n\n"
		return insertBeforeClosingProject(pom, block), nil
	}
	afterOpen := start + len(open)
	closeIdx := strings.Index(pom[afterOpen:], close)
	if closeIdx < 0 {
		return "", fmt.Errorf("pom.xml: unclosed <dependencies>")
	}
	closeIdx += afterOpen
	inner := pom[afterOpen:closeIdx]
	if strings.Contains(inner, "junit-jupiter") {
		return pom, nil
	}
	return pom[:afterOpen] + mavenJUnitDep + "\n" + pom[afterOpen:], nil
}

func insertMavenSurefirePlugin(pom string) (string, error) {
	if strings.Contains(pom, "maven-surefire-plugin") {
		return pom, nil
	}
	const openBuild = "<build>"
	const closeBuild = "</build>"
	const openPlugins = "<plugins>"
	const closePlugins = "</plugins>"

	bidx := strings.Index(pom, openBuild)
	if bidx < 0 {
		block := "  <build>\n    <plugins>" + mavenSurefirePlugin + "\n    </plugins>\n  </build>\n\n"
		return insertBeforeClosingProject(pom, block), nil
	}
	// Find first </build> after <build> (flat POM assumption).
	afterB := bidx + len(openBuild)
	endBuild := strings.Index(pom[afterB:], closeBuild)
	if endBuild < 0 {
		return "", fmt.Errorf("pom.xml: unclosed <build>")
	}
	endBuild += afterB
	buildInner := pom[afterB:endBuild]

	pidx := strings.Index(buildInner, openPlugins)
	if pidx < 0 {
		// <build> without <plugins>: inject plugins block
		insert := "\n    <plugins>" + mavenSurefirePlugin + "\n    </plugins>\n"
		return pom[:afterB] + insert + pom[afterB:], nil
	}
	afterP := afterB + pidx + len(openPlugins)
	relEnd := strings.Index(pom[afterP:], closePlugins)
	if relEnd < 0 {
		return "", fmt.Errorf("pom.xml: unclosed <plugins>")
	}
	relEnd += afterP
	return pom[:relEnd] + mavenSurefirePlugin + "\n" + pom[relEnd:], nil
}

func insertBeforeClosingProject(pom, snippet string) string {
	idx := strings.LastIndex(pom, "</project>")
	if idx < 0 {
		return pom + snippet
	}
	return pom[:idx] + snippet + pom[idx:]
}
