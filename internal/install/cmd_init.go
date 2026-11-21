package install

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
)

// InitArgs are the flags of `os-brain init`, which creates a brain inside a project and
// wires the agents' hooks. The binary ships alone, with no template directory to copy from,
// so it has to be able to do all of this itself.
//
// Everything init does is idempotent and additive. Re-running it on a live brain must never
// cost anyone a memory, so it creates what is missing and leaves the rest alone.
type InitArgs struct {
	Dir        string // where the brain goes, relative to the project root
	Project    string
	NoHook     bool
	NoRecall   bool
	NoGitgnore bool
	// Agents forces the hosts to wire, comma-separated, instead of detecting them.
	Agents string
	// Manual is "I will do it myself": wire no agent and print every one's block.
	Manual bool
	JSON   bool
	// Channel registers `os-brain channel` in .mcp.json, so a direct message wakes an idle
	// session instead of waiting for its next prompt. Opt-in: Claude Code asks the user to
	// approve a project MCP server, and the channel only runs in a session started with it.
	Channel bool
}

// initWiring is what the agent-wiring step of init produced.
type initWiring struct {
	did     []string // lines written
	unwired []string // hosts that could not be wired: blocks a human must add
	paste   []string // paste-only hosts: blocks for config init never edits
	wired   int      // hosts wired now, including those that needed nothing written
}

func Init(w io.Writer, root string, a InitArgs) error {
	explicitDir := a.Dir != ""
	if a.Dir == "" {
		a.Dir = "brain"
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := checkInitArgs(a); err != nil {
		return err
	}
	here := filepath.Join(root, a.Dir)
	if err := checkNoOtherBrain(w, root, here, explicitDir); err != nil {
		return err
	}
	if a.Project == "" {
		a.Project = filepath.Base(root)
	}
	if err := initLinkCheck(root, a.Dir); err != nil {
		return err
	}

	env, did, err := createBrain(root, here, a.Project)
	if err != nil {
		return err
	}
	// The store is every memory in cleartext. Keeping it out of git is not optional, and the
	// project's own .gitignore is the only place that reliably wins.
	if !a.NoGitgnore {
		added, err := EnsureIgnored(root, a.Dir)
		if err != nil {
			return err
		}
		if added != "" {
			did = append(did, added)
		}
	}
	// An agent with a brain and no instructions uses files instead. The docs ship inside the
	// binary precisely so this cannot be skipped by a single-binary install.
	docs, err := writeAgentDocs(here)
	if err != nil {
		return err
	}
	did = append(did, docs...)

	wiring, err := wireAgents(root, a)
	if err != nil {
		return err
	}
	did = append(did, wiring.did...)

	stale := staleClaudeMD(root, a.Dir)
	registerNow(env)
	competing := CompetingStores(root, a.Dir)

	lines := initReport(env, did, wiring, stale, competing)
	if err := store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"project": env.Project, "dir": a.Dir, "db": env.DBPath,
		"schema_version": store.SchemaVersion, "created": did, "unwired": wiring.unwired, "paste": wiring.paste,
		"competing_stores": competingJSON(competing),
	}); err != nil {
		return err
	}
	return unfinishedInstall(wiring)
}

// checkInitArgs refuses a bad --dir or --agent before anything is written.
func checkInitArgs(a InitArgs) error {
	// --dir is documented as relative to the project. An absolute path used to be joined onto
	// the working directory anyway: `init --dir /tmp/x/brain` from the wrong place wrote a
	// brain into a nested mirror of that path inside the current repository, plus a
	// .gitignore edit and a hooks file, with nothing said. Refuse rather than guess which one
	// was meant.
	if filepath.IsAbs(a.Dir) {
		return fmt.Errorf("--dir is relative to the project, and %s is absolute.\n"+
			"Run init from that directory with a relative --dir, or pass the project root "+
			"as the argument: os-brain init %s", pyfmt.Repr(a.Dir), filepath.Dir(a.Dir))
	}
	if err := CheckBrainDir(a.Dir); err != nil {
		return err
	}
	// An unknown agent caught only at the wiring step would leave brain.json, the database,
	// both docs and a .gitignore line on disk, reported as nothing but "unknown agent": a
	// half install presented as a failure, with nothing saying what had been created.
	return CheckAgents(a.Agents)
}

// checkNoOtherBrain stops a session that does not know the project already has a brain from
// building a second one.
//
// A brain installed under a non-default name (`init --dir notes`) is invisible to a bare
// `os-brain` run from a shell, because DiscoverHome only knows the default name and the
// hook. So the next session concludes the project has no brain and runs init, which builds
// a SECOND one at brain/: two stores in one project, which silently diverge.
//
// Not always a refusal: `init --dir marketing/brain` in a project that already has brain/ is
// a supported install. The distinction is whether the caller SAID where. An explicit --dir
// is a second brain on purpose and is only reported; a defaulted one is refused.
func checkNoOtherBrain(w io.Writer, root, here string, explicitDir bool) error {
	existing, ok := brainAlreadyHere(root, here)
	if !ok {
		return nil
	}
	was := relTo(root, existing)
	if !explicitDir {
		return fmt.Errorf("this project already has a brain at %s.\n"+
			"Installing at %s would leave two stores in one project, and the auto-loaded "+
			"one wins regardless of which is right.\n"+
			"  upgrade the existing one:  os-brain init --dir %s\n"+
			"  a second brain on purpose: os-brain init --dir <name>  (say where, explicitly)",
			was, relTo(root, here), was)
	}
	fmt.Fprintf(w, "note: this project already has a brain at %s; installing a second "+
		"one at %s because --dir said so\n", was, relTo(root, here))
	return nil
}

// createBrain writes brain.json if it is missing and creates or migrates the database, and
// reports what it created.
func createBrain(root, here, project string) (store.Env, []string, error) {
	var did []string
	// brain.json first: it is what makes the directory a brain, and Open reads it.
	cfgPath := filepath.Join(here, "brain.json")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.MkdirAll(here, 0o700); err != nil {
			return store.Env{}, nil, err
		}
		if err := store.SaveConfig(here, store.Config{Project: project, Root: root}); err != nil {
			return store.Env{}, nil, err
		}
		did = append(did, "brain.json for "+pyfmt.Repr(project))
	}

	env := store.NewEnv(here)
	// OS_BRAIN_DB overrides where the database lives, which is right for a one-off command
	// and wrong for init: the config lands in <dir>/brain.json while the database lands
	// wherever the variable points, and the success line then reports a path that has no
	// database at it. A leftover export from an earlier command is enough to do it.
	if want := filepath.Join(here, "brain.db"); env.DBPath != want {
		cause := "brain.json says this project is shared, so its database is"
		if os.Getenv("OS_BRAIN_DB") != "" {
			cause = "OS_BRAIN_DB points at"
		}
		return store.Env{}, nil, fmt.Errorf("%s %s, and init would put brain.json in %s.\n"+
			"A local brain is a directory holding both. Clear that and run init again.",
			cause, env.DBPath, here)
	}
	_, statErr := os.Stat(env.DBPath)
	fresh := os.IsNotExist(statErr)
	st, err := store.Open(env)
	if err != nil {
		return store.Env{}, nil, err
	}
	st.Close()
	if fresh {
		did = append(did, "brain.db at schema v"+fmt.Sprint(store.SchemaVersion))
	}
	return env, did, nil
}

// wireAgents installs the session hooks, unless --no-hook, and the channel when asked.
// Memory that depends on the model remembering to read it will eventually not be read: the
// session-open hook is the only thing here that makes the brain load without anybody asking.
func wireAgents(root string, a InitArgs) (initWiring, error) {
	var out initWiring
	if !a.NoHook {
		var res AutoexecResult
		var err error
		if a.Manual {
			res, err = AutoexecManual(root, a.Dir)
		} else {
			res, err = Autoexec(root, a.Dir, a.Agents, !a.NoRecall)
		}
		if err != nil {
			return out, err
		}
		out = initWiring{did: res.Did, unwired: res.Unwired, paste: res.Paste, wired: res.Wired}
	}
	if a.Channel {
		d, err := wireChannel(root, a.Dir)
		if err != nil {
			return out, err
		}
		if d != "" {
			out.did = append(out.did, d)
		}
	}
	return out, nil
}

// registerNow adds the project to the shared index straight away. The index is what tells
// other projects whether this one may be read at all; written only by brief or describe, a
// project would be absent from it, and so unprotected, for as long as nobody had looked at
// it. A failure here is not init's to report: the next brief registers again.
func registerNow(env store.Env) {
	if st, err := store.Open(env); err == nil {
		st.RegisterProject()
		st.Close()
	}
}

// initReport is init's human output. The competing-store warning comes first, because the
// failure it describes is silent and a warning after the next-steps block is a footnote.
func initReport(env store.Env, did []string, wiring initWiring, stale string, competing []CompetingStore) []string {
	var lines []string
	if len(competing) > 0 {
		lines = append(lines, competingWarning(competing), "")
	}
	lines = append(lines, fmt.Sprintf("brain ready at %s", env.DBPath))
	for _, d := range did {
		lines = append(lines, "  + "+d)
	}
	if len(did) == 0 {
		lines = append(lines, "  (nothing to do: everything was already in place)")
	}
	// The per-prompt hook keeps what the person types. That is a privacy fact, so it is said
	// where the hook is installed, not only on a page somebody may never read.
	if capturesPrompts(did) {
		lines = append(lines, "",
			"Prompts: the per-prompt hook stores what you type, verbatim up to 8000 "+
				"characters, kept 30 days in the local store (utterance_days in brain.json "+
				"changes it).",
			"  os-brain utterances lists them; os-brain init --no-auto-recall turns the hook off.")
	}
	if stale != "" {
		lines = append(lines, "", stale)
	}
	if len(wiring.unwired) > 0 {
		lines = append(lines, "", "NOT WIRED — nothing will load this brain until a human adds one of these.",
			"This command cannot write these files for you, so it has not pretended to:", "")
		lines = append(lines, wiring.unwired...)
		lines = append(lines, "",
			"Then re-run `os-brain init`, or pass --agent <name> to wire one directly.")
	}
	if len(wiring.paste) > 0 {
		lines = append(lines, "", "ADD BY HAND — these agents keep the brain's wiring in your own config, "+
			"which init prints and never edits:", "")
		lines = append(lines, wiring.paste...)
	}
	return append(lines, "",
		"Next:",
		"  os-brain bootstrap               seed it from this project's past sessions",
		"  os-brain remember --title \"...\"  store the first fact",
		"  os-brain wakeup                  what a session will see at open",
		"  os-brain ui                      browse it in a browser, port 7788")
}

// unfinishedInstall turns an install nothing reads into a failing exit, after the report
// has said what to add.
func unfinishedInstall(wiring initWiring) error {
	if len(wiring.unwired) > 0 {
		return fmt.Errorf("the brain exists but no agent is wired to read it: " +
			"add one of the hooks above, then re-run init")
	}
	// Nothing else was wired, or already in place, so until the block is pasted nothing
	// reads this brain: that is still an unfinished install, and it says so.
	if len(wiring.paste) > 0 && wiring.wired == 0 {
		return fmt.Errorf("the brain exists but nothing reads it until the block above is added")
	}
	return nil
}

// capturesPrompts reports whether this run wired a per-prompt hook, which is the one that
// captures the prompt as well as recalling for it.
func capturesPrompts(did []string) bool {
	for _, d := range did {
		if strings.Contains(d, "hook-recall") {
			return true
		}
	}
	return false
}

// EnsureIgnored adds to the project's .gitignore only the lines that are missing, deciding
// per line. Deciding per combination ("has the database line", "has the dumps line") once
// appended the whole block again after an uninstall had removed some lines and left others,
// and every write here is meant to be idempotent.
func EnsureIgnored(root, dir string) (string, error) {
	path := filepath.Join(root, ".gitignore")
	if err := fsguard.CheckNoLinks(root, path); err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	text := string(body)
	have := map[string]bool{}
	for _, ln := range strings.Split(text, "\n") {
		have[strings.TrimSpace(ln)] = true
	}
	want := ignoreLines(dir)
	var add []string
	for _, w := range want {
		if !have[w] {
			add = append(add, w)
		}
	}
	if len(add) == 0 {
		return "", nil
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if !have[ignoreHeader] {
		text += "\n" + ignoreHeader + "\n"
	}
	text += strings.Join(add, "\n") + "\n"
	if err := fsguard.WriteFileSafe(path, []byte(text), fsguard.WriteOpts{Root: root, Mode: 0o644, KeepMode: true}); err != nil {
		return "", err
	}
	if len(add) == len(want) {
		return ".gitignore entry for " + want[0], nil
	}
	return fmt.Sprintf(".gitignore: %d line(s) that were missing", len(add)), nil
}

// ignoreHeader is the comment line init writes above its .gitignore block.
const ignoreHeader = "# the brain is every memory in cleartext"

// ignoreLines is the ONE list of what a brain directory must keep out of git. init writes
// it and uninstall removes it; two copies drifted apart once already, and the failure mode
// of drift here is a store left half-ignored.
//
// The dumps are in it too, not just the database: `backup` writes into the brain directory
// by default and refuses a path git would track, so without them a fresh install could not
// run its first backup. The patterns are deliberately wider than the names in use, because
// a leak here is silent and new suffixes keep appearing: `brain.db-*` did not match
// `brain.db.pre-restore-<stamp>`, and `*-backup-*.json` did not match a dump that is a
// database, which left complete copies of a store one `git add -A` from being committed in
// cleartext. Nothing in a brain directory that IS meant to be committed (brain.json, the two
// docs, autoexec.sh) begins with `brain.db` or contains `-backup-`, so widening costs
// nothing.
func ignoreLines(dir string) []string {
	return []string{
		dir + "/brain.db*",
		dir + "/*-backup-*",
		dir + "/brain-prune-*",
		dir + "/*.snapshot.md",
		// ns-brain's team edition caches its brief here, memory bodies in cleartext. os-brain
		// never writes the file, but the line stays so a .gitignore written by either edition
		// is identical and a brain can move between them without a store leaking.
		dir + "/hive-brief-cache.json",
	}
}

// retiredIgnoreLines are patterns earlier versions of init wrote. uninstall drops them too,
// so an older brain leaves nothing behind, and a reinstall does not append its block beside
// them. Matched verbatim: this is what the binary wrote, not a guess at what somebody meant.
func retiredIgnoreLines(dir string) []string {
	return []string{
		dir + "/brain.db",
		dir + "/brain.db-*",
		dir + "/*-backup-*.json",
		dir + "/brain-prune-*.json",
	}
}

// staleClaudeMD reports the "Project memory" section older installs appended to the
// project's CLAUDE.md. It reads and never writes: that file belongs to the project, the
// section is now redundant rather than wrong, and deciding what lives in a project's own
// top-level document is not this tool's call.
func staleClaudeMD(root, dir string) string {
	body, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil || !strings.Contains(string(body), dir+"/HOWTO.md") {
		return ""
	}
	return "CLAUDE.md still carries the \"Project memory\" section an older init appended.\n" +
		"  The hook now carries that text at session open, so the section is redundant.\n" +
		"  Delete it yourself if you want it gone: this command no longer edits CLAUDE.md."
}

// relTo renders a path for a message, project-relative when it is inside the project.
func relTo(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// brainAlreadyHere answers "does this project already have a brain somewhere other than
// here", which is a wider question than DiscoverHome's and belongs only to init.
// DiscoverHome resolves the brain a COMMAND should use, so it is right for it to know just
// the default name and the hook, but that is exactly what makes a renamed brain invisible.
// Here the cost of looking wider is one readdir, and the cost of not looking is a second
// store.
//
// Depth one, deliberately: a brain is a directory in the project, and walking the whole
// tree would stat every node_modules in it to catch a case nobody has.
func brainAlreadyHere(root, here string) (string, bool) {
	other := func(p string) bool {
		return filepath.Clean(p) != filepath.Clean(here) && store.IsBrainDir(p)
	}
	if other(root) {
		return root, true
	}
	if p, ok := store.DiscoverHome(root); ok && other(p) {
		return p, true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if p := filepath.Join(root, e.Name()); other(p) {
			return p, true
		}
	}
	return "", false
}

// initLinkCheck refuses an install before anything is written when any file init may write,
// or read and rewrite, is a link or is reached through one. A repository commits links and
// git checks them out: `.claude/settings.json -> ~/.claude/settings.json` would make init
// merge the user's global settings into the repo, and `brain/HOWTO.md -> ~/.bashrc` would
// make it overwrite the target. Every writer also refuses on its own; this is what makes the
// refusal happen before brain.json and the database exist rather than half way.
func initLinkCheck(root, dir string) error {
	here := filepath.Join(root, dir)
	paths := []string{filepath.Join(root, ".gitignore"), filepath.Join(root, ".mcp.json")}
	for _, f := range []string{"brain.json", "brain.db", "HOWTO.md", "BEST-PRACTICES.md", autoexecScript} {
		paths = append(paths, filepath.Join(here, f))
	}
	for _, f := range hookFiles() {
		paths = append(paths, filepath.Join(root, f.rel))
	}
	for _, rel := range pluginPaths {
		paths = append(paths, filepath.Join(root, rel))
	}
	for _, f := range slashCommandFiles {
		paths = append(paths, filepath.Join(root, ".claude", "commands", f))
	}
	for _, p := range paths {
		if err := fsguard.CheckNoLinks(root, p); err != nil {
			return fmt.Errorf("refusing to install: %w\nNothing has been written. If the link "+
				"is yours on purpose (a settings file kept in dotfiles, say), run init with "+
				"--no-hook and add the hooks to the real file yourself", err)
		}
	}
	return nil
}

// ChannelServer is the name the channel is registered under, and so the name the session is
// started with: claude --dangerously-load-development-channels server:brain-channel
const ChannelServer = "brain-channel"

// wireChannel adds the channel listener to .mcp.json beside whatever servers are there. The
// brain directory is given relative to the project root, which is where Claude Code starts a
// project's servers, so the file carries no machine's absolute path and can be committed.
func wireChannel(root, dir string) (string, error) {
	path := filepath.Join(root, ".mcp.json")
	if err := fsguard.CheckNoLinks(root, path); err != nil {
		return "", err
	}
	cfg := map[string]interface{}{}
	if body, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(body, &cfg) != nil {
			return "", fmt.Errorf("%s exists but is not readable JSON, so it was left untouched", path)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	servers, _ := cfg["mcpServers"].(map[string]interface{})
	if servers == nil {
		servers = map[string]interface{}{}
	}
	if _, ok := servers[ChannelServer]; ok {
		return "", nil
	}
	servers[ChannelServer] = map[string]interface{}{
		"command": "os-brain", "args": []interface{}{"channel"},
		"env": map[string]interface{}{"OS_BRAIN_HOME": dir},
	}
	cfg["mcpServers"] = servers
	data, err := pyfmt.MarshalPy(cfg, "  ")
	if err != nil {
		return "", err
	}
	if err := fsguard.WriteFileSafe(path, append(data, '\n'), fsguard.WriteOpts{Root: root, Mode: 0o644,
		KeepMode: true}); err != nil {
		return "", err
	}
	return ".mcp.json: " + ChannelServer + " (start sessions with claude " +
		"--dangerously-load-development-channels server:" + ChannelServer + ")", nil
}
