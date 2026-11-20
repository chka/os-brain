package brain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A brain.json written by the team edition (ns-brain) names a Hive server or carries a
// zero-knowledge marker. os-brain must recognise both, so the command layer can refuse to
// answer from a local file that is not the real store, and must keep both keys unchanged
// when it rewrites the file, so the project can always go back to the team edition. An empty
// or null value is not a team-edition marker.
func TestATeamEditionConfigIsRecognisedAndKept(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`{"project":"a","hive":{"server":"https://hive.example.com","node":4,"org":"2"}}`,
			"a Hive server (https://hive.example.com)"},
		{`{"project":"a","blind":{"brain_uid":"00112233445566778899aabbccddeeff"}}`,
			"a zero-knowledge brain"},
		{`{"project":"a","hive":{}}`, ""},
		{`{"project":"a","blind":null}`, ""},
		{`{"project":"a"}`, ""},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "brain.json")
		if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if got := cfg.TeamEdition(); got != tc.want {
			t.Errorf("%s: TeamEdition() = %q, want %q", tc.raw, got, tc.want)
		}
		cfg.Context = map[string]string{"k": "v"}
		if err := SaveConfig(dir, cfg); err != nil {
			t.Fatal(err)
		}
		var before, after map[string]json.RawMessage
		_ = json.Unmarshal([]byte(tc.raw), &before)
		data, _ := os.ReadFile(path)
		if err := json.Unmarshal(data, &after); err != nil {
			t.Fatalf("SaveConfig wrote invalid JSON: %v\n%s", err, data)
		}
		for _, k := range []string{"hive", "blind"} {
			if b, ok := before[k]; ok && compact(b) != compact(after[k]) && !(k == "hive" && compact(b) == "{}") {
				t.Errorf("%s: the %s key changed on a rewrite:\n%s", tc.raw, k, data)
			}
		}
	}
}

// compact re-encodes raw JSON without whitespace, so two encodings of the same value compare
// equal.
func compact(raw json.RawMessage) string {
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return strings.TrimSpace(string(b))
}

// A brain.json may carry keys from a newer release or another edition. Rewriting the file to
// change one setting must keep them, while a known key that is now empty is dropped.
func TestSaveConfigKeepsKeysThisBinaryDoesNotKnow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brain.json")
	if err := os.WriteFile(path, []byte(`{"project":"a","blind_v2":{"policy":"strict"},"zz":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Project = "b"
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("SaveConfig wrote invalid JSON: %v\n%s", err, data)
	}
	if string(doc["blind_v2"]) == "" || string(doc["zz"]) != "1" || string(doc["project"]) != `"b"` {
		t.Fatalf("unknown keys were dropped or the known one not written:\n%s", data)
	}
	// A known key that is now empty must go, not be resurrected from the old file.
	cfg.Project = ""
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	doc = nil
	_ = json.Unmarshal(data, &doc)
	if _, ok := doc["project"]; ok {
		t.Fatalf("an emptied known key came back from the old file:\n%s", data)
	}
}
