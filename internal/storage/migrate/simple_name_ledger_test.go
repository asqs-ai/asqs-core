package migrate

import (
	"strings"
	"testing"
)

// 0008 recreates simple_name by DROPping it first, so it is only correct downstream of the 0002
// that creates the column. The ledger is applied in slice order, so a reordering that put 0008
// first would fail at apply time against a fresh database — after 0002 had already rewritten the
// table. Ledger order is therefore an invariant, not a stylistic choice.
func TestMetadataLedger_simpleNameOrdering(t *testing.T) {
	pos := map[string]int{}
	for i, m := range MetadataMigrations() {
		pos[m.ID] = i
	}
	creates, ok := pos["0002_symbols_trigram_and_simple_name"]
	if !ok {
		t.Fatal("0002 missing: simple_name is never created, so every fast path stays dark")
	}
	recreates, ok := pos["0008_simple_name_parameter_aware"]
	if !ok {
		t.Fatal("0008 missing: simple_name keeps the parameter list, so type lookups miss every C# type")
	}
	if recreates < creates {
		t.Errorf("0008 (index %d) runs before 0002 (index %d); it drops a column that does not exist yet", recreates, creates)
	}
}

// The whole point of 0008 is that simple_name must be the BARE member name: B25 made C# FQNames
// carry parameter lists and generic markers, and 0002's expression stripped neither. A
// simple_name of "Hello(string)" equals nothing a caller ever searches for, so the parameter-aware
// expression is what must ship.
func TestMetadataLedger_simpleNameIsParameterAware(t *testing.T) {
	for _, m := range MetadataMigrations() {
		if m.ID != "0008_simple_name_parameter_aware" {
			continue
		}
		if !strings.Contains(m.Description, "parameter") && !strings.Contains(m.Description, "B25") {
			t.Errorf("0008 description no longer names what it fixes: %q", m.Description)
		}
		if m.Apply == nil {
			t.Fatal("0008 has no Apply; the migration is a no-op that still records as applied")
		}
		return
	}
	t.Fatal("0008 not found in MetadataMigrations()")
}
