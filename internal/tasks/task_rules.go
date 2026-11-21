package tasks

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/schedule"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// The task contract: which subcommands exist, which flags each one reads, which states a
// task can be in and what moving between them means. It is checked before anything runs,
// because an unknown subcommand or an ignored flag that quietly prints the list and exits
// 0 makes a typo look exactly like success.

// taskStates: waiting is still owed, but the next move is somebody else's ("submitted,
// awaiting a reply"). It stays on the list and is still delivered when due, because a date
// on a waiting task is the day to chase it.
var taskStates = []string{"open", "waiting", "done", "dropped"}

// TaskLiveSQL is the predicate for "still owed": open and waiting.
const TaskLiveSQL = "state IN ('open','waiting')"

func taskLive(state string) bool { return state == "open" || state == "waiting" }

func validTaskState(state string) error {
	if store.Contains(taskStates, state) {
		return nil
	}
	return fmt.Errorf("--state is one of %s, not %q", strings.Join(taskStates, ", "), state)
}

// taskSubFlags is every subcommand and the flags it reads. A flag a subcommand would ignore
// is refused: `task 214 --state dropped` once ran a list and exited 0.
var taskSubFlags = map[string][]string{
	"add":    {"title", "body", "owner", "scope", "when", "notify", "allow-secret"},
	"list":   {"owner", "state", "all"},
	"due":    {},
	"done":   {},
	"drop":   {},
	"wait":   {},
	"reopen": {},
	"snooze": {"until"},
	"set":    {"title", "body", "owner", "scope", "when", "notify", "allow-secret"},
	"todo":   {"out", "allow-outside", "allow-tracked"},
}

// TaskTakesID is the subcommands that act on one task and need its id.
func TaskTakesID(sub string) bool {
	switch sub {
	case "done", "drop", "wait", "reopen", "snooze", "set":
		return true
	}
	return false
}

func taskSubs() []string {
	out := make([]string, 0, len(taskSubFlags))
	for k := range taskSubFlags {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CheckTaskArgs refuses an unknown subcommand and any flag the subcommand does not read.
func CheckTaskArgs(sub string, flags []string) error {
	allowed, ok := taskSubFlags[sub]
	if !ok {
		return fmt.Errorf("task has no %q. It takes one of: %s", sub, strings.Join(taskSubs(), ", "))
	}
	for _, f := range flags {
		if !store.Contains(allowed, f) {
			return fmt.Errorf("task %s does not take --%s%s", sub, f, taskFlagHint(f))
		}
	}
	return nil
}

// taskFlagHint points a refused flag at the subcommand that does read it, for the two
// mistakes people actually make: changing a state with --state, and editing with add.
func taskFlagHint(flag string) string {
	switch flag {
	case "state":
		return "; to change a task's state use task done|drop|wait|reopen <id>"
	case "title", "body", "owner", "scope", "when":
		return "; to edit a task use task set <id> --" + flag
	}
	return ""
}

// taskStateFor is the state a state-changing subcommand moves a task to.
func taskStateFor(sub string) string {
	switch sub {
	case "done":
		return "done"
	case "drop":
		return "dropped"
	case "wait":
		return "waiting"
	case "reopen":
		return "open"
	}
	return ""
}

// checkTaskOwner is the owner rule. The refusal explains both values because they decide
// how the task is shown: as context for the person, or as an instruction to the agent.
func checkTaskOwner(owner string) error {
	if owner != "user" && owner != "agent" {
		return fmt.Errorf("--owner is 'user' (what they owe, shown as context) or 'agent' "+
			"(what you owe, shown as the session's first instruction), not %q", owner)
	}
	return nil
}

// clearsSchedule: `task set --when none` removes the schedule, and so do "never" and an
// empty --when.
func clearsSchedule(when string) bool {
	w := strings.ToLower(strings.TrimSpace(when))
	return w == "" || w == "none" || w == "never"
}

// taskEdit is a `task set` worked out against the task as it stands: the final value of
// every editable column, whether the schedule was replaced, and what changed.
type taskEdit struct {
	Title, Body, Owner, Scope string
	Notify                    int
	Resched                   bool
	Kind, Unit, At, Days      string
	Says, TZ                  string
	N                         int
	Due                       interface{} // nil when the schedule was cleared
	Changed                   []string
}

func (e *taskEdit) message(id int64) string {
	msg := fmt.Sprintf("#%d updated (%s): %s", id, strings.Join(e.Changed, ", "), e.Title)
	if e.Resched {
		if d, ok := e.Due.(int64); ok {
			msg += "\n  due " + timefmt.ISO(d)
		} else {
			msg += "\n  no schedule now: it is a list item"
		}
	}
	return msg
}

// planTaskEdit works out `task set`. Only the flags named in a.Fields change anything, so
// `--body ""` clears a body and an absent --body leaves it alone; with none named it
// refuses rather than reporting an edit that did nothing.
func planTaskEdit(a TaskArgs, cur *Task, now time.Time) (*taskEdit, error) {
	named := map[string]bool{}
	for _, f := range a.Fields {
		named[f] = true
	}
	e := &taskEdit{Title: cur.Title, Body: cur.Body, Owner: cur.Owner, Scope: cur.Scope,
		Notify: cur.Notify}
	// The order of these steps is the order of e.Changed, which the output prints.
	steps := []struct {
		flag  string
		apply func() error
	}{
		{"title", func() error { return e.setTitle(a.Title) }},
		{"body", func() error { e.Body = a.Body; return nil }},
		{"owner", func() error { return e.setOwner(a.Owner) }},
		{"scope", func() error { return e.setScope(a.Scope) }},
		{"notify", func() error { e.Notify = store.BoolToInt(a.Notify); return nil }},
		{"when", func() error { return e.setWhen(a.When, now) }},
	}
	for _, step := range steps {
		if !named[step.flag] {
			continue
		}
		if err := step.apply(); err != nil {
			return nil, err
		}
		e.Changed = append(e.Changed, step.flag)
	}
	if len(e.Changed) == 0 {
		return nil, fmt.Errorf("task set changes nothing without one of: --title, --body, " +
			"--owner, --scope, --when (\"none\" clears it), --notify")
	}
	if err := textguard.GuardText(a.AllowSecret, e.Title, e.Body); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *taskEdit) setTitle(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("--title cannot be empty: a task needs a title")
	}
	if err := textguard.CheckLine("--title", title); err != nil {
		return err
	}
	e.Title = title
	return nil
}

func (e *taskEdit) setOwner(owner string) error {
	if err := checkTaskOwner(owner); err != nil {
		return err
	}
	e.Owner = owner
	return nil
}

func (e *taskEdit) setScope(scope string) error {
	if strings.TrimSpace(scope) == "" {
		return fmt.Errorf("--scope cannot be empty")
	}
	if err := textguard.CheckLine("--scope", scope); err != nil {
		return err
	}
	e.Scope = scope
	return nil
}

// setWhen replaces the schedule. A clearing value leaves every schedule field empty and
// Due nil, which is what turns the task back into a plain list item.
func (e *taskEdit) setWhen(when string, now time.Time) error {
	e.Resched = true
	if clearsSchedule(when) {
		return nil
	}
	sc, err := schedule.ParseSchedule(when, now)
	if err != nil {
		return err
	}
	if sc != nil {
		e.Kind, e.N, e.Unit, e.At, e.Days, e.Says, e.TZ =
			sc.Kind, sc.N, sc.Unit, sc.At, sc.Days, sc.Says, sc.TZ
		e.Due = sc.First
	}
	return nil
}
