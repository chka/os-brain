package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

// printHelp answers `help` and `help <command>`. Bare `help` used to be the only form, so a
// session that looked up one command got the same general list it already had, and learned
// that asking does not pay.
func printHelp(args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return helpFor(args[0], stdout)
	}
	fmt.Fprintln(stdout, usage)
	fmt.Fprint(stdout, helpTail())
	return nil
}

// helpFor prints one command's summary and its own flags. The flags come from the command's
// own registration through the same probe the MCP schemas use, and the summary from the
// `usage` block, so nothing here is a second list that can drift.
func helpFor(cmd string, stdout io.Writer) error {
	fs, err := probeFlags(cmd)
	if err != nil {
		return fmt.Errorf("no command called %q. `os-brain help` lists all of them", cmd)
	}
	fmt.Fprintf(stdout, "os-brain %s", cmd)
	if summary := usageSummary(cmd); summary != "" {
		fmt.Fprintf(stdout, " - %s", summary)
	}
	fmt.Fprintln(stdout)
	n := 0
	fs.VisitAll(func(*flag.Flag) { n++ })
	if n == 0 {
		fmt.Fprintln(stdout, "\n  no flags of its own.")
	} else {
		fmt.Fprintln(stdout)
		fs.SetOutput(stdout)
		fs.PrintDefaults()
	}
	// Several commands take a subcommand or an id positionally and register nothing for
	// it, so a flag list on its own reads as the whole interface when it is half of one.
	fmt.Fprintln(stdout, "\nPositional arguments (a subcommand, an id, a query) follow the "+
		"command name.\nRun it with none to see what it does by default.")
	return nil
}

// usageSummary is cmd's one-line description from the usage block, or "".
func usageSummary(cmd string) string {
	for _, line := range strings.Split(usage, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == cmd && strings.HasPrefix(line, "  ") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), cmd))
		}
	}
	return ""
}

// helpTail is what `help` adds over `--help`: the scoping rule and the decay tiers, which
// are the two things a session gets wrong without being told.
func helpTail() string {
	tier := func(m map[string]bool) string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	return "\nScopes nest on ':' and only flow inward: a recall in channel:deploy:prod\n" +
		"also sees channel:deploy, channel and global, never the reverse. Declared\n" +
		"scopes and event kinds live in brain.json; `os-brain map` reconciles them\n" +
		"against what is actually stored, `os-brain review` reports hygiene.\n" +
		"\nMemory types by decay tier:\n" +
		"  A (never decays): " + tier(brain.TierA) + "\n" +
		"  B (never decays): " + tier(brain.TierB) + "\n" +
		"  C (~60d):         " + tier(brain.TierC) + "\n" +
		"  D (~4d):          " + tier(brain.TierD) + "\n" +
		"  docs (no decay):  " + tier(brain.DocTypes) + "\n" +
		"\nEvery memory carries a confidence: confirmed (x1.15), reported (x1.0),\n" +
		"assumed (x0.8). Verify load-bearing facts periodically (os-brain verify <id>);\n" +
		"`os-brain review` lists the ones nobody has re-checked.\n"
}
