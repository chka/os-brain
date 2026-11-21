package fsguard

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/diag"
	"github.com/chka/os-brain/internal/gitsafe"
	"github.com/chka/os-brain/internal/textguard"
)

// GuardOutside refuses to write the brain's contents anywhere but the project directory.
// A mistyped --out is a typo; an agent acting on injected text is not. Either way the
// whole brain in cleartext should not land somewhere nobody will look for it.
func GuardOutside(path, projectRoot string, allowOutside bool) error {
	if allowOutside {
		return nil
	}
	resolved := Realpath(path)
	root := Realpath(projectRoot)
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return fmt.Errorf("%s resolves to %s, outside the project (%s).\nThis writes every "+
			"memory in cleartext. Choose a path inside the project, or pass --allow-outside "+
			"if that is deliberate.", path, resolved, root)
	}
	return nil
}

// GuardReadInside refuses a file a caller on the other end of a wire asks this process to
// read, unless it is inside the project once every link is resolved. The file must exist:
// a path that cannot be resolved cannot be shown to be inside.
func GuardReadInside(path, projectRoot string) error {
	abs, err := filepath.Abs(path)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	root, rerr := filepath.EvalSymlinks(projectRoot)
	if err != nil || rerr != nil ||
		(abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator))) {
		return fmt.Errorf("%s is outside the project (%s), or cannot be resolved inside it. "+
			"Over MCP a command reads files from inside the project only; copy the file there, "+
			"or run the command in a shell.", path, projectRoot)
	}
	return nil
}

// Realpath resolves symlinks the way Python's os.path.realpath does: it must work on a
// path that does not exist yet, because the whole point is to check an output file before
// writing it. EvalSymlinks fails outright on a missing leaf, and falling back to an
// unresolved Abs() silently breaks the comparison on macOS, where every temp directory is
// /var -> /private/var. Resolve the deepest existing ancestor, then re-join the rest.
func Realpath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	rest := ""
	cur := abs
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// gitWouldTrack reports whether path sits in a git repository without being ignored.
// Three states, and the third matters: nil means git could not be asked at all, which is
// not the same as "safe".
func gitWouldTrack(path string) *bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(abs)
	run := func(args ...string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := GitRead(ctx, dir, args...)
		return strings.TrimSpace(out), err == nil
	}
	inside, ok := run("rev-parse", "--is-inside-work-tree")
	if !ok {
		return nil
	}
	if inside != "true" {
		no := false
		return &no
	}
	// check-ignore exits 0 when the path IS ignored, 1 when it is not.
	_, ignored := run("check-ignore", "-q", abs)
	tracked := !ignored
	return &tracked
}

// GuardPlaintextOut refuses to write the brain into a git-tracked path. A dump is every
// memory in cleartext, and committing one is not undone by deleting the file.
func GuardPlaintextOut(path string, allowTracked bool) error {
	tracked := gitWouldTrack(path)
	if tracked != nil && *tracked && !allowTracked {
		return fmt.Errorf("%s is inside a git repository and is not gitignored.\n"+
			"A snapshot is every memory in cleartext.\n"+
			"  gitignore it, then re-run   no flag needed, and the usual answer\n"+
			"  a path outside the project  needs --allow-outside\n"+
			"  commit it deliberately      needs --allow-tracked", path)
	}
	if tracked == nil {
		diag.Warn("note: cannot ask git about %s; it is being written in cleartext", path)
	}
	return nil
}

// WriteOpts says how much of the path WriteFileSafe may trust.
type WriteOpts struct {
	// Root is the directory the file must sit under. Every component between it and the
	// file has to be a real directory: a repository can commit a symlink at any of them,
	// and git checks it out as one. Empty means only the file itself is checked, for
	// writes into the user's own config directories and for paths the caller allowed
	// outside the project.
	Root string
	// Mode is the file's mode from the moment it exists; 0 means 0600.
	Mode os.FileMode
	// KeepMode keeps the mode of a file being replaced, for host configs whose owner may
	// have tightened them.
	KeepMode bool
	// Marker, when set, makes this generated output: an existing file is replaced only if
	// it carries Marker or one of Also, unless Force.
	Marker string
	Also   []string
	Force  bool
	// Exclusive refuses to replace anything at all.
	Exclusive bool
	// MkdirMode, when set, creates missing parents with that mode once the existing ones
	// have been checked, so a directory is never made through a planted link.
	MkdirMode os.FileMode
}

// CheckNoLinks refuses a path that is a symlink, or that reaches the file through one
// between root and it. Callers that read a file before rewriting it run this first: a
// committed `.claude/settings.json -> ~/.claude/settings.json` made init merge the user's
// global settings, API key included, into a file in the repository.
func CheckNoLinks(root, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if root == "" {
		return refuseLink(path, abs)
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// Walked upward and compared resolved, because the root and the path can name the same
	// directory by different spellings (macOS /var is /private/var); a lexical Rel would
	// call that outside. The root itself may be a link: that is where the user keeps it.
	want := Realpath(base)
	for cur := abs; ; {
		if cur == base {
			return nil
		}
		if err := refuseLink(path, cur); err != nil {
			return err
		}
		if Realpath(cur) == want {
			return nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return fmt.Errorf("%s is outside %s; refusing to write it", path, root)
		}
		cur = parent
	}
}

// refuseLink refuses when at, a step on the way to path (or path itself), is a symlink.
// Something missing there is not a link and passes.
func refuseLink(path, at string) error {
	info, err := os.Lstat(at)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	dest, _ := os.Readlink(at)
	where := at
	if at != path {
		where = at + ", on the way to " + path + ","
	}
	return fmt.Errorf("%s is a symbolic link to %s.\nRefusing to write through it: a link "+
		"committed in a repository can point at any file you own, and the write would land "+
		"there.\nReplace the link with a real file or directory (or remove it) and run this "+
		"again.", where, dest)
}

// WriteFileSafe is the one way this binary writes a file. It refuses links (CheckNoLinks),
// writes to a fresh randomly named file in the same directory with the final mode set
// before any content, syncs it, and renames it over the target, so a crash leaves the old
// file or the new one and never half of either. A fixed `X.tmp` name is what let a
// committed `.codex/hooks.json.tmp -> ~/.bashrc` turn init into a write to ~/.bashrc.
func WriteFileSafe(path string, data []byte, o WriteOpts) error {
	if err := CheckNoLinks(o.Root, path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if o.MkdirMode != 0 {
		if err := os.MkdirAll(dir, o.MkdirMode); err != nil {
			return err
		}
		if err := CheckNoLinks(o.Root, path); err != nil {
			return err
		}
	}
	mode, err := targetMode(path, o)
	if err != nil {
		return err
	}
	if o.Marker != "" {
		if err := GuardOutfile(path, o.Marker, o.Force, o.Also...); err != nil {
			return err
		}
	}
	tmp, err := WriteTemp(dir, filepath.Base(path), data, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if o.Exclusive {
		// A hard link fails if anything appeared at path since the check, where a rename
		// would replace it. The temporary name goes either way.
		return os.Link(tmp, path)
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// targetMode is the mode the written file gets, after refusing a target that is not a
// regular file or, for an exclusive write, one that exists at all.
func targetMode(path string, o WriteOpts) (os.FileMode, error) {
	mode := o.Mode
	if mode == 0 {
		mode = 0o600
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return mode, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%s exists and is not a regular file; refusing to replace it", path)
	}
	if o.Exclusive {
		return 0, fmt.Errorf("%s already exists; refusing to replace it", path)
	}
	if o.KeepMode {
		mode = info.Mode().Perm()
	}
	return mode, nil
}

// WriteTemp writes data to a new randomly named file in dir, mode set before the content
// and synced, and returns its path. The name cannot be planted in advance: CreateTemp
// opens with O_EXCL, which neither follows nor reuses a link.
func WriteTemp(dir, base string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, "."+base+".os-brain-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// Dump is a file of cleartext memory: export, snapshot, backup, prune and wipe dumps, the
// uninstall dump, TODO.md. Every one of them gets the same four refusals, because the one
// that was missing a guard is the one that overwrote a tracked NOTES.md.
type Dump struct {
	Path         string
	Root         string
	Marker       string
	Also         []string
	Force        bool
	AllowOutside bool
	AllowTracked bool
	checked      bool
}

// Check runs the refusals without writing, so a command can refuse before it changes
// anything else.
func (d *Dump) Check() error {
	if err := GuardOutside(d.Path, d.Root, d.AllowOutside); err != nil {
		return err
	}
	if err := GuardPlaintextOut(d.Path, d.AllowTracked); err != nil {
		return err
	}
	if err := CheckNoLinks(d.linkRoot(), d.Path); err != nil {
		return err
	}
	if err := GuardOutfile(d.Path, d.Marker, d.Force, d.Also...); err != nil {
		return err
	}
	d.checked = true
	return nil
}

// linkRoot is the root links are checked below: none when the dump may go outside the
// project, since then only the file itself is checked.
func (d *Dump) linkRoot() string {
	if d.AllowOutside {
		return ""
	}
	return d.Root
}

// Write runs Check if the caller has not, and writes owner-only. The link and marker
// refusals run again at the write itself.
func (d *Dump) Write(text string) error {
	if !d.checked {
		if err := d.Check(); err != nil {
			return err
		}
	}
	return WriteFileSafe(d.Path, []byte(text), WriteOpts{Root: d.linkRoot(), Mode: 0o600,
		Marker: d.Marker, Also: d.Also, Force: d.Force})
}

// PrecreatePrivate makes an empty owner-only file for SQLite to open, so the database and
// the -wal and -shm it creates (which copy the main file's mode) are never readable by
// anyone else, not even for the moment before a chmod. An existing file is tightened in
// place. A link there is refused: SQLite would follow it.
func PrecreatePrivate(path string) error {
	if err := refuseLink(path, path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		return f.Close()
	}
	if !os.IsExist(err) {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm() != 0o600 {
		_ = os.Chmod(path, 0o600)
	}
	return nil
}

// GuardOutfile refuses to overwrite a file this tool did not generate. A wrong --out is a
// typo; silently replacing someone's notes with generated output is data loss.
func GuardOutfile(path, marker string, force bool, also ...string) error {
	if force {
		return nil
	}
	fh, err := os.Open(path)
	if os.IsNotExist(err) {
		if _, lerr := os.Lstat(path); lerr == nil {
			return fmt.Errorf("%s is a symbolic link to nowhere; writing it would create "+
				"the file it points at. Refusing", path)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s exists and cannot be read (%v), so whether this command wrote "+
			"it cannot be told. Refusing to overwrite it", path, err)
	}
	defer fh.Close()
	buf := make([]byte, 4096)
	n, _ := io.ReadFull(fh, buf)
	head := string(buf[:n])
	// `also` carries markers written by earlier versions. The marker is what decides
	// whether a file is ours to overwrite, so renaming it silently reclassifies every
	// file we already generated as somebody else's work and refuses to update it.
	for _, m := range append([]string{marker}, also...) {
		if strings.Contains(head, m) {
			return nil
		}
	}
	return fmt.Errorf("%s exists and was not generated by this command.\n"+
		"Refusing to overwrite it. Pick another --out, or pass --force", path)
}

// GitRead is how the brain reads from git: through gitsafe, with the output made
// visible text (keeping the tabs git separates fields with): a commit subject or a branch
// name is the repository's text, and an escape in it reaches the brief and the terminal as
// written.
func GitRead(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitsafe.Output(ctx, dir, args...)
	parts := strings.Split(out, "\t")
	for i, p := range parts {
		parts[i] = textguard.BodyText(p)
	}
	return strings.Join(parts, "\t"), err
}
