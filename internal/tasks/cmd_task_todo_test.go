package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/fsguard"
)

// `task todo` writes the open tasks to TODO.md. That file is generated, never authored: a
// hand-written list beside a stored one guarantees a silent contradiction, and the one the
// agent loads automatically wins regardless of which is right. The renderer takes task rows
// rather than a database, so the file depends only on the rows it is given and the same rows
// always produce the same file.
func TestRenderTodoWritesTheFileFromTheRowsItIsGiven(t *testing.T) {
	due := int64(1788200000)
	rows := []*Task{
		{ID: 1, Title: "move the root key", Owner: "user", DueAt: &due, Schedule: "once"},
		{ID: 2, Title: "audit the build server", Owner: "agent", Body: "it carries a public port now"},
	}
	got := RenderTodo("acme", rows)
	for _, want := range []string{
		TodoMarker, "# acme — open tasks",
		"- [ ] move the root key", "- [ ] audit the build server", "_(agent)_",
		"it carries a public port now", "_2 open",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the file is missing %q:\n%s", want, got)
		}
	}
	if empty := RenderTodo("acme", nil); !strings.Contains(empty, "_nothing open_") {
		t.Errorf("an empty list should say so: %s", empty)
	}
}

// The guard is about the FILE, not the rows: a TODO.md without the generated marker was
// written by a person, and overwriting it would destroy their list. The check itself must not
// modify the file either.
func TestTodoRefusesToOverwriteAFileItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TODO.md")
	if err := os.WriteFile(path, []byte("# my hand-written list\n- ship it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fsguard.GuardOutfile(path, TodoMarker, false); err == nil {
		t.Fatal("a hand-written TODO.md was about to be overwritten")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "hand-written") {
		t.Error("the file was modified by the guard check")
	}
}
