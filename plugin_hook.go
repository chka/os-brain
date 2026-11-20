package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

// plugin-hook is what an agent's plugin runs, in place of the per-project hooks init writes.
//
// The two differ in one way that decides everything here: a plugin is installed once per
// PERSON and fires in every project that person opens, while init's hooks live inside one
// project and name its brain. So a plugin hook cannot carry OS_BRAIN_HOME; it has to find the
// brain the session is in, and it has to cope with the two things init's hooks never meet.
//
//   - A project with no brain. Discovery fails closed with an error, which is right for a
//     person at a shell and wrong for a hook firing in every repository on the machine: the
//     session would open with an error. Session start says once that there is no brain and
//     how to make one; every other event says nothing.
//   - A project init already wired. Its own hooks brief the session, so a plugin that also
//     answered would brief it twice and run recall twice per prompt. The plugin steps aside
//     wherever this agent's project config already runs os-brain.
//
// Everything after that is the ordinary subcommand, so there is one implementation of what
// a brief, a recall or a stop record is.

// pluginHostFiles are the project files in which init wires each agent: where a plugin
// looks before it answers. Copilot CLI and Devin CLI run Claude's .claude/settings.json, so
// a project init wired for either has its hooks there.
var pluginHostFiles = map[string][]string{
	"claude":  {".claude/settings.json", ".claude/settings.local.json"},
	"codex":   {".codex/hooks.json"},
	"cursor":  {".cursor/hooks.json"},
	"gemini":  {".gemini/settings.json"},
	"copilot": {".claude/settings.json", ".claude/settings.local.json", ".github/hooks"},
	"devin":   {".claude/settings.json", ".claude/settings.local.json", ".devin/hooks.v1.json"},
	"qwen":    {".qwen/settings.json"},
	// OpenCode and Kilo run no hook commands; init wires each by writing a JS plugin, and
	// each reads only its own directory, so one is no sign the other is wired.
	"opencode": {".opencode/plugins/brain.js"},
	"kilo":     {".kilo/plugin/brain.js"},
}

// pluginSubs are the subcommands a plugin may run. Anything else is refused rather than
// passed through: a plugin hook runs unattended in every project, so it gets the hooks and
// nothing that writes on a model's say-so.
var pluginSubs = map[string]bool{
	"wakeup": true, "hook-recall": true, "hook-stop": true, "hook-tool": true, "hook-end": true,
}

func pluginHostNames() string {
	var names []string
	for k := range pluginHostFiles {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func runPluginHook(stdout io.Writer, argv []string) error {
	fs := flag.NewFlagSet("plugin-hook", flag.ContinueOnError)
	fs.SetOutput(stdout)
	usageWithDoubleDash(fs, "plugin-hook", stdout)
	host := fs.String("host", "", "the agent this plugin is installed in: "+pluginHostNames())
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if _, ok := pluginHostFiles[*host]; !ok {
		return fmt.Errorf("plugin-hook needs --host, one of: %s", pluginHostNames())
	}
	sub := fs.Args()
	if len(sub) == 0 || !pluginSubs[sub[0]] {
		return fmt.Errorf("plugin-hook runs one of wakeup, hook-recall, hook-stop, hook-tool, " +
			"hook-end, after its own flags: os-brain plugin-hook --host claude wakeup")
	}

	payload := hookStdin()
	root := pluginProjectRoot(payload)
	home, found := brain.DiscoverHome(root)
	if !found {
		if sub[0] != "wakeup" {
			return nil
		}
		msg := "os-brain is installed in this agent, but this project has no brain yet, so " +
			"nothing is remembered here. Tell the user it can be created by running " +
			"`os-brain init` in " + root + "."
		if emit := flagValue(sub[1:], "emit"); emit != "" {
			return brain.EmitEnvelope(stdout, emit, msg)
		}
		_, err := fmt.Fprintln(stdout, msg)
		return err
	}
	if pluginProjectWired(root, home, *host) {
		return nil
	}
	if err := os.Setenv("OS_BRAIN_HOME", home); err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	if payload != nil {
		if err := replayStdin(payload); err != nil {
			return err
		}
	}
	return run(sub, stdout)
}

// pluginProjectRoot is the directory the session is working in. The variables come first
// because they name the project root even when the agent's shell has wandered into a
// subdirectory; the payload's cwd second; the process's own directory last.
func pluginProjectRoot(payload []byte) string {
	for _, v := range []string{"CLAUDE_PROJECT_DIR", "GEMINI_PROJECT_DIR", "CURSOR_PROJECT_DIR",
		"DEVIN_PROJECT_DIR", "QWEN_PROJECT_DIR"} {
		if d := os.Getenv(v); d != "" && isDir(d) {
			return d
		}
	}
	var p struct {
		Cwd            string   `json:"cwd"`
		WorkspaceRoots []string `json:"workspace_roots"`
	}
	if len(payload) > 0 && json.Unmarshal(payload, &p) == nil {
		if p.Cwd != "" && isDir(p.Cwd) {
			return p.Cwd
		}
		if len(p.WorkspaceRoots) > 0 && isDir(p.WorkspaceRoots[0]) {
			return p.WorkspaceRoots[0]
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// pluginProjectWired reports whether init already wired this agent in this project, from
// the session's directory up to the one holding the brain: the project root is somewhere
// on that path, whether the session opened at the root or in a subdirectory of it.
func pluginProjectWired(root, home, host string) bool {
	dir, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	for {
		for _, rel := range pluginHostFiles[host] {
			if runsBrain(filepath.Join(dir, rel)) {
				return true
			}
		}
		if within(home, dir) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// runsBrain reports whether a hook file, or any JSON file in a hook directory, runs this
// binary. Both shapes init writes name it: `os-brain` directly, or the autoexec.sh shim.
func runsBrain(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	files := []string{path}
	if fi.IsDir() {
		ents, _ := os.ReadDir(path)
		files = files[:0]
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".json") {
				files = append(files, filepath.Join(path, e.Name()))
			}
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := string(b)
		if strings.Contains(s, "os-brain ") || strings.Contains(s, "/autoexec.sh") {
			return true
		}
	}
	return false
}

func within(path, dir string) bool {
	p, err1 := filepath.Abs(path)
	d, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	return p == d || strings.HasPrefix(p, d+string(filepath.Separator))
}

// flagValue reads --name value or --name=value out of argv without parsing the rest.
func flagValue(argv []string, name string) string {
	for i, a := range argv {
		for _, pre := range []string{"--" + name, "-" + name} {
			if a == pre && i+1 < len(argv) {
				return argv[i+1]
			}
			if strings.HasPrefix(a, pre+"=") {
				return strings.TrimPrefix(a, pre+"=")
			}
		}
	}
	return ""
}

// replayStdin hands the payload this command already read to the subcommand, which reads
// stdin itself: a pipe holding the same bytes, closed so the reader sees its end.
func replayStdin(payload []byte) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	go func() {
		_, _ = w.Write(payload)
		_ = w.Close()
	}()
	os.Stdin = r
	return nil
}
