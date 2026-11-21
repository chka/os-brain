// Package testutil is the test floor shared by the packages built on internal/store: a
// throwaway brain with every path pinned, and a real git repository under it for the tests
// that judge memories against a tree. It is imported only by tests, so no binary carries it,
// and it imports nothing above the store, so any package's tests can use it.
package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// NewStore opens a brain whose every path points into a throwaway directory. A test suite
// that is not hermetic writes into the store the developer actually uses: an earlier suite
// once left dozens of phantom projects in a real store that way.
func NewStore(t *testing.T) *store.Store {
	t.Helper()
	_, home := pinnedBrain(t)
	return open(t, home)
}

// NewGitStore is NewStore with a real git repository at the project root, because the
// whole point of the freshness verdict is what it says about an actual tree. A fake commit
// string would test the rendering and none of the resolution. It returns the store and the
// repository directory, which already has one commit.
func NewGitStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir, home := pinnedBrain(t)

	Git(t, dir, "init", "-q")
	Git(t, dir, "config", "user.email", "test@example.com")
	Git(t, dir, "config", "user.name", "Test")
	Commit(t, dir, "one")

	return open(t, home), dir
}

// Git runs a git command in dir with the user's and the system's git config ignored, so a
// signing hook or a global setting on the test machine cannot change the result. It returns
// the trimmed output and fails the test if git fails.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Commit writes the file name.txt and commits everything in dir with the message name.
func Commit(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "add", "-A")
	Git(t, dir, "commit", "-q", "-m", name)
}

// MustJSON runs a command that writes JSON and returns the decoded object, failing the test
// if the command errors or prints anything that is not a JSON object.
func MustJSON(t *testing.T, fn func(io.Writer) error) map[string]interface{} {
	t.Helper()
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("not json: %v (%s)", err, buf.String())
	}
	return out
}

// SeedTitles must be genuinely different from each other: `remember` refuses a second memory
// about the same subject in one scope, so a fixture of "fact 1", "fact 2" would be refused by
// the near-duplicate guard rather than stored.
var SeedTitles = []string{
	"nginx terminates TLS on port 8443 in front of the app",
	"postgres runs on the same box, loopback only, no published port",
	"the deploy user is called ship and owns /srv",
	"redis needs requirepass set or the botnet finds it",
	"certificates renew from cron at 03:20 every Monday",
	"the staging box has half the memory of production",
	"backups land on a second provider, not the same one",
	"the queue drains slowly whenever the embedder is down",
	"log rotation keeps fourteen days and then compresses",
	"the health endpoint answers ok even when the disk is full",
	"DNS is delegated, so a record change takes an hour to matter",
	"the firewall drops everything except 22, 80 and 443",
	"docker writes its own iptables and bypasses the firewall",
	"secrets come from the vault at boot and never touch the image",
}

// IDOf returns the id of the memory with exactly this title.
func IDOf(t *testing.T, s *store.Store, title string) int64 {
	t.Helper()
	var id int64
	if err := s.DB.QueryRow("SELECT id FROM memories WHERE title=?", title).Scan(&id); err != nil {
		t.Fatalf("no memory %q: %v", title, err)
	}
	return id
}

// WriteDump writes an export file with this body and returns its path.
func WriteDump(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dump.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// KnownProject puts a project in the shared index the way opening its brain once would
// have. A note is only sent to a project that exists, so note tests need a recipient.
func KnownProject(t *testing.T, s *store.Store, name string) {
	t.Helper()
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	defer s.CloseGlobal(g)
	if _, err := g.Exec("INSERT INTO projects(root_id,name,last_seen) VALUES(?,?,?)",
		"test-"+name, name, timefmt.Now()); err != nil {
		t.Fatal(err)
	}
}

// MarkPrivate declares a project private in the shared index, the way `private` does.
func MarkPrivate(t *testing.T, s *store.Store, name string) {
	t.Helper()
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no shared index")
	}
	defer s.CloseGlobal(g)
	if _, err := g.Exec(store.ProjectIDsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec("INSERT OR REPLACE INTO project_ids(name, root_id, private, bound_at) "+
		"VALUES(?, 'elsewhere', 1, 1)", name); err != nil {
		t.Fatal(err)
	}
}

// CaptureStderr returns what fn wrote to the process's stderr. Warnings and nudges go to
// the real stderr rather than the command's writer, so a test that only reads the writer
// never sees them.
func CaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// Clone returns a second handle on the same open store, sharing its database connections,
// whose Env can be changed without changing s. Tests use it to play a second project on the
// same store. It copies the store.Store itself: the feature packages wrap the store by
// pointer, so copying a wrapper (other := *memory.New(s)) would share the Env and the
// "second project" would silently be the first.
func Clone(s *store.Store) *store.Store {
	c := *s
	return &c
}

// SharedLab pins every store path inside a fresh directory, so projects opened with
// "shared":true in a test meet in one scratch shared store and nowhere else, and returns
// that directory.
func SharedLab(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(base, "config", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_DB", "")
	t.Setenv("OS_BRAIN_PROJECT", "")
	t.Setenv("OS_BRAIN_PROJECT_ROOT", "")
	return base
}

// OpenAt writes dir/brain/brain.json with cfg and opens it. The open's error is returned,
// not failed on, because refusing to open is what many identity tests expect.
func OpenAt(t *testing.T, dir, cfg string) (*store.Store, error) {
	t.Helper()
	here := filepath.Join(dir, "brain")
	if err := os.MkdirAll(here, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(here, "brain.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(store.NewEnv(here))
	if err == nil {
		t.Cleanup(func() { s.Close() })
	}
	return s, err
}

// MustOpenAt is OpenAt for a directory the test expects to open.
func MustOpenAt(t *testing.T, dir, cfg string) *store.Store {
	t.Helper()
	s, err := OpenAt(t, dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// LastID returns the id of the most recently stored memory.
func LastID(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var id int64
	if err := s.DB.QueryRow("SELECT MAX(id) FROM memories").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// pinnedBrain makes a brain directory for project "testproj" and points every machine-level
// path into the same throwaway directory, for the rest of the test.
func pinnedBrain(t *testing.T) (dir, home string) {
	t.Helper()
	dir = t.TempDir()
	home = filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"testproj"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "config", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_DB", filepath.Join(home, "brain.db"))
	t.Setenv("OS_BRAIN_PROJECT", "testproj")
	return dir, home
}

func open(t *testing.T, home string) *store.Store {
	t.Helper()
	s, err := store.Open(store.NewEnv(home))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
