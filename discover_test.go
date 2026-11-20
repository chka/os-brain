package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/brain"
)

// How a command finds its brain. A binary on PATH once resolved its brain against its own
// directory, so `os-brain bootstrap` run in a project created /usr/local/bin/brain.db and
// reported on a project called "local". The replacement walks up from the working
// directory to the project's brain and refuses to invent one anywhere else.

// discoverEnv clears every variable that names a brain outright, so the test exercises
// discovery alone and pins the machine-level paths to a scratch directory.
func discoverEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("OS_BRAIN_HOME", "")
	t.Setenv("OS_BRAIN_DB", "")
	t.Setenv("OS_BRAIN_PROJECT", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(root, "cfg", "brain", "brain.db"))
	return root
}

// A session's shell is often deep inside the project, not at its root.
func TestDiscoverFindsTheBrainFromASubdirectory(t *testing.T) {
	root := discoverEnv(t)
	if err := brain.Init(&bytes.Buffer{}, root, brain.InitArgs{}); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "internal", "server")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := brain.DiscoverHome(deep)
	if !ok {
		t.Fatal("no brain found from a subdirectory of the project")
	}
	if want := filepath.Join(root, "brain"); got != want {
		t.Errorf("found %s, want %s", got, want)
	}
}

// `init --dir` can put the brain anywhere in the project; discovery follows what init
// wired rather than assuming the default brain/ directory.
func TestDiscoverFindsABrainInstalledUnderACustomDirectory(t *testing.T) {
	root := discoverEnv(t)
	if err := brain.Init(&bytes.Buffer{}, root, brain.InitArgs{Dir: "marketing/brain"}); err != nil {
		t.Fatal(err)
	}
	got, ok := brain.DiscoverHome(root)
	if !ok {
		t.Fatal("a brain wired by init was not discoverable from the project root")
	}
	if want := filepath.Join(root, "marketing", "brain"); got != want {
		t.Errorf("found %s, want %s", got, want)
	}
}

// Outside any brain the command must fail closed and say how to fix it. A silently created
// empty store reads to a session as "this project has no memory", which is false.
func TestACommandOutsideAnyBrainRefusesInsteadOfCreatingOne(t *testing.T) {
	root := discoverEnv(t)
	t.Chdir(root)

	var out bytes.Buffer
	err := run([]string{"stats"}, &out)
	if err == nil {
		t.Fatal("stats outside a brain succeeded; it must fail closed")
	}
	if !strings.Contains(err.Error(), "os-brain init") || !strings.Contains(err.Error(), "OS_BRAIN_HOME") {
		t.Errorf("the refusal does not say what to do:\n%s", err)
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "brain.db") {
			t.Errorf("a database was created anyway: %s", e.Name())
		}
	}
}

// init is the one command that must run where no brain exists yet; the refusal above must
// not catch it.
func TestInitStillWorksWithNoBrainToDiscover(t *testing.T) {
	root := discoverEnv(t)
	t.Chdir(root)

	var out bytes.Buffer
	if err := run([]string{"init"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "brain", "brain.db")); err != nil {
		t.Fatalf("init did not create the brain: %v", err)
	}
	// The next command finds the new brain without being told where it is.
	out.Reset()
	if err := run([]string{"stats"}, &out); err != nil {
		t.Fatal(err)
	}
}
