package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/install"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/overview"
	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testutil"
	"github.com/chka/os-brain/internal/timefmt"
)

// Tests for the commands that move a brain's contents through time: backup, export and
// import, history and restore, update archiving, prune, purge and snapshot, plus the init
// path that replaces the hooks an older client installed.

// mustJSON runs a command that writes JSON and returns the decoded object, failing the test
// if the command errors or prints anything that is not a JSON object.
func mustJSON(t *testing.T, fn func(io.Writer) error) map[string]interface{} {
	t.Helper()
	return testutil.MustJSON(t, fn)
}

// firstHistoryID returns the id of the newest archived version in the history output.
func firstHistoryID(t *testing.T, s *store.Store, args memory.HistoryArgs) int64 {
	t.Helper()
	args.JSON = true
	hist := mustJSON(t, func(w io.Writer) error { return memory.New(s).History(w, args) })
	rows := hist["history"].([]interface{})
	if len(rows) == 0 {
		t.Fatal("history is empty")
	}
	return int64(rows[0].(map[string]interface{})["id"].(float64))
}

// --- backup, export and import ---

// backup is the command people are told to run before anything destructive. It once wrote a
// dump that nothing could read back in, so the round trip is tested end to end: back up,
// import into an empty store, and find the memories again.
func TestABackupCanBeReadBackIn(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Body: "tls terminates at nginx",
		Type: "infra", Pin: true})
	remember(t, s, memory.RememberArgs{Title: "φώσφορος στόχος", Body: "από τα λαμπ", Type: "gotcha",
		Scope: "channel:diet"})
	var buf bytes.Buffer
	if err := memory.New(s).Log(&buf, memory.LogArgs{Scope: "global", Kind: "note", Actor: "claude",
		Summary: "did a thing"}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(s.Env.Here, "dump.json")
	if err := dump.New(s).Backup(io.Discard, dump.BackupArgs{Out: out, JSON: true}); err != nil {
		t.Fatal(err)
	}

	fresh := newTestStore(t)
	res := mustJSON(t, func(w io.Writer) error {
		return dump.New(fresh).Import(w, dump.ImportArgs{File: out, Events: true, JSON: true})
	})
	if res["added"].(float64) != 2 {
		t.Errorf("restored %v memories, wanted 2", res["added"])
	}
	if res["events"].(float64) != 1 {
		t.Errorf("restored %v events, wanted 1", res["events"])
	}
	// A restore is only worth something if the restored memories can be found.
	if got := titles(recallJSON(t, fresh, recall.RecallArgs{Query: "nginx"})); len(got) != 1 {
		t.Errorf("restored memory is not recallable: %v", got)
	}
	if got := titles(recallJSON(t, fresh, recall.RecallArgs{Query: "φωσφορος", Scope: "channel:diet"})); len(got) != 1 {
		t.Errorf("accent folding lost across the round trip: %v", got)
	}
}

// Memory ids are autoincrement values, so they change on import. If the importer dropped the
// supersede links instead of remapping them, every fact that was deliberately retracted would
// come back into recall as current.
func TestImportRemapsSupersessionRatherThanResurrectingRetractedFacts(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "old price is 20", Type: "decision"})
	remember(t, s, memory.RememberArgs{Title: "price is 12", Type: "decision", Supersedes: 1})
	out := filepath.Join(s.Env.Here, "dump.json")
	if err := dump.New(s).Export(io.Discard, dump.ExportArgs{Out: out, JSON: true}); err != nil {
		t.Fatal(err)
	}

	fresh := newTestStore(t)
	res := mustJSON(t, func(w io.Writer) error {
		return dump.New(fresh).Import(w, dump.ImportArgs{File: out, JSON: true})
	})
	if res["relinked"].(float64) != 1 {
		t.Fatalf("supersession not restored: %v", res)
	}
	if res["dangling"].(float64) != 0 {
		t.Errorf("reported a dangling link: %v", res)
	}
	got := titles(recallJSON(t, fresh, recall.RecallArgs{Query: "old price"}))
	for _, ti := range got {
		if ti == "old price is 20" {
			t.Errorf("a retracted fact came back as current: %v", got)
		}
	}
}

// A dump from a newer version may carry fields this one does not understand. Importing it
// anyway would drop them in silence, so the import refuses and names the version.
func TestImportRefusesADumpFromANewerSchema(t *testing.T) {
	s := newTestStore(t)
	out := filepath.Join(s.Env.Here, "future.json")
	if err := os.WriteFile(out, []byte(`{"schema_version": 999, "memories": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := dump.New(s).Import(io.Discard, dump.ImportArgs{File: out})
	if err == nil || !strings.Contains(err.Error(), "999") {
		t.Errorf("expected a refusal naming the version, got %v", err)
	}
}

// The secret guard applies to imports too, and it fails closed on the whole file: importing
// half a dump and stopping would leave the store in a state nobody asked for.
func TestImportRefusesAWholeDumpContainingASecret(t *testing.T) {
	s := newTestStore(t)
	out := filepath.Join(s.Env.Here, "leaky.json")
	dumpJSON := `{"schema_version": 11, "memories": [{"title": "deploy key",
		"body": "AKIAIOSFODNN7EXAMPLE", "scope": "global", "mem_type": "note"}]}`
	if err := os.WriteFile(out, []byte(dumpJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dump.New(s).Import(io.Discard, dump.ImportArgs{File: out}); err == nil {
		t.Error("imported credential-shaped text without --allow-secret")
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM memories"); n != 0 {
		t.Errorf("a refused import still wrote %d rows", n)
	}
}

// Every bulk write is a dry run first; --dry-run must report the plan and write nothing.
func TestImportDryRunWritesNothing(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Type: "infra"})
	out := filepath.Join(s.Env.Here, "dump.json")
	if err := dump.New(s).Export(io.Discard, dump.ExportArgs{Out: out, JSON: true}); err != nil {
		t.Fatal(err)
	}
	fresh := newTestStore(t)
	res := mustJSON(t, func(w io.Writer) error {
		return dump.New(fresh).Import(w, dump.ImportArgs{File: out, DryRun: true, JSON: true})
	})
	if res["applied"] != false || res["would_add"].(float64) != 1 {
		t.Errorf("dry run reported %v", res)
	}
	if n, _ := fresh.Count("SELECT COUNT(*) FROM memories"); n != 0 {
		t.Errorf("a dry run wrote %d rows", n)
	}
}

// A dump of the shared store carries several projects. Importing all of them under this
// project's label mislabels other projects' memories, which once made a copy unreadable.
func TestImportSkipsOtherProjectsUnlessAsked(t *testing.T) {
	s := newTestStore(t)
	out := filepath.Join(s.Env.Here, "multi.json")
	dumpJSON := `{"schema_version": 11, "memories": [
		{"id": 1, "project": "elsewhere", "title": "theirs", "scope": "global", "mem_type": "note"},
		{"id": 2, "project": "testproj", "title": "mine", "scope": "global", "mem_type": "note"}]}`
	if err := os.WriteFile(out, []byte(dumpJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	res := mustJSON(t, func(w io.Writer) error {
		return dump.New(s).Import(w, dump.ImportArgs{File: out, JSON: true})
	})
	if res["added"].(float64) != 1 || res["foreign"].(float64) != 1 {
		t.Errorf("expected 1 added and 1 foreign, got %v", res)
	}
}

// --- history, restore and update ---

// forget archives the memory; restore must bring it back findable, and remove its tombstone
// so the fact is no longer reported as deliberately forgotten.
func TestRestoreBringsBackAForgottenMemory(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Body: "tls at nginx", Type: "infra"})
	if err := memory.New(s).Forget(io.Discard, memory.ForgetArgs{ID: 1, Reason: "wrong"}); err != nil {
		t.Fatal(err)
	}
	hist := mustJSON(t, func(w io.Writer) error { return memory.New(s).History(w, memory.HistoryArgs{JSON: true}) })
	rows := hist["history"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("history has %d rows", len(rows))
	}
	hid := int64(rows[0].(map[string]interface{})["id"].(float64))
	res := mustJSON(t, func(w io.Writer) error {
		return memory.New(s).Restore(w, memory.RestoreArgs{HID: hid, JSON: true})
	})
	if res["title"] != "nginx on 8443" {
		t.Errorf("restore returned %v", res)
	}
	if got := titles(recallJSON(t, s, recall.RecallArgs{Query: "nginx"})); len(got) != 1 {
		t.Errorf("restored memory is not recallable: %v", got)
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM tombstones"); n != 0 {
		t.Errorf("%d tombstones survived the restore", n)
	}
}

// Restoring an old version over a memory that is still live replaces what is there now, so
// it takes --force; with it, the old body comes back.
func TestRestoreRefusesToClobberALiveMemoryWithoutForce(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Body: "old", Type: "infra"})
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Body: "new", Type: "infra", Update: true})
	hid := firstHistoryID(t, s, memory.HistoryArgs{})

	if err := memory.New(s).Restore(io.Discard, memory.RestoreArgs{HID: hid}); err == nil {
		t.Fatal("clobbered a live memory without --force")
	}
	if err := memory.New(s).Restore(io.Discard, memory.RestoreArgs{HID: hid, Force: true}); err != nil {
		t.Fatal(err)
	}
	m, err := s.ByID(1, false)
	if err != nil {
		t.Fatal(err)
	}
	if m.Body != "old" {
		t.Errorf("rollback left the body as %q", m.Body)
	}
}

// A title edit was once the only overwrite with nothing archived behind it: history showed
// nothing and restore could not bring the old wording back, although a body edit archived.
func TestUpdateArchivesATitleChangeToo(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Body: "tls at nginx", Type: "infra"})
	title := "nginx on 443"
	if err := memory.New(s).Update(io.Discard, memory.UpdateArgs{ID: 1, Title: &title}); err != nil {
		t.Fatal(err)
	}
	hist := mustJSON(t, func(w io.Writer) error { return memory.New(s).History(w, memory.HistoryArgs{ID: 1, JSON: true}) })
	rows := hist["history"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("a title edit archived %d versions, want 1", len(rows))
	}
	if got := rows[0].(map[string]interface{})["title"]; got != "nginx on 8443" {
		t.Errorf("archived title %v", got)
	}
	// An update that changes nothing archives nothing, or history fills with copies.
	same := "nginx on 443"
	_ = memory.New(s).Update(io.Discard, memory.UpdateArgs{ID: 1, Title: &same})
	hist = mustJSON(t, func(w io.Writer) error { return memory.New(s).History(w, memory.HistoryArgs{ID: 1, JSON: true}) })
	if n := len(hist["history"].([]interface{})); n != 1 {
		t.Errorf("an unchanged title archived a version: %d", n)
	}
}

// The identity hash is computed from the title. Rolling a title back once left the hash where
// the newer title had put it, so the row could not be found by identity and remembering or
// importing the same title added a second row.
func TestRestoreRollbackKeepsTheIdentityHashInStep(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Body: "tls at nginx", Type: "infra"})
	title := "nginx on 443"
	if err := memory.New(s).Update(io.Discard, memory.UpdateArgs{ID: 1, Title: &title}); err != nil {
		t.Fatal(err)
	}
	hid := firstHistoryID(t, s, memory.HistoryArgs{ID: 1})
	if err := memory.New(s).Restore(io.Discard, memory.RestoreArgs{HID: hid, Force: true}); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := s.DB.QueryRow("SELECT hash FROM memories WHERE id=1").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != store.ContentHash(s.Env.Project, "global", "nginx on 8443") {
		t.Errorf("hash did not follow the restored title")
	}
	// The same title remembered again is now the same identity, not a second row.
	if err := memory.New(s).Remember(io.Discard, memory.RememberArgs{Title: "nginx on 8443", Body: "tls at nginx", Type: "infra"}); err == nil {
		n, err := s.Count("SELECT COUNT(*) FROM memories")
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows after re-remembering the restored title", n)
		}
	}
}

// --- prune and purge ---

// prune deletes by age, so it reports by default and takes a dump before --apply deletes
// anything. That dump is the whole safety net, so the test checks it exists and is not empty.
func TestPruneIsADryRunAndBacksUpBeforeDeleting(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "the box is rebooting", Type: "state"})
	old := timefmt.Now() - 40*86400
	if _, err := s.DB.Exec("UPDATE memories SET updated_at=?", old); err != nil {
		t.Fatal(err)
	}
	res := mustJSON(t, func(w io.Writer) error {
		return dump.New(s).Prune(w, dump.PruneArgs{OlderThan: 30, Tier: "D", JSON: true})
	})
	if res["applied"] != false || res["count"].(float64) != 1 {
		t.Errorf("dry run reported %v", res)
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM memories"); n != 1 {
		t.Fatal("the dry run deleted something")
	}
	if err := dump.New(s).Prune(io.Discard, dump.PruneArgs{OlderThan: 30, Tier: "D", Apply: true}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM memories"); n != 0 {
		t.Error("apply did not delete")
	}
	found := false
	entries, _ := os.ReadDir(s.Env.Here)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "brain-prune-") {
			found = true
			info, _ := e.Info()
			if info.Size() == 0 {
				t.Error("the pre-prune dump is empty")
			}
		}
	}
	if !found {
		t.Error("prune --apply deleted without dumping first")
	}
}

// Tiers A and B hold the load-bearing memories (infra, decisions, constraints), and a cutoff
// of zero days deletes things written today. Each of those takes an explicit override, and a
// prune that names neither a tier nor --superseded says nothing about what to delete.
func TestPruneRefusesTheLoadBearingTiersAndASameDayCutoff(t *testing.T) {
	s := newTestStore(t)
	if err := dump.New(s).Prune(io.Discard, dump.PruneArgs{OlderThan: 0, Tier: "D"}); err == nil {
		t.Error("accepted a same-day cutoff")
	}
	for _, tier := range []string{"A", "B"} {
		if err := dump.New(s).Prune(io.Discard, dump.PruneArgs{OlderThan: 30, Tier: tier}); err == nil {
			t.Errorf("pruned tier %s without --i-mean-it", tier)
		}
	}
	if err := dump.New(s).Prune(io.Discard, dump.PruneArgs{OlderThan: 30}); err == nil {
		t.Error("pruned with no --tier and no --superseded, which says nothing about what to delete")
	}
}

// forget --purge exists for a memory that should never have been stored, such as a secret.
// Every archived version still holds the text, so purge deletes them and writes no new one.
// The tombstone stays, so the deletion itself is still on record.
func TestPurgeLeavesNoVersionLocally(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "testSecret", Body: "testSecret=11229988", Type: "knowledge"})
	body := "testSecret=11229988 (rotated)"
	if err := memory.New(s).Update(io.Discard, memory.UpdateArgs{ID: 1, Body: &body}); err != nil {
		t.Fatal(err)
	}
	if err := memory.New(s).Forget(io.Discard, memory.ForgetArgs{ID: 1, Purge: true, Reason: "secret"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM mem_history WHERE mem_id=1"); n != 0 {
		t.Errorf("%d archived versions survived the purge", n)
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM memories"); n != 0 {
		t.Errorf("the memory survived")
	}
	if n, _ := s.Count("SELECT COUNT(*) FROM tombstones"); n != 1 {
		t.Errorf("the tombstone should remain: %d", n)
	}
}

// --- snapshot and output-file guards ---

// snapshot --out writes a markdown file. Overwriting a file it did not generate destroys
// somebody's own notes, so it refuses; its own output carries a marker and can be
// regenerated in place.
func TestSnapshotRefusesToOverwriteSomebodyElsesFile(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Type: "infra", Pin: true})
	out := filepath.Join(s.Env.Here, "notes.md")
	if err := os.WriteFile(out, []byte("# my own notes, not generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := overview.New(s).Snapshot(io.Discard, overview.SnapshotArgs{Out: out}); err == nil {
		t.Fatal("overwrote a file this command did not generate")
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "my own notes") {
		t.Error("the file was clobbered anyway")
	}
	gen := filepath.Join(s.Env.Here, "snap.md")
	if err := overview.New(s).Snapshot(io.Discard, overview.SnapshotArgs{Out: gen}); err != nil {
		t.Fatal(err)
	}
	if err := overview.New(s).Snapshot(io.Discard, overview.SnapshotArgs{Out: gen}); err != nil {
		t.Errorf("refused to regenerate its own output: %v", err)
	}
}

// The output guard keys on this exact string, and the team edition writes and recognises the
// same one. A marker that differs by one character means each refuses the other's snapshot
// as somebody's hand-written notes, and the file can never be regenerated without --force.
func TestTheSnapshotMarkerIsExactSoEitherEditionCanRegenerate(t *testing.T) {
	const want = "GENERATED BY `os-brain snapshot`"
	if overview.SnapshotMarker != want {
		t.Errorf("marker drifted: %q", overview.SnapshotMarker)
	}
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "nginx on 8443", Type: "infra", Pin: true})
	out := filepath.Join(s.Env.Here, "snap.md")
	if err := overview.New(s).Snapshot(io.Discard, overview.SnapshotArgs{Out: out}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), want) {
		t.Errorf("generated file does not carry the marker:\n%s", string(b[:min(200, len(b))]))
	}
}

// Snapshots generated before the command was renamed carry `brain snapshot` in their
// marker. Recognising only the new marker would reclassify every one of them as somebody's
// hand-written file and refuse to update it.
func TestSnapshotStillRecognisesTheMarkerTheOlderClientWrote(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old-snapshot.md")
	if err := os.WriteFile(old,
		[]byte("<!-- "+overview.LegacySnapshotMarker+" ON 2026-07-01. DO NOT EDIT.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsguard.GuardOutfile(old, overview.SnapshotMarker, false, overview.LegacySnapshotMarker); err != nil {
		t.Fatalf("refused to overwrite a snapshot it generated under the old name: %v", err)
	}
	// A file it did not generate is still protected.
	hand := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(hand, []byte("# my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsguard.GuardOutfile(hand, overview.SnapshotMarker, false, overview.LegacySnapshotMarker); err == nil {
		t.Fatal("overwrote a hand-written file")
	}
}

// A not-yet-existing output file cannot be resolved with EvalSymlinks, and falling back to
// the unresolved path breaks every comparison on macOS, where the temp dir is
// /var -> /private/var. That once made backup refuse its own default path.
func TestGuardOutsideResolvesSymlinkedTempDirs(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "does-not-exist-yet.json")
	if err := fsguard.GuardOutside(inside, dir, false); err != nil {
		t.Errorf("refused a path inside the project: %v", err)
	}
	if err := fsguard.GuardOutside(filepath.Join(os.TempDir(), "escaped.json"), dir, false); err == nil {
		t.Error("allowed a path outside the project")
	}
}

// --- init replacing the hooks of an older client ---

// initOverSettings writes a .claude/settings.json with the given body, runs init for Claude
// in that project, and returns init's output and a function listing the hook commands now
// registered for an event.
func initOverSettings(t *testing.T, settingsJSON string) (string, func(event string) []string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(root, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte(settingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := install.Init(&out, root, install.InitArgs{Dir: "brain", Agents: "claude"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	hooks := cfg["hooks"].(map[string]interface{})
	commands := func(event string) []string {
		var got []string
		for _, e := range hooks[event].([]interface{}) {
			for _, h := range e.(map[string]interface{})["hooks"].([]interface{}) {
				got = append(got, h.(map[string]interface{})["command"].(string))
			}
		}
		return got
	}
	return out.String(), commands
}

// Running init over an install made by the older brain/brain wrapper must replace the
// wrapper's hooks, not sit beside them: two SessionStart hooks mean two briefs in context
// and two clients bumping use_count.
func TestInitReplacesLegacyWrapperHooksInsteadOfDuplicating(t *testing.T) {
	out, commands := initOverSettings(t, `{"hooks":{
	  "SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR/brain/brain\" brief"}]}],
	  "UserPromptSubmit":[{"hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR/brain/brain\" hook-recall"}]}],
	  "PreToolUse":[{"hooks":[{"type":"command","command":"some/other/brain/brain guard"}]}]
	}}`)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		got := commands(ev)
		if len(got) != 1 {
			t.Fatalf("%s: want exactly one hook after init, got %d: %q", ev, len(got), got)
		}
		if !strings.Contains(got[0], "os-brain") {
			t.Fatalf("%s: wrapper was not replaced: %q", ev, got[0])
		}
		if strings.Contains(got[0], `"$CLAUDE_PROJECT_DIR/brain/brain"`) {
			t.Fatalf("%s: legacy wrapper still present: %q", ev, got[0])
		}
	}
	// A hook that merely mentions another brain must be left completely alone.
	if got := commands("PreToolUse"); len(got) != 1 || got[0] != "some/other/brain/brain guard" {
		t.Fatalf("unrelated hook was touched: %q", got)
	}
	if !strings.Contains(out, "replaced by") {
		t.Fatalf("the replacement was silent; output was:\n%s", out)
	}
}

// Matching only the exact wrapper call was too strict for what exists in real projects: an
// older installer wrote `cd "$CLAUDE_PROJECT_DIR" && ./brain/brain brief 2>/dev/null || true`,
// and projects had that beside a newer wrapper call. Leaving either behind is not cosmetic:
// the project then runs both clients, the older one refuses a database whose schema is newer
// than it understands, and every session opens with a migration error in its context.
func TestInitMigratesEveryLegacyHookShapeAndLeavesOne(t *testing.T) {
	_, commands := initOverSettings(t, `{"hooks":{
	  "SessionStart":[
	    {"matcher":"startup","hooks":[{"type":"command","command":"cd \"$CLAUDE_PROJECT_DIR\" && ./brain/brain brief 2>/dev/null || true"}]},
	    {"matcher":"startup","hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR/brain/brain\" brief"}]}],
	  "UserPromptSubmit":[{"hooks":[{"type":"command","command":"\"$CLAUDE_PROJECT_DIR/brain/brain\" hook-recall"}]}],
	  "PreToolUse":[{"hooks":[{"type":"command","command":"some/other/brain/brain guard"}]}]}}`)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		got := commands(ev)
		if len(got) != 1 {
			t.Fatalf("%s: want exactly one hook, got %d: %q", ev, len(got), got)
		}
		if !strings.Contains(got[0], "os-brain") || strings.Contains(got[0], "./brain/brain") {
			t.Fatalf("%s: legacy shape survived: %q", ev, got[0])
		}
	}
	if got := commands("PreToolUse"); len(got) != 1 || got[0] != "some/other/brain/brain guard" {
		t.Fatalf("an unrelated hook was rewritten: %q", got)
	}
}
