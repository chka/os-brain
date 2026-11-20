package brain

// Tests for init's warning about another memory store the agent already loads. Two stores
// answering the same question drift apart, and the agent cannot tell which one is current.
// init cannot settle that for the developer, so it detects and reports, prominently, and
// never touches the other store.
//
// Claude Code names its per-project auto-memory directory after the project path with every
// separator replaced by "-"; the tests build that slug the same way.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The store that caused the trouble in practice is Claude Code's own auto-memory, which lives
// outside the project under <config>/projects/<slug>/memory/. A check that looked only
// inside the project would pass it cleanly. The warning must lead the output, name the
// path, count the files and give the resolution, and the install must still happen.
func TestInitWarnsAboutClaudeCodesAutoMemoryOutsideTheProject(t *testing.T) {
	root := initEnv(t)
	slug := strings.ReplaceAll(root, string(filepath.Separator), "-")
	mem := filepath.Join(root, "cfg", "projects", slug, "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"MEMORY.md", "hosts.md", "rag.md"} {
		if err := os.WriteFile(filepath.Join(mem, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := Init(&out, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "ANOTHER MEMORY STORE IS ALREADY LOADED") {
		t.Fatalf("no competing-store warning:\n%s", got)
	}
	if !strings.Contains(got, mem) {
		t.Errorf("the warning does not name the path %s:\n%s", mem, got)
	}
	if !strings.Contains(got, "(3 files)") {
		t.Errorf("the warning does not count the files:\n%s", got)
	}
	// A footnote under the next steps is skimmed past, so it has to come first.
	if !strings.HasPrefix(got, "ANOTHER MEMORY STORE") {
		t.Errorf("the warning is not the first thing printed:\n%s", got)
	}
	if i, j := strings.Index(got, "ANOTHER MEMORY STORE"), strings.Index(got, "Next:"); j >= 0 && i > j {
		t.Errorf("the warning comes after the next-steps block:\n%s", got)
	}
	if !strings.Contains(got, "os-brain remember") || !strings.Contains(got, "delete that store yourself") {
		t.Errorf("the warning does not give the resolution:\n%s", got)
	}
	// Detection and report only: the other store is not init's to change.
	entries, err := os.ReadDir(mem)
	if err != nil || len(entries) != 3 {
		t.Fatalf("init disturbed the other store: %v %v", entries, err)
	}
	// A warning that blocked the install would trade one failure for a worse one.
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.db")); err != nil {
		t.Errorf("init did not install: %v", err)
	}
}

// A MEMORY.md or memory/ directory inside the project is reported too, and left in place.
func TestInitWarnsAboutAMemoryStoreInsideTheProject(t *testing.T) {
	root := initEnv(t)
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("# facts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory", "a.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Init(&out, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{filepath.Join(root, "MEMORY.md"), filepath.Join(root, "memory")} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was not reported:\n%s", want, got)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "MEMORY.md")); err != nil {
		t.Errorf("init removed MEMORY.md, which is not its call: %v", err)
	}
}

// A warning that fires when there is nothing to warn about teaches people to ignore it. An
// empty auto-memory directory is not a competing store, and neither is the auto-memory of
// some other project.
func TestInitSaysNothingWhenThereIsNoCompetingStore(t *testing.T) {
	root := initEnv(t)
	if err := os.MkdirAll(filepath.Join(root, "cfg", "projects",
		strings.ReplaceAll(root, string(filepath.Separator), "-"), "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "cfg", "projects", "-Users-someone-elsewhere", "memory")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "MEMORY.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Init(&out, root, InitArgs{Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(strings.ToUpper(got), "ANOTHER MEMORY STORE") {
		t.Errorf("warned about nothing:\n%s", got)
	}
	if !strings.HasPrefix(got, "brain ready at") {
		t.Errorf("stray output before the ordinary first line:\n%s", got)
	}
	if strings.Contains(got, other) {
		t.Errorf("another project's store was reported:\n%s", got)
	}
}

// Scripts and agents read init --json, so the competing stores appear there with path and
// file count.
func TestInitJSONCarriesTheCompetingStores(t *testing.T) {
	root := initEnv(t)
	slug := strings.ReplaceAll(root, string(filepath.Separator), "-")
	mem := filepath.Join(root, "cfg", "projects", slug, "memory")
	if err := os.MkdirAll(mem, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mem, "MEMORY.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Init(&out, root, InitArgs{Agents: "claude", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Competing []struct {
			Path  string `json:"path"`
			Files int    `json:"files"`
		} `json:"competing_stores"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("init --json is not valid JSON: %v\n%s", err, out.String())
	}
	if len(got.Competing) != 1 || got.Competing[0].Path != mem || got.Competing[0].Files != 1 {
		t.Fatalf("competing_stores wrong: %+v", got.Competing)
	}

	// The clean case emits the field as an empty list, not a missing key: a consumer that
	// has to tell "no competing store" from "this client cannot tell" needs the difference.
	clean := initEnv(t)
	var out2 bytes.Buffer
	if err := Init(&out2, clean, InitArgs{Agents: "claude", JSON: true}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(out2.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	v, ok := raw["competing_stores"]
	if !ok {
		t.Fatal("competing_stores is missing from a clean init")
	}
	if list, _ := v.([]interface{}); len(list) != 0 {
		t.Fatalf("a clean init reported competing stores: %v", v)
	}
}

// `init --dir memory` creates a directory named like a memory store. init must not then
// warn about its own brain.
func TestInitDoesNotReportItsOwnDirectoryAsACompetingStore(t *testing.T) {
	root := initEnv(t)
	var out bytes.Buffer
	if err := Init(&out, root, InitArgs{Agents: "claude", Dir: "memory"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(out.String()), "ANOTHER MEMORY STORE") {
		t.Errorf("init reported its own brain directory:\n%s", out.String())
	}
}
