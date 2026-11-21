package brief

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/store"
)

// The SessionStart hook opens the brain unattended in whatever repository a session starts
// in, including one somebody else wrote. Nothing in that repository's own configuration may
// run a program, reach the brief raw, or point the brain at another project's store.

// A repository's .git/config can name a program for core.fsmonitor or a clean filter, and
// git runs it on the next `git status`. The session's first git call used to run it, and a
// commit subject with terminal escapes reached the brief raw.
func TestBriefAndWakeupRunNothingARepositoryConfigNames(t *testing.T) {
	s, dir := newGitTestStore(t)
	marks := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "subject \x1b[31mRED\x1b[0m")
	git(t, dir, "config", "core.fsmonitor", "touch "+filepath.Join(marks, "FSMONITOR")+"; false")
	git(t, dir, "config", "filter.evil.clean", "touch "+filepath.Join(marks, "CLEAN")+"; cat")
	if err := os.WriteFile(filepath.Join(dir, "one.txt"), []byte("ONE"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := s.Brief(&out, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Wakeup(io.Discard, WakeupArgs{}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"FSMONITOR", "CLEAN"} {
		if _, err := os.Stat(filepath.Join(marks, m)); err == nil {
			t.Errorf("the repository's %s program ran during the session open", m)
		}
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("a raw escape from a commit subject reached the brief:\n%q", out.String())
	}
}

// A repository's .claude/settings.json `env` block, settings.local.json, or .mcp.json's
// server env sets variables for every call of the session without a hook, so the hook-time
// check never saw them. One setting OS_BRAIN_DB to a sibling project's private file had
// that file opened. A value the repository declared that points outside it is refused; the
// same variable set by the person, or pointing inside the repository or at the shared
// store, is allowed.
func TestARepositorysEnvBlockCannotPointAtAnotherBrain(t *testing.T) {
	base := t.TempDir()
	repo, other := filepath.Join(base, "repo"), filepath.Join(base, "other")
	for _, d := range []string{filepath.Join(repo, ".claude"), filepath.Join(other, "brain")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, "brain", "brain.json"), []byte(`{"project":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	t.Setenv("HOME", filepath.Join(base, "home"))
	for _, k := range store.BrainPathVars {
		t.Setenv(k, "")
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(repo, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ file, body, key, val string }{
		{".claude/settings.json", `{"env":{"OS_BRAIN_HOME":"../other/brain"}}`, "OS_BRAIN_HOME", "../other/brain"},
		{".claude/settings.json", `{"env":{"OS_BRAIN_DB":"` + filepath.Join(other, "brain", "brain.db") + `"}}`,
			"OS_BRAIN_DB", filepath.Join(other, "brain", "brain.db")},
		{".claude/settings.local.json", `{"env":{"OS_BRAIN_PROJECT_ROOT":"` + other + `"}}`, "OS_BRAIN_PROJECT_ROOT", other},
		{".mcp.json", `{"mcpServers":{"brain":{"env":{"OS_BRAIN_HOME":"` + filepath.Join(other, "brain") + `"}}}}`,
			"OS_BRAIN_HOME", filepath.Join(other, "brain")},
	} {
		for _, f := range []string{".claude/settings.json", ".claude/settings.local.json", ".mcp.json"} {
			_ = os.Remove(filepath.Join(repo, f))
		}
		write(c.file, c.body)
		t.Setenv(c.key, c.val)
		cwd := sub
		if !filepath.IsAbs(c.val) {
			cwd = repo
		}
		if err := store.CheckRepoEnv(cwd); err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s from %s pointing outside the repository was allowed: %v", c.key, c.file, err)
		}
		// The same variable set by the person to a value the repository never declared.
		t.Setenv(c.key, filepath.Join(other, "brain", "x"))
		if err := store.CheckRepoEnv(cwd); err != nil {
			t.Errorf("the person's own %s was refused: %v", c.key, err)
		}
		t.Setenv(c.key, "")
	}

	write(".claude/settings.json", `{"env":{"OS_BRAIN_HOME":"brain","OS_BRAIN_DB":"`+store.SharedDBPath()+`"}}`)
	t.Setenv("OS_BRAIN_HOME", "brain")
	t.Setenv("OS_BRAIN_DB", store.SharedDBPath())
	if err := store.CheckRepoEnv(repo); err != nil {
		t.Errorf("a repository naming its own brain was refused: %v", err)
	}
}
