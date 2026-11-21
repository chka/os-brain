package cli

import (
	"flag"
	"strings"
	"testing"
)

// How argv is split into flags and positional words. Two failures shaped this: a flag that
// takes a value silently swallowing the next flag as that value and text a person or an
// agent typed being read as a flag.

// swallowFS is a flag set with one value flag (--backup, returned so a test can read it)
// next to the kinds of flag it might swallow: a bool, an int and another string.
func swallowFS() (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	backup := fs.String("backup", "", "")
	fs.Bool("json", false, "")
	fs.Int("n", 0, "")
	fs.String("title", "", "")
	return fs, backup
}

// Go's flag package gives a value flag whatever word follows it, so `--backup --json` would
// write the backup to a file named "--json" and print text where JSON was asked for, both
// without a word. A value flag followed by another of the command's own flags is refused
// instead and the refusal names the flag that is missing its value.
func TestAValueFlagRefusesAnotherFlagAsItsValue(t *testing.T) {
	for _, args := range [][]string{
		{"--apply", "--backup", "--json"},
		{"--backup", "-json"},
		{"--backup", "--n=3"},
		{"pos", "--backup", "--title", "x"},
	} {
		fs, _ := swallowFS()
		fs.Bool("apply", false, "")
		_, err := parseInterleaved(fs, args)
		if err == nil || !strings.Contains(err.Error(), "--backup needs a value") {
			t.Errorf("%q: want the swallowed-flag refusal, got %v", args, err)
		}
	}
}

// The refusal above must not catch real values that happen to start with a dash: a value
// joined with "=", stdin's "-", a negative number, a bare "--", or a dashed word that is
// not one of the command's flags.
func TestValuesThatOnlyLookLikeFlagsStillParse(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--backup", "/tmp/b.json", "--json"}, "/tmp/b.json"},
		{[]string{"--backup=--json"}, "--json"},
		{[]string{"--backup", "-"}, "-"},
		{[]string{"--backup", "-5"}, "-5"},
		{[]string{"--backup", "--"}, "--"},
		{[]string{"--backup", "--not-a-flag-here"}, "--not-a-flag-here"},
	} {
		fs, backup := swallowFS()
		if _, err := parseInterleaved(fs, tc.args); err != nil {
			t.Errorf("%q: %v", tc.args, err)
			continue
		}
		if *backup != tc.want {
			t.Errorf("%q: backup=%q, want %q", tc.args, *backup, tc.want)
		}
	}
}

// Text typed into the UI's search box, or passed in an MCP tool's args string, is words to
// search or store, never flags. Otherwise a search for "--all-projects" would widen the
// search to every project and an agent's args could switch on any flag the tool schema
// does not offer. The text is passed after a "--" terminator, so:
//   - the parser stops reading flags at "--" and a "--" given as a flag's value does not
//     count as the terminator;
//   - MCP args and the UI's argv put the "--" in front of the text (help, which takes no
//     flags, gets none);
//   - the UI's routes answer a query that looks like a flag instead of failing on it.
func TestUserTextNeverBecomesAFlag(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	all := fs.Bool("all-projects", false, "")
	title := fs.String("title", "", "")
	pos, err := parseInterleaved(fs, []string{"q1", "--title", "t", "--", "--all-projects", "q2", "--title", "u"})
	if err != nil {
		t.Fatal(err)
	}
	if *all || *title != "t" || strings.Join(pos, "|") != "q1|--all-projects|q2|--title|u" {
		t.Fatalf("all=%v title=%q pos=%q", *all, *title, pos)
	}
	fs = flag.NewFlagSet("x", flag.ContinueOnError)
	all = fs.Bool("all-projects", false, "")
	title = fs.String("title", "", "")
	pos, err = parseInterleaved(fs, []string{"--title", "--", "q", "--all-projects"})
	if err != nil {
		t.Fatal(err)
	}
	if !*all || *title != "--" || strings.Join(pos, "|") != "q" {
		t.Fatalf("a -- taken as a value ended the flags: all=%v title=%q pos=%q", *all, *title, pos)
	}

	// The same rule over MCP and from the UI: TestUserTextNeverBecomesAFlagOverMCP in
	// internal/mcp and TestUserTextNeverBecomesAFlagInTheUI in internal/webui.
}
