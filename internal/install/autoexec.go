package install

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
)

// The autoexec is whichever hook the host agent runs at session open without being asked.
// Every agent worth wiring has one, and every one of them injects the hook's output into the
// model's context before the first turn. That is the only activation surface this project
// trusts: a document asking a session to read the brain is an instruction, and an
// instruction will eventually not be followed.
//
// This file holds the host table and the wiring loop; autoexec_hosts.go has each host's
// config format and autoexec_plugin.go the JavaScript plugin for OpenCode and Kilo.

// Host is one agent's autoexec surface: where its config lives, and how to wire it.
type Host struct {
	Key   string
	Label string
	Dir   string // detection: this directory, in the project or under $HOME
	// PasteOnly marks an agent whose wiring lives in the user's own config, which init prints
	// and never edits. Its block is an instruction, not a failure: a Claude Code user who also
	// has Continue installed must not be told the install failed.
	PasteOnly bool
	// HomeDir is where the agent keeps its user config when that is not $HOME/<Dir>:
	// OpenCode and Kilo both live under ~/.config.
	HomeDir string
	// Emit is the envelope wakeup must produce for this host. Empty means plain text.
	Emit string
	wire func(root, dir, script string, autoRecall bool) ([]string, error)
	// manual is what a human has to add when this cannot be written for them: the file, and
	// the block to put in it.
	manual func(root, dir, script string) (string, string)
}

func Hosts() []Host {
	return []Host{
		{Key: "claude", Label: "Claude Code", Dir: ".claude", wire: wireClaude, manual: manualClaude},
		{Key: "codex", Label: "Codex CLI", Dir: ".codex", wire: wireCodex, manual: manualCodex},
		{Key: "cursor", Label: "Cursor", Dir: ".cursor", Emit: "cursor", wire: wireCursor, manual: manualCursor},
		{Key: "gemini", Label: "Gemini CLI", Dir: ".gemini", Emit: "gemini", wire: wireGemini, manual: manualGemini},
		{Key: "opencode", Label: "OpenCode", Dir: ".opencode", HomeDir: ".config/opencode",
			wire: pluginWirer("OpenCode", openCodePluginPath), manual: pluginManual("OpenCode", openCodePluginPath)},
		// Copilot CLI runs .claude/settings.json with Claude's own payloads, so it is wired
		// through that file and no second one: a .github/hooks beside it would brief twice.
		{Key: "copilot", Label: "Copilot CLI", Dir: ".copilot", wire: wireCopilot, manual: manualClaude},
		// Devin CLI runs the same file the same way; the brain answers it in Claude's nested
		// JSON, the only shape it reads, and nothing goes in .devin/, which it also reads.
		{Key: "devin", Label: "Devin CLI", Dir: ".devin", HomeDir: ".config/devin", wire: wireDevin, manual: manualClaude},
		{Key: "qwen", Label: "Qwen Code", Dir: ".qwen", wire: wireQwen, manual: manualQwen},
		{Key: "hermes", Label: "Hermes Agent", Dir: ".hermes", PasteOnly: true, wire: wireHermes, manual: manualHermes},
		{Key: "continue", Label: "Continue CLI", Dir: ".continue", PasteOnly: true, wire: wireContinue, manual: manualContinue},
		{Key: "kilo", Label: "Kilo", Dir: ".kilo", HomeDir: ".config/kilo",
			wire: pluginWirer("Kilo", kiloPluginPath), manual: pluginManual("Kilo", kiloPluginPath)},
	}
}

// autoexecScript is the shim the non-Claude hosts invoke. Claude Code exports
// CLAUDE_PROJECT_DIR into its hooks, so its command can name the brain inline; the other
// hosts document project-root-relative commands and say nothing about the shell they run
// through, so a `VAR=x cmd` prefix is a guess. A file that resolves the brain from its own
// location works whatever the cwd and whatever the shell.
const autoexecScript = "autoexec.sh"

// writeAutoexecScript writes the shim into the brain directory, unless it is already there
// byte for byte, and returns its project-relative path.
func writeAutoexecScript(root, dir string) (string, error) {
	rel := filepath.Join(dir, autoexecScript)
	path := filepath.Join(root, rel)
	body := "#!/bin/sh\n" +
		"# Written by `os-brain init`. Resolves this brain from its own location, so the\n" +
		"# hook works whatever directory the agent runs it from.\n" +
		"OS_BRAIN_HOME=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n" +
		"export OS_BRAIN_HOME\n" +
		"exec os-brain \"$@\"\n"
	if err := fsguard.CheckNoLinks(root, path); err != nil {
		return "", err
	}
	old, err := os.ReadFile(path)
	if err == nil && string(old) == body {
		return rel, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := fsguard.WriteFileSafe(path, []byte(body), fsguard.WriteOpts{Root: root, Mode: 0o755}); err != nil {
		return "", err
	}
	return rel, nil
}

// invoke renders the shim as a command a shell can run from anywhere.
//
// "./" goes only in front of a relative path. Prepending it to an absolute one gave
// ".//var/.../autoexec.sh", which is a RELATIVE path (the leading dot plus a collapsed double
// slash), so a hook written that way resolved only if the shell happened to be at /. The
// hook then failed with "No such file or directory" and the session simply started without
// a brain: the silent failure this whole mechanism exists to prevent.
func invoke(script string) string {
	p := filepath.ToSlash(script)
	if !filepath.IsAbs(script) && !strings.HasPrefix(p, "./") {
		p = "./" + p
	}
	return shellQuote(p)
}

// shellQuote is a path as one shell word. The script path is absolute and starts with the
// project root, which is whatever the directory was called: a space splits the command, and
// a $( or a backtick in it runs on every session open. A path of plain characters stays as
// it was, so a hook an earlier version wrote still matches what uninstall looks for.
func shellQuote(p string) string {
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("._/-+=:,@%", r)) {
			return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
		}
	}
	return p
}

// wakeupCmd is what a host's session-start hook runs, in that host's envelope.
func wakeupCmd(script string, h Host) string {
	cmd := invoke(script) + " wakeup"
	if h.Emit != "" {
		cmd += " --emit " + h.Emit
	}
	return cmd
}

func hookCmd(script, sub string) string {
	return invoke(script) + " " + sub
}

// CheckAgents validates a comma-separated agent list without writing anything, so init can
// refuse an unknown name before it has created a database, two documents and a .gitignore
// line that its own error message never mentions.
func CheckAgents(agents string) error {
	_, err := selectedHosts(agents)
	return err
}

// selectedHosts resolves a comma-separated agent list to hosts, in the order given. Blank
// names are skipped; an unknown name is an error.
func selectedHosts(agents string) ([]Host, error) {
	all := Hosts()
	byKey := map[string]Host{}
	var known []string
	for _, h := range all {
		byKey[h.Key] = h
		known = append(known, h.Key)
	}
	sort.Strings(known)
	var chosen []Host
	for _, name := range strings.Split(agents, ",") {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		h, ok := byKey[name]
		if !ok {
			return nil, fmt.Errorf("unknown agent %s. Known: %s.\nAn unrecognised name is not "+
				"wired to anything, so it would install a brain nothing reads",
				pyfmt.Repr(name), strings.Join(known, ", "))
		}
		chosen = append(chosen, h)
	}
	return chosen, nil
}

// AutoexecResult is what wiring did: lines written, hosts it could not wire (a failure),
// blocks a human pastes into config init never edits (an instruction), and how many hosts
// are wired now, counting those that needed nothing written this time.
type AutoexecResult struct {
	Did, Unwired, Paste []string
	Wired               int
}

// Autoexec wires the named hosts, or every detected one when agents is empty, and reports
// what it could not do. It never falls back to writing prose into a document the project
// owns: when the wiring cannot be written, the developer is told exactly what to add, in
// full, and the install says it is not finished.
func Autoexec(root, dir, agents string, autoRecall bool) (AutoexecResult, error) {
	return autoexec(root, dir, agents, autoRecall, false)
}

// AutoexecManual wires nothing and returns every host's block to add by hand: the answer to
// init's "I will do it myself", which must not quietly wire whatever happens to be detected.
func AutoexecManual(root, dir string) (AutoexecResult, error) {
	return autoexec(root, dir, "", false, true)
}

// Detected reports whether this host's config directory exists in the project or the home
// directory, which is what init's agent menu marks.
func Detected(root string, h Host) bool { return detected(root, h) }

func autoexec(root, dir, agents string, autoRecall, manual bool) (AutoexecResult, error) {
	var r AutoexecResult
	chosen, err := chooseHosts(root, agents, manual)
	if err != nil {
		return r, err
	}
	script, err := writeAutoexecScript(root, dir)
	if err != nil {
		return r, err
	}
	// ABSOLUTE, deliberately. The shim resolves the BRAIN from its own location, but the host
	// still has to FIND the shim, and a project-relative path only works if the agent happens
	// to have changed to the project root first. That is exactly the promise these hosts do
	// not make, which is the reason the shim exists at all. An absolute path costs moving the
	// config file between machines; a relative one costs the hook firing.
	script = filepath.Join(root, script)

	for _, h := range chosen {
		lines, werr := h.wire(root, dir, script, autoRecall)
		if werr != nil {
			path, snippet := h.manual(root, dir, script)
			block := manualBlock(h, path, snippet, werr.Error())
			if h.PasteOnly {
				r.Paste = append(r.Paste, block)
			} else {
				r.Unwired = append(r.Unwired, block)
			}
			continue
		}
		r.Did = append(r.Did, lines...)
		r.Wired++
	}
	if len(chosen) == 0 {
		for _, h := range Hosts() {
			path, snippet := h.manual(root, dir, script)
			r.Unwired = append(r.Unwired, manualBlock(h, path, snippet, ""))
		}
	}
	return r, nil
}

// chooseHosts is the named hosts when agents is set, nothing in manual mode, and otherwise
// every host detected on this machine.
func chooseHosts(root, agents string, manual bool) ([]Host, error) {
	if strings.TrimSpace(agents) != "" {
		return selectedHosts(agents)
	}
	if manual {
		return nil, nil
	}
	var chosen []Host
	for _, h := range Hosts() {
		if detected(root, h) {
			chosen = append(chosen, h)
		}
	}
	return chosen, nil
}

func detected(root string, h Host) bool {
	if _, err := os.Stat(filepath.Join(root, h.Dir)); err == nil {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if agentHomeDir(filepath.Join(home, h.Dir)) {
		return true
	}
	if h.HomeDir == "" {
		return false
	}
	_, err = os.Stat(filepath.Join(home, h.HomeDir))
	return err == nil
}

// agentHomeDir reports whether a per-user agent directory is the agent's own. ~/.claude is
// also where a brain keeps its machine-wide store, so on a machine that never had Claude
// Code the first init could create ~/.claude itself and the second one "detect" Claude Code
// from it and wire hooks for an agent that is not installed. A directory holding nothing but
// store directories is therefore not counted: "os-brain" is this binary's store, and
// "brain" is ns-brain's, which may sit beside it. An empty agent directory still counts.
func agentHomeDir(p string) bool {
	ents, err := os.ReadDir(p)
	if err != nil {
		return false
	}
	if len(ents) == 0 {
		return true
	}
	for _, e := range ents {
		if e.Name() != "os-brain" && e.Name() != "brain" {
			return true
		}
	}
	return false
}

// manualBlock renders one host's paste-in instructions: a heading naming the file, why it
// was not written, and the block indented under it.
func manualBlock(h Host, path, snippet, why string) string {
	head := h.Label + " — " + path
	if why != "" {
		head += "\n    " + why
	}
	return head + "\n" + Indent(snippet, "    ")
}

func Indent(s, pad string) string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, pad+ln)
	}
	return strings.Join(out, "\n")
}
