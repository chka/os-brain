package mcp

// The MCP tool list, and the translation of a tool call back into a command line.
//
// The tool list is DERIVED from the CLI's own flag registration (cli.ProbeFlags) and its
// usage block (cli.Usage), never written by hand. A flag added to a command becomes an MCP
// input on the next build. A hand-written schema is a second copy of the command surface, and a second copy
// drifts: that has happened more than once.

import (
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/chka/os-brain/internal/brain"
	"github.com/chka/os-brain/internal/cli"
)

type toolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

type toolDef struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *toolAnnotations `json:"annotations,omitempty"`
}

// behaviour classifies what a command does to the store, so a client can put a human in
// the loop where one is warranted. openWorld is false almost everywhere: a brain is a
// closed world by definition, which is exactly the example the MCP spec itself gives.
type behaviour struct{ readOnly, destructive, idempotent, openWorld bool }

// behaviours also decides which usage lines are commands at all: summaries() skips a
// usage line whose first word is not a key here. An annotation describes the WORST case
// a command can reach, not what it does by default, because that is what a client
// deciding whether to ask for confirmation needs.
var behaviours = map[string]behaviour{
	// Pure reads.
	"recall": {readOnly: true}, "get": {readOnly: true}, "events": {readOnly: true},
	"map": {readOnly: true}, "stats": {readOnly: true}, "review": {readOnly: true},
	"brief": {readOnly: true}, "env": {readOnly: true}, "doctor": {readOnly: true},
	"history": {readOnly: true}, "global": {readOnly: true}, "axiom": {readOnly: true},
	"standard": {readOnly: true}, "describe": {readOnly: true}, "context": {readOnly: true},
	"scope": {readOnly: true}, "kind": {readOnly: true}, "snapshot": {readOnly: true},
	"export": {readOnly: true}, "bootstrap": {readOnly: true}, "help": {readOnly: true},
	"version": {readOnly: true}, "lead": {readOnly: true}, "sessions": {readOnly: true},
	"measure": {readOnly: true}, "note": {readOnly: true}, "open": {readOnly: true},
	"utterances": {readOnly: true}, "ladder": {readOnly: true}, "coverage": {readOnly: true},

	// Listed so summaries() recognises their usage lines; notATool keeps them off the
	// tool list.
	"hook-recall": {readOnly: true}, "channel": {readOnly: true},
	"mcp": {readOnly: true}, "ui": {readOnly: true},
	"hook-tool": {}, "hook-end": {idempotent: true},

	// hook-stop writes only its own session's row in session_log, which is bookkeeping
	// about the caller rather than a change to what the brain believes.
	"hook-stop": {idempotent: true},
	"dm":        {},

	// wakeup changes nothing in the store, but it executes commands the project declared,
	// and openWorldHint is exactly the flag for a tool whose effects leave the closed
	// world of the brain. "Read-only" would be true of the memory and false of the machine.
	"wakeup": {idempotent: true, openWorld: true},

	// Additive writes: they add or refine, they do not destroy.
	"remember": {idempotent: true}, "log": {}, "prefer": {idempotent: true},
	"verify": {idempotent: true}, "promote": {idempotent: true},
	"publish": {idempotent: true}, "release": {idempotent: true},
	"backup": {idempotent: true}, "reindex": {idempotent: true},
	// capture stores and never overwrites; object appends a row.
	"capture": {}, "object": {},
	// init creates files in the project, additively and idempotently.
	"init": {idempotent: true},

	// Destructive at worst.
	// uninstall removes files from the project and a row from the shared index. It is a
	// dry run unless --apply.
	"uninstall": {destructive: true},
	// whoami --forget drops a trait by name.
	"whoami": {destructive: true},
	// private --off widens what every other project can read, retroactively.
	"private": {destructive: true},
	// share moves every row this project owns between two files and, with --off, deletes
	// them from the one they came from. A dry run by default.
	"share": {destructive: true},
	// task done/drop close somebody's item; add and list do not.
	"task":   {destructive: true},
	"forget": {destructive: true}, "prune": {destructive: true},
	"update": {destructive: true}, "restore": {destructive: true},
	"import": {destructive: true}, "absorb": {destructive: true},
	"relabel": {destructive: true}, "vacuum": {destructive: true},
}

// notATool lists commands that exist on the CLI but must never be offered as tools.
var notATool = map[string]bool{
	// Serving a tool that starts another server is nonsense: each of these blocks until
	// it is killed, and an agent that calls one has hung its own session.
	"mcp": true, "ui": true, "channel": true,
	// These hooks read their payload from os.Stdin, which over stdio IS the JSON-RPC
	// stream. Calling one as a tool made the server block reading a second line that could
	// never arrive, because the caller was already blocked waiting for the response: a
	// deadlocked connection rather than an error. The host runs hooks on real events; there
	// is nothing here for an agent to call.
	"hook-recall": true, "hook-tool": true, "hook-end": true,
	// plugin-hook is the same stdin read, run by an agent's plugin, never by a model.
	"plugin-hook": true,
	// `outstanding` and `status` are `open` under two more names. The aliases exist for a
	// person TYPING a guess; a model picking from a list is not guessing, and three
	// identical tools in it make the real one harder to find.
	"outstanding": true, "status": true,
}

// preSwitch commands are handled before the dispatch switch and register no flags, so
// probing them would fall through to "unknown command".
var preSwitch = map[string]bool{"help": true, "version": true}

// personOnlyFlags are CLI flags an MCP tool does not offer at all: a person's decision at a
// terminal, which the command refuses over MCP anyway.
var personOnlyFlags = map[string]map[string]bool{
	"relabel": {"claim": true},
	"backup":  {"whole-store": true},
}

// summaries are lifted from the usage block, which is already the one-line description of
// every command. Parsed rather than duplicated, for the same reason the schemas are.
func summaries() map[string]string {
	out := map[string]string{}
	for _, ln := range strings.Split(cli.Usage, "\n") {
		if !strings.HasPrefix(ln, "  ") || strings.HasPrefix(strings.TrimSpace(ln), "-") {
			continue
		}
		f := strings.Fields(ln)
		if len(f) < 2 {
			continue
		}
		name := f[0]
		if _, known := behaviours[name]; !known {
			continue
		}
		out[name] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), name))
	}
	return out
}

// commandNames is every command offered as a tool, taken from the usage block so the list
// cannot fall behind the dispatch switch.
func commandNames() []string {
	var out []string
	for name := range summaries() {
		if !notATool[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func toolName(cmd string) string { return "brain_" + strings.ReplaceAll(cmd, "-", "_") }

func commandFor(tool string) string {
	want := strings.TrimPrefix(tool, "brain_")
	for _, c := range commandNames() {
		if strings.ReplaceAll(c, "-", "_") == want {
			return c
		}
	}
	return ""
}

func buildTools() ([]toolDef, error) {
	sums := summaries()
	var out []toolDef
	for _, cmd := range commandNames() {
		schema, err := schemaFor(cmd)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cmd, err)
		}
		b := behaviours[cmd]
		out = append(out, toolDef{
			Name:        toolName(cmd),
			Title:       cmd,
			Description: sums[cmd],
			InputSchema: schema,
			Annotations: &toolAnnotations{
				Title:           cmd,
				ReadOnlyHint:    b.readOnly,
				DestructiveHint: b.destructive,
				IdempotentHint:  b.idempotent,
				OpenWorldHint:   b.openWorld,
			},
		})
	}
	return out, nil
}

// flagsOf is the FlagSet the CLI registers for cmd, probed rather than restated.
func flagsOf(cmd string) (*flag.FlagSet, error) {
	if preSwitch[cmd] {
		return flag.NewFlagSet(cmd, flag.ContinueOnError), nil
	}
	return cli.ProbeFlags(cmd)
}

// schemaFor builds a tool's inputSchema from the FlagSet the CLI itself registers, plus the
// transport-level inputs every tool shares.
func schemaFor(cmd string) (map[string]any, error) {
	fs, err := flagsOf(cmd)
	if err != nil {
		return nil, err
	}
	props := map[string]any{}
	fs.VisitAll(func(f *flag.Flag) {
		// json is offered below with a description worth reading.
		if personOnlyFlags[cmd][f.Name] || f.Name == "json" {
			return
		}
		props[f.Name] = flagProperty(f)
	})
	for name, prop := range sharedToolInputs() {
		props[name] = prop
	}
	return map[string]any{"type": "object", "properties": props}, nil
}

func flagProperty(f *flag.Flag) map[string]any {
	p := map[string]any{"type": flagType(f)}
	if f.Usage != "" {
		p["description"] = f.Usage
	}
	if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" {
		p["default"] = f.DefValue
	}
	return p
}

// sharedToolInputs are the inputs every tool takes besides its command's own flags.
func sharedToolInputs() map[string]any {
	return map[string]any{
		"json": map[string]any{
			"type": "boolean",
			"description": "return the machine-readable envelope instead of the human " +
				"rendering. Recall's envelope carries `matched`, and a caller acting on " +
				"`results` must check it: a miss returns nothing rather than the whole scope.",
		},
		// Which project the CALLING SESSION is working in. MCP carries no notion of the
		// client's working directory, so without this a server started for one project
		// files every other project's memories under its own label, silently: that once
		// made dozens of memories invisible. The leading underscore means it can never
		// collide with a CLI flag (absorb already has a --project meaning something else).
		"_project": map[string]any{
			"type": "string",
			"description": "the project this session is working in. Required when the server " +
				"fronts a store holding more than one project; there is no way for it to know " +
				"otherwise, and guessing would file your memories under someone else's name.",
		},
		// Who is calling, as distinct from which project they are in. One server writes on
		// behalf of many sessions, so without this every memory it stores carries the
		// SERVER's session id and the attribution column collapses to a constant. Defaults
		// to the MCP session id, so a client that says nothing is still told apart.
		"_session": map[string]any{
			"type": "string",
			"description": "an identifier for the calling session, so two agents working the " +
				"same project can be told apart. Defaults to this MCP connection.",
		},
		// Every command that takes positional arguments reads them as one string.
		"args": map[string]any{
			"type": "string",
			"description": "positional arguments, exactly as they would follow the command on " +
				"the CLI (a recall query, an id, a subcommand like `add` or `list`).",
		},
	}
}

// flagType maps a Go flag to a JSON Schema type. The flag package does not expose the
// underlying kind, so this reads the zero value's rendering, which is stable.
func flagType(f *flag.Flag) string {
	if cli.IsBoolFlag(f) {
		return "boolean"
	}
	if _, err := strconv.Atoi(f.DefValue); err == nil {
		return "integer"
	}
	if _, err := strconv.ParseFloat(f.DefValue, 64); err == nil {
		return "number"
	}
	return "string"
}

// argvFor turns tool arguments back into a command line. Going through argv rather than
// calling the store directly is deliberate: the MCP path then IS the CLI path, so guards,
// refusals and output cannot diverge between the two surfaces.
func argvFor(cmd string, args map[string]any) ([]string, error) {
	fs, err := flagsOf(cmd)
	if err != nil {
		return nil, err
	}
	known := map[string]*flag.Flag{}
	fs.VisitAll(func(f *flag.Flag) { known[f.Name] = f })

	argv := []string{cmd}
	var names []string
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names) // a deterministic argv, so a failure reproduces
	for _, k := range names {
		words, err := flagWords(cmd, k, args[k], known)
		if err != nil {
			return nil, err
		}
		argv = append(argv, words...)
	}
	positional, err := positionalWords(cmd, args)
	if err != nil {
		return nil, err
	}
	return append(argv, positional...), nil
}

// flagWords renders one tool argument as the flag words it stands for, or none.
func flagWords(cmd, name string, v any, known map[string]*flag.Flag) ([]string, error) {
	switch name {
	case "args", "_project", "_session":
		return nil, nil // not flags: positionalWords, resolveProject and resolveSession
	case "json":
		if b, _ := v.(bool); b {
			return []string{"--json"}, nil
		}
		return nil, nil
	}
	f, ok := known[name]
	if !ok || personOnlyFlags[cmd][name] {
		return nil, fmt.Errorf("unknown argument %q for %s", name, cmd)
	}
	if cli.IsBoolFlag(f) {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s expects true or false", name)
		}
		if b {
			return []string{"--" + name}, nil
		}
		return nil, nil
	}
	sv, err := scalarArg(name, v)
	if err != nil {
		return nil, err
	}
	return []string{"--" + name, sv}, nil
}

// positionalWords splits the args input into the words that follow the flags.
func positionalWords(cmd string, args map[string]any) ([]string, error) {
	raw, ok := args["args"]
	if !ok {
		return nil, nil
	}
	sv, err := scalarArg("args", raw)
	if err != nil || sv == "" {
		return nil, err
	}
	words, err := splitArgs(sv)
	if err != nil {
		return nil, err
	}
	// Flags are the tool's own inputs. A word in args is positional even when it starts
	// with "-", or args would set flags the schema never offered.
	if preSwitch[cmd] {
		return words, nil
	}
	return append([]string{"--"}, words...), nil
}

// splitArgs splits the args string the way the schema promises: "exactly as they would
// follow the command on the CLI", quotes included. Plain whitespace splitting made a quoted
// note body lose its newlines, and any word in it starting with "-" was parsed as a flag: a
// body mentioning --title was refused, and one naming a real flag would have set it.
//
// A quote opens only at the start of a word and closes only before whitespace or the end,
// so the apostrophe in `what's the nginx port` is a letter rather than an unterminated
// string. Inside double quotes \" and \\ are escapes; nothing else is.
func splitArgs(s string) ([]string, error) {
	var out []string
	r := []rune(s)
	i := 0
	for i < len(r) {
		for i < len(r) && unicode.IsSpace(r[i]) {
			i++
		}
		if i >= len(r) {
			break
		}
		var word string
		var err error
		if r[i] == '"' || r[i] == '\'' {
			word, i, err = quotedWord(r, i)
			if err != nil {
				return nil, err
			}
		} else {
			word, i = bareWord(r, i)
		}
		out = append(out, word)
	}
	return out, nil
}

// quotedWord reads the quoted word that opens at r[start] and returns it with the index
// just past its closing quote.
func quotedWord(r []rune, start int) (string, int, error) {
	q := r[start]
	var b strings.Builder
	for j := start + 1; j < len(r); j++ {
		if q == '"' && r[j] == '\\' && j+1 < len(r) && (r[j+1] == '"' || r[j+1] == '\\') {
			b.WriteRune(r[j+1])
			j++
			continue
		}
		if r[j] == q && (j+1 == len(r) || unicode.IsSpace(r[j+1])) {
			return b.String(), j + 1, nil
		}
		b.WriteRune(r[j])
	}
	return "", 0, fmt.Errorf("args has a %c that is never closed: %s", q, brain.Clip(string(r[start:]), 40))
}

// bareWord reads an unquoted word starting at r[start], up to the next whitespace.
func bareWord(r []rune, start int) (string, int) {
	j := start
	for j < len(r) && !unicode.IsSpace(r[j]) {
		j++
	}
	return string(r[start:j]), j
}

// scalarArg refuses what scalar cannot represent. An array sent where the schema says
// string used to fall through to fmt.Sprint and become the Go literal "[alpha secret]",
// which was then split into "[alpha" and "secret]" and searched for: silent corruption of
// the actual query, not just of the display. A typed protocol that quietly reinterprets a
// type mismatch is worse than one that has no types at all.
func scalarArg(name string, v any) (string, error) {
	switch v.(type) {
	case string, bool, float64, nil:
		return scalar(v), nil
	}
	return "", fmt.Errorf("%s must be a string, number or boolean, not %T. The tool schema "+
		"says which; an array or object here would be silently flattened into text", name, v)
}

// scalar renders a JSON scalar as the CLI would have been given it. JSON numbers arrive as
// float64, so a whole number is printed without a decimal point.
func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
