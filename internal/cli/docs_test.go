package cli

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/brain"
	"github.com/chka/os-brain/internal/clitest"
)

// Before the Go binary, each brain carried a Python script at brain/brain that agents ran
// directly. That client is retired. A doc that still tells an agent to run `brain/brain`
// sends it to a script that is either gone or an old copy and an old copy refuses a
// migrated database ("this brain.py only understands v12") and stops. So no line of the
// docs may mention `brain/brain`, with three kinds of exception that are not instructions
// to run the old client:
//   - a path ending in brain/brain.db, which is a project's database, not the script;
//   - a line saying the client is retired, or what to substitute for it;
//   - a line about backups, which names the old file in order to keep it safe.
func TestDocsDoNotTeachTheRetiredClient(t *testing.T) {
	for _, doc := range []string{"HOWTO.md", "BEST-PRACTICES.md"} {
		body, err := brain.AgentDoc(doc)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, "brain/brain") {
				continue
			}
			if strings.Contains(line, "brain/brain.db") ||
				strings.Contains(line, "retired") || strings.Contains(line, "substitute") ||
				strings.Contains(line, "backup") {
				continue
			}
			t.Errorf("%s:%d still teaches the retired client: %s", doc, i+1, strings.TrimSpace(line))
		}
	}
}

// The help output is what a session checks when the docs and its memory disagree. Go's
// flag package renders `-flag`; every document here writes `--flag`. A real session read
// the single-dash help, concluded the double-dash form had been dropped, rewrote its
// project's CLAUDE.md and stored a memory saying so. The tool taught it that.
func TestHelpRendersFlagsTheWayTheDocsWriteThem(t *testing.T) {
	// A brain of its own: run from a checkout with no brain.json, the command would stop at
	// "no brain here" before reaching what this test checks.
	clitest.Brain(t, "mcptest")
	for _, cmd := range []string{"recall", "remember", "update", "measure"} {
		var buf bytes.Buffer
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		fs.SetOutput(&buf)
		usageWithDoubleDash(fs, cmd, &buf)
		if err := runWith(cmd, []string{"-h"}, &buf, fs, false, false, "", ""); err != nil &&
			!errors.Is(err, flag.ErrHelp) {
			t.Fatalf("%s -h: %v", cmd, err)
		}
		out := buf.String()
		if strings.Contains(out, "\n  -") && !strings.Contains(out, "\n  --") {
			t.Errorf("%s help still renders single-dash flags:\n%s", cmd, out)
		}
		if !strings.Contains(out, "--json") {
			t.Errorf("%s help does not show --json:\n%s", cmd, out)
		}
	}
}

// OS_BRAIN_HOME once bypassed discovery and with it the promise the docs make: "if it
// cannot find a brain it says so and stops. It will not invent one." A session whose shell
// was left in a subdirectory got an empty store, concluded the project had no memory, and
// had a stray brain.db written into its repository.
func TestBrainHomeAtANonBrainRefusesInsteadOfCreatingOne(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "not-a-brain")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_HOME", empty)
	var buf bytes.Buffer
	err := Run([]string{"stats"}, &buf)
	if err == nil {
		t.Fatalf("stats invented a brain instead of refusing:\n%s", buf.String())
	}
	for _, want := range []string{"not a brain", "no brain.json", "os-brain init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(empty, "brain.db")); statErr == nil {
		t.Fatal("it created a database in a directory that was not a brain")
	}
}

// The docs exist twice: authored at the repo root and embedded under internal/brain/docs
// so they travel inside the binary. Two copies of a file always drift and the one that
// drifts silently is the one nobody edits, which here is the copy that actually ships.
func TestEmbeddedDocsMatchTheAuthoredOnes(t *testing.T) {
	for _, name := range []string{"HOWTO.md", "BEST-PRACTICES.md"} {
		embedded, err := brain.AgentDoc(name)
		if err != nil {
			t.Fatal(err)
		}
		// The authored copy is at the repo root, two levels above this package.
		authored, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(embedded) != string(authored) {
			t.Errorf("%s differs between the repo root and internal/brain/docs. "+
				"Copy it across: cp %s internal/brain/docs/", name, name)
		}
	}
}
