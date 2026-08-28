package javaproj

import (
	"regexp"
	"strings"
)

var (
	xmlCommentRE   = regexp.MustCompile(`(?s)<!--.*?-->`)
	propertiesRE   = regexp.MustCompile(`(?s)<properties>(.*?)</properties>`)
	propertyPairRE = regexp.MustCompile(`(?s)<([\w.\-]+)>\s*([^<]*?)\s*</([\w.\-]+)>`)
	parentBlockRE  = regexp.MustCompile(`(?s)<parent>(.*?)</parent>`)
	dependencyRE   = regexp.MustCompile(`(?s)<dependency>(.*?)</dependency>`)
	propRefRE      = regexp.MustCompile(`^\$\{([\w.\-]+)\}$`)
)

// StripXMLComments removes comments so a commented-out <dependency> block is not read as real.
func StripXMLComments(xml string) string { return xmlCommentRE.ReplaceAllString(xml, "") }

func tagValue(xml, tag string) string {
	re := regexp.MustCompile(`(?s)<` + regexp.QuoteMeta(tag) + `>\s*([^<]*?)\s*</` + regexp.QuoteMeta(tag) + `>`)
	m := re.FindStringSubmatch(xml)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// ParseProperties returns the <properties> map.
func ParseProperties(xml string) map[string]string {
	out := map[string]string{}
	block := propertiesRE.FindStringSubmatch(StripXMLComments(xml))
	if block == nil {
		return out
	}
	for _, m := range propertyPairRE.FindAllStringSubmatch(block[1], -1) {
		if m[1] != m[3] {
			continue
		}
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

// ResolveProperty expands a `${name}` reference using props, with ONE extra hop for chained
// references (${spring-boot.version} -> ${revision} is common). Unresolved references return "" —
// never the literal `${…}`, which would teach the model a version that does not exist.
func ResolveProperty(v string, props map[string]string) string {
	v = strings.TrimSpace(v)
	for i := 0; i < 2; i++ {
		m := propRefRE.FindStringSubmatch(v)
		if m == nil {
			return v
		}
		next, ok := props[m[1]]
		if !ok {
			return ""
		}
		v = strings.TrimSpace(next)
	}
	if propRefRE.MatchString(v) {
		return ""
	}
	return v
}

// Parent describes a <parent> coordinate.
type Parent struct {
	GroupID    string
	ArtifactID string
	Version    string
}

// ParseParent reads the <parent> block.
func ParseParent(xml string) Parent {
	block := parentBlockRE.FindStringSubmatch(StripXMLComments(xml))
	if block == nil {
		return Parent{}
	}
	return Parent{
		GroupID:    tagValue(block[1], "groupId"),
		ArtifactID: tagValue(block[1], "artifactId"),
		Version:    tagValue(block[1], "version"),
	}
}

// ParseJavaVersion returns the compile target, checking the conventional properties in the order
// Maven itself resolves them.
func ParseJavaVersion(xml string, props map[string]string) string {
	clean := StripXMLComments(xml)
	if props == nil {
		props = ParseProperties(clean)
	}
	for _, key := range []string{"java.version", "maven.compiler.release", "maven.compiler.source", "maven.compiler.target"} {
		if v := ResolveProperty(props[key], props); v != "" {
			return v
		}
	}
	for _, tag := range []string{"release", "source", "target"} {
		if v := ResolveProperty(tagValue(clean, tag), props); v != "" {
			return v
		}
	}
	return ""
}

// ParseSpringBootVersion returns the Spring Boot version in effect.
//
// When the parent is spring-boot-starter-parent its <version> IS the Boot version — the single
// most valuable fact here, and it needs no POM resolution at all. Otherwise fall back to the
// conventional managed-version properties.
func ParseSpringBootVersion(xml string, props map[string]string) string {
	clean := StripXMLComments(xml)
	if props == nil {
		props = ParseProperties(clean)
	}
	if p := ParseParent(clean); strings.EqualFold(p.ArtifactID, "spring-boot-starter-parent") {
		if v := ResolveProperty(p.Version, props); v != "" {
			return v
		}
	}
	for _, key := range []string{"spring-boot.version", "springboot.version", "spring.boot.version"} {
		if v := ResolveProperty(props[key], props); v != "" {
			return v
		}
	}
	// spring-boot-dependencies imported in <dependencyManagement>.
	for _, m := range dependencyRE.FindAllStringSubmatch(clean, -1) {
		if strings.EqualFold(tagValue(m[1], "artifactId"), "spring-boot-dependencies") {
			if v := ResolveProperty(tagValue(m[1], "version"), props); v != "" {
				return v
			}
		}
	}
	return ""
}

// Dep is a dependency coordinate.
type Dep struct {
	GroupID    string
	ArtifactID string
	Version    string
}

// String renders group:artifact (version omitted: it is usually managed by the parent and an
// unresolved property would be noise).
func (d Dep) String() string {
	if d.GroupID == "" {
		return d.ArtifactID
	}
	return d.GroupID + ":" + d.ArtifactID
}

// testScopedArtifactRE matches artifactIds that are test libraries even without <scope>test</scope>
// (some repos rely on the parent's managed scope).
var testScopedArtifactRE = regexp.MustCompile(`(?i)(junit|mockito|assertj|testcontainers|spring-boot-starter-test|testng|hamcrest|wiremock|rest-assured|playwright|selenium|spock)`)

// ParseTestDependencies returns the test-scoped dependencies, so the prompt can state which test
// libraries are actually on the classpath instead of leaving the model to assume.
func ParseTestDependencies(xml string, props map[string]string) []Dep {
	clean := StripXMLComments(xml)
	if props == nil {
		props = ParseProperties(clean)
	}
	var out []Dep
	seen := map[string]bool{}
	for _, m := range dependencyRE.FindAllStringSubmatch(clean, -1) {
		artifact := tagValue(m[1], "artifactId")
		if artifact == "" {
			continue
		}
		scope := strings.ToLower(tagValue(m[1], "scope"))
		if scope != "test" && !testScopedArtifactRE.MatchString(artifact) {
			continue
		}
		d := Dep{
			GroupID:    tagValue(m[1], "groupId"),
			ArtifactID: artifact,
			Version:    ResolveProperty(tagValue(m[1], "version"), props),
		}
		if seen[d.String()] {
			continue
		}
		seen[d.String()] = true
		out = append(out, d)
	}
	return out
}
