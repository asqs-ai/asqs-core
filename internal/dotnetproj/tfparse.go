// Package dotnetproj parses SDK-style MSBuild project files for generator/eval context (no MSBuild execution).
package dotnetproj

import (
	"regexp"
	"strings"
)

var (
	reTargetFramework  = regexp.MustCompile(`(?i)<TargetFramework>\s*([^<]*?)\s*</TargetFramework>`)
	reTargetFrameworks = regexp.MustCompile(`(?i)<TargetFrameworks>\s*([^<]*?)\s*</TargetFrameworks>`)
	reLangVersion      = regexp.MustCompile(`(?i)<LangVersion>\s*([^<]*?)\s*</LangVersion>`)
	reXMLComments      = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// StripXMLComments removes <!-- ... --> blocks so commented-out TFMs do not affect parsing.
func StripXMLComments(s string) string {
	return reXMLComments.ReplaceAllString(s, "")
}

func splitTFMList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ";") {
		t := strings.TrimSpace(p)
		if t == "" || strings.HasPrefix(t, "$(") {
			continue
		}
		out = append(out, t)
	}
	return out
}

// ParseTargetFrameworkMonikers returns concrete TFMs from TargetFrameworks (preferred) or TargetFramework.
// MSBuild property references like $(Foo) are skipped. Order is document order with de-duplication (case-insensitive).
func ParseTargetFrameworkMonikers(csprojXML string) []string {
	s := StripXMLComments(csprojXML)
	if m := reTargetFrameworks.FindStringSubmatch(s); len(m) >= 2 {
		if parts := splitTFMList(m[1]); len(parts) > 0 {
			return dedupeTFMs(parts)
		}
	}
	var collected []string
	for _, sm := range reTargetFramework.FindAllStringSubmatch(s, -1) {
		collected = append(collected, splitTFMList(sm[1])...)
	}
	return dedupeTFMs(collected)
}

func dedupeTFMs(in []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, t := range in {
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

// ParseLangVersion returns the LangVersion element value, or "" if absent/empty.
func ParseLangVersion(csprojXML string) string {
	s := StripXMLComments(csprojXML)
	m := reLangVersion.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	v := strings.TrimSpace(m[1])
	if v == "" || strings.HasPrefix(v, "$(") {
		return ""
	}
	return v
}
