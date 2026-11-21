package integration_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/bootstrap"
	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/lead"
	"github.com/chka/os-brain/internal/measure"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/project"
	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/spine"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testutil"
	"github.com/chka/os-brain/internal/timefmt"
)

// Tests for project identity and isolation on a shared store.
//
// Several projects can share one database. A project's memories are rows labelled with its
// name, so the name is what stands between one project and another's memories. A name is
// cheap to claim: a clone carries a committed brain.json, and an environment variable can
// say anything. So a name is bound to the directory that first used it, and every read,
// write, count and backup has to stay inside what the current project may see. Each test
// below is one way a project once reached, or could have reached, another's data.

// The index can hold more than one row for a name: a stale one from an old directory, or a
// duplicate. A row saying private=0 must not make a private project readable again, through
// --all-projects recall, the OTHER PROJECTS list or `global`.
func TestAStaleOrDuplicateIndexRowDoesNotUnprivateAProject(t *testing.T) {
	base := testutil.SharedLab(t)
	victim := testutil.MustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, memory.RememberArgs{Title: "zebracorp acquisition target confidential", Body: "x"})
	if err := project.New(victim).Private(io.Discard, project.PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	other := testutil.MustOpenAt(t, filepath.Join(base, "other"), `{"project":"other","shared":true}`)
	if err := other.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(base, "stale"), filepath.Join(base, "dup")} {
		if _, err := other.DB.Exec("INSERT INTO projects(root_id,name,private,last_seen) VALUES(?,?,0,?)",
			store.HashRoot(root), "victim", timefmt.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if store.Contains(other.ReadableProjects(), "victim") {
		t.Fatal("a private project is readable through a stale or duplicate index row")
	}
	got := recallJSON(t, other, recall.RecallArgs{Query: "zebracorp", AllProjects: true, Strict: true, NoTouch: true})
	if n := len(got["results"].([]interface{})); n != 0 {
		t.Errorf("--all-projects returned %d of a private project's memories", n)
	}
	for _, sb := range other.OtherProjects() {
		if sb.Name == "victim" {
			t.Error("a private project is listed under OTHER PROJECTS")
		}
	}
	var buf bytes.Buffer
	if err := spine.New(other).Global(&buf, spine.GlobalArgs{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "victim") {
		t.Errorf("global lists a private project:\n%s", buf.String())
	}
}

// A project of the same name with its own database opens, but the name in the shared index
// is bound elsewhere. private must fail loudly rather than report a declaration the index
// never received, must not write private into brain.json, and the project must not read
// that name's mail.
func TestPrivateFailsLoudlyWhenTheIndexIsNotWritten(t *testing.T) {
	base := testutil.SharedLab(t)
	owner := testutil.MustOpenAt(t, filepath.Join(base, "owner"), `{"project":"app","shared":true}`)
	if err := owner.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	// A project of the same name with its own database: it opens, but the name is not its.
	other := testutil.MustOpenAt(t, filepath.Join(base, "other"), `{"project":"app"}`)
	err := project.New(other).Private(io.Discard, project.PrivateArgs{})
	if err == nil || !strings.Contains(err.Error(), "index was not updated") {
		t.Fatalf("private reported success over an index it did not write: %v", err)
	}
	cfg, _ := store.LoadConfig(filepath.Join(base, "other", "brain", "brain.json"))
	if cfg.Private {
		t.Error("brain.json says private although the index was never told")
	}
	// And the name-addressed mail is not that project's either.
	if err := other.OwnsName(other.GlobalConnect(false)); err == nil {
		t.Error("a project whose name is bound elsewhere reads that name's mail")
	}
}

// A memory id is a guessable number. get by id, the filtered-out count in recall's JSON and
// the use counter must all stay inside what this project may read: a private project's
// memory answers as missing, its matches are not even counted, and searching another
// project on purpose must not change that project's ranking.
func TestAPrivateProjectsMemoryIsNotReachableById(t *testing.T) {
	base := testutil.SharedLab(t)
	victim := testutil.MustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, memory.RememberArgs{Title: "victim private payroll zebracorn figure", Body: "250k"})
	var id int64
	if err := victim.DB.QueryRow("SELECT id FROM memories WHERE project='victim'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := project.New(victim).Private(io.Discard, project.PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	alpha := testutil.MustOpenAt(t, filepath.Join(base, "alpha"), `{"project":"alpha","shared":true}`)
	if err := alpha.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	for _, all := range []bool{false, true} {
		var buf bytes.Buffer
		err := memory.New(alpha).Get(&buf, id, all, true)
		if err == nil || err.Error() != store.NoMemory(id).Error() {
			t.Errorf("get #%d (all-projects=%v) of a private project: %v %s", id, all, err, buf.String())
		}
	}
	var buf bytes.Buffer
	if err := recall.New(alpha).Recall(&buf, recall.RecallArgs{Query: "zebracorn", Type: "decision", JSON: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "filtered_out") {
		t.Errorf("the filtered-out count reached a private project: %s", buf.String())
	}

	// A readable project, searched on purpose, is still not this project's to rerank.
	if err := project.New(victim).Private(io.Discard, project.PrivateArgs{Off: true, Yes: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		recallJSON(t, alpha, recall.RecallArgs{Query: "zebracorn", AllProjects: true})
	}
	var used int
	if err := alpha.DB.QueryRow("SELECT use_count FROM memories WHERE id=?", id).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 0 {
		t.Errorf("another project's recall bumped this memory's use count to %d", used)
	}
	if err := memory.New(alpha).Get(io.Discard, id, true, false); err != nil {
		t.Errorf("a readable project's memory, asked for across projects: %v", err)
	}
}

// The MCP server lets a caller name the project it wants. Naming a private project is
// refused, and the roster reports it as private. Naming a new project still works.
func TestNamingAPrivateProjectToTheServerIsRefused(t *testing.T) {
	base := testutil.SharedLab(t)
	victim := testutil.MustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, memory.RememberArgs{Title: "zzzsecret holds nothing", Body: "x"})
	if err := project.New(victim).Private(io.Discard, project.PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	srv := testutil.MustOpenAt(t, filepath.Join(base, "srv"), `{"project":"srv","shared":true}`)
	if !srv.PrivateProject("victim") {
		t.Error("a private project is not reported private to the roster")
	}
	env := store.NewEnvFor(filepath.Join(base, "srv", "brain"), "victim")
	if !env.Named {
		t.Fatal("an explicitly named project is not marked Named")
	}
	if s, err := store.Open(env); err == nil {
		s.Close()
		t.Fatal("a named private project opened through the server")
	}
	if s, err := store.Open(store.NewEnvFor(filepath.Join(base, "srv", "brain"), "brandnew")); err != nil {
		t.Errorf("naming a new project to the server was refused: %v", err)
	} else {
		s.Close()
	}
}

// Leads and measures are addressed by id too. Another project's id must answer as missing,
// for reads and for writes, and must not be changed.
func TestLeadsAndMeasuresByIdStayInTheirProject(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("INSERT INTO leads(project,name,email,status,created_at,updated_at) "+
		"VALUES('alpha','Jane Private','jane@acme.example','new',?,?)", timefmt.Now(), timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := lead.New(s).Lead(&buf, lead.LeadArgs{Sub: "show", ID: 1}); err == nil || strings.Contains(buf.String(), "Jane") {
		t.Errorf("lead show read another project's lead: %v %s", err, buf.String())
	}
	st := "lost"
	if err := lead.New(s).Lead(io.Discard, lead.LeadArgs{Sub: "update", ID: 1, Status: &st}); err == nil {
		t.Error("lead update wrote another project's lead")
	}
	var status string
	_ = s.DB.QueryRow("SELECT status FROM leads WHERE id=1").Scan(&status)
	if status != "new" {
		t.Errorf("another project's lead was changed to %q", status)
	}
	if _, err := s.DB.Exec("INSERT INTO measures(project,key,value,unit,taken_on,taken_at,created_at) "+
		"VALUES('alpha','weight',80,'kg','2026-11-01',?,?)", timefmt.Now(), timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"forget", "relabel"} {
		err := measure.New(s).Measure(io.Discard, measure.MeasureArgs{Sub: sub, ID: 1, Unit: "lb"})
		if err == nil || err.Error() != "no measure #1" {
			t.Errorf("measure %s of another project's id: %v", sub, err)
		}
	}
}

// standard add --from copies a memory into the machine-wide standards every project reads.
// It may lift only this project's own memory, and not while the project is private.
func TestStandardFromLiftsOnlyThisProjectsMemory(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source,"+
		"importance,pinned,confidence,source_date,happened_on,hash,created_at,updated_at) "+
		"VALUES('alpha','global','note','alpha secret diagnosis','condition X','session',3,0,"+
		"'reported','','',?,?,?)", "h1", timefmt.Now(), timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	if err := spine.New(s).Standard(io.Discard, spine.StandardArgs{Confirm: yes, Sub: "add", FromID: 1}); err == nil {
		t.Fatal("standard --from lifted another project's memory into the spine")
	}
	remember(t, s, memory.RememberArgs{Title: "testproj convention about tabs", Body: "tabs"})
	id := lastID(t, s)
	s.Env.Private = true
	if err := spine.New(s).Standard(io.Discard, spine.StandardArgs{Confirm: yes, Sub: "add", FromID: id}); err == nil {
		t.Error("standard --from lifted a private project's memory into the spine")
	}
	s.Env.Private = false
	if err := spine.New(s).Standard(io.Discard, spine.StandardArgs{Confirm: yes, Sub: "add", FromID: id}); err != nil {
		t.Errorf("lifting this project's own memory: %v", err)
	}
}

// relabel renames this project's rows and recomputes their hashes. It must not touch
// another project's rows, and a duplicate check on remember must not echo another
// project's body back.
func TestRelabelRehashesOnlyThisProject(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "mine under an old name entirely", Body: "x"})
	if _, err := s.DB.Exec("UPDATE memories SET project='oldlabel'"); err != nil {
		t.Fatal(err)
	}
	victim := testutil.Clone(s)
	victim.Env.Project = "victim"
	remember(t, victim, memory.RememberArgs{Title: "victim prod database password rotation procedure", Body: "SECRET-VICTIM-BODY"})
	var before string
	if err := s.DB.QueryRow("SELECT hash FROM memories WHERE project='victim'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := project.New(s).Relabel(io.Discard, project.RelabelArgs{Old: "oldlabel", Apply: true, IMeanIt: true}); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := s.DB.QueryRow("SELECT hash FROM memories WHERE project='victim'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("relabel rehashed another project's memory")
	}
	var buf bytes.Buffer
	_ = memory.New(s).Remember(&buf, memory.RememberArgs{Scope: "global", Type: "note", Importance: 3, Source: "session",
		Title: "victim prod database password rotation procedure", Body: "mine"})
	if strings.Contains(buf.String(), "SECRET-VICTIM-BODY") {
		t.Error("remember echoed another project's body")
	}
}

// A backup lands in the project directory, where it may be committed or shared. On a shared
// store it holds this project's rows only: no other project's memories or notes, and no
// machine-wide tables. A whole-store backup needs a person to confirm it.
func TestABackupOfASharedStoreHoldsOnlyThisProject(t *testing.T) {
	base := testutil.SharedLab(t)
	victim := testutil.MustOpenAt(t, filepath.Join(base, "victim"), `{"project":"victim","shared":true}`)
	remember(t, victim, memory.RememberArgs{Title: "victim memory about zebracorn deals", Body: "x"})
	if err := project.New(victim).Private(io.Discard, project.PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	other := testutil.MustOpenAt(t, filepath.Join(base, "other"), `{"project":"other","shared":true}`)
	if err := other.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	if _, err := other.DB.Exec("INSERT INTO notes(to_project,from_project,body,created_at) "+
		"VALUES('victim','other','PRIVATE-NOTE-FROM-OTHER-TO-VICTIM',?)", timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	third := testutil.MustOpenAt(t, filepath.Join(base, "third"), `{"project":"third","shared":true}`)
	remember(t, third, memory.RememberArgs{Title: "third keeps one fact of its own", Body: "y"})
	out := filepath.Join(base, "third", "bk.json")
	if err := dump.New(third).Backup(io.Discard, dump.BackupArgs{Out: out, AllowTracked: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"PRIVATE-NOTE", "zebracorn", `"victim"`, "lesson_fts"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("the backup carries %s", leak)
		}
	}
	if !strings.Contains(string(data), "third keeps one fact") {
		t.Error("the backup lost this project's own memory")
	}
	if err := dump.New(third).Backup(io.Discard, dump.BackupArgs{Out: filepath.Join(base, "third", "w.json"),
		AllowTracked: true, WholeStore: true}); err == nil {
		t.Error("a whole-store backup ran with no person confirming it")
	}
}

// bootstrap --done marks this project as bootstrapped, not every project in the store.
func TestBootstrapDoneIsPerProject(t *testing.T) {
	base := testutil.SharedLab(t)
	a := testutil.MustOpenAt(t, filepath.Join(base, "a"), `{"project":"a","shared":true}`)
	b := testutil.MustOpenAt(t, filepath.Join(base, "b"), `{"project":"b","shared":true}`)
	if err := bootstrap.New(a).Bootstrap(io.Discard, bootstrap.BootstrapArgs{Done: true}); err != nil {
		t.Fatal(err)
	}
	if bootstrap.New(b).BootstrappedAt() != 0 {
		t.Error("one project's --done marked another bootstrapped")
	}
	if bootstrap.New(a).BootstrappedAt() == 0 {
		t.Error("--done did not mark its own project")
	}
}

// prune refuses to delete half a brain. That guard has to count the rows this prune can
// reach, this project's alone, or a large neighbour hides a small project being emptied.
// An --all-projects prune backs up every row it deletes and tombstones each under its own
// project.
func TestPruneCountsAndBacksUpWhatItReaches(t *testing.T) {
	base := testutil.SharedLab(t)
	fill := func(s *store.Store, prefix, typ string, n int) {
		for i := 0; i < n; i++ {
			remember(t, s, memory.RememberArgs{Title: prefix + " " + string(rune('a'+i)), Type: typ,
				Body: "x", AllowDuplicate: true})
		}
	}
	big := testutil.MustOpenAt(t, filepath.Join(base, "big"), `{"project":"big","shared":true}`)
	if err := big.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	fill(big, "big durable fact", "infra", 30)
	fill(big, "big decayed status", "state", 2)
	small := testutil.MustOpenAt(t, filepath.Join(base, "small"), `{"project":"small","shared":true}`)
	fill(small, "small decayed status", "state", 6)
	fill(small, "small durable fact", "infra", 5)
	if _, err := small.DB.Exec("UPDATE memories SET updated_at=? WHERE mem_type='state'",
		timefmt.Now()-90*86400); err != nil {
		t.Fatal(err)
	}
	err := dump.New(small).Prune(io.Discard, dump.PruneArgs{Tier: "D", OlderThan: 30, Apply: true, MaxDelete: 25,
		AllowTracked: true})
	if err == nil || !strings.Contains(err.Error(), "refusing to delete 6 of 11") {
		t.Fatalf("the half-brain guard did not count this project alone: %v", err)
	}
	bk := filepath.Join(base, "small", "prune.json")
	if err := dump.New(small).Prune(io.Discard, dump.PruneArgs{Tier: "D", OlderThan: 30, Apply: true, Yes: true,
		AllProjects: true, IMeanIt: true, MaxDelete: 25, Backup: bk, AllowTracked: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bk)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "big decayed status") {
		t.Error("the backup left out the other project's rows the prune deleted")
	}
	var c int
	if err := small.DB.QueryRow("SELECT COUNT(*) FROM tombstones WHERE project='big' AND " +
		"reason='pruned'").Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 2 {
		t.Errorf("the other project's pruned rows have %d tombstones under their own name, want 2", c)
	}
}
