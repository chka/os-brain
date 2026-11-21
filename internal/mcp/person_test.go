package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/chka/os-brain/internal/cli"
	"github.com/chka/os-brain/internal/clitest"
)

// The person-only commands refuse a caller on an MCP transport, which the CLI's own tests
// prove (internal/cli/*_person_test.go). These prove the same refusal reaches a model that
// calls the command as a tool.

// Accepting a remote-do releases an instruction into this session, so it is a person's
// decision; over MCP the caller is always an agent (TestDMAcceptIsAPersonsDecision).
func TestDMAcceptIsRefusedAsAnMCPTool(t *testing.T) {
	clitest.Brain(t, "mcptest")
	clitest.UseTerminal(t, nil)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	s.transport = cli.TransportMCPStdio
	if out, isErr := callText(t, s, "brain_dm", map[string]any{"accept": 7}); !isErr ||
		!strings.Contains(out, "refused over MCP") {
		t.Errorf("brain_dm {accept} was not refused over MCP: %s", out)
	}
}

// A release writes the banner every project's brief prints, so it is a person's act
// (TestReleaseIsAPersonsDecision).
func TestReleaseIsRefusedAsAnMCPTool(t *testing.T) {
	clitest.Brain(t, "mcptest")
	clitest.UseTerminal(t, nil)
	v := time.Now().Format("060102") + "01" // a version in the YYMMDDNN form, numbered today
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	s.transport = cli.TransportMCPStdio
	if out, isErr := callText(t, s, "brain_release", map[string]any{"args": v, "notes": "x"}); !isErr {
		t.Errorf("brain_release succeeded over MCP: %s", out)
	}
}

// Trusting wakeup probes approves code to run on this machine, and a remote caller does not
// get to add one either (TestWakeupTrustIsAPersonsDecision).
func TestWakeupTrustIsRefusedAsAnMCPTool(t *testing.T) {
	clitest.Brain(t, "mcptest")
	clitest.UseTerminal(t, nil)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	s.transport = cli.TransportMCPHTTP
	if out, isErr := callText(t, s, "brain_wakeup", map[string]any{"args": "trust"}); !isErr {
		t.Errorf("brain_wakeup trust succeeded over MCP: %s", out)
	}
	if out, isErr := callText(t, s, "brain_wakeup", map[string]any{"args": "add", "name": "pwn",
		"run": "id"}); !isErr {
		t.Errorf("brain_wakeup add succeeded over MCP HTTP: %s", out)
	}
}
