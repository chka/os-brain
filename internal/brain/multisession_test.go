package brain

// Tests for several sessions sharing one store: concurrent writes, supersede forks, session
// attribution on every row, conflict flags in recall, per-session cursors, and the schema
// migration that added the session columns. Also here: measure relabel, same-title
// supersession and init's refusal to split an install, which arrived with the same change.

import (
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// buildBinary compiles the real CLI. The in-process concurrency test cannot reproduce the
// bug the cross-process test guards: one Store holds one *sql.DB with SetMaxOpenConns(1),
// so goroutines serialise inside the pool and never contend at the SQLite level. Two agent
// sessions are two processes, and only two processes take two connections to one file.
func buildBinary(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "os-brain-test")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/os-brain")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the CLI for a cross-process test: %v\n%s", err, out)
	}
	return bin
}

// distinctTitle maps a word to a title sharing no tokens with any other. The near-duplicate
// guard refuses a title overlapping an existing one by half or more, so a concurrency test
// built on "<word> plus the same five words" would measure the guard, not locking.
func distinctTitle(w string) string {
	m := map[string]string{
		"alpha":   "nginx terminates TLS on the edge box",
		"bravo":   "redis eviction policy is allkeys-lru",
		"charlie": "postgres autovacuum tuned for the events table",
		"delta":   "certbot renews from the webroot challenge",
		"echo":    "systemd restarts the collector on failure",
		"foxtrot": "grafana dashboards live in provisioning",
		"golf":    "kafka retention set to seven days",
		"hotel":   "vault unseals with three shamir shares",
		"india":   "haproxy health checks hit /livez",
		"juliet":  "borgmatic prunes monthly archives",
		"kilo":    "wireguard peers rotate keys quarterly",
		"lima":    "clickhouse partitions by ingest month",
	}
	return m[w]
}

// sqlOpen opens a database file directly, bypassing Store, with a busy timeout.
func sqlOpen(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
}

// supersededBy reads memory id's superseded_by link; nil means it is live.
func supersededBy(t *testing.T, st *Store, id int64) *int64 {
	t.Helper()
	var by *int64
	if err := st.DB.QueryRow("SELECT superseded_by FROM memories WHERE id=?", id).Scan(&by); err != nil {
		t.Fatal(err)
	}
	return by
}

// --- concurrent writes ---

// The write path reads before it writes, so a deferred BEGIN has to promote its read lock to
// a write lock, and SQLite refuses that with SQLITE_BUSY_SNAPSHOT at once, ignoring
// busy_timeout. Measured before transactions were opened with _txlock=immediate: 12 of 20
// concurrent writes lost, each in about 31ms.
func TestConcurrentWritesAreNotLost(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()

	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf",
		"hotel", "india", "juliet", "kilo", "lima"}
	var wg sync.WaitGroup
	errs := make([]error, len(words))
	for i, w := range words {
		wg.Add(1)
		go func(i int, w string) {
			defer wg.Done()
			errs[i] = st.Remember(io.Discard, RememberArgs{
				Scope: "global", Type: "note",
				Title: distinctTitle(w),
				Body:  "body " + w,
			})
		}(i, w)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil && strings.Contains(err.Error(), "locked") {
			t.Fatalf("write %d lost to a lock: %v", i, err)
		}
	}
	var n int
	if err := st.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(words) {
		t.Fatalf("stored %d of %d concurrent writes", n, len(words))
	}
}

// The same as above with separate processes, which is the real case: two agent sessions are
// not two goroutines (see buildBinary).
func TestConcurrentWritesAcrossProcesses(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"conc","shared":false,"global":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot",
		"golf", "hotel", "india", "juliet"}
	var wg sync.WaitGroup
	out := make([]string, len(words))
	for i, w := range words {
		wg.Add(1)
		go func(i int, w string) {
			defer wg.Done()
			cmd := exec.Command(bin, "remember", "--type", "note",
				"--title", distinctTitle(w), "--body", "b")
			cmd.Env = append(os.Environ(), "OS_BRAIN_HOME="+home,
				"CLAUDE_CONFIG_DIR="+dir, "OS_BRAIN_GLOBAL_DB="+filepath.Join(dir, "g.db"))
			b, _ := cmd.CombinedOutput()
			out[i] = string(b)
		}(i, w)
	}
	wg.Wait()
	for i, o := range out {
		if strings.Contains(strings.ToLower(o), "locked") || strings.Contains(o, "BUSY") {
			t.Fatalf("process %d lost its write: %s", i, o)
		}
	}
}

// --- supersession across sessions ---

// Two sessions correcting the same memory used to be last-writer-wins on one column: the
// first correction stayed live but superseded nothing, competing at equal weight with the
// error it retracted. That breaks the guarantee the whole design leans on, that a retracted
// fact is answered by its correction. The second supersession is refused and told where the
// first one went.
func TestSupersedeRefusesAFork(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "Postgres for the job queue", Body: "orig"})

	t.Setenv("OS_BRAIN_SESSION_ID", "sessionAAA")
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "queue moved to Redis streams instead", Body: "A", Supersedes: 1})

	t.Setenv("OS_BRAIN_SESSION_ID", "sessionBBB")
	err := st.Remember(io.Discard, RememberArgs{Scope: "global", Type: "decision",
		Title: "queue stays in-process with no broker", Body: "B", Supersedes: 1})
	if err == nil {
		t.Fatal("a second supersession of #1 was accepted silently")
	}
	for _, want := range []string{"already been superseded", "another session",
		"--fork-ok", "--supersedes 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not mention %q:\n%s", want, err)
		}
	}
	// The first correction must still own the link.
	if by := supersededBy(t, st, 1); by == nil || *by != 2 {
		t.Fatalf("session A's supersession was clobbered: superseded_by=%v", by)
	}
}

// --fork-ok is the deliberate way to supersede an already-superseded memory a second time.
// It is accepted, and the output says a fork was taken rather than taking it silently.
func TestForkOKTakesTheBranchDeliberately(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "Postgres for the job queue", Body: "orig"})
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "queue moved to Redis streams instead", Body: "A", Supersedes: 1})
	var buf strings.Builder
	if err := st.Remember(&buf, RememberArgs{Scope: "global", Type: "decision",
		Title: "queue stays in-process with no broker", Body: "B",
		Supersedes: 1, ForkOK: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "fork: #2 also superseded it") {
		t.Fatalf("a deliberate fork was taken silently: %s", buf.String())
	}
}

// Undoing a supersession once had no route except raw SQL on the memories table, which the
// search index design forbids. `update --unsupersede` clears the link, keeps both
// memories, and refuses on a memory that has no link to clear.
func TestUnsupersedeClearsTheLinkAndKeepsBothMemories(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "Postgres for the job queue", Body: "orig"})
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "queue moved to Redis streams instead", Body: "A", Supersedes: 1})
	if err := st.Update(io.Discard, UpdateArgs{ID: 1, Unsupersede: true}); err != nil {
		t.Fatal(err)
	}
	if by := supersededBy(t, st, 1); by != nil {
		t.Fatalf("link not cleared: %v", by)
	}
	var n int
	_ = st.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n)
	if n != 2 {
		t.Fatalf("unsupersede destroyed a memory: %d left", n)
	}
	if err := st.Update(io.Discard, UpdateArgs{ID: 1, Unsupersede: true}); err == nil {
		t.Fatal("unsuperseding an unsuperseded memory should refuse")
	}
}

// Same title, new truth is the ordinary way a fact changes. It used to be refused by the
// identity guard, whose advice was to pass --supersedes, which the caller had done. The
// successor takes the title's identity hash and the retracted row keeps one of its own.
func TestSupersedingTheMemoryThatHoldsTheTitleIsAllowed(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "the queue runs on Postgres", Body: "original"})
	var buf strings.Builder
	if err := st.Remember(&buf, RememberArgs{Scope: "global", Type: "decision",
		Title: "the queue runs on Postgres", Body: "CORRECTED: it moved to Redis",
		Supersedes: 1}); err != nil {
		t.Fatalf("same-title supersession refused: %v", err)
	}
	if by := supersededBy(t, st, 1); by == nil || *by != 2 {
		t.Fatalf("the retraction was not linked: %v", by)
	}
	var hashes int
	_ = st.DB.QueryRow("SELECT COUNT(DISTINCT hash) FROM memories").Scan(&hashes)
	if hashes != 2 {
		t.Fatalf("want two distinct hashes, got %d", hashes)
	}
	// Without --supersedes the guard must still protect the body.
	if err := st.Remember(io.Discard, RememberArgs{Scope: "global", Type: "decision",
		Title: "the queue runs on Postgres", Body: "a third body"}); err == nil {
		t.Fatal("the identity guard stopped protecting the existing body")
	}
}

// --- session attribution ---

// Every memory is stamped with the session that wrote it (OS_BRAIN_SESSION_ID), which is
// what the concurrent-write alert and the conflict flags read.
func TestMemoriesRecordTheirSession(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	t.Setenv("OS_BRAIN_SESSION_ID", "sessionAAA")
	remember(t, st, RememberArgs{Scope: "global", Type: "note",
		Title: "written by the first session", Body: "x"})
	var got string
	if err := st.DB.QueryRow("SELECT session FROM memories WHERE id=1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "sessionAAA" {
		t.Fatalf("session not stamped: %q", got)
	}
}

// Attribution has to cover the whole write surface, not only `remember`. The first version
// of the session column was wired into one INSERT of five, so events carried nothing and an
// import silently relabelled every memory as unattributed.
func TestEveryWritePathCarriesTheSession(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	t.Setenv("OS_BRAIN_SESSION_ID", "sess-ORIGINAL")
	remember(t, st, RememberArgs{Scope: "global", Type: "infra",
		Title: "nginx terminates TLS on the edge box", Body: "x"})
	if err := st.Log(io.Discard, LogArgs{Kind: "note", Summary: "an event with attribution",
		Scope: "global", Actor: "claude"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT session FROM memories WHERE session='sess-ORIGINAL'",
		"SELECT session FROM events WHERE session='sess-ORIGINAL'",
	} {
		var got string
		if err := st.DB.QueryRow(q).Scan(&got); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// Attribution that cannot be read is attribution nobody has. The events table carried the
// session before this test existed and no read path selected it, so `events` and the brief
// both reported unattributed rows from a store that knew who wrote them.
func TestEventsExposeTheirSession(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	t.Setenv("OS_BRAIN_SESSION_ID", "sess-READBACK")
	if err := st.Log(io.Discard, LogArgs{Kind: "note", Summary: "readable attribution",
		Scope: "global", Actor: "claude"}); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := st.Events(&buf, EventsArgs{N: 5, JSON: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "sess-READBACK") {
		t.Fatalf("events output does not carry the session:\n%s", buf.String())
	}
}

// Two blank sessions mean "we do not know", and calling that agreement would be the
// widest-possible answer, which this codebase refuses to give on ambiguity.
func TestSameSessionTreatsUnknownAsUnknown(t *testing.T) {
	if SameSession("", "") {
		t.Fatal("two unrecorded sessions reported as the same session")
	}
	if !SameSession("a", "a") {
		t.Fatal("identical sessions not recognised")
	}
	if SameSession("a", "b") {
		t.Fatal("different sessions reported as the same")
	}
}

// --- conflict flags in recall ---

// Two live decisions in one scope that score level with each other and came from different
// sessions are two answers to one question. Recall flags both, so the reader knows to ask
// which stands instead of taking whichever happens to sort first.
func TestConflictFlagsLevelAnswersFromDifferentSessions(t *testing.T) {
	rows := []*Memory{
		{ID: 1, Scope: "global", MemType: "decision", Score: 0.483, Session: "sessionAAA"},
		{ID: 2, Scope: "global", MemType: "decision", Score: 0.483, Session: "sessionBBB"},
	}
	MarkConflicts(rows)
	for _, r := range rows {
		if r.Conflict == nil {
			t.Fatalf("#%d not flagged", r.ID)
		}
		if !strings.Contains(r.Conflict.Note, "different session") {
			t.Fatalf("#%d wrong note: %s", r.ID, r.Conflict.Note)
		}
	}
}

// A flag that fires on everything is ignored. Different types, different scopes, a pair
// already resolved by supersession, or scores far apart are not conflicts.
func TestConflictDoesNotFireAcrossTypesScopesOrSupersessions(t *testing.T) {
	sup := int64(9)
	cases := []struct {
		name string
		rows []*Memory
	}{
		{"different types", []*Memory{
			{ID: 1, Scope: "global", MemType: "decision", Score: 0.5, Session: "a"},
			{ID: 2, Scope: "global", MemType: "infra", Score: 0.5, Session: "b"}}},
		{"different scopes", []*Memory{
			{ID: 1, Scope: "channel:a", MemType: "decision", Score: 0.5, Session: "a"},
			{ID: 2, Scope: "channel:b", MemType: "decision", Score: 0.5, Session: "b"}}},
		{"already superseded", []*Memory{
			{ID: 1, Scope: "global", MemType: "decision", Score: 0.5, Session: "a", SupersededBy: &sup},
			{ID: 2, Scope: "global", MemType: "decision", Score: 0.5, Session: "b"}}},
		{"scores far apart", []*Memory{
			{ID: 1, Scope: "global", MemType: "decision", Score: 0.9, Session: "a"},
			{ID: 2, Scope: "global", MemType: "decision", Score: 0.2, Session: "b"}}},
	}
	for _, c := range cases {
		MarkConflicts(c.rows)
		for _, r := range c.rows {
			if r.Conflict != nil {
				t.Fatalf("%s: #%d flagged when it should not be (%s)", c.name, r.ID, r.Conflict.Note)
			}
		}
	}
}

// --- per-session cursors ---

// The brief shows what changed since the last brief, and the preference nudge is spent once
// per window. Two sessions sharing one cursor would each consume the other's window.
func TestCursorsAreNamespacedPerSession(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	t.Setenv("OS_BRAIN_SESSION_ID", "sessionAAA")
	if err := st.setSessionCursor("last_brief_at", 1000); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_SESSION_ID", "sessionBBB")
	if got := st.sessionCursor("last_brief_at"); got != 1000 {
		t.Fatalf("a new session should fall back to the shared cursor, got %d", got)
	}
	if err := st.setSessionCursor("last_brief_at", 2000); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_SESSION_ID", "sessionAAA")
	if got := st.sessionCursor("last_brief_at"); got != 1000 {
		t.Fatalf("session A's cursor was clobbered by session B: %d", got)
	}
}

// The shared store holds every project on the machine in ONE meta table, and a new session's
// fallback was the bare key: "last briefed" came from whichever project anybody opened last,
// so a session starting in one project marked another project's news as seen. The fallback
// is now per project, and the bare key an older version wrote is still read.
func TestANewSessionsCursorComesFromItsOwnProject(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	mine := st.Env.Project
	t.Setenv("OS_BRAIN_SESSION_ID", "sessionAAA")
	if err := st.setSessionCursor("last_brief_at", 1000); err != nil {
		t.Fatal(err)
	}
	st.Env.Project = "someoneelse"
	t.Setenv("OS_BRAIN_SESSION_ID", "sessionBBB")
	if err := st.setSessionCursor("last_brief_at", 9000); err != nil {
		t.Fatal(err)
	}
	st.Env.Project = mine
	t.Setenv("OS_BRAIN_SESSION_ID", "sessionCCC")
	if got := st.sessionCursor("last_brief_at"); got != 1000 {
		t.Fatalf("a new session here read another project's marker: %d", got)
	}
	if _, err := st.DB.Exec("DELETE FROM meta WHERE k LIKE 'last_brief_at%'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec("INSERT INTO meta(k,v) VALUES('last_brief_at','500')"); err != nil {
		t.Fatal(err)
	}
	if got := st.sessionCursor("last_brief_at"); got != 500 {
		t.Fatalf("the marker an older version wrote must still be read: %d", got)
	}
}

// --- the migration that added sessions ---

// Schema v13 adds the session columns. Migration bugs have more than once been invisible
// against a fresh database, so this winds a real store with real rows back to v12 first.
func TestMigrateV12ToV13AddsSessionWithoutInventingOne(t *testing.T) {
	st := newTestStore(t)
	remember(t, st, RememberArgs{Scope: "global", Type: "note",
		Title: "written before sessions were tracked", Body: "x"})
	path := st.Env.DBPath
	st.Close()

	// Wind it back to v12 exactly: drop the columns v13 adds and reset the version.
	db, err := sqlOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		// v14's index names the column v13 added, and a real v12 database never had it.
		"DROP INDEX IF EXISTS idx_mem_session",
		"ALTER TABLE memories DROP COLUMN session",
		"ALTER TABLE events DROP COLUMN session",
		"UPDATE meta SET v='12' WHERE k='schema_version'",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	db.Close()

	st2, err := Open(NewEnv(filepath.Dir(path)))
	if err != nil {
		t.Fatalf("reopening a v12 database: %v", err)
	}
	defer st2.Close()

	var ver string
	if err := st2.DB.QueryRow("SELECT v FROM meta WHERE k='schema_version'").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	// Current, not 13: a wound-back database migrates all the way forward, and pinning the
	// number here would make every later schema bump fail a test about v13.
	if ver != strconv.Itoa(SchemaVersion) {
		t.Fatalf("schema_version = %s, want %d", ver, SchemaVersion)
	}
	// Never backfilled: an empty session means "written before the brain tracked sessions",
	// which is true. Stamping today's id would assert who wrote it.
	var sess string
	if err := st2.DB.QueryRow("SELECT session FROM memories WHERE id=1").Scan(&sess); err != nil {
		t.Fatal(err)
	}
	if sess != "" {
		t.Fatalf("a pre-v13 memory was backfilled with session %q", sess)
	}
	var n int
	_ = st2.DB.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n)
	if n != 1 {
		t.Fatalf("migration lost a memory: %d left", n)
	}
}

// --- measures ---

// doctor reported "measure series mix units" and offered no command that could fix it, so a
// session corrected the unit with raw SQL. That write happened to be safe (measures sit
// outside the search index), but a tool that names a fault and offers no route teaches
// people to reach past it. relabel changes the unit and never converts the value, because
// only the caller knows whether the two units measured the same thing.
func TestMeasureRelabelFixesAUnitMixWithoutTouchingTheValue(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	for _, m := range []struct {
		v    float64
		unit string
		on   string
	}{{3.8, "kg", "2026-07-27"}, {2.3, "L", "2026-07-31"}} {
		if err := st.Measure(io.Discard, MeasureArgs{Sub: "add", Key: "idwg",
			Value: m.v, Unit: m.unit, Date: m.on, Scope: "global"}); err != nil {
			t.Fatal(err)
		}
	}
	err := st.Measure(io.Discard, MeasureArgs{Sub: "relabel", ID: 2})
	if err == nil || !strings.Contains(err.Error(), "needs --unit") {
		t.Fatalf("relabel without a unit should refuse and show the reading, got %v", err)
	}
	if err := st.Measure(io.Discard, MeasureArgs{Sub: "relabel", ID: 2, Unit: "kg"}); err != nil {
		t.Fatal(err)
	}
	var unit string
	var value float64
	if err := st.DB.QueryRow("SELECT unit, value FROM measures WHERE id=2").Scan(&unit, &value); err != nil {
		t.Fatal(err)
	}
	if unit != "kg" {
		t.Fatalf("unit not relabelled: %q", unit)
	}
	if value != 2.3 {
		t.Fatalf("relabel changed the value to %v; it must never convert", value)
	}
	if err := st.Measure(io.Discard, MeasureArgs{Sub: "relabel", ID: 2, Unit: "kg"}); err == nil {
		t.Fatal("relabelling to the unit it already has should say so")
	}
	if err := st.Measure(io.Discard, MeasureArgs{Sub: "relabel", ID: 999, Unit: "kg"}); err == nil {
		t.Fatal("relabelling a missing measure should refuse")
	}
}

// --- init ---

// A stray OS_BRAIN_DB split the install: config in <dir>/brain.json, the database wherever
// the variable pointed, and the success line reported a path with no database at it.
func TestInitRefusesToSplitTheInstall(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OS_BRAIN_DB", filepath.Join(dir, "elsewhere", "brain.db"))
	err := Init(io.Discard, dir, InitArgs{Dir: "brain", NoHook: true})
	if err == nil {
		t.Fatal("init split the database from its config without complaint")
	}
	if !strings.Contains(err.Error(), "OS_BRAIN_DB") {
		t.Fatalf("refusal does not name the cause: %v", err)
	}
}
