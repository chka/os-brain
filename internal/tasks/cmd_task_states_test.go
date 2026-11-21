package tasks

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/timefmt"
)

// A task is open, waiting (owed, but the next move is someone else's), done or dropped, and
// can move from any state to any other. These tests pin the moves, the `task set` edit and the
// refusal of malformed task commands.

// taskOut runs one task subcommand and returns what it printed, failing the test on an error.
func taskOut(t *testing.T, s *Store, a TaskArgs) string {
	t.Helper()
	var buf bytes.Buffer
	if err := s.Task(&buf, a); err != nil {
		t.Fatalf("task %s: %v", a.Sub, err)
	}
	return buf.String()
}

// taskByID reads a task row of this project straight from the store.
func taskByID(t *testing.T, s *Store, id int64) *TaskRow {
	t.Helper()
	r, err := s.ownTask(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Two gaps this closes: "submitted, awaiting a reply" had no state of its own, so it lived in a
// local file outside the brain, and a task closed by mistake sat in the stats for ever because
// nothing could reopen it. Every move reports where the task came from, and a move to the
// state it is already in says so instead of pretending to change something.
func TestATaskMovesBetweenEveryStateAndSaysWhenItAlreadyIsThere(t *testing.T) {
	s := newTestStore(t)
	taskOut(t, s, TaskArgs{Sub: "add", Title: "send the customs filing"})

	if got := taskOut(t, s, TaskArgs{Sub: "wait", ID: 1}); !strings.Contains(got, "waiting (was open)") {
		t.Errorf("wait did not report the move: %q", got)
	}
	list := taskOut(t, s, TaskArgs{Sub: "list"})
	if !strings.Contains(list, "[~]") || !strings.Contains(list, "waiting on someone else") {
		t.Errorf("a waiting task is still owed and belongs on the default list:\n%s", list)
	}
	if got := taskOut(t, s, TaskArgs{Sub: "wait", ID: 1}); !strings.Contains(got, "already waiting") {
		t.Errorf("waiting twice should say so: %q", got)
	}
	taskOut(t, s, TaskArgs{Sub: "done", ID: 1})
	if r := taskByID(t, s, 1); r.State != "done" || r.ClosedAt == nil {
		t.Fatalf("done from waiting did not close it: %#v", r.Task)
	}
	// The case that was stuck: drop on a done task said "already done" and did nothing.
	if got := taskOut(t, s, TaskArgs{Sub: "drop", ID: 1}); !strings.Contains(got, "dropped (was done)") {
		t.Errorf("drop on a done task: %q", got)
	}
	taskOut(t, s, TaskArgs{Sub: "reopen", ID: 1})
	r := taskByID(t, s, 1)
	if r.State != "open" || r.ClosedAt != nil {
		t.Errorf("reopen must clear closed_at, or it still reads as finished: %#v", r.Task)
	}
	if got := taskOut(t, s, TaskArgs{Sub: "reopen", ID: 1}); !strings.Contains(got, "already open") {
		t.Errorf("reopening an open task should say so: %q", got)
	}
}

// Waiting is not the same as parked: a waiting task with a date is the day to chase the other
// party, so it is still delivered as due.
func TestAWaitingTaskIsStillDeliveredWhenItsChaseDateComes(t *testing.T) {
	s := newTestStore(t)
	taskOut(t, s, TaskArgs{Sub: "add", Title: "chase the broker", When: "in 1 minute"})
	taskOut(t, s, TaskArgs{Sub: "wait", ID: 1})
	if _, err := s.DB.Exec("UPDATE tasks SET due_at=? WHERE id=1", timefmt.Now()-60); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueTasks(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("a waiting task with a date passed is the day to chase it, got %d due", len(due))
	}
}

// `task set` edits only the fields named on the command line (Fields), so an omitted flag can
// never blank a field by accident, while an explicitly empty --body does empty it and
// --when none clears the schedule. Nothing-to-change, a blank title and an unknown owner are
// refused rather than reported as success.
func TestTaskSetChangesOnlyWhatWasNamed(t *testing.T) {
	s := newTestStore(t)
	taskOut(t, s, TaskArgs{Sub: "add", Title: "file the T1", Body: "draft", Scope: "customs",
		When: "tomorrow 9am"})

	if err := s.Task(&bytes.Buffer{}, TaskArgs{Sub: "set", ID: 1}); err == nil {
		t.Error("set with nothing to change reported success")
	}
	taskOut(t, s, TaskArgs{Sub: "set", ID: 1, Body: "submitted 4 Oct", Fields: []string{"body"}})
	r := taskByID(t, s, 1)
	if r.Body != "submitted 4 Oct" || r.Title != "file the T1" || r.Scope != "customs" ||
		r.DueAt == nil {
		t.Fatalf("set --body touched something else: %#v", r.Task)
	}
	taskOut(t, s, TaskArgs{Sub: "set", ID: 1, Title: "file the T1 for Rotterdam",
		When: "none", Fields: []string{"title", "when"}})
	r = taskByID(t, s, 1)
	if r.Title != "file the T1 for Rotterdam" || r.DueAt != nil || r.SchedKind != "" {
		t.Fatalf("--when none should clear the schedule: %#v", r.Task)
	}
	taskOut(t, s, TaskArgs{Sub: "set", ID: 1, Body: "", Fields: []string{"body"}})
	if r = taskByID(t, s, 1); r.Body != "" {
		t.Errorf(`--body "" should empty the body, got %q`, r.Body)
	}
	if err := s.Task(&bytes.Buffer{}, TaskArgs{Sub: "set", ID: 1, Title: " ",
		Fields: []string{"title"}}); err == nil {
		t.Error("a blank title was accepted")
	}
	if err := s.Task(&bytes.Buffer{}, TaskArgs{Sub: "set", ID: 1, Owner: "boss",
		Fields: []string{"owner"}}); err == nil {
		t.Error("an unknown owner was accepted")
	}
}

// `task 214 --state dropped` (a typo for `task drop 214`) once printed the task list and exited
// 0, so the caller believed the task was dropped. An unknown subcommand, or a flag the
// subcommand does not take, is refused instead, and so is a --state filter that names no state.
func TestATaskTypoIsRefusedNotAnsweredWithTheList(t *testing.T) {
	for _, c := range []struct {
		sub   string
		flags []string
	}{
		{"bogus", nil},
		{"214", []string{"state"}},
		{"done", []string{"body"}},
		{"list", []string{"body"}},
		{"add", []string{"until"}},
	} {
		if err := CheckTaskArgs(c.sub, c.flags); err == nil {
			t.Errorf("task %s %v was accepted", c.sub, c.flags)
		}
	}
	if err := CheckTaskArgs("set", []string{"body", "when", "title"}); err != nil {
		t.Errorf("a valid set was refused: %v", err)
	}
	s := newTestStore(t)
	if err := s.Task(&bytes.Buffer{}, TaskArgs{Sub: "bogus"}); err == nil {
		t.Error("the local store accepted an unknown subcommand")
	}
	if err := s.Task(&bytes.Buffer{}, TaskArgs{Sub: "list", State: "closed"}); err == nil {
		t.Error("an unknown --state filtered to nothing instead of being refused")
	}
}
