package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// A repository arrives with a clone, and with it its .claude/settings.json and .mcp.json.
// Those files can set the variables that choose which brain a command opens, so a hostile
// repository could point every session at a sibling project's brain and read or write its
// memories. The checks here let a repository name only its own brain, while a value the
// person typed at a prompt stays theirs to choose.

// CheckHookHome refuses an OS_BRAIN_HOME outside the project a Claude Code hook runs for. The
// hook's command line comes from the repository's committed .claude/settings.json, so a
// clone could point it at `$CLAUDE_PROJECT_DIR/../other/brain`. CLAUDE_PROJECT_DIR is set for
// hooks, not for a person's shell, so a deliberate OS_BRAIN_HOME typed at a prompt is
// untouched.
func CheckHookHome(here string) error {
	proj := os.Getenv("CLAUDE_PROJECT_DIR")
	if proj == "" {
		return nil
	}
	if db := os.Getenv("OS_BRAIN_DB"); db != "" && !within(db, proj) && realpath(db) != realpath(sharedDBPath()) {
		return fmt.Errorf("OS_BRAIN_DB is %s, outside the project this hook runs for (%s).\n"+
			"Refusing: that is another brain's database, and a repository's settings do not "+
			"get to choose it.", realpath(db), realpath(proj))
	}
	if here == "" || within(here, proj) {
		return nil
	}
	return fmt.Errorf("OS_BRAIN_HOME is %s, outside the project this hook runs for (%s).\n"+
		"Refusing: a hook in a repository's settings names that repository's own brain, and "+
		"one pointing elsewhere reads another project's memories. Fix the hook in "+
		".claude/settings.json (os-brain init rewrites it).", realpath(here), realpath(proj))
}

// brainPathVars are the variables that choose which brain a command opens.
var brainPathVars = []string{"OS_BRAIN_HOME", "OS_BRAIN_DB", "OS_BRAIN_PROJECT_ROOT"}

// CheckRepoEnv is CheckHookHome for the variables a repository sets without a hook: the
// `env` block of its .claude/settings.json (or settings.local.json), which Claude Code puts
// into every hook and every Bash call of the session, and the env of a server in .mcp.json.
// A value they declare may only name something inside the repository. A value that matches
// nothing the repository declares is the person's own and untouched: that is how
// `OS_BRAIN_HOME=../other/brain os-brain recall` typed at a prompt keeps working.
func CheckRepoEnv(cwd string) error {
	proj := os.Getenv("CLAUDE_PROJECT_DIR")
	if proj == "" {
		proj = settingsRoot(cwd)
	}
	if proj == "" {
		return nil
	}
	declared := repoDeclaredEnv(proj)
	for _, k := range brainPathVars {
		v := os.Getenv(k)
		if v == "" || !declared[k][v] {
			continue
		}
		target := expandHome(v)
		if !filepath.IsAbs(target) {
			target = filepath.Join(cwd, target)
		}
		if within(target, proj) || (k == "OS_BRAIN_DB" && realpath(target) == realpath(sharedDBPath())) {
			continue
		}
		return fmt.Errorf("%s is %s, set by this repository's own settings (%s), and it is "+
			"outside the repository.\nRefusing: a repository's settings name that repository's "+
			"own brain, and one pointing elsewhere reads and writes another project's memories. "+
			"Remove %s from the env block there; to reach another brain on purpose, set it "+
			"yourself in your shell.", k, realpath(target), realpath(proj), k)
	}
	return nil
}

// repoSettingsFiles are the files, relative to a repository root, that can set environment
// variables for a session.
var repoSettingsFiles = []string{filepath.Join(".claude", "settings.json"),
	filepath.Join(".claude", "settings.local.json"), ".mcp.json"}

// settingsRoot is the nearest directory at or above dir with a .claude/settings.json,
// settings.local.json or .mcp.json of its own, never the home directory: ~/.claude holds
// the person's own settings, not a repository's.
func settingsRoot(dir string) string {
	home, _ := os.UserHomeDir()
	cur := realpath(dir)
	for {
		if home == "" || cur != realpath(home) {
			for _, f := range repoSettingsFiles {
				if _, err := os.Stat(filepath.Join(cur, f)); err == nil {
					return cur
				}
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// repoDeclaredEnv is every value the repository at root declares for brainPathVars, keyed by
// variable and then by value.
func repoDeclaredEnv(root string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	add := func(env map[string]interface{}) {
		for _, k := range brainPathVars {
			if v, ok := env[k].(string); ok && v != "" {
				if out[k] == nil {
					out[k] = map[string]bool{}
				}
				out[k][v] = true
			}
		}
	}
	for _, f := range []string{filepath.Join(".claude", "settings.json"),
		filepath.Join(".claude", "settings.local.json")} {
		if env, ok := readJSONObject(root, f)["env"].(map[string]interface{}); ok {
			add(env)
		}
	}
	if servers, ok := readJSONObject(root, ".mcp.json")["mcpServers"].(map[string]interface{}); ok {
		for _, srv := range servers {
			if m, ok := srv.(map[string]interface{}); ok {
				if env, ok := m["env"].(map[string]interface{}); ok {
					add(env)
				}
			}
		}
	}
	return out
}

// readJSONObject reads root/rel as a JSON object, or nil when it is missing or unreadable.
func readJSONObject(root, rel string) map[string]interface{} {
	var m map[string]interface{}
	if body, err := os.ReadFile(filepath.Join(root, rel)); err == nil {
		_ = json.Unmarshal(body, &m)
	}
	return m
}
