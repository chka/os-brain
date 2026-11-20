package brain

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chka/os-brain/internal/testguard"
)

// Hermeticity is a property of the RUN, not a habit of each test: every command registers
// its project against the shared index, so one test that forgets to pin its paths writes a
// row into the developer's real spine and nothing fails. The paths are pinned once here for
// the whole package, and internal/testguard then proves no real spine gained a project.
func TestMain(m *testing.M) {
	guard := testguard.Start()
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

// mustConfig reads brain.json in a test, where a parse error is a failure rather than a value.
func mustConfig(t *testing.T, path string) Config {
	t.Helper()
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
