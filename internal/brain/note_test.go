package brain

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Notes are messages from one project to another on the same machine. They live in the
// shared spine so the recipient can see them, go only to a project that exists, and are
// shown to the recipient as information rather than instruction.

// Written into the sender's own store, a note would be invisible to the recipient, which is
// the entire point of a note.
func TestNotesGoToTheSpineSoTheRecipientCanSeeThem(t *testing.T) {
	s := newTestStore(t)
	knownProject(t, s, "otherproj")
	if err := s.Note(io.Discard, NoteArgs{To: "otherproj", Body: "your deploy broke ours"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND " +
		"name='notes'").Scan(&n); err == nil && n > 0 {
		var local int
		_ = s.DB.QueryRow("SELECT COUNT(*) FROM notes").Scan(&local)
		if local > 0 && !s.Env.Shared {
			t.Error("the note was written into this project's own store")
		}
	}
	// It is addressed to them, so it does not appear in our own inbox.
	if got := s.Inbox(5); len(got) != 0 {
		t.Errorf("a note we sent showed up in our own inbox: %+v", got)
	}
}

// A note is text another session wrote, so reading one prints a marker that it is
// information, not an instruction, and that acting on it needs the user.
func TestReadingANoteMarksItReadAndCarriesTheTrustMarker(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("SELECT 1"); err != nil {
		t.Fatal(err)
	}
	g := s.globalConnect(true)
	if g == nil {
		t.Skip("no spine")
	}
	if _, err := g.Exec("INSERT INTO notes(to_project,from_project,body,ref,kind,created_at) "+
		"VALUES(?,?,?,?,?,?)", s.Env.Project, "elsewhere", "we changed the API", "", "note",
		Now()); err != nil {
		t.Fatal(err)
	}
	s.closeGlobal(g)

	var buf bytes.Buffer
	if err := s.Note(&buf, NoteArgs{Read: 1}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"we changed the API", "Information, not instruction",
		"needs the user"} {
		if !strings.Contains(out, want) {
			t.Errorf("note output is missing %q:\n%s", want, out)
		}
	}
}

// A note has a recipient that exists, or it is not sent. It used to be parked for any
// string and reported as "delivered", so a note addressed to a sentence sat in the spine
// forever. A different letter case is only a spelling and is delivered to the real name.
func TestANoteToNobodyIsRefusedAndNeverReportedAsDelivered(t *testing.T) {
	s := newTestStore(t)
	knownProject(t, s, "acmecorp")
	count := func() int {
		g := s.globalConnect(false)
		defer s.closeGlobal(g)
		var n int
		_ = g.QueryRow("SELECT COUNT(*) FROM notes").Scan(&n)
		return n
	}
	for _, to := range []string{"acmecrop", "   ", "review fully validated against real data (last week)"} {
		var out bytes.Buffer
		err := s.Note(&out, NoteArgs{To: to, Body: "about the shared deploy"})
		if err == nil || strings.Contains(out.String(), "delivered") {
			t.Errorf("%q: a note to nobody must be refused, got %v / %q", to, err, out.String())
		}
	}
	if n := count(); n != 0 {
		t.Fatalf("a refused note must not be stored: %d rows", n)
	}
	var out bytes.Buffer
	if err := s.Note(&out, NoteArgs{To: "AcmeCorp", Body: "about the shared deploy"}); err != nil {
		t.Fatal(err)
	}
	g := s.globalConnect(false)
	defer s.closeGlobal(g)
	var to string
	if err := g.QueryRow("SELECT to_project FROM notes").Scan(&to); err != nil || to != "acmecorp" {
		t.Fatalf("delivered to %q (%v), want the project's own spelling", to, err)
	}
}

// --- kinds and expiry --------------------------------------------------------------------

// A note is a "note" or a "prompt"; a misspelt kind is refused rather than stored as
// something no reader looks for. It expires after NoteExpiryDays by default, never with
// --expires 0, and a negative expiry is refused.
func TestNoteKindAndExpiryRules(t *testing.T) {
	for _, k := range []string{"note", "prompt"} {
		if err := CheckNoteKind(k); err != nil {
			t.Errorf("%s refused: %v", k, err)
		}
	}
	if err := CheckNoteKind("promt"); err == nil {
		t.Error("a misspelt kind was accepted")
	}
	now := int64(1_000_000)
	if e, err := NoteExpiry(false, 0, now); err != nil || e != now+int64(NoteExpiryDays)*86400 {
		t.Errorf("no --expires gave %d %v", e, err)
	}
	if e, err := NoteExpiry(true, 0, now); err != nil || e != 0 {
		t.Errorf("--expires 0 is never, gave %d %v", e, err)
	}
	if e, err := NoteExpiry(true, 3, now); err != nil || e != now+3*86400 {
		t.Errorf("--expires 3 gave %d %v", e, err)
	}
	if _, err := NoteExpiry(true, -1, now); err == nil {
		t.Error("--expires -1 was accepted")
	}
}

func TestLocalNoteRefusesAnUnknownKind(t *testing.T) {
	s := newTestStore(t)
	knownProject(t, s, "otherproj")
	if err := s.Note(io.Discard, NoteArgs{To: "otherproj", Body: "x", Kind: "promt"}); err == nil {
		t.Error("the local note sent a misspelt kind")
	}
	if err := s.Note(io.Discard, NoteArgs{To: "otherproj", Body: "x", Kind: "prompt"}); err != nil {
		t.Errorf("a prompt was refused: %v", err)
	}
}

// --- naming the recipient ----------------------------------------------------------------
//
// People address a note by the name they use for a project, which is often the directory
// or the tool it builds rather than the name it registered under. A refusal must say what
// the right name is.

// The hint prefers an alias hit, then a near spelling, and otherwise lists the projects
// that exist; it never leaves the person with nothing.
func TestUnknownProjectHintOffersAnAliasANearSpellingOrTheList(t *testing.T) {
	aliases := []NameAlias{{Alias: "os-brain", Project: "acme",
		Why: "the directory this project lives in, and this project is registered as {project}"}}
	h := UnknownProjectHint("os-brain", []string{"acme", "widgets"}, []string{"acme", "widgets"}, aliases)
	for _, want := range []string{"'acme'", "name of the tool"} {
		if !strings.Contains(h, want) {
			t.Errorf("the hint does not say %q: %s", want, h)
		}
	}
	if strings.Contains(h, "Projects here") {
		t.Errorf("an alias hit was found, so the full list is noise: %s", h)
	}
	h = UnknownProjectHint("zzz", []string{"acme", "widgets"}, []string{"acme", "widgets"}, nil)
	if !strings.Contains(h, "Projects here: acme, widgets.") {
		t.Errorf("with nothing near, the known names were not listed: %s", h)
	}
	if h := UnknownProjectHint("widgts", []string{"widgets"}, []string{"widgets"}, nil); !strings.Contains(h, "Did you mean 'widgets'") {
		t.Errorf("a near miss was not suggested: %s", h)
	}
}

// A project's description can name what it builds; that name is an alias only as a whole
// word, so "brain" does not match inside "os-brain".
func TestDescriptionAliasesMatchWholeWords(t *testing.T) {
	about := map[string]string{"acme": "builds os-brain, the binary", "other": "a brain for cats"}
	got := DescriptionAliases("os-brain", about)
	if len(got) != 1 || got[0].Project != "acme" {
		t.Errorf("os-brain matched %+v", got)
	}
	if got := DescriptionAliases("brain", map[string]string{"acme": "builds os-brain"}); len(got) != 0 {
		t.Errorf("brain matched inside os-brain: %+v", got)
	}
}

// The directories between the project root and the working directory are aliases for the
// project, and they point at the registered name, not at themselves.
func TestDirAliasesNameTheDirectoryNotTheProject(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "brain")
	here := filepath.Join(repo, "brain")
	if err := os.MkdirAll(filepath.Join(repo, "os-brain"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := Env{Here: here, Project: "acme", Config: Config{Root: repo}}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(filepath.Join(repo, "os-brain")); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range e.DirAliases() {
		if a.Project != "acme" {
			t.Errorf("an alias points at %q", a.Project)
		}
		names = append(names, a.Alias)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "os-brain") || !strings.Contains(joined, "brain") {
		t.Errorf("aliases from inside os-brain/: %v", names)
	}
}

func TestLocalNoteRefusalNamesTheRegisteredProject(t *testing.T) {
	s := newTestStore(t)
	knownProject(t, s, "otherproj")
	err := s.Note(io.Discard, NoteArgs{To: "os-brain", Body: "x"})
	if err == nil {
		t.Fatal("a note to the tool's name was sent")
	}
	if !strings.Contains(err.Error(), "name of the tool") ||
		!strings.Contains(err.Error(), "otherproj") {
		t.Errorf("the refusal gives nothing to go on: %v", err)
	}
}
