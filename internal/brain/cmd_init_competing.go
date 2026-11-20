package brain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A brain beside another auto-loaded memory store guarantees a silent contradiction: both
// are read into every session and the one that loads first wins regardless of which is
// right. init detects such a store and says so; it never moves or deletes it.

// competingStore is another auto-loaded memory store found beside the brain being
// installed: where it is, and how many files are in it.
type competingStore struct {
	Path  string
	Files int
}

// competingStores looks in both places a second memory store actually lives: inside the
// project, and in Claude Code's own auto-memory, which sits outside the repository under
// <config>/projects/<slug>/memory/. Looking only inside the project misses the common case:
// a project can be perfectly clean and still have files loading into every session. In one
// real case a stale host fact from the auto-memory was believed and acted on, then corrected
// in BOTH stores, so the divergence went two ways.
//
// It reads and counts. It never moves, rewrites or deletes anything: a user's own memory is
// not init's to destroy.
func competingStores(root, dir string) []competingStore {
	var found []competingStore
	add := func(path string) {
		if path == "" || alreadyFound(found, path) {
			return
		}
		if n, ok := storeFileCount(path); ok {
			found = append(found, competingStore{Path: path, Files: n})
		}
	}

	// Inside the project. The brain's own directory is excluded by name, so `--dir memory`
	// does not report itself.
	add(filepath.Join(root, "MEMORY.md"))
	if dir != "memory" {
		add(filepath.Join(root, "memory"))
	}

	// Claude Code's auto-memory, outside the project. slugDirs (cmd_global.go) is the one
	// implementation of the slug rule. Only the EXACT slug counts here: the host loads the
	// store for the path the session is actually running in, so a directory that merely
	// ends with this project's name is a DIFFERENT project's memory. Naming it would tell
	// the user to go and delete somebody else's store, which is the worse of the two
	// failures this warning can have.
	exactDirs, _ := slugDirs(root)
	for _, d := range exactDirs {
		add(filepath.Join(d, "memory"))
	}
	return found
}

func alreadyFound(found []competingStore, path string) bool {
	for _, f := range found {
		if f.Path == path {
			return true
		}
	}
	return false
}

// storeFileCount is 1 for a file, and for a directory the number of files directly in it.
// A missing path, an unreadable directory or an empty one is not a store.
func storeFileCount(path string) (int, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	if !info.IsDir() {
		return 1, true
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, false
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n, n > 0
}

func competingJSON(found []competingStore) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, f := range found {
		out = append(out, map[string]interface{}{"path": f.Path, "files": f.Files})
	}
	return out
}

// competingWarning is deliberately the first thing init prints. A warning that arrives
// after the next-steps block is a footnote, and the failure it describes is silent: two
// auto-loaded stores never announce that they disagree.
func competingWarning(found []competingStore) string {
	var b strings.Builder
	b.WriteString("ANOTHER MEMORY STORE IS ALREADY LOADED FOR THIS PROJECT.\n")
	for _, f := range found {
		word := "files"
		if f.Files == 1 {
			word = "file"
		}
		b.WriteString(fmt.Sprintf("  %s  (%d %s)\n", f.Path, f.Files, word))
	}
	b.WriteString("\n" +
		"  Two auto-loaded memory stores will silently contradict each other. Both are read\n" +
		"  into every session, nothing records which is newer, and the session believes\n" +
		"  whichever it reads — including a fact that was corrected months ago in the other\n" +
		"  one. Nothing will report the divergence; it is paid as a wrong answer.\n" +
		"\n" +
		"  Resolve it yourself, in this order:\n" +
		"    1. read what is STILL TRUE out of the store(s) above and store it here:\n" +
		"         os-brain remember --title \"...\" --body \"...\"\n" +
		"    2. then delete that store yourself.\n" +
		"\n" +
		"  init has not read, moved, rewritten or deleted any of it, and will not: that store\n" +
		"  is yours, and only you can say which half of it is still true.")
	return b.String()
}
