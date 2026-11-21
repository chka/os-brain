package install

import (
	"bytes"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testguard"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// remember stores a memory through the ordinary command, filling in the defaults the CLI
// would supply, and returns what the command printed. memory does not import install, so
// the tests may seed through it.
func remember(t *testing.T, s *store.Store, a memory.RememberArgs) string {
	t.Helper()
	var buf bytes.Buffer
	if a.Type == "" {
		a.Type = "note"
	}
	if a.Scope == "" {
		a.Scope = "global"
	}
	if a.Importance == 0 {
		a.Importance = 3
	}
	if a.Source == "" {
		a.Source = "session"
	}
	if err := memory.New(s).Remember(&buf, a); err != nil {
		t.Fatalf("remember %q: %v", a.Title, err)
	}
	return buf.String()
}

// mustConfig reads brain.json in a test, where a parse error is a failure rather than a value.
func mustConfig(t *testing.T, path string) store.Config {
	t.Helper()
	c, err := store.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
