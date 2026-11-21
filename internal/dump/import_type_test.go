package dump

import "testing"

// Dumps written by other tools key the memory type as "type", and the importer once read
// only "mem_type", defaulting everything else to "note". "note" is tier N, which never
// decays, so a whole imported corpus came home untyped and outranked properly aged facts for
// ever. Both keys are accepted, because the dumps written before the fix still exist and are
// the ones most likely to be restored.
func TestImportAcceptsEitherSpellingOfTheTypeKey(t *testing.T) {
	for _, k := range []string{"mem_type", "type"} {
		if got := importedType(map[string]interface{}{k: "gotcha"}); got != "gotcha" {
			t.Errorf("%s: got %q, want gotcha", k, got)
		}
	}
	// mem_type wins when a dump carries both, because that is this store's own spelling.
	both := map[string]interface{}{"mem_type": "decision", "type": "gotcha"}
	if got := importedType(both); got != "decision" {
		t.Errorf("both keys: got %q, want decision", got)
	}
	if got := importedType(map[string]interface{}{}); got != "note" {
		t.Errorf("neither key: got %q, want note", got)
	}
}
