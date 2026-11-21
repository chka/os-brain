package brief

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One line, only when it is true. An unconditional reminder gets muted, and the muting
// takes the useful one with it.
func TestTheBriefReportsAnOpenBootstrapAndGoesQuietWhenItIsClosed(t *testing.T) {
	s := newTestStore(t)
	transcriptLab(t, s, "/Users/mtest/lab/widget", map[string][]string{
		"-Users-mtest-lab-widget": {"a.jsonl", "b.jsonl", "c.jsonl"},
	})

	brief := func() string {
		var buf bytes.Buffer
		if err := s.Brief(&buf, BriefArgs{NoCursor: true}); err != nil {
			t.Fatalf("brief: %v", err)
		}
		return buf.String()
	}

	if strings.Contains(brief(), "bootstrap open") {
		t.Error("a brain with transcripts but no bootstrap started says a bootstrap is open")
	}

	_, obj := bootstrapRun(t, s, BootstrapArgs{})
	files := jsonPaths(t, obj, "files")
	var buf bytes.Buffer
	if err := s.Bootstrap(&buf, BootstrapArgs{Read: []string{files[0]}}); err != nil {
		t.Fatalf("--read: %v", err)
	}

	out := brief()
	if !strings.Contains(out, "bootstrap open: 1 of 3 transcripts read") {
		t.Errorf("the brief does not report the open bootstrap:\n%s", out)
	}
	if n := strings.Count(out, "bootstrap open"); n != 1 {
		t.Errorf("the brief spent %d lines on it; one is the budget", n)
	}

	buf.Reset()
	if err := s.Bootstrap(&buf, BootstrapArgs{Read: files[1:]}); err != nil {
		t.Fatalf("--read: %v", err)
	}
	if got := brief(); !strings.Contains(got, "all 3 transcripts read, not closed") {
		t.Errorf("every transcript read but not closed is still open:\n%s", got)
	}

	buf.Reset()
	if err := s.Bootstrap(&buf, BootstrapArgs{Done: true}); err != nil {
		t.Fatalf("--done: %v", err)
	}
	if got := brief(); strings.Contains(got, "bootstrap open") {
		t.Errorf("a finished bootstrap still reports as open:\n%s", got)
	}
}

// transcriptLab builds the Claude Code transcript tree under the config directory
// newTestStore already pinned, and moves the project to a path of our choosing. Nothing
// here touches the real store: CLAUDE_CONFIG_DIR and OS_BRAIN_GLOBAL_DB are both temporary.
// The project path is created for real under a temporary directory, because a declared root
// that does not hold the brain is not believed, and every slug is prefixed to match it.
func transcriptLab(t *testing.T, s *Store, projectRoot string, dirs map[string][]string) {
	t.Helper()
	base := t.TempDir()
	here := filepath.Join(base, projectRoot, "brain")
	if err := os.MkdirAll(here, 0o700); err != nil {
		t.Fatal(err)
	}
	s.Env.Here = here
	prefix := strings.ReplaceAll(base, string(filepath.Separator), "-")
	root := filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects")
	for slug, files := range dirs {
		d := filepath.Join(root, prefix+slug)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(d, f), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// bootstrapRun runs bootstrap twice with the same arguments, once for the human listing and
// once with --json, and returns both.
func bootstrapRun(t *testing.T, s *Store, a BootstrapArgs) (string, map[string]interface{}) {
	t.Helper()
	var human, raw bytes.Buffer
	if err := s.Bootstrap(&human, a); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	a.JSON = true
	if err := s.Bootstrap(&raw, a); err != nil {
		t.Fatalf("bootstrap --json: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw.Bytes(), &out); err != nil {
		t.Fatalf("bootstrap json: %v (%s)", err, raw.String())
	}
	return human.String(), out
}

// jsonPaths reads a list of transcript paths out of bootstrap's JSON, whether the list holds
// bare strings or objects with a "path" field.
func jsonPaths(t *testing.T, obj map[string]interface{}, key string) []string {
	t.Helper()
	var out []string
	list, _ := obj[key].([]interface{})
	for _, f := range list {
		switch v := f.(type) {
		case string:
			out = append(out, v)
		case map[string]interface{}:
			out = append(out, v["path"].(string))
		}
	}
	return out
}
