package install

// Guards in `os-brain init` against two quiet ways to lose or leak a store: building a
// second brain beside one that already exists, and leaving one of the tool's own dumps of
// the store where `git add -A` would commit it.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- A second brain in the same project -------------------------------------------------

// A brain under a non-default directory name is invisible to a bare `os-brain`, so the next
// session concludes the project has none and runs init. That used to build a second store
// beside the first without a word, splitting the project's memory in two. Renaming brain/
// to something else is all it takes to set this up.
func TestInitRefusesASecondBrainItWasNotToldToMake(t *testing.T) {
	root := t.TempDir()
	if err := Init(io.Discard, root, InitArgs{Dir: "notesBrain", NoHook: true}); err != nil {
		t.Fatal(err)
	}
	err := Init(io.Discard, root, InitArgs{NoHook: true})
	if err == nil {
		t.Fatal("init built a second brain in a project that already had one")
	}
	if !strings.Contains(err.Error(), "notesBrain") {
		t.Errorf("the refusal does not name the brain already there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.json")); err == nil {
		t.Error("the refusal still created the second brain")
	}
}

// The guard above must not block a second brain asked for by name: `init --dir
// marketing/brain` beside brain/ is a documented install. init still says the first exists.
func TestAnExplicitDirMayStillMakeASecondBrain(t *testing.T) {
	root := t.TempDir()
	if err := Init(io.Discard, root, InitArgs{NoHook: true}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := Init(&out, root, InitArgs{Dir: "marketing/brain", NoHook: true}); err != nil {
		t.Fatalf("an explicit --dir was refused: %v", err)
	}
	if !strings.Contains(out.String(), "already has a brain") {
		t.Error("a second brain was created without saying the first one exists")
	}
}

// --- Keeping the store and its dumps out of git -----------------------------------------

// backup writes into the brain directory and refuses to write a path git would track. If
// init ignored only the database, a fresh install could not run backup, the one command
// that has to run before anything destructive.
func TestInitGitignoresTheDumpsAsWellAsTheDatabase(t *testing.T) {
	root := t.TempDir()
	if err := Init(io.Discard, root, InitArgs{Dir: "brain", NoHook: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"brain/brain.db*", "brain/*-backup-*"} {
		if !strings.Contains(string(body), want) {
			t.Errorf(".gitignore is missing %q:\n%s", want, body)
		}
	}
}

// Every file name the tool writes for a copy of the store must match a .gitignore pattern,
// checked with real glob matching rather than by eye. Two narrower patterns each missed one:
// `brain.db-*` misses `brain.db.pre-restore-<stamp>` (a dot, not a dash) and
// `*-backup-*.json` misses `brain-backup-<stamp>.db`. Several such files were once found
// unignored in a real project, each a complete copy of the store.
func TestEveryDumpTheToolWritesIsIgnored(t *testing.T) {
	root := t.TempDir()
	if err := Init(io.Discard, root, InitArgs{NoHook: true}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	patterns := strings.Split(string(body), "\n")
	for _, name := range []string{
		"brain.db", "brain.db-wal", "brain.db-shm",
		"brain.db-premove-20260810-164637",
		"brain.db-preupgrade-20260811-131811.json",
		"brain.db.pre-restore-20260811-195423",
		"brain.db.pre-localaxioms-20260811-2302",
		"brain-backup-20260812-173957.json",
		"brain-backup-20260812-173957.db",
		"brain-prune-20260812-173957.json",
		"everything.snapshot.md",
	} {
		if !gitignorePatternsCover(patterns, "brain/"+name) {
			t.Errorf("%s is not covered by .gitignore: a full copy of the store, one "+
				"`git add -A` from being committed in cleartext", name)
		}
	}
}

// gitignorePatternsCover reports whether any non-comment line of a .gitignore matches path.
func gitignorePatternsCover(patterns []string, path string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if ok, _ := filepath.Match(p, path); ok {
			return true
		}
	}
	return false
}
