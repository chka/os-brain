package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
)

// hookFile is one host's config and the shape its entries take. Removal has to cover every
// file init can write: a brain that is uninstalled from one agent and still wired in
// another leaves a hook running a command whose brain has just been deleted.
type hookFile struct {
	rel  string
	flat bool // Cursor: event -> [{command}]; everything else nests a hooks array
}

func hookFiles() []hookFile {
	return []hookFile{
		{rel: filepath.Join(".claude", "settings.json")},
		{rel: filepath.Join(".codex", "hooks.json")},
		{rel: filepath.Join(".gemini", "settings.json")},
		{rel: filepath.Join(".qwen", "settings.json")},
		{rel: filepath.Join(".cursor", "hooks.json"), flat: true},
	}
}

// ownPlugin reports whether the agent plugin at rel was written by init for THIS brain: the
// marker, and the shim path init bakes into it.
func ownPlugin(root, rel, dir string) bool {
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil || !strings.HasPrefix(string(body), pluginMarker) {
		return false
	}
	for _, script := range ownScripts(root, dir) {
		q, _ := json.Marshal(script)
		if strings.Contains(string(body), "\nconst AUTOEXEC = "+string(q)+"\n") {
			return true
		}
	}
	return false
}

// ownScripts is every path init can have written for this brain's shim: under the root as
// given and as resolved, since the root a later uninstall sees may differ by a link.
func ownScripts(root, dir string) []string {
	rel := filepath.Join(dir, autoexecScript)
	out := []string{filepath.Join(root, rel)}
	if real := fsguard.Realpath(root); real != root {
		out = append(out, filepath.Join(real, rel))
	}
	return out
}

// ours reports whether a hook command belongs to THIS brain: it starts with exactly what
// init writes for this brain directory. Testing whether the command merely contains the
// directory's name is not enough, because "os-brain" contains "brain": uninstalling brain/
// would strip a second brain's hooks at marketing/brain and any other tool whose command
// mentioned os-brain.
func ours(cmd, root, dir string) bool {
	cmd = strings.TrimSpace(cmd)
	prefixes := []string{"OS_BRAIN_HOME=\"$CLAUDE_PROJECT_DIR/" + dir + "\" os-brain ",
		invoke(filepath.Join(dir, autoexecScript)) + " "}
	for _, script := range ownScripts(root, dir) {
		prefixes = append(prefixes, invoke(script)+" ")
	}
	for _, p := range prefixes {
		if strings.HasPrefix(cmd, p) {
			return true
		}
	}
	return false
}

// brainHookEntries reads one host's config and lists this brain's hooks in it as
// "event: command". It returns the parsed config (nil when the file is missing or does not
// parse) and the file's path.
func brainHookEntries(root, dir string, f hookFile) (map[string]interface{}, []string, string) {
	path := filepath.Join(root, f.rel)
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, path
	}
	cfg := map[string]interface{}{}
	if json.Unmarshal(body, &cfg) != nil {
		return nil, nil, path
	}
	hooks, _ := cfg["hooks"].(map[string]interface{})
	if hooks == nil {
		return cfg, nil, path
	}
	var found []string
	for event, v := range hooks {
		list, _ := v.([]interface{})
		for _, entry := range list {
			for _, c := range entryCommands(entry, f.flat) {
				if ours(c, root, dir) {
					found = append(found, event+": "+c)
				}
			}
		}
	}
	return cfg, found, path
}

// entryCommands is every command string in one hook entry, in either shape.
func entryCommands(entry interface{}, flat bool) []string {
	e, _ := entry.(map[string]interface{})
	if flat {
		c, _ := e["command"].(string)
		return []string{c}
	}
	var out []string
	inner, _ := e["hooks"].([]interface{})
	for _, h := range inner {
		hm, _ := h.(map[string]interface{})
		c, _ := hm["command"].(string)
		out = append(out, c)
	}
	return out
}

func plannedHookRemovals(root, dir string) []removal {
	var out []removal
	for _, f := range hookFiles() {
		_, found, path := brainHookEntries(root, dir, f)
		for _, entry := range found {
			out = append(out, removal{"remove hook", path, entry})
		}
	}
	return out
}

func applyHookRemovals(root, dir string) error {
	for _, f := range hookFiles() {
		if err := stripHookFile(root, dir, f); err != nil {
			return err
		}
	}
	return nil
}

// stripHookFile removes this brain's hooks from one host's config, dropping any entry and
// any event left empty, and the hooks block itself when nothing is left in it.
func stripHookFile(root, dir string, f hookFile) error {
	cfg, found, path := brainHookEntries(root, dir, f)
	if cfg == nil || len(found) == 0 {
		return nil
	}
	hooks, _ := cfg["hooks"].(map[string]interface{})
	for event, v := range hooks {
		list, _ := v.([]interface{})
		kept := []interface{}{}
		for _, entry := range list {
			if e, keep := withoutOurHooks(entry, f.flat, root, dir); keep {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
			continue
		}
		hooks[event] = kept
	}
	if len(hooks) == 0 {
		delete(cfg, "hooks")
	} else {
		cfg["hooks"] = hooks
	}
	// The file stays even when it ends up empty. It is the project's, not the brain's,
	// and something else may be about to write to it.
	data, err := pyfmt.MarshalPy(cfg, "  ")
	if err != nil {
		return err
	}
	return fsguard.WriteFileSafe(path, append(data, '\n'), fsguard.WriteOpts{Root: root, Mode: 0o644, KeepMode: true})
}

// withoutOurHooks returns entry with this brain's hooks taken out, and whether anything is
// left of it to keep.
func withoutOurHooks(entry interface{}, flat bool, root, dir string) (map[string]interface{}, bool) {
	e, _ := entry.(map[string]interface{})
	if flat {
		c, _ := e["command"].(string)
		return e, !ours(c, root, dir)
	}
	inner, _ := e["hooks"].([]interface{})
	keptInner := []interface{}{}
	for _, h := range inner {
		hm, _ := h.(map[string]interface{})
		if c, _ := hm["command"].(string); ours(c, root, dir) {
			continue
		}
		keptInner = append(keptInner, h)
	}
	if len(keptInner) == 0 {
		return nil, false
	}
	e["hooks"] = keptInner
	return e, true
}

// claudeMDHeading opens the section older versions of init appended to CLAUDE.md.
const claudeMDHeading = "## Project memory"

func plannedClaudeMD(root, dir string) removal {
	path := filepath.Join(root, "CLAUDE.md")
	body, err := os.ReadFile(path)
	if err != nil {
		return removal{}
	}
	if !strings.Contains(string(body), dir+"/HOWTO.md") {
		return removal{}
	}
	return removal{"remove section", path, claudeMDHeading + " — only the section init added"}
}

// stripClaudeMD removes that section, from its heading to the next heading of the same level
// or the end of the file. An error means there was nothing to remove.
func stripClaudeMD(root, dir string) error {
	path := filepath.Join(root, "CLAUDE.md")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(body)
	start := strings.Index(text, "\n"+claudeMDHeading)
	if start < 0 || !strings.Contains(text, dir+"/HOWTO.md") {
		return fmt.Errorf("no section")
	}
	rest := text[start+1:]
	end := len(text)
	if n := strings.Index(rest[len(claudeMDHeading):], "\n## "); n >= 0 {
		end = start + 1 + len(claudeMDHeading) + n + 1
	}
	out := strings.TrimRight(text[:start], "\n") + "\n"
	if end < len(text) {
		out += text[end:]
	}
	return fsguard.WriteFileSafe(path, []byte(out), fsguard.WriteOpts{Root: root, Mode: 0o644, KeepMode: true})
}

func plannedGitignore(root, dir string) removal {
	path := filepath.Join(root, ".gitignore")
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), dir+"/brain.db") {
		return removal{}
	}
	return removal{"remove entries", path, dir + "/brain.db, " + dir + "/brain.db-* and the dump patterns"}
}

// stripGitignore removes every line init writes or ever wrote, not just the obvious ones:
// leaving the dump patterns behind meant a reinstall found a half-ignored file and appended
// its own block again, so uninstall-then-init produced duplicates.
func stripGitignore(root, dir string) error {
	path := filepath.Join(root, ".gitignore")
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	drop := map[string]bool{ignoreHeader: true}
	for _, ln := range append(ignoreLines(dir), retiredIgnoreLines(dir)...) {
		drop[ln] = true
	}
	var kept []string
	for _, ln := range strings.Split(string(body), "\n") {
		if drop[strings.TrimSpace(ln)] {
			continue
		}
		kept = append(kept, ln)
	}
	out := strings.Join(kept, "\n")
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return fsguard.WriteFileSafe(path, []byte(out), fsguard.WriteOpts{Root: root, Mode: 0o644, KeepMode: true})
}
