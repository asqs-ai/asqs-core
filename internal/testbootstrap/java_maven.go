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

const mavenSurefirePlugin = `
      <plugin>
        <groupId>org.apache.maven.plugins</groupId>
        <artifactId>maven-surefire-plugin</artifactId>
        <version>` + VersionMavenSurefirePlugin + `</version>
      </plugin>`

// applyMavenJUnit merges junit-jupiter and maven-surefire-plugin into pom.xml when missing.
func applyMavenJUnit(pomPath string) (changed bool, err error) {
	b, err := os.ReadFile(pomPath)
	if err != nil {
		return false, err
	}
	s := string(b)
	orig := s

	needDep := !strings.Contains(s, "junit-jupiter")
	needPlugin := !strings.Contains(s, "maven-surefire-plugin")

	if !needDep && !needPlugin {
		return false, nil
	}

	if needDep {
		s, err = insertMavenJUnitDependency(s)
		if err != nil {
			return false, err
		}
	}
	if needPlugin {
		s, err = insertMavenSurefirePlugin(s)
		if err != nil {
			return false, err
		}
	}

	if s == orig {
		return false, nil
	}
	return true, atomicWrite(pomPath, []byte(s))
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
