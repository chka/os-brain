package brief

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/safety"
	"github.com/chka/os-brain/internal/store"
)

// Wakeup is what the SessionStart hook runs. It is the brief plus the two things the brief
// deliberately does not carry: a detailed read of the working tree (wakeup_tree.go), and
// the output of whatever commands this project declared as worth knowing at session open
// (wakeup_probes.go).
//
// The split from Brief is not cosmetic. Brief is a pure read of the store: MCP annotates
// it read-only, the viewer calls it, and half the test suite asserts on it. Wakeup executes
// strings out of the project's own config, which has to be annotated, gated and refusable
// as the different kind of thing it is. Folding shell execution into the command everything
// else calls to read memory would make every one of those callers an execution surface.

type WakeupArgs struct {
	MaxBytes    int
	MaxBytesSet bool
	Sub         string // "", "add", "rm", "list", "trust"
	Name        string
	Run         string
	Timeout     int
	Chars       int
	NoBrief     bool
	NoTree      bool
	NoProbes    bool
	NoCursor    bool
	// Emit is the host agent's envelope: "cursor", "gemini", or empty for plain text.
	// Some hosts inject a hook's stdout as it stands and others accept context only
	// inside a JSON field, so the envelope varies and the body never does.
	Emit string
	JSON bool
	// Confirm shows a person exactly what `trust` would approve and reports their answer.
	// Only the CLI sets it, after proving a person is at a terminal; without it, trust
	// refuses, so no other caller of Wakeup can approve code to run on this machine.
	Confirm func(listing string) (bool, error)
}

// usingThisBrain is the header every session opens with: the brain exists, here is what
// to type, and the session is expected to write before it ends. It replaces a section
// that used to live in CLAUDE.md, moved into the one output every host injects, so it is
// paid once per session instead of staying resident for all of it.
func usingThisBrain() []string {
	// The first line is written to survive alone. Under a tight budget every line after it
	// can go, and a session that reads only the header must still learn that the brain
	// exists, what to type, and that it is expected to write before the end.
	return []string{
		"## THIS PROJECT HAS A BRAIN — os-brain: brief, recall, remember, prefer, log, note. " +
			"Read it before searching files; write to it before the session ends.",
		"  Persistent memory that outlives compaction, session end and subagent boundaries.",
		"  Six commands carry the work: brief (what this project believes), recall (before you",
		"  search files), remember, prefer, log, note.",
		// This once said "everything else is maintenance", and it cost a real session:
		// asked for a weekly reminder, it reached for the host's scheduler and had to be
		// stopped and told the brain has tasks. Filing four working domains under
		// "maintenance" was a wrong answer given at session open to every session.
		"  Asked what is outstanding, what is next, or where you left off? `os-brain open`",
		"  answers it from tasks, work in flight and the log. Run THAT, not `git log` and a",
		"  directory listing: reconstructing it costs thousands of tokens and mostly agrees.",
		"  Before reaching for a host feature, check here first: task (reminders, recurrence,",
		"  what is owed and by whom), measure (numbers that only mean something as a series),",
		"  whoami, coverage. The rest is maintenance, run on request.",
		"  `os-brain help` lists every command; `os-brain help <command>` explains one.",
		"  Read the brain before searching files, and write to it before the session ends: a",
		"  Stop hook records whether you stored anything, and the next session is told.",
		"",
	}
}

func (s *Store) Wakeup(w io.Writer, a WakeupArgs) error {
	switch a.Sub {
	case "":
		return s.wakeupRun(w, a)
	case "add":
		return s.wakeupAdd(w, a)
	case "rm":
		return s.wakeupRm(w, a)
	case "list":
		return s.wakeupList(w, a)
	case "trust":
		return s.wakeupTrust(w, a)
	}
	return fmt.Errorf("unknown subcommand %s. `wakeup` runs it; add/rm/list/trust configure it",
		pyfmt.Repr(a.Sub))
}

// wakeupRun assembles the session-open output: header, brief, working tree, probes, and
// the safety copy, in that order, into one text and one JSON envelope.
func (s *Store) wakeupRun(w io.Writer, a WakeupArgs) error {
	if a.Emit != "" && a.Emit != "cursor" && a.Emit != "gemini" && a.Emit != "codex" {
		return fmt.Errorf("unknown envelope %s. `--emit cursor` and `--emit gemini` are the two "+
			"hosts that take context only as JSON; every other host reads plain stdout, which "+
			"is the default", pyfmt.Repr(a.Emit))
	}

	lines := usingThisBrain()
	obj := map[string]interface{}{}
	if !a.NoBrief {
		briefLines, briefObj, err := s.briefParts(BriefArgs{NoCursor: a.NoCursor})
		if err != nil {
			return err
		}
		lines, obj = append(lines, briefLines...), briefObj
	}

	if !a.NoTree {
		if tree := s.TreeFacts(); tree != nil {
			obj["tree"] = tree
			lines = append(lines, treeLines(tree)...)
		} else if s.Env.Remote {
			lines = append(lines, "", "## WORKING TREE",
				"  This brain is not on your machine, so it cannot see your tree. Read it yourself:",
				"    git status --short --branch && git stash list")
		}
	}

	if !a.NoProbes {
		probeLines, results := s.probeBlock()
		if results != nil {
			obj["probes"] = results
		}
		lines = append(lines, probeLines...)
	}

	if !a.NoBrief && !s.Env.Remote {
		lines = append(lines, s.safetyCopyLines()...)
	}

	if a.JSON && a.NoBrief {
		obj["project"] = s.Env.Project
	}
	// --emit wraps the human text for a host that will not read plain stdout. It is not
	// --json: that is the machine-readable envelope every command has, and a host handed it
	// would inject the brain's data model instead of the brief.
	if !a.JSON {
		lines = Budget(lines, MaxBytesFrom(a.MaxBytes, a.MaxBytesSet, DefaultMaxBytes))
	}
	if a.Emit != "" && !a.JSON {
		return EmitEnvelope(w, a.Emit, strings.Join(lines, "\n"))
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), obj)
}

// safetyCopyLines takes the automatic safety copy and reports it if one was written.
//
// It runs here, at session open, because there is no daemon and no timer and the hook is
// the mechanism for everything else in this file. It is silent unless it acted, and it can
// never fail the session: a brain that refuses to open because it could not write a copy
// has turned a safety net into an outage.
//
// Not over MCP (the caller checks Remote): the store and the disk a copy would land on
// belong to the server, and whoever runs it backs it up. A caller on the far end of a wire
// filling it with copies on every session open is not a backup story.
func (s *Store) safetyCopyLines() []string {
	var sb strings.Builder
	_ = safety.New(s.Store).SafetyAuto(&sb)
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return nil
	}
	lines := []string{"", "## SAFETY COPY"}
	for _, ln := range strings.Split(out, "\n") {
		lines = append(lines, "  "+ln)
	}
	return lines
}

// EmitEnvelope wraps a session-start body for a host that will not read plain stdout.
func EmitEnvelope(w io.Writer, emit, body string) error {
	return EmitEventEnvelope(w, emit, "SessionStart", body)
}

// EmitEventEnvelope renders a hook's body in the shape one host requires. Each field name
// is the host's own and is not interchangeable: Cursor reads additional_context, Gemini
// reads hookSpecificOutput.additionalContext, and a hook that returns the wrong one is
// silently ignored, which is the failure this whole surface exists to remove. Gemini names
// the event inside the object; Cursor's field is the same for every event.
func EmitEventEnvelope(w io.Writer, emit, event, body string) error {
	var v interface{}
	switch emit {
	case "cursor":
		v = map[string]interface{}{"additional_context": body}
	// Codex reads stdout that opens with "[" as JSON and drops the hook as failed when it
	// does not parse, which is every recall block ("[brain] ..."); it parses this shape.
	case "gemini", "codex":
		v = map[string]interface{}{"hookSpecificOutput": map[string]interface{}{
			"hookEventName": event, "additionalContext": body}}
	case "hermes":
		v = map[string]interface{}{"context": body}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
