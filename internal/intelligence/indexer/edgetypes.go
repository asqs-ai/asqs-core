package indexer

import "strings"

// EdgeTypeRegistry is the single list of edge types the system produces, with the retrieval
// confidence each carries.
//
// L-2: there was no list. ~45 edge type strings were spread across the language indexers, the
// canonicalizer's switch, and a confidence switch in the retrieval package, with an untyped TEXT
// column underneath. Three consequences, all of which had already happened at least once:
//
//   - A typo in a producer created a NEW edge type silently. Nothing rejected it, nothing counted
//     it, and it simply never matched anything downstream.
//   - Confidence lived in a Go switch, so a type absent from that switch fell to the default of 2
//     without anyone noticing it had never been considered.
//   - Nobody could answer "what edge types exist" without grepping four packages.
//
// The registry does not add a database constraint. A CHECK constraint would reject an unknown type
// at write time, which sounds stricter but would fail an index run on a producer typo rather than
// degrade — and the indexers are the thing most likely to grow a new type legitimately. Unknown
// types are therefore still stored; what changes is that they are now nameable, and
// UnknownEdgeTypes reports them.
type EdgeTypeRegistry struct {
	// Confidence is the retrieval weight: higher means a stronger signal that the callee is
	// genuinely relevant to the caller. Used to rank dependency chunks.
	Confidence int
	// Description is for operators reading a graph they did not build.
	Description string
}

// Confidence bands. These were bare integers in a switch; naming them makes the ordering explicit
// and makes a new type's placement a decision rather than a default.
const (
	// EdgeConfidenceDirect: the caller genuinely uses the callee at runtime.
	EdgeConfidenceDirect = 3
	// EdgeConfidenceStructural: a declaration-level relationship — real, but weaker evidence that
	// the callee matters to a specific behaviour.
	EdgeConfidenceStructural = 2
	// EdgeConfidenceAmbient: package- and import-level. Present in almost every file, so it says
	// little about relevance on its own.
	EdgeConfidenceAmbient = 1
	// EdgeConfidenceDefault is what an unregistered type receives.
	EdgeConfidenceDefault = 2
)

// EdgeTypes is the registry. Keys are canonical (uppercase) edge types.
var EdgeTypes = map[string]EdgeTypeRegistry{
	// Direct use.
	"CALLS":              {EdgeConfidenceDirect, "caller invokes callee"},
	"INJECTS":            {EdgeConfidenceDirect, "dependency injected into the caller"},
	"INJECTS_NAMED":      {EdgeConfidenceDirect, "named/qualified injection"},
	"ROUTE_TO_HANDLER":   {EdgeConfidenceDirect, "HTTP route bound to its handler"},
	"HANDLER_USES_DTO":   {EdgeConfidenceDirect, "handler reads or returns this DTO"},
	"USES_GUARD":         {EdgeConfidenceDirect, "Nest guard applied"},
	"USES_PIPE":          {EdgeConfidenceDirect, "Nest pipe applied"},
	"USES_INTERCEPTOR":   {EdgeConfidenceDirect, "Nest interceptor applied"},
	"TARGETS_API_ROUTE":  {EdgeConfidenceDirect, "client call resolved to an indexed API route"},
	"CALLS_API":          {EdgeConfidenceDirect, "outbound API call"},
	"USES_SELECTOR":      {EdgeConfidenceDirect, "component uses this selector"},
	"RENDERS":            {EdgeConfidenceDirect, "component renders this component"},
	"USES_HOOK":          {EdgeConfidenceDirect, "React hook used"},
	"ACCEPTS_PROPS_TYPE": {EdgeConfidenceDirect, "component's props type"},
	"IMPLEMENTS_SERVICE": {EdgeConfidenceDirect, "concrete implementation of a service contract"},
	"REGISTERS_SERVICE":  {EdgeConfidenceDirect, "DI registration"},

	// Structural.
	"EXTENDS":          {EdgeConfidenceStructural, "inheritance"},
	"IMPLEMENTS":       {EdgeConfidenceStructural, "interface implementation"},
	"CONTAINS":         {EdgeConfidenceStructural, "type contains member"},
	"DECLARES":         {EdgeConfidenceStructural, "module declares symbol"},
	"MODULE_IMPORTS":   {EdgeConfidenceStructural, "Nest/Angular module imports module"},
	"MODULE_EXPORTS":   {EdgeConfidenceStructural, "module exports provider"},
	"MODULE_PROVIDERS": {EdgeConfidenceStructural, "module provides service"},
	"MODULE_REGISTERS": {EdgeConfidenceStructural, "module registers component"},

	// Ambient.
	"IMPORTS":         {EdgeConfidenceAmbient, "source-level import"},
	"DEPENDS_ON":      {EdgeConfidenceAmbient, "package dependency"},
	"DEPENDS_ON_DEV":  {EdgeConfidenceAmbient, "package devDependency"},
	"PACKAGE_MAIN":    {EdgeConfidenceAmbient, "package main entry"},
	"PACKAGE_MODULE":  {EdgeConfidenceAmbient, "package module entry"},
	"PACKAGE_EXPORT":  {EdgeConfidenceAmbient, "package exports entry"},
	"PACKAGE_ENTRY":   {EdgeConfidenceAmbient, "package entry point"},
	"PACKAGE_BIN":     {EdgeConfidenceAmbient, "package bin entry"},
	"TESTS_SOURCE":    {EdgeConfidenceStructural, "materialized test-to-source trace link"},
	"PAGE_ROUTE_LINK": {EdgeConfidenceStructural, "UI route linked to a page component"},
}

// EdgeTypeConfidence returns the retrieval confidence for an edge type.
//
// Unregistered types get EdgeConfidenceDefault, exactly as the switch this replaces did — the
// registry changes where the answer lives, not what an unknown type scores.
func EdgeTypeConfidence(edgeType string) int {
	if e, ok := EdgeTypes[strings.ToUpper(strings.TrimSpace(edgeType))]; ok {
		return e.Confidence
	}
	return EdgeConfidenceDefault
}

// IsRegisteredEdgeType reports whether the type is known.
func IsRegisteredEdgeType(edgeType string) bool {
	_, ok := EdgeTypes[strings.ToUpper(strings.TrimSpace(edgeType))]
	return ok
}

// UnknownEdgeTypes returns the canonical types in the input that the registry does not know, sorted
// by first appearance. Intended for an index run to report "this producer emitted something nobody
// downstream understands" rather than silently storing it.
func UnknownEdgeTypes(edgeTypes []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range edgeTypes {
		et := strings.ToUpper(strings.TrimSpace(raw))
		if et == "" || seen[et] || IsRegisteredEdgeType(et) {
			continue
		}
		seen[et] = true
		out = append(out, et)
	}
	return out
}
