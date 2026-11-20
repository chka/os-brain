package brain

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bootstrap seeds a new brain from the project's past Claude Code transcripts: it lists the
// transcript files that belong to this project, records which ones the agent has read, and
// keeps a one-line reminder in the brief until the pass is closed.

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

// Claude Code names a transcript directory after the project's path with every separator
// turned into "-". /Users/mtest/lab/acme and /Users/mtest/lab/advisor-acme both produce a
// slug ending "-acme", so matching on the suffix once offered a session ten transcripts of
// which three belonged to the other project. Suffix matches are now excluded unless asked for.
// Both directions are asserted, because deleting the suffix rule would lose the moved-path
// case it exists for (a project that was moved keeps its old transcripts under the old slug).
func TestASuffixSlugIsExcludedByDefaultAndIncludedOnRequest(t *testing.T) {
	s := newTestStore(t)
	transcriptLab(t, s, "/Users/mtest/lab/acme", map[string][]string{
		"-Users-mtest-lab-acme":          {"a.jsonl", "b.jsonl"},
		"-Users-mtest-lab-advisor-acme":  {"other1.jsonl", "other2.jsonl", "other3.jsonl"},
		"-Users-mtest-old-path-lab-acme": {"moved.jsonl"},
		"-Users-mtest-lab-acme2":         {"nope.jsonl"},
	})

	human, obj := bootstrapRun(t, s, BootstrapArgs{})
	for _, p := range jsonPaths(t, obj, "files") {
		if strings.Contains(p, "advisor-acme") || strings.Contains(p, "acme2") {
			t.Errorf("another project's transcript was offered for reading: %s", p)
		}
	}
	if n := len(jsonPaths(t, obj, "files")); n != 2 {
		t.Errorf("want the 2 exact-slug transcripts, got %d", n)
	}
	// Never silently dropped either: the near miss is named, and so is the way in.
	if !strings.Contains(human, "advisor-acme") || !strings.Contains(human, "EXCLUDED") {
		t.Errorf("the excluded candidate is not reported:\n%s", human)
	}
	if !strings.Contains(human, "--include-moved") {
		t.Errorf("the flag that would include it is not named:\n%s", human)
	}
	cands := jsonPaths(t, obj, "candidate_dirs")
	if len(cands) != 2 {
		t.Fatalf("want 2 candidate dirs (advisor-acme, old-path), got %v", cands)
	}
	// A candidate is a suffix match on "-"+name, never a substring: acme2 is not one.
	for _, c := range cands {
		if strings.Contains(c, "acme2") {
			t.Errorf("acme2 was treated as a candidate for acme: %s", c)
		}
	}
	if n := len(jsonPaths(t, obj, "candidate_files")); n != 4 {
		t.Errorf("want 4 excluded transcripts, got %d", n)
	}

	_, wide := bootstrapRun(t, s, BootstrapArgs{IncludeMoved: true})
	got := jsonPaths(t, wide, "files")
	if len(got) != 6 {
		t.Fatalf("--include-moved should offer all 6 matching transcripts, got %v", got)
	}
	var moved bool
	for _, p := range got {
		if strings.Contains(p, "old-path") {
			moved = true
		}
		if strings.Contains(p, "acme2") {
			t.Errorf("acme2 was included even with --include-moved: %s", p)
		}
	}
	if !moved {
		t.Error("the moved-path case the suffix rule exists for was not included")
	}
}

// Reading a project's history takes many turns and is often interrupted. Progress used to be
// one boolean written at the end, so an interrupted pass left nothing behind and the next one
// started over. Each transcript read is now recorded as it is read.
func TestAnInterruptedBootstrapRemembersWhatWasRead(t *testing.T) {
	s := newTestStore(t)
	transcriptLab(t, s, "/Users/mtest/lab/widget", map[string][]string{
		"-Users-mtest-lab-widget": {"a.jsonl", "b.jsonl", "c.jsonl"},
	})
	_, obj := bootstrapRun(t, s, BootstrapArgs{})
	files := jsonPaths(t, obj, "files")
	if len(files) != 3 {
		t.Fatalf("want 3 transcripts, got %v", files)
	}

	var buf bytes.Buffer
	if err := s.Bootstrap(&buf, BootstrapArgs{Read: []string{files[0]}}); err != nil {
		t.Fatalf("--read: %v", err)
	}
	if !strings.Contains(buf.String(), "1 of 3") {
		t.Errorf("--read did not report progress: %s", buf.String())
	}

	human, again := bootstrapRun(t, s, BootstrapArgs{})
	if again["read"].(float64) != 1 {
		t.Errorf("the next invocation forgot the read transcript: %v", again["read"])
	}
	if !strings.Contains(human, "READ") || !strings.Contains(human, "to read") {
		t.Errorf("the listing does not say which files remain:\n%s", human)
	}
	if !strings.Contains(human, "1 read, 2 to go") {
		t.Errorf("the counts are not shown:\n%s", human)
	}
}

// Fail closed: a mark nobody can match to a file would report progress that does not
// exist, so the path is refused and nothing at all is written.
func TestMarkingAnUnknownTranscriptIsRefused(t *testing.T) {
	s := newTestStore(t)
	transcriptLab(t, s, "/Users/mtest/lab/widget", map[string][]string{
		"-Users-mtest-lab-widget": {"a.jsonl"},
	})
	var buf bytes.Buffer
	err := s.Bootstrap(&buf, BootstrapArgs{Read: []string{"/tmp/not-a-transcript.jsonl"}})
	if err == nil {
		t.Fatal("an unknown path was accepted")
	}
	if !strings.Contains(err.Error(), "Nothing was recorded") {
		t.Errorf("the refusal does not say what it did: %v", err)
	}
	if len(s.bootstrapRead()) != 0 {
		t.Error("a refused path was recorded anyway")
	}
}

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
