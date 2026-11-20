package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

// gate runs between finding the brain and dispatching the command, for every CLI command,
// every hook and every MCP tool call. It holds the checks that are about the call rather
// than about any one command. handled=true means the call ended here and err is its result.
func gate(via callVia, cmd string, rest []string, stdout io.Writer, env brain.Env,
	session string) (handled bool, err error) {
	event := hookEventFor(via, cmd, rest, stdout, session)
	refuse := func(e error) (bool, error) {
		// A hook that fails prints to stderr, which the host does not show for exit 0, and a
		// hook that exits non-zero blocks the person's prompt. So the refusal goes on stdout
		// as JSON the host renders, and the hook succeeds having done nothing else.
		if event != "" {
			fmt.Fprintln(stdout, hookNotice(event, e.Error()))
			return true, nil
		}
		return true, e
	}
	// A brain.json that does not parse is refused rather than read as an empty config: an
	// unreadable file would otherwise read as a brain with no name and no settings.
	if env.ConfigErr != nil {
		return refuse(env.ConfigErr)
	}
	// A caller on the other end of a wire (MCP, the UI) is not a person at this machine, and
	// a file it names is read with this person's rights and handed back by recall. So every
	// file such a caller names must be inside the project: --body-file /etc/passwd over
	// MCP, followed by a recall, once handed the file back.
	if via.transport != transportCLI {
		files, ferr := fileArguments(cmd, rest)
		if errors.Is(ferr, flag.ErrHelp) {
			return false, nil
		}
		if ferr != nil {
			return refuse(ferr)
		}
		for _, f := range files {
			if ierr := brain.GuardReadInside(f, env.ProjectRoot()); ierr != nil {
				return refuse(ierr)
			}
		}
	}
	return false, nil
}

// fileArguments is every path cmd will read, found by parsing rest with the command's own
// flag registration, so this check and the command cannot disagree about which word is a
// file. Skipping every word that began with a dash once missed `import -- -x.json`.
func fileArguments(cmd string, rest []string) ([]string, error) {
	switch cmd {
	case "import", "absorb", "remember", "update", "backup":
	default:
		return nil, nil
	}
	fs, err := probeFlags(cmd)
	if err != nil {
		return nil, err
	}
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return nil, err
	}
	flags := map[string]string{}
	fs.Visit(func(f *flag.Flag) { flags[f.Name] = f.Value.String() })
	switch cmd {
	case "import", "absorb":
		return pos, nil
	case "remember", "update":
		if f := flags["body-file"]; f != "" {
			return []string{f}, nil
		}
	case "backup":
		if f := flags["restore"]; f != "" {
			return []string{f}, nil
		}
	}
	return nil, nil
}

// hookEventFor names the host event when this invocation IS the host's hook. wakeup is also
// a command people type, so it counts as the hook only when it carries no subcommand and
// nobody is reading it at a terminal.
func hookEventFor(via callVia, cmd string, rest []string, stdout io.Writer, session string) string {
	if via.transport != transportCLI || session != "" {
		return ""
	}
	ev := hookEvents[cmd]
	if ev == "" {
		return ""
	}
	if cmd == "wakeup" {
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") || a == "--json" || a == "-json" {
				return ""
			}
		}
		if isTerminal(stdout) {
			return ""
		}
	}
	return ev
}

// hookEvents maps each hook command to the host event it serves.
var hookEvents = map[string]string{
	"wakeup":      "SessionStart",
	"hook-recall": "UserPromptSubmit",
	"hook-stop":   "Stop",
	"hook-tool":   "PostToolUse",
	"hook-end":    "SessionEnd",
}

// hookNotice renders a notice through the host's visible channel. Claude Code shows nothing
// a hook writes to stderr when it exits 0, so the notice is JSON on stdout: systemMessage for
// the person, and additionalContext for the model on the events that take it.
func hookNotice(event, msg string) string {
	v := map[string]interface{}{"systemMessage": "[os-brain] " + msg}
	switch event {
	case "SessionStart", "UserPromptSubmit", "PostToolUse":
		v["hookSpecificOutput"] = map[string]interface{}{"hookEventName": event,
			"additionalContext": "[os-brain] " + msg}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// refuseTeamBrain stops a command in a project whose brain.json belongs to ns-brain's team
// edition: memories on a Hive server, or a zero-knowledge brain. os-brain cannot read
// either, and falling back to a local file would answer confidently from a store that does
// not hold this project's memories, so it refuses instead.
func refuseTeamBrain(env brain.Env) error {
	if where := env.Config.TeamEdition(); where != "" {
		return fmt.Errorf("this project's brain is %s, which is ns-brain's team edition.\n"+
			"os-brain is the local edition and cannot read it, and it will not answer from a "+
			"local file instead.\nUse ns-brain in this project.", where)
	}
	return nil
}
