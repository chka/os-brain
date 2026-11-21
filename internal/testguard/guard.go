// Package testguard proves a test run wrote nothing into the real stores on this machine.
//
// Every command registers the project it ran in against the shared index (the "spine"), so a
// test that opens a store without pinned paths writes a row into the developer's real spine,
// and nothing fails. ns-brain learned this from 67 phantom projects and from a project "001"
// rooted in a test's temporary directory. Each package's TestMain pins the paths once for
// the whole run; this package is the proof that the pinning held.
//
// The proof compares the real spines' project ids before and after the run. It cannot look
// for temporary paths: the index stores a hash of the project directory, never the directory
// (see HashRoot in internal/store), so a leaked row carries no path to recognise. A new id
// that appears while the suite runs is reported. The one false alarm is a person creating a
// brand new project in another terminal during the run, which the message says.
package testguard

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Guard holds the project ids each real spine had when the run started.
type Guard struct {
	before map[string]map[string]bool
}

// RealSpines are the stores a test run must never write into, resolved from the environment
// the suite was started with, before TestMain pins anything: os-brain's own store, and the
// store of an ns-brain installed on the same machine, which holds that user's real memories.
func RealSpines() []string {
	var candidates []string
	if v := os.Getenv("OS_BRAIN_GLOBAL_DB"); v != "" {
		candidates = append(candidates, v)
	}
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg = filepath.Join(home, ".claude")
		}
	}
	if cfg != "" {
		candidates = append(candidates,
			filepath.Join(cfg, "os-brain", "brain.db"),
			filepath.Join(cfg, "brain", "brain.db"))
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Start records the real spines' project ids. Call it before pinning any path.
func Start() Guard {
	g := Guard{before: map[string]map[string]bool{}}
	for _, spine := range RealSpines() {
		// A spine that cannot be read now is left out rather than recorded as empty.
		if ids, ok := projectIDs(spine); ok {
			g.before[spine] = ids
		} else {
			fmt.Fprintf(os.Stderr, "testguard: %s could not be read; not checked this run\n", spine)
		}
	}
	return g
}

// Leaks reports, one line per row, every project that appeared in a real spine since Start.
func (g Guard) Leaks() []string {
	var out []string
	for spine, before := range g.before {
		after, ok := projectNames(spine)
		if !ok {
			out = append(out, fmt.Sprintf("%s: could not be read after the run, so it was not checked", spine))
			continue
		}
		for id, name := range after {
			if !before[id] {
				out = append(out, fmt.Sprintf("%s: new project %q (root id %s)", spine, name, id))
			}
		}
	}
	sort.Strings(out)
	return out
}

// Report prints the leaks to stderr and returns the exit code the run should end with.
func (g Guard) Report(code int) int {
	leaks := g.Leaks()
	if len(leaks) == 0 {
		return code
	}
	fmt.Fprintln(os.Stderr, "\nTHIS TEST RUN WROTE INTO A REAL SHARED INDEX:")
	for _, l := range leaks {
		fmt.Fprintln(os.Stderr, "  "+l)
	}
	fmt.Fprintln(os.Stderr, "A test opened a store without pinned paths. Remove the rows, then find "+
		"the test. (If you created a new project in another terminal during the run, that is it.)")
	return 1
}

func projectIDs(spine string) (map[string]bool, bool) {
	names, ok := projectNames(spine)
	ids := map[string]bool{}
	for id := range names {
		ids[id] = true
	}
	return ids, ok
}

// projectNames reads root_id -> name from a spine, read-only, waiting out a writer. ok is
// false when the file could not be read at all: a real spine is in use by the person's own
// sessions while the suite runs, and a read that lost to a lock must not be mistaken for an
// empty index, or every existing project would look new afterwards. A file that is not a
// spine (no projects table) has nothing to protect and reads as empty.
func projectNames(spine string) (map[string]string, bool) {
	out := map[string]string{}
	db, err := sql.Open("sqlite", "file:"+spine+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return out, false
	}
	defer db.Close()
	rows, err := db.Query("SELECT root_id, name FROM projects")
	if err != nil {
		return out, strings.Contains(err.Error(), "no such table")
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			out[id] = strings.TrimSpace(name)
		}
	}
	return out, rows.Err() == nil
}
