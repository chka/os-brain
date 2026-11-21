package mcp

import (
	"regexp"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/brain"
)

// The agent docs ship inside the binary, so they are part of the release and can be
// checked like code. Drift here is not cosmetic: HOWTO.md is the only thing telling an
// agent what the commands are and a command that no longer exists sends it back to
// reading files, which is the failure the brain exists to stop. A command missing from the
// table is one no agent will ever use.
func TestHowtoTeachesOnlyCommandsThatExist(t *testing.T) {
	body, err := brain.AgentDoc("HOWTO.md")
	if err != nil {
		t.Fatal(err)
	}
	// Only the reference table: the document has other tables (decay tiers, confidence
	// values) whose first column is not a command.
	text := string(body)
	from := strings.Index(text, "## 12. Full command reference")
	if from < 0 {
		t.Fatal("the command reference section is gone")
	}
	table := text[from:]
	if end := strings.Index(table, "\n## 13"); end > 0 {
		table = table[:end]
	}
	rows := regexp.MustCompile("(?m)^\\| `([a-z-]+)")
	documented := map[string]bool{}
	for _, m := range rows.FindAllStringSubmatch(table, -1) {
		documented[m[1]] = true
	}
	// A parse that finds almost nothing means the table's layout changed and every check
	// below would pass vacuously.
	if len(documented) < 30 {
		t.Fatalf("only %d commands parsed from the reference table; its format changed", len(documented))
	}
	known := map[string]bool{}
	for _, c := range commandNames() {
		known[c] = true
	}
	// commandNames lists the MCP tools. The commands in notATool (the servers that block
	// until killed and the hooks that read stdin) are real commands but not tools, so the
	// table may document them in either direction without failing.
	for cmd := range documented {
		if notATool[cmd] {
			continue
		}
		if !known[cmd] {
			t.Errorf("HOWTO documents %q, which is not a command", cmd)
		}
	}
	for cmd := range known {
		if notATool[cmd] {
			continue
		}
		if !documented[cmd] {
			t.Errorf("command %q is missing from the HOWTO reference table", cmd)
		}
	}
}
