package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brain.json is committed to git and edited by people and other tools. A field of the
// wrong type (a project's spec.updated_at written as a number) used to refuse every
// command, init and the SessionStart hook included, while git held the same file. Such a
// field is now skipped and kept verbatim on rewrite; only a file that is not JSON, or JSON
// that is not an object, refuses.
func TestABrainJSONWithAWrongTypedFieldStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brain.json")
	raw := `{"project":"acme","shared":true,"spec":{"does":"x","updated_at":1785247110},"scopes":{"a":"b"},"later":1}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if c.Project != "acme" || !c.Shared || c.Scopes["a"] != "b" {
		t.Fatalf("the well-typed fields were lost: %+v", c)
	}
	// A rewrite keeps the skipped field verbatim, and the unknown one.
	c.Scopes["c"] = "d"
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	back, _ := os.ReadFile(path)
	for _, want := range []string{`"updated_at": 1785247110`, `"later": 1`, `"c": "d"`} {
		if !strings.Contains(string(back), want) {
			t.Errorf("rewrite lost %s:\n%s", want, back)
		}
	}
	// Valid JSON that is not an object carries no keys, so it would read as a brain with no
	// name and no settings.
	for _, bad := range []string{`{"project":`, `42`, `"x"`, `[]`, `true`, `null`} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
