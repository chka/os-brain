package brain

import (
	"os"
	"path/filepath"
	"testing"
)

// Host detection treats ~/.claude as evidence that Claude Code is installed. But ~/.claude
// is also where os-brain keeps its own store by default, and where the team edition
// (ns-brain) keeps its store on a machine that has both. A ~/.claude holding only those
// stores is evidence of a brain, not of Claude Code, and must not get Claude Code wired into
// every project init touches.
func TestOwnSpineAloneIsNotClaudeCode(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if agentHomeDir(claude) {
		t.Fatal("a missing directory counted as an agent")
	}
	if err := os.MkdirAll(claude, 0o700); err != nil {
		t.Fatal(err)
	}
	if !agentHomeDir(claude) {
		t.Fatal("an empty agent directory stopped counting as installed")
	}
	if err := os.MkdirAll(filepath.Join(claude, "os-brain"), 0o700); err != nil {
		t.Fatal(err)
	}
	if agentHomeDir(claude) {
		t.Fatal("~/.claude holding only os-brain's store counted as Claude Code")
	}
	if err := os.MkdirAll(filepath.Join(claude, "brain"), 0o700); err != nil {
		t.Fatal(err)
	}
	if agentHomeDir(claude) {
		t.Fatal("~/.claude holding only the two editions' stores counted as Claude Code")
	}
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !agentHomeDir(claude) {
		t.Fatal("a real Claude Code directory was not detected")
	}
}
