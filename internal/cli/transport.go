package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chka/os-brain/internal/person"
)

// Transport is how a call reached the dispatcher. It matters because some commands are a
// person's decision (axioms, standards, a release, approving probes) and are refused over
// MCP, where the caller is a model, whatever it says about itself.
type Transport string

const (
	TransportCLI      Transport = "cli"
	TransportMCPStdio Transport = "mcp-stdio"
	TransportMCPHTTP  Transport = "mcp-http"
	TransportUI       Transport = "ui"
)

// callVia carries the transport through the dispatcher.
type callVia struct {
	transport Transport
}

var viaCLI = callVia{transport: TransportCLI}

// isTerminal reports whether w is a terminal, which is how a hook subcommand typed by a
// person is told from the same subcommand run by the host.
func isTerminal(w io.Writer) bool {
	if u, ok := w.(interface{ Unwrap() io.Writer }); ok {
		w = u.Unwrap()
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// needTTY refuses a person-only command when no person is at a terminal.
func needTTY(what string) (person.TTY, error) { return person.Need(what) }

// confirmAtTerminal refuses up front when no person is at a terminal, and otherwise returns
// the confirmation a person-only command calls once it knows what it is about to do: it
// shows the summary on the terminal and accepts only a typed "yes". The question goes to the
// terminal, never stdout, because an agent reads and answers stdout itself.
func confirmAtTerminal(what, prompt string) (func(summary string) (bool, error), error) {
	tty, err := needTTY(what)
	if err != nil {
		return nil, err
	}
	return func(summary string) (bool, error) {
		if err := tty.Show(summary + "\n"); err != nil {
			return false, err
		}
		line, err := tty.ReadLine(prompt)
		return strings.TrimSpace(line) == "yes", err
	}, nil
}

// spinePerson is the person gate for axioms and standards: the rules every project reads as
// binding are changed at a terminal by a person who types yes, never over MCP and never by a
// session on its own.
func spinePerson(via callVia, cmd, what string) (func(string) (bool, error), error) {
	if via.transport != viaCLI.transport {
		return nil, fmt.Errorf("%s add/edit/rm is refused over MCP: %s is read as a house rule "+
			"by every project, a person's decision made at a terminal. Ask the person to run "+
			"`os-brain %s` themselves", cmd, what, cmd)
	}
	return confirmAtTerminal("changing "+what, "Change it for every project? Type yes: ")
}
