package cli

import (
	"flag"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/clitest"
	"github.com/chka/os-brain/internal/person"
	"github.com/chka/os-brain/internal/person/persontest"
)

// A remote-do (`dm --rdo`) asks another session to carry out an instruction and accepting
// one releases that instruction into this session. If an agent could accept on its own, a
// prompt injected into its context could send itself a remote-do and then accept it. So
// accepting is a person's decision:
//   - refused over every MCP transport, where the caller is always an agent;
//   - refused on the CLI when there is no terminal to ask at;
//   - refused from an agent's process even when a terminal exists, without prompting;
//   - otherwise confirmed by typing yes, after the prompt names the remote-do.
//
// Rejecting authorises nothing, so it stays open to the agent.
func TestDMAcceptIsAPersonsDecision(t *testing.T) {
	clitest.Brain(t, "mcptest")
	clitest.UseTerminal(t, nil)
	run := func(tr Transport, args ...string) (string, error) {
		var buf strings.Builder
		err := runWithVia(callVia{transport: tr}, "dm", args, &buf,
			flag.NewFlagSet("dm", flag.ContinueOnError), false, false, "", "")
		return buf.String(), err
	}
	// Over MCP: the command run on an MCP transport's behalf. The tool itself is
	// TestDMAcceptIsRefusedAsAnMCPTool in internal/mcp.
	for _, tr := range []Transport{TransportMCPStdio, TransportMCPHTTP} {
		if _, err := run(tr, "--accept", "7"); err == nil || !strings.Contains(err.Error(), "refused over MCP") {
			t.Errorf("dm --accept over %s: %v", tr, err)
		}
	}

	// On the CLI with no terminal.
	if _, err := run(TransportCLI, "--accept", "7"); err == nil || !strings.Contains(err.Error(), "controlling terminal") {
		t.Errorf("dm --accept on the CLI with no terminal: %v", err)
	}

	// From an agent's process: refused even though the terminal would answer yes. The
	// prompt is never shown, so a person is not asked to approve what an agent started.
	tty := &persontest.FakeTTY{Inputs: []string{"yes"}}
	clitest.UseTerminal(t, tty)
	t.Setenv(person.EnvTestAgent, "1")
	if _, err := run(TransportCLI, "--accept", "7"); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Errorf("dm --accept from an agent's process with a terminal: %v", err)
	}
	t.Setenv(person.EnvTestAgent, "")
	if len(tty.Shown) != 0 {
		t.Errorf("an agent's process was shown the prompt: %q", tty.Shown)
	}

	// A person who answers no accepts nothing and the prompt named the remote-do.
	tty = &persontest.FakeTTY{Inputs: []string{"no"}}
	clitest.UseTerminal(t, tty)
	if _, err := run(TransportCLI, "--accept", "7"); err == nil || !strings.Contains(err.Error(), "nothing accepted") {
		t.Errorf("an accept the person declined: %v", err)
	}
	if !strings.Contains(strings.Join(tty.Shown, ""), "#7") {
		t.Errorf("the person was not told which remote-do: %q", tty.Shown)
	}
	// A person who answers yes gets past the gate to the store, which on this empty brain
	// holds nothing to accept: that error proves the gate let the call through.
	tty = &persontest.FakeTTY{Inputs: []string{"yes"}}
	clitest.UseTerminal(t, tty)
	if _, err := run(TransportCLI, "--accept", "7"); err == nil || !strings.Contains(err.Error(), "nothing to accept") {
		t.Errorf("a confirmed accept did not reach the store: %v", err)
	}

	// Rejecting needs no person: over MCP with no terminal it reaches the store as well.
	clitest.UseTerminal(t, nil)
	if _, err := run(TransportMCPStdio, "--reject", "7"); err == nil || !strings.Contains(err.Error(), "nothing to reject") {
		t.Errorf("dm --reject should stay the agent's to run: %v", err)
	}
}
