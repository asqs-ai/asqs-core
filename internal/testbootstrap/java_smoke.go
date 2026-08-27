package testbootstrap

import (
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed testdata/AsqsBootstrapSmokeTest.java.template
var javaUnitSmokeClass string // tabs, not spaces: space indents fail spring-javaformat:validate

//go:embed testdata/AsqsSpringBootFrameworkSmokeTest.java.template
var javaSpringBootSmokeClass string

//go:embed testdata/AsqsQuarkusFrameworkSmokeTest.java.template
var javaQuarkusSmokeClass string

//go:embed testdata/AsqsMicronautFrameworkSmokeTest.java.template
var javaMicronautSmokeClass string

const (
	// javaSmokePackage is where the unit smoke test lives. It has no framework requirements, so a
	// dedicated package keeps it out of the way of generated artifacts and of the repo's own tests.
	javaSmokePackage = "com.asqs.bootstrap"
	// javaSmokeClassSimpleName / javaFrameworkSmokeClassSimpleName must match the templates.
	javaSmokeClassSimpleName          = "AsqsBootstrapSmokeTest"
	javaFrameworkSmokeClassSimpleName = "AsqsFrameworkSmokeTest"
	// javaSmokePackageToken is replaced with the application's base package for frameworks that
	// resolve configuration by package proximity (Spring Boot).
	javaSmokePackageToken = "__ASQS_PACKAGE__"
)

// javaSmokeImportRE captures the fully-qualified name bound by a single-type or static import.
var javaSmokeImportRE = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([\w.$]+)\s*;`)

// smokeVerifiedImports lists the imports the smoke tests that actually RAN compiled against.
//
// Read from the templates rather than written out beside them, so the list cannot drift from the
// files it describes. java.* is dropped: the JDK is not what "verified" is claiming.
//
// This is the bound on Contract.Verified. A true value there means a smoke test compiled and ran,
// and the Spring framework smoke imports exactly one class under org.springframework.boot.test.* —
// so the prompt must be able to say what was covered instead of leaving "verified" to be read as a
// warranty over every package under every advertised root.
func smokeVerifiedImports(sources ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, src := range sources {
		for _, m := range javaSmokeImportRE.FindAllStringSubmatch(src, -1) {
			fq := strings.TrimSpace(m[1])
			if fq == "" || strings.HasPrefix(fq, "java.") || seen[fq] {
				continue
			}
			seen[fq] = true
			out = append(out, fq)
		}
	}
	sort.Strings(out)
	return out
}

// javaSmokeFile is a smoke test staged on disk: where it lives and how to name it to a test runner.
type javaSmokeFile struct {
	// Abs is the absolute path written.
	Abs string
	// FQCN is the fully-qualified class name, for `-Dtest=` / `--tests`.
	FQCN string
	// Wrote is false when the file already existed (never clobber a repo's own file).
	Wrote bool
}

// javaTestSourceRoot returns <module>/src/test/java.
func javaTestSourceRoot(moduleRoot string) string {
	return filepath.Join(moduleRoot, "src", "test", "java")
}

// writeJavaSmokeSource materialises one smoke class at the location its package declares.
func writeJavaSmokeSource(moduleRoot, pkg, simpleName, source string) (javaSmokeFile, error) {
	dir := filepath.Join(javaTestSourceRoot(moduleRoot), filepath.FromSlash(strings.ReplaceAll(pkg, ".", "/")))
	abs := filepath.Join(dir, simpleName+".java")
	f := javaSmokeFile{Abs: abs, FQCN: pkg + "." + simpleName}
	if fileExists(abs) {
		return f, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return f, fmt.Errorf("mkdir smoke test dir: %w", err)
	}
	if err := atomicWrite(abs, []byte(source)); err != nil {
		return f, fmt.Errorf("write smoke test: %w", err)
	}
	f.Wrote = true
	return f, nil
}

// writeJavaUnitSmokeTest stages the mandatory smoke test proving JUnit 5 + Mockito + AssertJ resolve.
func writeJavaUnitSmokeTest(moduleRoot string) (javaSmokeFile, error) {
	return writeJavaSmokeSource(moduleRoot, javaSmokePackage, javaSmokeClassSimpleName, javaUnitSmokeClass)
}

// writeJavaFrameworkSmokeTest stages the framework-representative smoke test.
//
// Spring Boot is the case that needs care: @SpringBootTest resolves its configuration by walking UP
// the package tree looking for @SpringBootConfiguration, so a smoke test parked in com.asqs.bootstrap
// fails with "Unable to find a @SpringBootConfiguration" no matter how correct the dependencies are.
// It goes in the application's own base package instead. When no application class exists (a library
// module), there is nothing to boot and the caller skips the framework smoke entirely.
func writeJavaFrameworkSmokeTest(moduleRoot string, kind javaFrameworkSmoke) (javaSmokeFile, bool, error) {
	switch kind {
	case javaSmokeSpringBoot:
		pkg, ok := javaApplicationBasePackage(moduleRoot, "@SpringBootApplication", "@SpringBootConfiguration")
		if !ok {
			return javaSmokeFile{}, false, nil
		}
		src := strings.ReplaceAll(javaSpringBootSmokeClass, javaSmokePackageToken, pkg)
		f, err := writeJavaSmokeSource(moduleRoot, pkg, javaFrameworkSmokeClassSimpleName, src)
		return f, true, err
	case javaSmokeQuarkus:
		f, err := writeJavaSmokeSource(moduleRoot, javaSmokePackage, javaFrameworkSmokeClassSimpleName, javaQuarkusSmokeClass)
		return f, true, err
	case javaSmokeMicronaut:
		f, err := writeJavaSmokeSource(moduleRoot, javaSmokePackage, javaFrameworkSmokeClassSimpleName, javaMicronautSmokeClass)
		return f, true, err
	default:
		return javaSmokeFile{}, false, nil
	}
}

// javaFrameworkSmokeSource returns the template a framework smoke is staged from, so callers can
// read what it imports without re-deriving the switch above. "" when the kind stages nothing.
func javaFrameworkSmokeSource(kind javaFrameworkSmoke) string {
	switch kind {
	case javaSmokeSpringBoot:
		return javaSpringBootSmokeClass
	case javaSmokeQuarkus:
		return javaQuarkusSmokeClass
	case javaSmokeMicronaut:
		return javaMicronautSmokeClass
	default:
		return ""
	}
}

// removeJavaSmokeFile deletes a smoke test this run created.
//
// Leaving a failed framework smoke on disk would hand the evaluator a permanently broken file and
// reproduce the exact failure mode this whole change exists to prevent. Files that were already
// present are never removed.
func removeJavaSmokeFile(f javaSmokeFile) {
	if !f.Wrote || f.Abs == "" {
		return
	}
	_ = os.Remove(f.Abs)
}

var (
	javaPackageDeclRE = regexp.MustCompile(`(?m)^\s*package\s+([\w.]+)\s*;`)
)

// javaApplicationBasePackage finds the package of the class carrying any of the given annotations,
// scanning src/main/java. When several match, the shallowest package wins — that is the one whose
// component scan covers the rest of the module.
func javaApplicationBasePackage(moduleRoot string, annotations ...string) (string, bool) {
	mainRoot := filepath.Join(moduleRoot, "src", "main", "java")
	if !dirExists(mainRoot) {
		return "", false
	}
	var found []string
	_ = filepath.WalkDir(mainRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".java") {
			return nil //nolint:nilerr // an unreadable entry is not a reason to abandon the scan
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		src := string(b)
		hit := false
		for _, ann := range annotations {
			if strings.Contains(src, ann) {
				hit = true
				break
			}
		}
		if !hit {
			return nil
		}
		if m := javaPackageDeclRE.FindStringSubmatch(src); m != nil {
			found = append(found, m[1])
		}
		return nil
	})
	if len(found) == 0 {
		return "", false
	}
	sort.Slice(found, func(i, j int) bool {
		di, dj := strings.Count(found[i], "."), strings.Count(found[j], ".")
		if di != dj {
			return di < dj
		}
		return found[i] < found[j]
	})
	return found[0], true
}

// removeRelPath drops a path from a files-changed list. Smoke tests are announced when written and
// withdrawn when removed, so the audit never reports a file the repository does not end up with.
func removeRelPath(list []string, rel string) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		if p != rel {
			out = append(out, p)
		}
	}
	return out
}
