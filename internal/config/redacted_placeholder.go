package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// RedactedSecretPlaceholder is the scalar a redacting round-trip writes in place of a secret. It
// lives next to the loader because the loader is the choke point that must refuse it.
//
// Nothing in the open core PRODUCES this placeholder today — the redacting API is enterprise-only.
// The guard is here anyway because core does store the run's config body verbatim in
// config_revisions (pipeline/config_revision.go), which is exactly the surface a redacting reader
// would be built on. A guard that fails closed costs one function; discovering the need for it
// after a literal "<redacted>" has been used as a credential costs an incident.
const RedactedSecretPlaceholder = "<redacted>"

// findRedactedPlaceholderPaths returns the dotted paths of every scalar VALUE in the document that
// still equals RedactedSecretPlaceholder, sorted.
//
// Why this must fail closed: such a round-trip can only replace a placeholder when the previous
// revision has a value at the SAME path. A placeholder sitting at a path the baseline does not
// have — because the key was renamed, moved, or simply typo'd — survives the merge untouched, and
// without this check the literal string "<redacted>" is then stored and used as the credential.
// The failure mode is a silent auth error against a real provider, so the load must reject it.
//
// Values are matched, never raw bytes: "<redacted>" appearing inside a comment or an unrelated
// free-text field is not a leaked secret and must not fail an otherwise valid config.
func findRedactedPlaceholderPaths(data []byte) []string {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || len(root.Content) == 0 {
		return nil
	}
	var found []string
	var walk func(n *yaml.Node, path string)
	walk = func(n *yaml.Node, path string) {
		if n == nil {
			return
		}
		switch n.Kind {
		case yaml.DocumentNode:
			for _, c := range n.Content {
				walk(c, path)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				key := n.Content[i].Value
				child := path + key
				if path == "" {
					child = key
				}
				walk(n.Content[i+1], child+".")
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				walk(c, fmt.Sprintf("%s[%d].", strings.TrimSuffix(path, "."), i))
			}
		case yaml.ScalarNode:
			if n.Value == RedactedSecretPlaceholder {
				found = append(found, strings.TrimSuffix(path, "."))
			}
		case yaml.AliasNode:
			walk(n.Alias, path)
		}
	}
	walk(&root, "")
	sort.Strings(found)
	return found
}

// rejectSurvivingRedactedPlaceholders is the error half of findRedactedPlaceholderPaths.
func rejectSurvivingRedactedPlaceholders(data []byte) error {
	paths := findRedactedPlaceholderPaths(data)
	if len(paths) == 0 {
		return nil
	}
	return fmt.Errorf("config: unresolved %q placeholder at %s — the previous revision has no value "+
		"at that path (renamed, moved or misspelled key), so the placeholder cannot be merged; "+
		"supply the real value or restore the original key",
		RedactedSecretPlaceholder, strings.Join(paths, ", "))
}
