package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chka/os-brain/internal/brief"
	"github.com/chka/os-brain/internal/dm"
	"github.com/chka/os-brain/internal/hook"
	"github.com/chka/os-brain/internal/install"
	"github.com/chka/os-brain/internal/store"
)

// The hooks are what the agent host runs on its own events, whether or not the model
// thought of the brain: session open (wakeup), every prompt (hook-recall), every tool call
// (hook-tool), every stop (hook-stop) and session end (hook-end). MCP tools are only called
// when the model chooses to, so these are the path that always runs.

func cmdHookRecall(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := hook.HookRecallArgs{}
	fs.IntVar(&a.N, "n", 3, "")
	fs.IntVar(&a.Chars, "chars", 180, "body characters per hit")
	fs.IntVar(&a.MinChars, "min-chars", 15, "ignore prompts shorter than this")
	fs.Float64Var(&a.MinScore, "min-score", 0.55, "")
	fs.StringVar(&a.Scope, "scope", "", "")
	fs.IntVar(&a.IdleAfter, "idle-after", 20, "minutes of silence before the gap is called out")
	fs.BoolVar(&a.NoClock, "no-clock", false, "")
	fs.BoolVar(&a.NoNudge, "no-nudge", false, "")
	emit := fs.String("emit", "", "envelope the host agent needs: gemini, codex, hermes (default plain text)")
	if _, err := c.parse(); err != nil {
		return err
	}
	if err := hookEmitOK(*emit, true); err != nil {
		return err
	}
	if *emit != "" {
		var flush func()
		stdout, flush = promptEnvelope(stdout, *emit)
		defer flush()
	}
	raw := hookStdin()
	adoptHookSession(raw)
	if *emit == "hermes" {
		// Hermes's pre_llm_call is the only hook whose output reaches the model, so the
		// brief rides on the first turn of a session, ahead of what recall found.
		var first bool
		raw, first = hermesPayload(raw)
		if first {
			if err := Run([]string{"wakeup"}, stdout); err != nil {
				fmt.Fprintln(stdout, "[brain] the brief failed: "+err.Error())
			}
		}
	}
	if *emit == "" {
		var flush func()
		stdout, flush = hostEnvelope(stdout, raw, "UserPromptSubmit")
		defer flush()
	}
	// Direct messages first: a message somebody sent to this running session outranks
	// anything recall might guess is relevant.
	deliverTo(stdout, c.env, dm.HookPrompt)
	return c.store(func(s *store.Store) error {
		return hook.New(s).HookRecall(bytes.NewReader(raw), stdout, a)
	})
}

// promptEnvelope collects everything hook-recall prints, delivered messages included, and
// hands it to stdout as one JSON object at the end: Gemini, Codex and Hermes take context
// only that way.
func promptEnvelope(stdout io.Writer, emit string) (io.Writer, func()) {
	var buf bytes.Buffer
	return &buf, func() {
		if body := strings.TrimSpace(buf.String()); body != "" {
			event := "BeforeAgent"
			if emit == "codex" {
				event = "UserPromptSubmit"
			}
			_ = brief.EmitEventEnvelope(stdout, emit, event, body)
		}
	}
}

func cmdHookStop(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brief.SessionEndArgs{}
	emit := fs.String("emit", "", "envelope the host agent needs: gemini, codex (default plain text)")
	if _, err := c.parse(); err != nil {
		return err
	}
	if err := hookEmitOK(*emit, false); err != nil {
		return err
	}
	a.JSON = c.json()
	raw := hookStdin()
	adoptHookSession(raw)
	out := stdout
	if *emit != "" {
		// Gemini's AfterAgent and Codex's Stop read stdout as JSON and have no way to show a
		// delivered message or the footprint line, so under --emit the hook records and
		// prints nothing; the next BeforeAgent delivers what is waiting.
		out = io.Discard
	} else if stopDelivers(c, a, raw) && deliverTo(stdout, c.env, dm.HookStop) {
		// A message waiting for this session is the one thing that should stop it stopping.
		// Delivered, the hook's output is the block and nothing else: Claude Code parses the
		// whole of stdout as the decision, so the footprint report goes nowhere this turn
		// and runs again at the next stop.
		out = io.Discard
	}
	return c.store(func(s *store.Store) error { return brief.New(s).SessionEnd(out, a) })
}

// stopDelivers reports whether hook-stop may hand this session its waiting messages. Only
// as the host's hook: over MCP the process's own session is the server's, not the caller's,
// and claiming for it would hand one session another's messages. And never at sessionEnd,
// where there is no next turn to show a message in, so claiming one would mark it delivered
// to nobody.
func stopDelivers(c *invocation, a brief.SessionEndArgs, raw []byte) bool {
	return !a.JSON && c.env.Session == "" && !hookEvent(raw, "sessionEnd")
}

func cmdWakeup(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brief.WakeupArgs{}
	fs.StringVar(&a.Name, "name", "", "probe name, for add/rm")
	fs.StringVar(&a.Run, "run", "", "shell command the probe runs, for add")
	fs.IntVar(&a.Timeout, "timeout", 0, "seconds a probe may take (default 10, max 60)")
	fs.IntVar(&a.Chars, "chars", 0, "probe output kept (default 1200, max 8000)")
	fs.BoolVar(&a.NoBrief, "no-brief", false, "only the live state, without the brief")
	fs.BoolVar(&a.NoTree, "no-tree", false, "skip the working-tree read")
	fs.BoolVar(&a.NoProbes, "no-probes", false, "skip this project's declared probes")
	fs.BoolVar(&a.NoCursor, "no-cursor", false, "do not move the last-brief marker")
	fs.StringVar(&a.Emit, "emit", "", "envelope the host agent needs: cursor, gemini (default plain text)")
	fs.IntVar(&a.MaxBytes, "max-bytes", brief.DefaultMaxBytes,
		"trim from the least important end to fit; 0 for all of it")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.MaxBytesSet = flagSet(fs, "max-bytes")
	if len(pos) > 0 {
		a.Sub, pos = pos[0], pos[1:]
	}
	if a.Name == "" && len(pos) > 0 {
		a.Name = strings.Join(pos, " ")
	}
	a.JSON = c.json()
	if err := gateProbeChange(c, &a); err != nil {
		return err
	}
	// Session open registers this session as running, so it can be addressed before
	// anybody has typed into it, and hands it anything sent while it was starting. Only
	// as the host's own hook: over MCP the session in this process is the server's.
	if a.Sub == "" && c.env.Session == "" {
		raw := hookStdin()
		adoptHookSession(raw)
		if a.Emit == "" && !a.JSON {
			if install.CursorDeliversBrief(raw) {
				return nil
			}
			var flush func()
			stdout, flush = hostEnvelope(stdout, raw, "SessionStart")
			defer flush()
			deliverTo(stdout, c.env, dm.HookPrompt)
		}
	}
	return c.store(func(s *store.Store) error { return brief.New(s).Wakeup(stdout, a) })
}

// gateProbeChange guards the wakeup subcommands that decide what runs on this machine. The
// trust gate exists so that commands somebody else wrote never run without a person
// approving them. An MCP caller is not that person, nor is an agent's shell: the transport
// is checked, a terminal is required, and the person types the answer into it after seeing
// exactly what they approve.
func gateProbeChange(c *invocation, a *brief.WakeupArgs) error {
	switch a.Sub {
	case "trust":
		if c.via.transport != viaCLI.transport {
			return errors.New("wakeup trust is refused over MCP: approving a probe approves " +
				"code to run on this machine, a person's decision made at a terminal. Ask the " +
				"person to run `os-brain wakeup trust` themselves")
		}
		confirm, err := confirmAtTerminal("approving wakeup probes",
			"Approve these to run at every session open on this machine? Type yes: ")
		if err != nil {
			return err
		}
		a.Confirm = confirm
	case "add", "rm":
		if c.via.transport != viaCLI.transport && c.via.transport != TransportMCPStdio {
			return fmt.Errorf("wakeup %s is refused over HTTP: probes are commands this machine "+
				"runs, and a remote caller does not get to edit them", a.Sub)
		}
	}
	return nil
}

func cmdHookTool(c *invocation) error {
	if _, err := c.parse(); err != nil {
		return err
	}
	// The payload carries the whole tool response, which can be megabytes. Only the
	// session id is needed, but leaving the rest unread can block the host writing it.
	adoptHookSession(hookStdin())
	_, _ = io.Copy(io.Discard, os.Stdin)
	if dm.ToolPollDue(filepath.Dir(c.env.GlobalDB)) {
		deliverTo(c.stdout, c.env, dm.HookTool)
	}
	return nil
}

func cmdHookEnd(c *invocation) error {
	if _, err := c.parse(); err != nil {
		return err
	}
	adoptHookSession(hookStdin())
	_, _ = io.Copy(io.Discard, os.Stdin)
	dm.ClearToolPoll(filepath.Dir(c.env.GlobalDB))
	_ = withMessenger(c.cmd, c.env, func(m dm.Messenger) error { return m.EndSession() })
	return nil
}

func cmdChannel(c *invocation) error {
	if _, err := c.parse(); err != nil {
		return err
	}
	if servers.Channel == nil {
		return notWiredIn("channel")
	}
	return servers.Channel(c.env, os.Stdin, c.stdout)
}
