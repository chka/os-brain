package cli

import (
	"flag"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/clitest"
)

// wakeup runs the shell commands (probes) a project declares and `wakeup trust` approves
// them, so trusting is approving code to run on this machine. It is a person's act:
// refused over MCP and refused on the CLI without a terminal. Adding or removing a probe
// is refused over HTTP as well: probes are commands this machine runs and a remote caller
// does not get to edit them.
func TestWakeupTrustIsAPersonsDecision(t *testing.T) {
	clitest.Brain(t, "mcptest")
	clitest.UseTerminal(t, nil)
	run := func(tr Transport, args ...string) error {
		var buf strings.Builder
		return runWithVia(callVia{transport: tr}, "wakeup", args, &buf,
			flag.NewFlagSet("wakeup", flag.ContinueOnError), false, false, "", "")
	}
	if err := run(TransportCLI, "add", "--name", "stack", "--run", "echo up"); err != nil {
		t.Fatal(err)
	}
	for _, tr := range []Transport{TransportMCPStdio, TransportMCPHTTP} {
		if err := run(tr, "trust"); err == nil || !strings.Contains(err.Error(), "refused over MCP") {
			t.Errorf("trust over %s: %v", tr, err)
		}
	}
	if err := run(TransportCLI, "trust"); err == nil || !strings.Contains(err.Error(), "controlling terminal") {
		t.Errorf("trust on the CLI with no terminal: %v", err)
	}
	for _, sub := range [][]string{{"add", "--name", "x", "--run", "id"}, {"rm", "--name", "stack"}} {
		if err := run(TransportMCPHTTP, sub...); err == nil || !strings.Contains(err.Error(), "refused over HTTP") {
			t.Errorf("wakeup %s over MCP HTTP: %v", sub[0], err)
		}
	}

	// The tool itself is TestWakeupTrustIsRefusedAsAnMCPTool in internal/mcp.
}
