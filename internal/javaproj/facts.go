package javaproj

import "strings"

// Facts is what the generator prompt needs to state about a repo's JVM build.
type Facts struct {
	BuildFileRel     string
	Kind             BuildKind
	JavaVersion      string
	SpringBootVer    string
	TestDependencies []Dep
	// AncestorPomRel is set when a parent pom in the same repo supplied a missing fact.
	AncestorPomRel string
}

// Found reports whether anything worth putting in the prompt was discovered.
func (f Facts) Found() bool {
	return f.BuildFileRel != "" && (f.JavaVersion != "" || f.SpringBootVer != "" || len(f.TestDependencies) > 0)
}

// Resolve gathers build facts for the module owning sourceFileRel.
//
// For Maven, when the module POM does not carry java.version or a Boot version, ONE hop up to the
// next in-repo pom.xml is attempted (child wins on conflicts). That covers the standard two-level
// parent/module layout without turning into a POM resolver.
func Resolve(repoRoot, sourceFileRel string) Facts {
	rel, kind, ok := NearestBuildFileRel(repoRoot, sourceFileRel)
	if !ok {
		return Facts{}
	}
	f := Facts{BuildFileRel: rel, Kind: kind}
	src := ReadIfPresent(repoRoot, rel)
	if src == "" {
		return f
	}

	switch kind {
	case BuildMaven:
		props := ParseProperties(src)
		f.JavaVersion = ParseJavaVersion(src, props)
		f.SpringBootVer = ParseSpringBootVersion(src, props)
		f.TestDependencies = ParseTestDependencies(src, props)
		if f.JavaVersion == "" || f.SpringBootVer == "" {
			if parentRel, ok := AncestorPomRel(repoRoot, rel); ok {
				if parentSrc := ReadIfPresent(repoRoot, parentRel); parentSrc != "" {
					parentProps := ParseProperties(parentSrc)
					// Child properties win; the parent only fills gaps.
					merged := map[string]string{}
					for k, v := range parentProps {
						merged[k] = v
					}
					for k, v := range props {
						merged[k] = v
					}
					if f.JavaVersion == "" {
						f.JavaVersion = ParseJavaVersion(parentSrc, merged)
					}
					if f.SpringBootVer == "" {
						f.SpringBootVer = ParseSpringBootVersion(parentSrc, merged)
					}
					if f.JavaVersion != "" || f.SpringBootVer != "" {
						f.AncestorPomRel = parentRel
					}
				}
			}
		}
	default:
		f.JavaVersion = GradleJavaVersion(src)
		f.SpringBootVer = GradleSpringBootVersion(src)
		f.TestDependencies = GradleTestDependencies(src)
	}
	return f
}

// TestDependencyNames renders the coordinates for the prompt, marking catalog aliases unresolved.
func (f Facts) TestDependencyNames() []string {
	out := make([]string, 0, len(f.TestDependencies))
	for _, d := range f.TestDependencies {
		if IsUnresolvedCatalogAlias(d) {
			out = append(out, d.ArtifactID+" (version catalog alias — version not resolved here)")
			continue
		}
		out = append(out, d.String())
	}
	return out
}

// MajorVersion returns the leading numeric component of a version string ("4.0.0" -> "4").
func MajorVersion(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexAny(v, ".-"); i > 0 {
		return v[:i]
	}
	return v
}
