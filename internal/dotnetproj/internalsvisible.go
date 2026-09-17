package dotnetproj

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// reInternalsVisibleToItem matches the MSBuild ITEM form, which is what modern SDK-style projects
// use and what this writes.
var reInternalsVisibleToItem = regexp.MustCompile(`(?i)<InternalsVisibleTo\s+[^>]*Include\s*=\s*"([^"]+)"`)

// reInternalsVisibleToAttribute matches the C# ATTRIBUTE form, written in source rather than in the
// project file. Both grant the same access, so finding either means there is nothing to add.
var reInternalsVisibleToAttribute = regexp.MustCompile(
	`(?i)\[\s*assembly\s*:\s*InternalsVisibleTo\s*\(\s*"([^"]+)"`)

// GrantsInternalsVisibleTo reports whether a project already makes its internals visible to the
// named assembly, by either mechanism.
func GrantsInternalsVisibleTo(csprojXML, assemblyName string) bool {
	assemblyName = strings.TrimSpace(assemblyName)
	if assemblyName == "" {
		return false
	}
	for _, re := range []*regexp.Regexp{reInternalsVisibleToItem, reInternalsVisibleToAttribute} {
		for _, m := range re.FindAllStringSubmatch(csprojXML, -1) {
			// A grant may carry a public key: "Shop.Tests, PublicKey=0024...".
			got := strings.TrimSpace(strings.SplitN(m[1], ",", 2)[0])
			if strings.EqualFold(got, assemblyName) {
				return true
			}
		}
	}
	return false
}

// EnsureInternalsVisibleTo adds an <InternalsVisibleTo> item to a project so a test assembly can
// reach its internal types.
//
// A seam makes a constructor or a member internal so a test can reach it without making it part of
// the public API. That only works if the test ASSEMBLY is granted access — otherwise the seam
// compiles, the production project is modified, and the test that motivated it fails on CS0122
// with no indication that one line in a project file is all that is missing.
//
// Returns false with no error when the grant already exists in either form, so applying this twice
// is safe and a repository that already grants access is left exactly as it was.
func EnsureInternalsVisibleTo(csprojAbs, testAssemblyName string) (bool, error) {
	testAssemblyName = strings.TrimSpace(testAssemblyName)
	if testAssemblyName == "" {
		return false, nil
	}
	b, err := os.ReadFile(csprojAbs)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", csprojAbs, err)
	}
	xml := string(b)
	if GrantsInternalsVisibleTo(xml, testAssemblyName) {
		return false, nil
	}
	closing := strings.LastIndex(xml, "</Project>")
	if closing < 0 {
		// Not a project file this understands. Writing into it blind would corrupt the build.
		return false, fmt.Errorf("%s has no </Project> element", csprojAbs)
	}
	item := fmt.Sprintf(
		"  <ItemGroup>\n    <InternalsVisibleTo Include=\"%s\" />\n  </ItemGroup>\n", testAssemblyName)
	updated := xml[:closing] + item + xml[closing:]
	if err := os.WriteFile(csprojAbs, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", csprojAbs, err)
	}
	return true, nil
}

// AssemblyNameFor returns the assembly a project produces: its explicit <AssemblyName> when it sets
// one, otherwise the project file's own name, which is the SDK's default.
func AssemblyNameFor(csprojAbs string) string {
	b, err := os.ReadFile(csprojAbs)
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`(?is)<AssemblyName>\s*([^<]+?)\s*</AssemblyName>`).FindStringSubmatch(string(b)); m != nil {
		if name := strings.TrimSpace(m[1]); name != "" && !strings.Contains(name, "$(") {
			return name
		}
	}
	base := csprojAbs
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".csproj")
}
