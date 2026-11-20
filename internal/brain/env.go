package brain

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Env is everything the process needs to know about which brain it is talking to. It is a
// value rather than package globals so tests can point it anywhere without touching the real
// store: tests that used the real store once left 67 phantom projects in the one the
// developer actually used.
type Env struct {
	Here     string // directory holding brain.json and, for a local store, brain.db
	DBPath   string
	GlobalDB string
	Project  string
	Shared   bool
	Private  bool
	Config   Config
	// Remote is set when this Env was resolved for a project OTHER than the one this
	// directory belongs to: an MCP session working elsewhere. Everything machine-local then
	// describes the server, not the caller: the git state, the paths, the host. The brief
	// must say so rather than report the wrong tree with a straight face.
	Remote bool
	// Named is set when the CALLER named the project (an MCP _project or X-Brain-Project)
	// and it is not the one this directory declares. Only such an Env may open the shared
	// store under a name bound to another directory, and never a private one: a directory
	// declaring a name is not the same act as a server being asked for one, and the
	// declaration is what a cloned repository controls.
	Named bool
	// Claim lets `relabel --claim`, a person at a terminal, open the shared store from a
	// directory the project's name is not bound to, in order to move the binding here.
	Claim bool
	// Session is the CALLER's session, when the process is not the caller. Over MCP one
	// server writes on behalf of many sessions, so reading the environment would stamp every
	// one of them with the server's own id and make the attribution column a constant.
	// Empty means "this process is the caller", the CLI case.
	Session string
	// ConfigErr is brain.json failing to parse. Open refuses on it, and so does the gate in
	// the command package, so no caller can proceed on the zero Config it would otherwise
	// have read.
	ConfigErr error
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// ConfigDir is where os-brain keeps its machine-level files (the shared store, the MCP and UI
// tokens): beside the shared store, so pointing OS_BRAIN_GLOBAL_DB elsewhere moves them too.
func ConfigDir() string { return filepath.Dir(sharedDBPath()) }

func sharedDBPath() string {
	if v := os.Getenv("OS_BRAIN_GLOBAL_DB"); v != "" {
		return v
	}
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		cfg = expandHome("~/.claude")
	}
	return filepath.Join(cfg, "os-brain", "brain.db")
}

// NewEnv resolves the brain in here for the project this directory belongs to. A brain.json
// without "shared" stays on its own file, so an existing install never silently starts
// looking at a different database.
func NewEnv(here string) Env { return NewEnvFor(here, "") }

// NewEnvFor resolves the environment for a NAMED project inside the same store. One server
// can front a shared store holding many projects, and the caller (an MCP session working in
// one of them) is the only thing that knows which. An empty name means the project this
// directory belongs to.
func NewEnvFor(here, project string) Env {
	here, _ = filepath.Abs(here)
	cfg, cfgErr := LoadConfig(filepath.Join(here, "brain.json"))

	db := os.Getenv("OS_BRAIN_DB")
	if db == "" {
		if cfg.Shared {
			db = sharedDBPath()
		} else {
			db = filepath.Join(here, "brain.db")
		}
	}
	own, err := ownProjectName(here, cfg)
	if err != nil && cfgErr == nil {
		cfgErr = err
	}
	if err := CheckLine("the project name", project); err != nil && cfgErr == nil {
		cfgErr = err
	}
	named := project != "" && project != own
	if project == "" {
		project = own
	}
	remote := project != "" && cfg.Project != "" && project != cfg.Project
	dbAbs, _ := filepath.Abs(db)
	globalAbs, _ := filepath.Abs(sharedDBPath())
	return Env{
		Here:      here,
		DBPath:    dbAbs,
		GlobalDB:  globalAbs,
		Project:   project,
		Shared:    dbAbs == globalAbs,
		Private:   cfg.Private,
		Config:    cfg,
		Remote:    remote,
		Named:     named,
		ConfigErr: cfgErr,
	}
}

// ownProjectName is the name this directory declares: OS_BRAIN_PROJECT, then brain.json's
// "project", then the name of the directory the brain sits in. The error is a newline in
// OS_BRAIN_PROJECT, which is reported but does not stop the name being used.
func ownProjectName(here string, cfg Config) (string, error) {
	own := os.Getenv("OS_BRAIN_PROJECT")
	err := CheckLine("OS_BRAIN_PROJECT", own)
	if own == "" {
		own = cfg.Project
	}
	if own == "" {
		own = filepath.Base(filepath.Dir(here))
	}
	if own == "" || own == "." || own == string(filepath.Separator) {
		own = "project"
	}
	return own, err
}

// ProjectRoot is where this project lives. init writes it into brain.json as "root", and
// OS_BRAIN_PROJECT_ROOT can say it too, but both are claims a committed file or a
// repository's settings can make, and the root is what identity and the outside-the-project
// write guard are computed from. `"root": "~"` widened that guard to the whole home
// directory, and a clone carrying the original author's path borrowed that project's
// identity. So a declared root counts only when the brain directory is inside it and it is
// inside the same repository, or the root's own hook names this brain; otherwise the brain's
// parent directory is the root, which is what every default install has anyway.
func (e Env) ProjectRoot() string {
	parent := filepath.Dir(e.Here)
	for _, v := range []string{os.Getenv("OS_BRAIN_PROJECT_ROOT"), e.Config.Root} {
		if v == "" {
			continue
		}
		abs, err := filepath.Abs(expandHome(v))
		if err != nil || !within(e.Here, abs) {
			continue
		}
		if realpath(abs) == realpath(parent) {
			return abs
		}
		if top := repoTop(e.Here); top != "" && within(abs, top) {
			return abs
		}
		if h := hookedHome(abs); h != "" && realpath(h) == realpath(e.Here) {
			return abs
		}
	}
	return parent
}

// repoTop is the nearest directory at or above dir holding .git, or "" outside a
// repository. A stat walk rather than git: this runs on every identity check.
func repoTop(dir string) string {
	cur := realpath(dir)
	for {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// DiscoverHome finds the brain that the current directory belongs to, walking upward.
//
// The binary lives on PATH, so without this a command would resolve against the directory
// of the executable: `os-brain bootstrap` run in a project with a perfectly good brain once
// created /usr/local/bin/brain.db and went looking for the transcripts of a project called
// "local". The hooks pass OS_BRAIN_HOME and are not affected; a person at a shell gets no
// such help, which is exactly when it happened.
//
// Three shapes per level, in order: the directory IS a brain, it holds the default brain/,
// or its .claude/settings.json names one (which is how `init --dir marketing/brain` stays
// discoverable without a second convention to keep in sync).
func DiscoverHome(start string) (string, bool) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if isBrainDir(dir) {
			return dir, true
		}
		if p := filepath.Join(dir, "brain"); isBrainDir(p) {
			return p, true
		}
		if p := hookedHome(dir); p != "" && isBrainDir(p) {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func isBrainDir(p string) bool {
	_, err := os.Stat(filepath.Join(p, "brain.json"))
	return err == nil
}

// hookHomeRE finds the brain directory in the SessionStart hook init writes. The quotes
// arrive escaped, because the command is a string inside settings.json.
var hookHomeRE = regexp.MustCompile(`OS_BRAIN_HOME=\\?"?\$\{?CLAUDE_PROJECT_DIR\}?/([^"\\\s]+)`)

// hookedHome reads the brain directory back out of the hook that init wrote. settings.json
// is committed, so it is the repository's word, not the person's: a path that leaves the
// repository (`../sibling/brain`, or a link out of it) is not this project's brain, and
// following it would read a sibling project's private file from a clone.
func hookedHome(root string) string {
	body, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	m := hookHomeRE.FindSubmatch(body)
	if m == nil {
		return ""
	}
	p := filepath.Join(root, string(m[1]))
	if !within(p, root) {
		return ""
	}
	return p
}

// within reports whether path is dir or below it once both are resolved, so neither `..`
// nor a symbolic link carries a path out.
func within(path, dir string) bool {
	p, d := realpath(path), realpath(dir)
	return p == d || strings.HasPrefix(p, d+string(filepath.Separator))
}
