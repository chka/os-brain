package testguard

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Run is the body of every package's TestMain. Hermeticity is a property of the RUN, not a
// habit of each test: every command registers its project against the shared index, so one
// test that forgets to pin its paths writes a row into the developer's real spine and
// nothing fails. Run records the real spines, pins every machine-level path to a sandbox for
// the whole package, runs the tests, proves no real spine gained a project, and exits.
//
// A package with extra floor of its own (internal/clitest's stand-in for the person at the
// terminal) sets it up first and then calls Run.
func Run(m *testing.M) {
	guard := Start()
	sandbox, err := os.MkdirTemp("", "os-brain-suite-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create the test sandbox:", err)
		os.Exit(1)
	}
	// Pinned for the whole package. An individual test may still t.Setenv its own paths;
	// this is the floor, not a ceiling.
	os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(sandbox, "config"))
	os.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(sandbox, "config", "brain", "brain.db"))
	// These name a specific store or project, and a value inherited from the shell that
	// launched `go test` would point the suite at whatever brain that shell was in.
	os.Unsetenv("OS_BRAIN_DB")
	os.Unsetenv("OS_BRAIN_PROJECT")
	os.Unsetenv("OS_BRAIN_HOME")

	code := guard.Report(m.Run())
	os.RemoveAll(sandbox)
	os.Exit(code)
}
