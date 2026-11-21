package cli

import (
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/chka/os-brain/internal/clitest"
	"github.com/chka/os-brain/internal/person/persontest"
)

// `release` writes the banner that every project's brief prints, so whatever it says is put
// in front of every session on the machine. An agent able to publish one could plant an
// instruction in all of them at once. So it is a person's act: refused over MCP, refused on
// the CLI without a terminal and published only after a person at one has seen the notes
// and typed yes.
func TestReleaseIsAPersonsDecision(t *testing.T) {
	clitest.Brain(t, "mcptest")
	clitest.UseTerminal(t, nil)
	v := time.Now().Format("060102") + "01" // a version in the YYMMDDNN form, numbered today
	run := func(tr Transport, args ...string) (string, error) {
		var buf strings.Builder
		err := runWithVia(callVia{transport: tr}, "release", args, &buf,
			flag.NewFlagSet("release", flag.ContinueOnError), false, false, "", "")
		return buf.String(), err
	}
	for _, tr := range []Transport{TransportMCPStdio, TransportMCPHTTP} {
		if _, err := run(tr, v, "--notes", "x"); err == nil || !strings.Contains(err.Error(), "refused over MCP") {
			t.Errorf("release over %s: %v", tr, err)
		}
	}
	if _, err := run(TransportCLI, v, "--notes", "x"); err == nil || !strings.Contains(err.Error(), "controlling terminal") {
		t.Errorf("release on the CLI with no terminal: %v", err)
	}
	// The tool itself is TestReleaseIsRefusedAsAnMCPTool in internal/mcp.

	// The first prompt is answered no, the second yes.
	tty := &persontest.FakeTTY{Inputs: []string{"no", "yes"}}
	clitest.UseTerminal(t, tty)
	if _, err := run(TransportCLI, v, "--notes", "fixes recall"); err == nil {
		t.Fatal("a release the person declined was published")
	}
	if _, err := run(TransportCLI, v, "--notes", "fixes recall"); err != nil {
		t.Fatalf("a person at a terminal could not publish: %v", err)
	}
	if !strings.Contains(strings.Join(tty.Shown, ""), "fixes recall") {
		t.Errorf("the person was not shown the notes: %q", tty.Shown)
	}
}
