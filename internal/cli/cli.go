// Package cli is os-brain's command line: the table of commands, their flags, the checks
// every call passes through, and the agent hooks. The servers (MCP, the web UI and the
// direct-message channel) live in their own packages and run this same dispatch in process,
// so a call over any of them takes exactly the path a typed command takes.
//
// Where to find things in this package:
//
//	cli.go        the entry points, the usage text, help and version
//	dispatch.go   finding the brain, the probe, and the table of commands
//	cmd_*.go      one file per command family: each command's flags and what it runs
//	args.go       argument parsing shared by every command
//	gate.go       checks about the call itself (transport, file arguments, hook notices)
//	transport.go  how a call arrived (CLI, MCP, UI) and the person-at-a-terminal checks
//	servers.go    the registration point the servers' packages are wired in through
//	hook_payload.go  reading what an agent host hands its hook, and answering in its shape
//	plugin_hook.go   the hook an agent plugin runs in every project
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/chka/os-brain/internal/brain"
)

// Usage is the general usage text. It is also the list of commands: the MCP tool list and
// `help <command>` take each command's one-line summary from it.
const Usage = `os-brain <command> [options]

  remember   store a memory (same scope+title updates in place)
  recall     retrieve ranked memories
  get        show one memory by id
  update     edit a memory
  prefer     store how the user wants to be worked with
  verify     mark a memory re-checked today
  forget     delete a memory (leaves a tombstone)
  log        append an activity event
  events     recent activity
  lead       sales pipeline (optional)
  capture    store what the user said, before the turn is answered
  utterances what was said here, and how it was classified
  object     record an objection to a memory, with its reasoning
  ladder     where a memory stands: stated, hardened or law
  whoami     the user model, and the evidence behind every line of it
  coverage   what this project knows about, by category
  task       what is owed, by whom and when: reminders, recurrence, an ordered list
  open       what is outstanding: open tasks, work in flight, what just happened
  measure    numbers that only mean something as a series
  map        index of scopes, types, event kinds and pinned memories
  review     hygiene report: duplicates, decay, promotions
  snapshot   generated markdown mirror of the brain
  hook-recall  UserPromptSubmit hook: auto-recall from the prompt
  hook-stop  Stop hook: what this session did, and whether it stored any of it
  promote    move a memory up to a wider scope
  scope      declare scopes in brain.json
  kind       declare event kinds in brain.json
  axiom      rules that hold in every project
  standard   house defaults every new project inherits
  describe   this project's spec in the shared index
  global     axioms, the project index, published lessons
  publish    share one memory as a cross-project lesson
  bootstrap  seed a new brain from past session transcripts
  note       leave a note for another project, or read yours
  sessions   the running Claude Code sessions this brain can reach
  dm         send a direct message to one running session ("-" reads the body from stdin)
  channel    MCP channel server: wakes this session the moment a direct message arrives
  hook-tool  PostToolUse hook: hand a running session its direct messages mid-turn
  hook-end   SessionEnd hook: mark this session gone, so nobody messages it
  plugin-hook  what an agent plugin runs: finds the brain itself, or says there is none
  relabel    claim rows filed under another label as this project
  absorb     pull a standalone brain.db in as a project
  context    project facts injected into every brief
  private    keep this project's memories out of other projects' --all-projects
  share      move this project into the shared store, or back out (dry run by default)
  prune      delete decayed or superseded memories (dry run by default)
  export     dump the whole brain as JSON (take one before pruning)
  import     merge a JSON export into this brain
  wakeup     session open: the brief, the working tree, and this project's own probes
  brief      session handoff snapshot
  doctor     check the brain for inconsistency
  history    archived versions of memories
  restore    bring back an archived version
  backup     timestamped full dump, every table enumerated from the schema
  release    publish the current brain version to the spine
  env        date, time, paths, git and project context
  reindex    rebuild the FTS index from the memories table
  vacuum     checkpoint the WAL and compact the database
  stats      counts
  version    print the version
  init       create a brain in this project and wire the session hooks
  uninstall  remove it again: dry run unless --apply, keeps the database unless --purge
  ui         the web UI, served from this binary (no Node, no install)
  mcp        serve every command over MCP (stdio, or --http for another machine)
  help       show full command surface

Global: --json on any command. OS_BRAIN_DB, OS_BRAIN_PROJECT, OS_BRAIN_GLOBAL_DB and
CLAUDE_CONFIG_DIR override paths and identity.`

// Run runs one command line, as the os-brain binary does with its arguments.
func Run(argv []string, stdout io.Writer) error {
	return runForVia(viaCLI, argv, stdout, "", "")
}

// RunVia is Run for a caller that is not a plain CLI process, with the project and session
// named explicitly: the MCP server says which transport a call came over, because some
// commands are refused over some transports, and it knows which project a call is for even
// though the process was not started there.
func RunVia(t Transport, argv []string, stdout io.Writer, project, session string) error {
	return runForVia(callVia{transport: t}, argv, stdout, project, session)
}

// RunJSON runs one command in process with --json on, the way the web UI asks for data. It
// is the dispatch alone: no usage, help or version handling, and a flag error is returned
// rather than printed, because the caller is a page reading JSON.
func RunJSON(t Transport, argv []string, stdout io.Writer) error {
	fs := flag.NewFlagSet(argv[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return runWithVia(callVia{transport: t}, argv[0], argv[1:], stdout, fs, false, true, "", "")
}

// runForVia is the one path every command line takes: the transport says how the call
// arrived, and project and session are what the caller named (empty: found from the
// working directory and the environment).
func runForVia(via callVia, argv []string, stdout io.Writer, project, session string) error {
	stdout = brain.SafeOutput(stdout)
	// Hooks and scripts write `os-brain --json <cmd>`, so the global flag is accepted before
	// the subcommand as well as after it.
	globalJSON := false
	for len(argv) > 0 && (argv[0] == "--json" || argv[0] == "-json") {
		globalJSON, argv = true, argv[1:]
	}
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" {
		fmt.Fprintln(stdout, Usage)
		return nil
	}
	if argv[0] == "help" {
		return printHelp(argv[1:], stdout)
	}
	cmd, rest := argv[0], argv[1:]
	// Whether a version is asked for as a command or as a flag is a habit, not a question:
	// the install steps say --version, and it used to answer "unknown command".
	if cmd == "--version" || cmd == "-version" || cmd == "-v" {
		cmd = "version"
	}
	if cmd == "version" {
		fmt.Fprintf(stdout, "os-brain %s (schema v%d)\n", brain.Version, brain.SchemaVersion)
		return nil
	}

	// A plugin hook finds its own brain, or answers that there is none, so it is intercepted
	// before the discovery that would refuse a project without one. Never over MCP: it is
	// the agent's hook, and a tool call that ran it would be a model choosing to.
	if cmd == "plugin-hook" {
		if via.transport != viaCLI.transport || project != "" || session != "" {
			return errors.New("plugin-hook is an agent's hook, so it only runs as one, locally")
		}
		return runPluginHook(stdout, rest)
	}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stdout)
	usageWithDoubleDash(fs, cmd, stdout)
	return runWithVia(via, cmd, rest, stdout, fs, false, globalJSON, project, session)
}

// usageWithDoubleDash prints `--flag` where the flag package prints `-flag`. Go parses one
// dash and two identically, but the help output is what a session checks when the docs and
// its memory disagree. A session once read `-all-projects` here, concluded the double-dash
// form had been dropped and rewrote its project's docs to match. Only the rendering changes.
func usageWithDoubleDash(fs *flag.FlagSet, cmd string, out io.Writer) {
	fs.Usage = func() {
		fmt.Fprintf(out, "Usage of %s:\n", cmd)
		fs.VisitAll(func(f *flag.Flag) {
			kind, usage := flag.UnquoteUsage(f)
			line := "  --" + f.Name
			if kind != "" {
				line += " " + kind
			}
			fmt.Fprintln(out, line)
			if usage != "" {
				fmt.Fprintf(out, "    \t%s\n", usage)
			}
		})
	}
}
