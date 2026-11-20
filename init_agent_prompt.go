package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

// agentPromptIn is where init reads the agent choice. It is nil when nobody is at a terminal
// to answer (a script, a hook, MCP, `</dev/null`), and init then detects as it always has. A
// variable so a test can answer it.
var agentPromptIn = func() io.Reader {
	if stdinIsProtocol {
		return nil
	}
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return nil
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return nil
	}
	return os.Stdin
}

const agentPromptMax = 5

// askAgent puts the agent menu to the person and returns the host key, or manual=true for
// "I will do it myself". An answer that names nothing is asked again; running out of input
// is a refusal before anything is written, never a guess.
func askAgent(in io.Reader, out io.Writer, root string) (key string, manual bool, err error) {
	hosts := brain.Hosts()
	fmt.Fprintln(out, "To autowire your agent you need to tell us what agent you want to use:")
	for i, h := range hosts {
		mark := ""
		if brain.Detected(root, h) {
			mark = "  (found on this machine)"
		}
		fmt.Fprintf(out, "  %2d) %s%s\n", i+1, h.Label, mark)
	}
	self := len(hosts) + 1
	fmt.Fprintf(out, "  %2d) I will do it myself\n", self)

	r := bufio.NewReader(in)
	for try := 0; try < agentPromptMax; try++ {
		fmt.Fprintf(out, "Choose 1-%d: ", self)
		line, rerr := r.ReadString('\n')
		ans := strings.ToLower(strings.TrimSpace(line))
		if ans != "" {
			if n, perr := strconv.Atoi(ans); perr == nil {
				switch {
				case n == self:
					return "", true, nil
				case n >= 1 && n <= len(hosts):
					return hosts[n-1].Key, false, nil
				}
			}
			for _, h := range hosts {
				if ans == h.Key || ans == strings.ToLower(h.Label) {
					return h.Key, false, nil
				}
			}
			fmt.Fprintf(out, "%s is not on the list.\n", brain.Repr(strings.TrimSpace(line)))
		}
		if rerr != nil {
			fmt.Fprintln(out)
			return "", false, errors.New("no agent chosen, so nothing was written. " +
				"Re-run init, or pass --agent <name> to skip the question")
		}
	}
	return "", false, errors.New("no agent chosen, so nothing was written. " +
		"Pass --agent <name> to skip the question")
}
