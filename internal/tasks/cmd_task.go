package tasks

import (
	"database/sql"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/schedule"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// Tasks: what is owed, by whom, and when.
//
// The delivery rule is the design, and it is the reason this needs no daemon: NOTHING
// PUSHES. The hooks that already run (session open, every user message, turn end) ask
// what is due, so a reminder reaches a session that is already running. The cost is
// stated rather than discovered: latency is bounded by the user's next keystroke, not by
// the clock. That is a feature at 03:00 and a limitation at 17:00, and it is the price of
// never acting unattended.
//
// Every rule below is against a failure that has killed a reminder feature somewhere.

type Task struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	Owner       string `json:"owner"`
	Scope       string `json:"scope"`
	State       string `json:"state"`
	SchedKind   string `json:"sched_kind,omitempty"`
	SchedSays   string `json:"sched_says,omitempty"`
	Schedule    string `json:"schedule,omitempty"`
	TZ          string `json:"tz,omitempty"`
	DueAt       *int64 `json:"due_at,omitempty"`
	SnoozeUntil *int64 `json:"snooze_until,omitempty"`
	FireCount   int    `json:"fire_count"`
	Offers      int    `json:"offers_since_ack"`
	Skipped     int    `json:"skipped_count"`
	Notify      int    `json:"notify"`
	CreatedAt   int64  `json:"created_at"`
	ClosedAt    *int64 `json:"closed_at,omitempty"`
	// Session is the session that added it: an agent-owned task is an instruction from an
	// earlier session, and the one carrying it out has to be able to see that.
	Session string `json:"session,omitempty"`
	// Late is filled on delivery: how overdue this was when it was handed over. A list
	// that cannot tell "due now" from "due last Tuesday" is a list nobody reads twice.
	Late string `json:"late,omitempty"`
}

const taskCols = `id,title,body,owner,scope,state,sched_kind,sched_n,sched_unit,sched_at,
	sched_days,sched_says,tz,due_at,snooze_until,fire_count,offers_since_ack,skipped_count,
	notify,created_at,closed_at,session`

// TaskRow is a Task plus the schedule columns that only NextDue needs. They stay out of
// Task so the JSON shows the schedule once, as the rendered Schedule string.
type TaskRow struct {
	Task
	n    int
	unit string
	at   string
	days string
}

// recurring is true for every schedule that has a next occurrence.
func (t *TaskRow) recurring() bool { return t.SchedKind != "" && t.SchedKind != "once" }

func scanTask(rows interface{ Scan(...any) error }) (*TaskRow, error) {
	var t TaskRow
	err := rows.Scan(&t.ID, &t.Title, &t.Body, &t.Owner, &t.Scope, &t.State, &t.SchedKind,
		&t.n, &t.unit, &t.at, &t.days, &t.SchedSays, &t.TZ, &t.DueAt, &t.SnoozeUntil,
		&t.FireCount, &t.Offers, &t.Skipped, &t.Notify, &t.CreatedAt, &t.ClosedAt, &t.Session)
	if err != nil {
		return nil, err
	}
	if t.recurring() {
		t.Schedule = schedule.Describe(t.SchedKind, t.n, t.unit, t.at, t.days)
	}
	return &t, nil
}

func tasksOf(rows []*TaskRow) []*Task {
	out := make([]*Task, 0, len(rows))
	for _, r := range rows {
		out = append(out, &r.Task)
	}
	return out
}

type TaskArgs struct {
	Sub         string
	ID          int64
	Title       string
	Body        string
	Owner       string
	Scope       string
	When        string
	Until       string
	State       string
	Notify      bool
	AllowSecret bool
	// Fields names the flags the caller passed, so `task set --body ""` can clear a body
	// while an absent --body leaves it alone.
	Fields []string
	All    bool
	Out    string
	JSON   bool
	// todo's file refusals, the ones every other generated file takes.
	AllowOutside bool
	AllowTracked bool
}

func (s *Store) Task(w io.Writer, a TaskArgs) error {
	switch a.Sub {
	case "add":
		return s.taskAdd(w, a)
	case "list", "":
		return s.taskList(w, a)
	case "due":
		return s.taskDue(w, a)
	case "done", "drop", "wait", "reopen":
		return s.taskClose(w, a)
	case "snooze":
		return s.taskSnooze(w, a)
	case "set":
		return s.taskSet(w, a)
	case "todo":
		return s.taskTodo(w, a)
	}
	return CheckTaskArgs(a.Sub, nil)
}

func (s *Store) taskAdd(w io.Writer, a TaskArgs) error {
	title := strings.TrimSpace(a.Title)
	if title == "" {
		return fmt.Errorf("a task needs a title")
	}
	if err := textguard.CheckLines("the title", title, "--scope", a.Scope); err != nil {
		return err
	}
	if err := textguard.GuardText(a.AllowSecret, title, a.Body); err != nil {
		return err
	}
	owner := a.Owner
	if owner == "" {
		owner = "user"
	}
	if err := checkTaskOwner(owner); err != nil {
		return err
	}
	scope := a.Scope
	if scope == "" {
		scope = "global"
	}
	sc, err := schedule.ParseSchedule(a.When, time.Now())
	if err != nil {
		return err
	}
	if sc == nil {
		sc = &schedule.Schedule{} // no schedule: every schedule column stays empty
	}
	var due interface{}
	if sc.Kind != "" {
		due = sc.First
	}
	id, err := s.insertTask(title, a.Body, owner, scope, sc, due, a.Notify)
	if err != nil {
		return err
	}
	_ = memory.New(s.Store).Log(io.Discard, memory.LogArgs{Scope: scope, Kind: "task", Actor: "claude",
		Summary: fmt.Sprintf("task #%d added: %s", id, title)})

	return store.Out(w, a.JSON, taskAddedMessage(id, owner, title, sc), map[string]interface{}{
		"id": id, "title": title, "owner": owner, "due_at": due,
		"schedule": schedule.Describe(sc.Kind, sc.N, sc.Unit, sc.At, sc.Days), "says": sc.Says, "tz": sc.TZ})
}

// insertTask writes a new open task at the bottom of the project's list.
func (s *Store) insertTask(title, body, owner, scope string, sc *schedule.Schedule, due interface{},
	notify bool) (int64, error) {
	var pos float64
	_ = s.DB.QueryRow("SELECT COALESCE(MAX(position),0)+1 FROM tasks WHERE project=?",
		s.Env.Project).Scan(&pos)
	now := timefmt.Now()
	res, err := s.DB.Exec("INSERT INTO tasks(project,title,body,owner,scope,state,position,"+
		"sched_kind,sched_n,sched_unit,sched_at,sched_days,sched_says,tz,due_at,notify,"+
		"created_at,updated_at,session) VALUES(?,?,?,?,?,'open',?,?,?,?,?,?,?,?,?,?,?,?,?)",
		s.Env.Project, title, body, owner, scope, pos, sc.Kind, sc.N, sc.Unit, sc.At, sc.Days,
		sc.Says, sc.TZ, due, store.BoolToInt(notify), now, now, s.Env.SessionID())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// taskAddedMessage says when the task will surface, and says plainly that nothing will
// wake a machine for it, so nobody sets a reminder expecting an alarm.
func taskAddedMessage(id int64, owner, title string, sc *schedule.Schedule) string {
	line := fmt.Sprintf("task #%d [%s] %s", id, owner, textguard.OneLine(title))
	if sc.Kind == "" {
		return line + "\n  no schedule: it is a list item, and shows in `os-brain task list`"
	}
	when := schedule.Describe(sc.Kind, sc.N, sc.Unit, sc.At, sc.Days)
	line += fmt.Sprintf("\n  due %s (%s)", timefmt.ISO(sc.First), when)
	line += "\n  Delivered when a session is open and somebody is typing — nothing here " +
		"wakes a machine or acts unattended."
	return line
}

// Tasks reads this project's tasks matching `where`, ordered for reading: open before
// waiting before closed, then soonest due, then undated in list order.
func (s *Store) Tasks(where string, args ...interface{}) ([]*TaskRow, error) {
	q := "SELECT " + taskCols + " FROM tasks WHERE project=?"
	all := append([]interface{}{s.Env.Project}, args...)
	if where != "" {
		q += " AND " + where
	}
	q += " ORDER BY CASE state WHEN 'open' THEN 0 WHEN 'waiting' THEN 1 ELSE 2 END, " +
		"CASE WHEN due_at IS NULL THEN 1 ELSE 0 END, due_at, position"
	rows, err := s.DB.Query(q, all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TaskRow
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// taskListFilter is the WHERE for `task list`: live tasks by default, every task with
// --all, one state with --state, narrowed by --owner in each case.
func taskListFilter(a TaskArgs) (string, []interface{}, error) {
	where, args := TaskLiveSQL, []interface{}{}
	if a.All {
		where = ""
	} else if a.State != "" {
		if err := validTaskState(a.State); err != nil {
			return "", nil, err
		}
		where, args = "state=?", []interface{}{a.State}
	}
	if a.Owner != "" {
		if where != "" {
			where += " AND "
		}
		where += "owner=?"
		args = append(args, a.Owner)
	}
	return where, args, nil
}

func (s *Store) taskList(w io.Writer, a TaskArgs) error {
	where, args, err := taskListFilter(a)
	if err != nil {
		return err
	}
	rows, err := s.Tasks(where, args...)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return store.Out(w, a.JSON, "no tasks", []*Task{})
	}
	var lines []string
	for _, t := range rows {
		lines = append(lines, RenderTask(&t.Task, false))
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), tasksOf(rows))
}

// RenderTask is one task as a list line, with its body indented beneath. Every view of a
// task (list, due, the DUE NOW block, open) goes through it: a second copy of a
// renderer is how the UI drifted from the CLI three times before anybody noticed. With
// late set, the due time is shown with how overdue it is.
func RenderTask(t *Task, late bool) string {
	line := fmt.Sprintf("[%s] #%-3d %s", taskStateMark(t.State), t.ID, textguard.OneLine(t.Title))
	if tags := taskTags(t, late); len(tags) > 0 {
		line += "  (" + strings.Join(tags, " · ") + ")"
	}
	if t.Body != "" {
		line += "\n       " + strings.ReplaceAll(textguard.BodyText(t.Body), "\n", "\n       ")
	}
	return line
}

func taskStateMark(state string) string {
	switch state {
	case "done":
		return "x"
	case "dropped":
		return "-"
	case "waiting":
		return "~"
	}
	return " "
}

func taskTags(t *Task, late bool) []string {
	var tags []string
	if t.State == "waiting" {
		tags = append(tags, "waiting on someone else")
	}
	if t.Owner == "agent" {
		tags = append(tags, "you owe this", "set "+taskOrigin(t))
	}
	if t.DueAt != nil && taskLive(t.State) {
		if late {
			tags = append(tags, "due "+lateness(*t.DueAt))
		} else {
			tags = append(tags, "due "+timefmt.ISO(*t.DueAt))
		}
	}
	if t.Schedule != "" {
		tags = append(tags, t.Schedule)
	}
	if t.SnoozeUntil != nil {
		tags = append(tags, "snoozed to "+timefmt.ISO(*t.SnoozeUntil))
	}
	if t.Skipped > 0 {
		tags = append(tags, fmt.Sprintf("%d occurrences passed while nothing was running",
			t.Skipped))
	}
	return tags
}

// taskOrigin names who added a task and when, so a task an earlier session wrote is never
// read as the person asking now.
func taskOrigin(t *Task) string {
	who := "by an unrecorded session"
	if t.Session != "" {
		who = "by session " + store.ShortSession(textguard.OneLine(t.Session))
	}
	if t.CreatedAt > 0 {
		who += " on " + timefmt.Day(t.CreatedAt)
	}
	return who
}

// lateness says WHEN it was due and how overdue it is. "Due 17:00, four hours ago" is the
// useful sentence; a bare time teaches you to ignore the list.
func lateness(due int64) string {
	d := time.Duration(timefmt.Now()-due) * time.Second
	if d < 0 {
		return timefmt.ISO(due)
	}
	switch {
	case d < time.Minute:
		return timefmt.ISO(due) + ", just now"
	case d < time.Hour:
		return fmt.Sprintf("%s, %dm ago", timefmt.ISO(due), int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%s, %dh ago", timefmt.ISO(due), int(d.Hours()))
	}
	return fmt.Sprintf("%s, %dd ago", timefmt.ISO(due), int(d.Hours()/24))
}

// DueTasks returns what is due now and, when deliver is true, records the delivery: it
// advances recurring schedules, counts what was skipped, and increments the nag counter.
// Reading without delivering is what `task due` does; the hooks deliver.
func (s *Store) DueTasks(deliver bool) ([]*TaskRow, error) {
	if !s.TableExists("tasks") {
		return nil, nil
	}
	now := timefmt.Now()
	rows, err := s.Tasks(TaskLiveSQL+" AND due_at IS NOT NULL AND due_at <= ? "+
		"AND (snooze_until IS NULL OR snooze_until <= ?)", now, now)
	if err != nil {
		return nil, err
	}
	if !deliver {
		return rows, nil
	}
	for i, t := range rows {
		if err := s.recordDelivery(t, now); err != nil {
			return rows[:i+1], err
		}
	}
	return rows, nil
}

// recordDelivery notes that a due task was handed to a session. Each delivery counts as
// an offer; the nag rule in renderDueTask reads that count, and any answer to the task
// (a change of state, a snooze or a new schedule) resets it.
func (s *Store) recordDelivery(t *TaskRow, now int64) error {
	next, skipped := int64(0), 0
	if t.recurring() {
		next, skipped = schedule.NextDue(t.SchedKind, t.n, t.unit, t.at, t.days, t.TZ, *t.DueAt, now)
	}
	if next > 0 {
		// A recurring task moves on to its next occurrence, and a snooze, which only ever
		// applied to the occurrence just delivered, is spent.
		_, err := s.DB.Exec("UPDATE tasks SET due_at=?, skipped_count=?, last_fired_at=?, "+
			"fire_count=fire_count+1, offers_since_ack=offers_since_ack+1, snooze_until=NULL, "+
			"updated_at=? WHERE id=? AND project=?",
			next, skipped, now, now, t.ID, s.Env.Project)
		t.Skipped = skipped
		return err
	}
	// A one-shot stays open and stops being re-offered by the nag rule rather than closing
	// itself: nothing marks somebody's work done on their behalf.
	_, err := s.DB.Exec("UPDATE tasks SET last_fired_at=?, fire_count=fire_count+1, "+
		"offers_since_ack=offers_since_ack+1, updated_at=? WHERE id=? AND project=?",
		now, now, t.ID, s.Env.Project)
	return err
}

// DueBlock is what a hook prints. Empty when nothing is due: a hook that speaks on every
// turn gets muted, and the one line that matters goes with it.
func (s *Store) DueBlock(deliver bool) string {
	rows, err := s.DueTasks(deliver)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return renderDueBlock(tasksOf(rows))
}

// maxDueShown caps the DUE NOW block. The session-open block is a budget. Directives are
// uncapped by design; due tasks are not, or a fortnight away from the machine turns the
// block that carries the axioms into a wall of reminders nobody reads to the bottom of.
const maxDueShown = 7

// nagAfterOffers is when a reminder stops repeating itself: three unanswered offers turn
// it into a question (keep, snooze or drop) rather than a fourth identical repeat.
const nagAfterOffers = 3

// renderDueBlock is the DUE NOW block: agent-owned tasks first, because they are
// instructions to the session reading it, then the person's.
func renderDueBlock(rows []*Task) string {
	if len(rows) == 0 {
		return ""
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Owner == "agent" && rows[j].Owner != "agent"
	})
	hidden := 0
	if len(rows) > maxDueShown {
		hidden = len(rows) - maxDueShown
		rows = rows[:maxDueShown]
	}
	var lines []string
	for _, t := range rows {
		lines = append(lines, renderDueTask(t))
	}
	total := len(rows) + hidden
	head := "## DUE NOW — 1 task"
	if total != 1 {
		head = fmt.Sprintf("## DUE NOW — %d tasks", total)
	}
	if hidden > 0 {
		// Counted, never dropped silently: a list that quietly truncates is a list that
		// lies about how much is owed.
		lines = append(lines, fmt.Sprintf("  ... and %d more due (os-brain task due)", hidden))
	}
	// The closing lines frame the block for the agent: a task is something written
	// earlier, not the person speaking now, so it authorises only what it names.
	return head + "\n" + strings.Join(lines, "\n") +
		"\n  os-brain task done <id> · snooze <id> --until \"in 2 hours\" · drop <id>" +
		"\n  A task was written earlier, by the person or by a session (see \"set by\"), not said now." +
		"\n  Do what it names within its stated scope; anything beyond that, ask the user first."
}

func renderDueTask(t *Task) string {
	if t.Offers >= nagAfterOffers {
		return fmt.Sprintf("[!] #%d %s\n      offered %d times and never "+
			"acknowledged. Keep it, snooze it, or drop it: os-brain task done|snooze|drop %d",
			t.ID, textguard.OneLine(t.Title), t.Offers, t.ID)
	}
	return RenderTask(t, true)
}

func (s *Store) taskDue(w io.Writer, a TaskArgs) error {
	rows, err := s.DueTasks(false)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return store.Out(w, a.JSON, "nothing due", []*Task{})
	}
	var lines []string
	for _, t := range rows {
		lines = append(lines, RenderTask(&t.Task, true))
		t.Late = lateness(*t.DueAt)
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), tasksOf(rows))
}

// taskClose handles done, drop, wait and reopen.
func (s *Store) taskClose(w io.Writer, a TaskArgs) error {
	t, err := s.ownTask(a.ID)
	if err != nil {
		return err
	}
	state := taskStateFor(a.Sub)
	// Re-closing a closed task once re-stamped closed_at and logged a second event, so the
	// activity trail said the work finished twice. Say what is already true instead.
	if t.State == state {
		return store.Out(w, a.JSON, fmt.Sprintf("#%d is already %s (%s)", t.ID, t.State, t.Title),
			map[string]interface{}{"id": t.ID, "state": t.State, "changed": false})
	}
	// A recurring task that is "done" closes THIS occurrence and schedules the next; drop
	// closes the series. Conflating them is how a weekly task disappears after one week.
	if state == "done" && taskLive(t.State) && t.recurring() && t.DueAt != nil {
		return s.taskDoneForThisOccurrence(w, a, t)
	}
	// closed_at belongs to done and dropped only: a reopened task that kept it would read
	// as closed to anything that asks "when did this finish?".
	var closed interface{}
	if !taskLive(state) {
		closed = timefmt.Now()
	}
	if _, err := s.DB.Exec("UPDATE tasks SET state=?, closed_at=?, offers_since_ack=0, "+
		"updated_at=? WHERE id=? AND project=?", state, closed, timefmt.Now(), t.ID, s.Env.Project); err != nil {
		return err
	}
	_ = memory.New(s.Store).Log(io.Discard, memory.LogArgs{Scope: t.Scope, Kind: "task", Actor: "claude",
		Summary: fmt.Sprintf("task #%d %s (was %s): %s", t.ID, state, t.State, t.Title)})
	return store.Out(w, a.JSON, fmt.Sprintf("#%d %s (was %s): %s", t.ID, state, t.State, t.Title),
		map[string]interface{}{"id": t.ID, "state": state, "was": t.State})
}

// taskDoneForThisOccurrence keeps a recurring task open and moves it to its next
// occurrence after the one that was due, clearing the nag count and any snooze.
func (s *Store) taskDoneForThisOccurrence(w io.Writer, a TaskArgs, t *TaskRow) error {
	next, _ := schedule.NextDue(t.SchedKind, t.n, t.unit, t.at, t.days, t.TZ, *t.DueAt, timefmt.Now())
	if _, err := s.DB.Exec("UPDATE tasks SET state='open', due_at=?, offers_since_ack=0, "+
		"snooze_until=NULL, updated_at=? WHERE id=? AND project=?",
		next, timefmt.Now(), t.ID, s.Env.Project); err != nil {
		return err
	}
	_ = memory.New(s.Store).Log(io.Discard, memory.LogArgs{Scope: t.Scope, Kind: "task", Actor: "claude",
		Summary: fmt.Sprintf("task #%d done for this occurrence", t.ID)})
	return store.Out(w, a.JSON, fmt.Sprintf("#%d done for this occurrence; next %s (%s)",
		t.ID, timefmt.ISO(next), t.Schedule),
		map[string]interface{}{"id": t.ID, "state": "open", "next_due": next})
}

func (s *Store) taskSet(w io.Writer, a TaskArgs) error {
	t, err := s.ownTask(a.ID)
	if err != nil {
		return err
	}
	ed, err := planTaskEdit(a, &t.Task, time.Now())
	if err != nil {
		return err
	}
	// The schedule columns take the edit's values only when --when was given (Resched);
	// otherwise they are written back as they were. A new schedule also spends any snooze
	// and resets the nag count, since both belonged to the old one.
	if _, err := s.DB.Exec("UPDATE tasks SET title=?, body=?, owner=?, scope=?, notify=?, "+
		"sched_kind=?, sched_n=?, sched_unit=?, sched_at=?, sched_days=?, sched_says=?, tz=?, "+
		"due_at=?, snooze_until=CASE WHEN ? THEN NULL ELSE snooze_until END, "+
		"offers_since_ack=CASE WHEN ? THEN 0 ELSE offers_since_ack END, updated_at=? "+
		"WHERE id=? AND project=?",
		ed.Title, ed.Body, ed.Owner, ed.Scope, ed.Notify,
		pick(ed.Resched, ed.Kind, t.SchedKind), pick(ed.Resched, ed.N, t.n),
		pick(ed.Resched, ed.Unit, t.unit), pick(ed.Resched, ed.At, t.at),
		pick(ed.Resched, ed.Days, t.days), pick(ed.Resched, ed.Says, t.SchedSays),
		pick(ed.Resched, ed.TZ, t.TZ), pick[interface{}](ed.Resched, ed.Due, t.DueAt),
		ed.Resched, ed.Resched, timefmt.Now(), t.ID, s.Env.Project); err != nil {
		return err
	}
	_ = memory.New(s.Store).Log(io.Discard, memory.LogArgs{Scope: ed.Scope, Kind: "task", Actor: "claude",
		Summary: fmt.Sprintf("task #%d edited (%s): %s", t.ID, strings.Join(ed.Changed, ", "),
			ed.Title)})
	return store.Out(w, a.JSON, ed.message(t.ID), map[string]interface{}{"id": t.ID,
		"changed": ed.Changed})
}

func pick[T any](yes bool, a, b T) T {
	if yes {
		return a
	}
	return b
}

func (s *Store) taskSnooze(w io.Writer, a TaskArgs) error {
	t, err := s.ownTask(a.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(a.Until) == "" {
		return fmt.Errorf("snooze needs --until: \"in 2 hours\", \"tomorrow 9am\", \"17:00\"")
	}
	sc, err := schedule.ParseSchedule(a.Until, time.Now())
	if err != nil {
		return err
	}
	if sc == nil || sc.First <= timefmt.Now() {
		return fmt.Errorf("--until has to be in the future")
	}
	// Snoozing moves THIS occurrence and nothing else. A weekly task snoozed tonight is
	// still weekly tomorrow.
	if _, err := s.DB.Exec("UPDATE tasks SET snooze_until=?, offers_since_ack=0, updated_at=? "+
		"WHERE id=? AND project=?", sc.First, timefmt.Now(), t.ID, s.Env.Project); err != nil {
		return err
	}
	msg := fmt.Sprintf("#%d snoozed to %s", t.ID, timefmt.ISO(sc.First))
	if t.Schedule != "" {
		msg += " (this occurrence only; still " + t.Schedule + ")"
	}
	return store.Out(w, a.JSON, msg, map[string]interface{}{"id": t.ID, "snooze_until": sc.First})
}

// ownTask loads one task of this project. Another project's id answers as missing, the
// same as an id that was never used.
func (s *Store) ownTask(id int64) (*TaskRow, error) {
	if id <= 0 {
		return nil, fmt.Errorf("which task? os-brain task list")
	}
	row := s.DB.QueryRow("SELECT "+taskCols+" FROM tasks WHERE id=? AND project=?",
		id, s.Env.Project)
	t, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no task #%d in %s", id, pyfmt.Repr(s.Env.Project))
	}
	return t, err
}

// taskTodo generates TODO.md from the live tasks.
func (s *Store) taskTodo(w io.Writer, a TaskArgs) error {
	out := a.Out
	if out == "" {
		out = "TODO.md"
	}
	rows, err := s.Tasks(TaskLiveSQL)
	if err != nil {
		return err
	}
	file := todoFile(out, s.Env.ProjectRoot(), a)
	if err := file.Check(); err != nil {
		return err
	}
	if err := file.Write(RenderTodo(s.Env.Project, tasksOf(rows))); err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("wrote %s (%d open)", out, len(rows)),
		map[string]interface{}{"out": out, "count": len(rows)})
}

// todoFile is where `task todo` writes, with the refusals of every other generated file:
// outside the project, git-tracked, a link, or a file it did not write.
func todoFile(out, root string, a TaskArgs) fsguard.Dump {
	return fsguard.Dump{Path: out, Root: root, Marker: TodoMarker, AllowOutside: a.AllowOutside,
		AllowTracked: a.AllowTracked}
}

// TodoMarker heads every generated TODO.md and is what the writer looks for before
// overwriting. Generated, never authored: a hand-written list beside a stored one
// guarantees a silent contradiction, and the auto-loaded one wins regardless of which is
// right.
const TodoMarker = "<!-- generated by os-brain task todo — edits here are lost; " +
	"os-brain task add/done -->"

// RenderTodo is the TODO.md body for the given tasks.
func RenderTodo(project string, rows []*Task) string {
	lines := []string{TodoMarker, "", "# " + textguard.OneLine(project) + " — open tasks", ""}
	if len(rows) == 0 {
		lines = append(lines, "_nothing open_")
	}
	for _, t := range rows {
		lines = append(lines, todoLine(t))
		if t.Body != "" {
			lines = append(lines, "      "+strings.ReplaceAll(textguard.BodyText(t.Body), "\n", "\n      "))
		}
	}
	lines = append(lines, "", fmt.Sprintf("_%d open · generated %s_", len(rows), timefmt.ISO(timefmt.Now())))
	return strings.Join(lines, "\n") + "\n"
}

func todoLine(t *Task) string {
	l := "- [ ] " + textguard.OneLine(t.Title)
	var tags []string
	if t.State == "waiting" {
		tags = append(tags, "waiting")
	}
	if t.Owner == "agent" {
		tags = append(tags, "agent")
	}
	if t.DueAt != nil {
		tags = append(tags, "due "+timefmt.ISO(*t.DueAt))
	}
	if t.Schedule != "" {
		tags = append(tags, t.Schedule)
	}
	if len(tags) > 0 {
		l += "  _(" + strings.Join(tags, " · ") + ")_"
	}
	return l
}
