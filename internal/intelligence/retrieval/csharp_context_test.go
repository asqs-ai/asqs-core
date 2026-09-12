package retrieval

import "testing"

// An enum encloses its members exactly as a class encloses its methods, and the C# indexer emits
// both. Leaving it out meant an enum member's container never joined the retrieved context, so the
// model saw `BasketState#Locked` and not the set it belongs to — which is the one thing a test
// switching over a state machine has to know.
func TestIsEnclosingContainerKind_enum(t *testing.T) {
	for _, kind := range []string{"enum", "Enum", "ENUM", "class", "record", "struct", "interface"} {
		if !isEnclosingContainerKind(kind) {
			t.Errorf("isEnclosingContainerKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"method", "field", "property", "MODULE", ""} {
		if isEnclosingContainerKind(kind) {
			t.Errorf("isEnclosingContainerKind(%q) = true, want false", kind)
		}
	}
}
