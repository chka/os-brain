// Package clitest is the test floor shared by every package whose tests run commands (cli,
// mcp, webui): the hermetic TestMain, a fresh brain to run them against, and a stand-in for
// the person at the terminal. It is imported only by tests, so no binary carries it.
package clitest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chka/os-brain/internal/person"
	"github.com/chka/os-brain/internal/testguard"
)

// Main is the body of TestMain for a package that runs commands. It gives the package the
// same floor every package suite has, for the same reason: every command registers its
// project in the machine-wide index (the spine), so one test that forgets to pin its paths
// writes a row into the developer's real spine and nothing fails. Every path is pinned to a
// sandbox before any test runs, and internal/testguard reports a run that touched the real
// store anyway.
func Main(m *testing.M) {
	// Whoever runs the suite (a person's shell, an agent, CI) has a process tree of its own,
	// and whether a command sees a person must not depend on it: test mode makes the agent
	// check read only person.EnvTestAgent, which the tests that play an agent set themselves.
	os.Setenv(person.EnvTest, "1")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("CLAUDECODE")
	// A suite run from a terminal has a real /dev/tty: no test may prompt on it.
	person.ReplaceTerminal(func() (person.TTY, error) { return nil, person.ErrNoTTY })

	testguard.Run(m)
}

// Brain makes a fresh single-project brain called project in a temporary directory, pins
// every machine-level path beside it, names it through OS_BRAIN_HOME and returns the
// directory: the brain is dir/brain and the machine-level paths are under dir/cfg.
func Brain(t *testing.T, project string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"`+project+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "cfg", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_PROJECT", project)
	return dir
}

// UseTerminal makes the person-only commands read from tty for the rest of the test; nil
// means there is no terminal at all.
func UseTerminal(t *testing.T, tty person.TTY) {
	t.Helper()
	restore := person.ReplaceTerminal(func() (person.TTY, error) {
		if tty == nil {
			return nil, person.ErrNoTTY
		}
		return tty, nil
	})
	t.Cleanup(restore)
}
