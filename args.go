package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// parseInterleaved lets flags follow positionals and honours "--": every word after it is
// positional. Without that, re-parsing after each positional turned the second word behind
// "--" back into a flag, so text a page or a tool call supplied as a query
// (`?q=--all-projects`) set flags the caller was never offered.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if err := swallowedFlag(fs, args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		if endedFlags(fs, args) {
			return append(positional, fs.Args()...), nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// swallowedFlag refuses a value-taking flag whose value is another of this command's flags.
// `uninstall --apply --backup --json` used to take "--json" as the backup path and refuse
// with "--json is inside a git repository", which is true and tells the caller nothing.
// A value that really is a flag's spelling can still be passed as --flag=value.
func swallowedFlag(fs *flag.FlagSet, args []string) error {
	used := len(args) - fs.NArg()
	for i := 0; i < used; i++ {
		a := args[i]
		if a == "--" {
			return nil
		}
		if !strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
			continue
		}
		f := fs.Lookup(strings.TrimLeft(a, "-"))
		if f == nil || isBoolFlag(f) {
			continue
		}
		i++
		if i >= used {
			return nil
		}
		v := args[i]
		if !strings.HasPrefix(v, "-") {
			continue
		}
		name := strings.TrimLeft(v, "-")
		if j := strings.Index(name, "="); j >= 0 {
			name = name[:j]
		}
		if name != "" && fs.Lookup(name) != nil {
			return fmt.Errorf("--%s needs a value, and the word after it is the flag %s. "+
				"Nothing was run. Put the value straight after --%s, or write --%s=%s if "+
				"that really is the value", f.Name, v, f.Name, f.Name, v)
		}
	}
	return nil
}

// endedFlags reports whether the Parse just made over args stopped at a "--" terminator
// rather than at a positional. It walks what Parse consumed, so a "--" taken as a flag's
// value (`--title --`) is not mistaken for one.
func endedFlags(fs *flag.FlagSet, args []string) bool {
	used := len(args) - fs.NArg()
	for i := 0; i < used; i++ {
		a := args[i]
		if a == "--" {
			return true
		}
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if !isBoolFlag(f) {
			i++
		}
	}
	return false
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// multiFlag is a repeatable string flag: every --ref adds one.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// flagSet reports whether the caller set the flag, which an empty value cannot tell.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// flagsSeen is the set of flags the caller passed. Commands that edit in place use it so a
// zero value means "not given", never "set it to empty".
func flagsSeen(fs *flag.FlagSet) map[string]bool {
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	return seen
}

// sub splits `<verb> <name> <value...>` for the small registry-shaped commands, falling
// back to the listing verb when no verb was given.
func sub(pos []string, def string, verbs ...string) (verb, name, value string) {
	verb = def
	if len(pos) > 0 {
		for _, v := range verbs {
			if pos[0] == v {
				verb, pos = v, pos[1:]
				break
			}
		}
	}
	if len(pos) > 0 {
		name = pos[0]
	}
	if len(pos) > 1 {
		value = strings.Join(pos[1:], " ")
	}
	return verb, name, value
}

// argID reads the one memory id a command acts on, with or without a leading '#'.
func argID(args []string) (int64, error) {
	if len(args) == 0 {
		return 0, fmt.Errorf("an id is required")
	}
	// `forget 30 31` once deleted #30 and said nothing about #31. Every command that reads
	// an id acts on one, so a second is refused before anything runs.
	if len(args) > 1 {
		return 0, fmt.Errorf("one id per command, and this got %d (%s): nothing was done. "+
			"Run it once for each", len(args), strings.Join(args, " "))
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a memory id", args[0])
	}
	return id, nil
}

// readBody: --body-file PATH reads a file, --body - reads stdin, --body "text" is
// literal. Agents should prefer stdin for anything with newlines or quotes.
func readBody(body, bodyFile string) (string, error) {
	if bodyFile != "" {
		b, err := os.ReadFile(bodyFile)
		if err != nil {
			// Name the flag, not the syscall: the caller mistyped a path, and
			// "open /x: no such file" reads like an internal failure.
			if pe, ok := err.(*os.PathError); ok {
				return "", fmt.Errorf("--body-file %s: %v", bodyFile, pe.Err)
			}
			return "", err
		}
		return strings.TrimRight(string(b), "\n"), nil
	}
	if body == "-" {
		return readStdinText()
	}
	return body, nil
}

// stdinIsProtocol is set while this process serves MCP. Every command then runs in-process,
// and os.Stdin is the JSON-RPC stream (stdio) or a daemon's nothing (HTTP): a command that
// read its body from it would hang the connection or swallow the next request.
var stdinIsProtocol bool

// readStdinText is the one way a command reads a body from stdin.
func readStdinText() (string, error) {
	if stdinIsProtocol {
		return "", fmt.Errorf("a body cannot come from stdin over MCP: pass the text itself")
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// oneText refuses a rule given as more than one argument. `axiom add <name> "<text>"` once
// stored the name as the whole rule and dropped the text without a word: the rule is one
// argument.
func oneText(cmd, verb, text, extra string) error {
	if extra == "" || (verb != "add" && verb != "edit") {
		return nil
	}
	return fmt.Errorf("%s %s takes the rule as ONE argument and got more: %q, then %q.\n"+
		"Nothing was stored. Quote the whole rule: os-brain %s %s \"<text>\" --why \"...\"",
		cmd, verb, text, extra, cmd, verb)
}

// titleFromWords joins unquoted words into a title. Two or more arguments where one carries a
// space were quoted separately, which is "title" "body" with the body landing in the title
// and nothing stored as the body. A shell word typed bare cannot contain a space, so this
// refuses only that case and never `remember fix the nginx reload`.
func titleFromWords(pos []string, cmd string) (string, error) {
	if len(pos) > 1 {
		for _, w := range pos {
			if strings.ContainsAny(w, " \t\n") {
				return "", fmt.Errorf("%s got %d separate quoted arguments, and only the first is a "+
					"title. Nothing was stored. The body goes in --body:\n  os-brain %s --title %q --body \"...\"",
					cmd, len(pos), cmd, pos[0])
			}
		}
	}
	return strings.Join(pos, " "), nil
}
