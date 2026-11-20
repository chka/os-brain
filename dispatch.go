package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chka/os-brain/internal/brain"
)

// Every command has the same shape: register its flags on fs, call c.parse(), then act.
// probeFlags relies on that shape. In probe mode c.parse() returns errProbe, which stops the
// command exactly between declaring its interface and using it.
var errProbe = errors.New("probe")

// probeFlags runs a command far enough to register its flags, then aborts. The MCP tool
// schemas and `help <command>` are built from the result, so the CLI's own flag
// registration is the only list of flags: one added here appears there with no second edit.
// Hand-written parallel lists drifted from the CLI more than once.
func probeFlags(cmd string) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	err := runWith(cmd, nil, io.Discard, fs, true, false, "", "")
	if err == nil || errors.Is(err, errProbe) {
		return fs, nil
	}
	return nil, err
}

func runWith(cmd string, rest []string, stdout io.Writer, fs *flag.FlagSet, probe, globalJSON bool, project, session string) error {
	return runWithVia(viaCLI, cmd, rest, stdout, fs, probe, globalJSON, project, session)
}

func runWithVia(via callVia, cmd string, rest []string, stdout io.Writer, fs *flag.FlagSet, probe, globalJSON bool, project, session string) error {
	env, err := discoverBrainEnv(cmd, probe, project, session)
	if err != nil {
		return err
	}
	if !probe {
		if handled, err := gate(via, cmd, rest, stdout, env, session); handled {
			return err
		}
	}
	return dispatch(via, cmd, rest, stdout, fs, probe, globalJSON, env)
}

// discoverBrainEnv finds this call's brain directory and builds its Env. It fails closed:
// if there is no brain it says so and stops, it never invents one. init is the exception,
// because it is about to create the brain, and so is a probe, which never touches a store.
func discoverBrainEnv(cmd string, probe bool, project, session string) (brain.Env, error) {
	here := os.Getenv("OS_BRAIN_HOME")
	if !probe {
		if err := checkCallerEnv(here); err != nil {
			return brain.Env{}, err
		}
	}
	if here != "" && !probe && cmd != "init" {
		if err := requireBrainAt(here); err != nil {
			return brain.Env{}, err
		}
	}
	if here == "" {
		found, err := discoverHome(cmd, probe)
		if err != nil {
			return brain.Env{}, err
		}
		here = found
	}
	env := brain.NewEnvFor(here, project)
	env.Session = session
	return env, nil
}

// checkCallerEnv refuses a hook environment or a repository that points this call at the
// wrong place before anything is opened.
func checkCallerEnv(here string) error {
	if err := brain.CheckHookHome(here); err != nil {
		return err
	}
	if cwd, err := os.Getwd(); err == nil {
		if err := brain.CheckRepoEnv(cwd); err != nil {
			return err
		}
	}
	return nil
}

// requireBrainAt refuses an OS_BRAIN_HOME that is not a brain. It used to create one there
// and answer `memories=0`, which a session reads as "this project has nothing stored": a
// shell left in a subdirectory got an empty store and a stray brain.db in the repository.
func requireBrainAt(here string) error {
	if _, err := os.Stat(filepath.Join(here, "brain.json")); err != nil {
		return fmt.Errorf("OS_BRAIN_HOME is set to %s, which is not a brain: no brain.json there.\n"+
			"Point it at an existing brain directory, unset it to search upward from the "+
			"working directory, or run `os-brain init` there to create one.", here)
	}
	return nil
}

// discoverHome searches upward from the working directory for a brain.
func discoverHome(cmd string, probe bool) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if found, ok := brain.DiscoverHome(cwd); ok {
		return found, nil
	}
	if probe || cmd == "init" {
		return cwd, nil
	}
	// The old fallback was the directory holding the binary, which silently created a
	// brain in /usr/local/bin and answered from it.
	return "", fmt.Errorf("no brain here: %s and its parents hold no brain.json.\n"+
		"Run `os-brain init` in the project root to create one, or set OS_BRAIN_HOME "+
		"to an existing brain directory.", cwd)
}

// invocation is one command call as the command functions in cmd_*.go see it.
type invocation struct {
	via        callVia
	cmd        string
	rest       []string
	stdout     io.Writer
	fs         *flag.FlagSet
	env        brain.Env
	probe      bool
	globalJSON bool
	asJSON     *bool
}

// parse parses the command's arguments with the flags it has registered so far, and returns
// the positional words. Go's flag package stops at the first positional argument, so
// `recall foo --json` would silently treat the flag as part of the query; parseInterleaved
// takes flags wherever they appear. A --json given before the command name is folded in.
func (c *invocation) parse() ([]string, error) {
	// Probe mode: the caller wanted the interface, not the effect. Every flag this command
	// declares is now registered on fs, and nothing has run.
	if c.probe {
		return nil, errProbe
	}
	positional, err := parseInterleaved(c.fs, c.rest)
	if err != nil {
		return nil, err
	}
	*c.asJSON = *c.asJSON || c.globalJSON
	return positional, nil
}

// json reports whether the caller asked for JSON output. Read it only after parse.
func (c *invocation) json() bool { return *c.asJSON }

// dispatch runs one command against env, the discovered brain.
func dispatch(via callVia, cmd string, rest []string, stdout io.Writer, fs *flag.FlagSet, probe, globalJSON bool, env brain.Env) error {
	c := &invocation{via: via, cmd: cmd, rest: rest, stdout: stdout, fs: fs, env: env,
		probe: probe, globalJSON: globalJSON}
	c.asJSON = fs.Bool("json", false, "machine-readable JSON output")
	if handler := commandHandler(cmd); handler != nil {
		return handler(c)
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

// commandHandler is the function that runs cmd, or nil for no such command. help, version
// and plugin-hook never get here: runForVia answers them before any brain is found.
func commandHandler(cmd string) func(*invocation) error {
	switch cmd {
	// memories: cmd_memories.go
	case "remember":
		return cmdRemember
	case "recall":
		return cmdRecall
	case "get":
		return cmdGet
	case "update":
		return cmdUpdate
	case "verify":
		return cmdVerify
	case "forget":
		return cmdForget
	case "prefer":
		return cmdPrefer
	case "promote":
		return cmdPromote
	case "history":
		return cmdHistory
	case "restore":
		return cmdRestore
	case "object":
		return cmdObject
	case "ladder":
		return cmdLadder

	// activity, the user model, tasks, measures and leads: cmd_record.go
	case "log":
		return cmdLog
	case "events":
		return cmdEvents
	case "capture":
		return cmdCapture
	case "utterances":
		return cmdUtterances
	case "whoami":
		return cmdWhoami
	case "coverage":
		return cmdCoverage
	case "task":
		return cmdTask
	case "measure":
		return cmdMeasure
	case "lead":
		return cmdLead

	// the agent hooks: cmd_hooks.go
	case "hook-recall":
		return cmdHookRecall
	case "hook-stop":
		return cmdHookStop
	case "wakeup":
		return cmdWakeup
	case "hook-tool":
		return cmdHookTool
	case "hook-end":
		return cmdHookEnd
	case "channel":
		return cmdChannel

	// read-only views of the brain: cmd_read.go
	case "brief":
		return cmdBrief
	case "open", "outstanding", "status":
		return cmdOpen
	case "map":
		return cmdMap
	case "stats":
		return cmdStats
	case "env":
		return cmdEnv
	case "review":
		return cmdReview
	case "bootstrap":
		return cmdBootstrap

	// the store itself: health, dumps, pruning, moving projects: cmd_store.go
	case "doctor":
		return cmdDoctor
	case "reindex":
		return cmdReindex
	case "backup":
		return cmdBackup
	case "export":
		return cmdExport
	case "import":
		return cmdImport
	case "prune":
		return cmdPrune
	case "snapshot":
		return cmdSnapshot
	case "vacuum":
		return cmdVacuum
	case "relabel":
		return cmdRelabel
	case "absorb":
		return cmdAbsorb
	case "private":
		return cmdPrivate
	case "share":
		return cmdShare

	// what every project shares (the spine) and this project's registry: cmd_spine.go
	case "context":
		return cmdContext
	case "scope", "kind":
		return cmdRegistry
	case "axiom":
		return cmdAxiom
	case "standard":
		return cmdStandard
	case "describe":
		return cmdDescribe
	case "publish":
		return cmdPublish
	case "release":
		return cmdRelease
	case "global":
		return cmdGlobal

	// notes between projects and messages between sessions: cmd_messages.go
	case "note":
		return cmdNote
	case "sessions":
		return cmdSessions
	case "dm":
		return cmdDM

	// installing, uninstalling and the servers: cmd_install.go
	case "init":
		return cmdInit
	case "uninstall":
		return cmdUninstall
	case "ui":
		return cmdUI
	case "mcp":
		return cmdMCP
	}
	return nil
}

// withStore opens this project's memories, and refuses when they are not here.
//
// The check lives here, on the one path every command takes to the store, so that it fails
// closed: a project whose brain.json belongs to ns-brain's team edition has its memories
// somewhere os-brain cannot read, and the local file it would otherwise open can be a shared
// store that holds every other project and returns a confident wrong answer.
func withStore(cmd string, env brain.Env, fn func(*brain.Store) error) error {
	if err := refuseTeamBrain(env); err != nil {
		return err
	}
	s, err := brain.Open(env)
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(s)
}

// store runs fn against this call's store.
func (c *invocation) store(fn func(*brain.Store) error) error {
	return withStore(c.cmd, c.env, fn)
}
