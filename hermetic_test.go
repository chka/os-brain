package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chka/os-brain/internal/person"
	"github.com/chka/os-brain/internal/testguard"
)

// TestMain gives this package the same floor the internal/brain suite has, for the same
// reason: every command registers its project in the machine-wide index (the spine), so one
// test that forgets to pin its paths writes a row into the developer's real spine and
// nothing fails. Every path is pinned to a sandbox before any test runs, and
// internal/testguard reports a run that touched the real store anyway.
func TestMain(m *testing.M) {
	guard := testguard.Start()
	sandbox, err := os.MkdirTemp("", "os-brain-cli-suite-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create the test sandbox:", err)
		os.Exit(1)
	}
	os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(sandbox, "config"))
	os.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(sandbox, "config", "brain", "brain.db"))
	os.Unsetenv("OS_BRAIN_DB")
	os.Unsetenv("OS_BRAIN_PROJECT")
	os.Unsetenv("OS_BRAIN_HOME")
	// Whoever runs the suite (a person's shell, an agent, CI) has a process tree of its own,
	// and whether a command sees a person must not depend on it: test mode makes the agent
	// check read only person.EnvTestAgent, which the tests that play an agent set themselves.
	os.Setenv(person.EnvTest, "1")
	os.Unsetenv("CLAUDE_CODE_SESSION_ID")
	os.Unsetenv("CLAUDECODE")
	// A suite run from a terminal has a real /dev/tty: no test may prompt on it.
	person.ReplaceTerminal(func() (person.TTY, error) { return nil, person.ErrNoTTY })

	code := guard.Report(m.Run())
	os.RemoveAll(sandbox)
	os.Exit(code)
}

// useTerminal makes the person-only commands read from tty for the rest of the test; nil
// means there is no terminal at all.
func useTerminal(t *testing.T, tty person.TTY) {
	t.Helper()
	restore := person.ReplaceTerminal(func() (person.TTY, error) {
		if tty == nil {
			return nil, person.ErrNoTTY
		}
		return tty, nil
	})
	t.Cleanup(restore)
}
