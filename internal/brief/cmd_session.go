package brief

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/tasks"
	"github.com/chka/os-brain/internal/timefmt"
)

// A session that did work below these thresholds and stored nothing is not evidence of
// anything: a two-minute look at a file is supposed to leave no memory. The point is to
// catch the session that ran for an hour, changed the tree, and left no trace.
const (
	silentSessionMinSeconds = 15 * 60
	silentSessionMinCommits = 1
)

// SessionReport is one session's footprint in this project. Counts are queried, never
// accumulated: a counter column would drift from the store it claims to describe, and
// the whole value of this record is that it cannot lie about what was written.
type SessionReport struct {
	Session   string `json:"session"`
	StartedAt int64  `json:"started_at"`
	LastSeen  int64  `json:"last_seen"`
	EndedAt   *int64 `json:"ended_at"`
	Seconds   int64  `json:"seconds"`
	Memories  int    `json:"memories"`
	Events    int    `json:"events"`
	Commits   int    `json:"commits"`
	Dirty     int    `json:"dirty_files"`
}

// Silent reports the failure this whole file exists for: a session that did enough work to
// have learned something and wrote nothing down. Deliberately conservative — it must be
// impossible to earn this label by doing nothing, or the warning becomes wallpaper and
// gets skipped along with the ones that matter.
func (r SessionReport) Silent() bool {
	return r.Memories == 0 && r.Events == 0 && r.didRealWork()
}

// Unlogged is the other half, and it went unreported for as long as this file has existed
// because Silent treats the two as interchangeable: one memory written and the hook goes
// quiet, whatever else was skipped. They answer different questions - what is TRUE, and
// what HAPPENED - and `os-brain open` answers "where did we leave off" from the log, so a
// project that only ever remembers has nothing for it to read. Same conservatism: it takes
// a session that did real work and stored something, so it can never be earned by idling.
func (r SessionReport) Unlogged() bool {
	return r.Memories > 0 && r.Events == 0 && r.didRealWork()
}

func (r SessionReport) didRealWork() bool {
	return r.Seconds >= silentSessionMinSeconds || r.Commits >= silentSessionMinCommits
}

// Line is the one-sentence account of a session: how long it ran, what it changed, what it
// stored. The order is deliberate — the work comes first and the storing comes last,
// because the sentence has to end on the number that is zero.
func (r SessionReport) Line() string {
	var parts []string
	if r.Seconds >= 60 {
		parts = append(parts, "ran "+timefmt.HumanDuration(r.Seconds))
	}
	if r.Commits > 0 {
		parts = append(parts, fmt.Sprintf("made %s", timefmt.Plural(r.Commits, "commit")))
	}
	if r.Dirty > 0 {
		parts = append(parts, fmt.Sprintf("left %s uncommitted", timefmt.Plural(r.Dirty, "file")))
	}
	if len(parts) == 0 {
		parts = append(parts, "did nothing measurable")
	}
	parts = append(parts, fmt.Sprintf("stored %s", timefmt.Plural(r.Memories, "memory")))
	if r.Events > 0 {
		parts = append(parts, fmt.Sprintf("logged %s", timefmt.Plural(r.Events, "event")))
	}
	return strings.Join(parts, ", ")
}

// touchSession records that this session is open, with the tree it started against. Called
// from brief, which is the one command guaranteed to run at the start of every session.
func (s *Store) touchSession() {
	if !s.TableExists("session_log") {
		return
	}
	id := s.Env.SessionID()
	if id == "" {
		return
	}
	now := timefmt.Now()
	// Only the first touch records the commit: the whole question later is what moved
	// while this session was open, and a start that follows the work answers nothing.
	_, _ = s.DB.Exec("INSERT INTO session_log(project,session,started_at,last_seen,start_commit) "+
		"VALUES(?,?,?,?,?) ON CONFLICT(project,session) DO UPDATE SET last_seen=excluded.last_seen",
		s.Env.Project, id, now, now, s.HeadCommit())
}

// sessionReport builds the footprint of one session from the store and from git.
func (s *Store) sessionReport(id string) (*SessionReport, error) {
	if !s.TableExists("session_log") || id == "" {
		return nil, nil
	}
	r := SessionReport{Session: id}
	var startCommit string
	err := s.DB.QueryRow("SELECT started_at, last_seen, ended_at, start_commit FROM session_log "+
		"WHERE project=? AND session=?", s.Env.Project, id).
		Scan(&r.StartedAt, &r.LastSeen, &r.EndedAt, &startCommit)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	end := r.LastSeen
	if r.EndedAt != nil && *r.EndedAt > end {
		end = *r.EndedAt
	}
	r.Seconds = end - r.StartedAt
	if r.Memories, err = s.Count("SELECT COUNT(*) FROM memories WHERE project=? AND session=?",
		s.Env.Project, id); err != nil {
		return nil, err
	}
	if r.Events, err = s.Count("SELECT COUNT(*) FROM events WHERE project=? AND session=?",
		s.Env.Project, id); err != nil {
		return nil, err
	}
	r.Commits = s.commitsSince(startCommit)
	if d, ok := s.gitFacts()["dirty_files"].(int); ok {
		r.Dirty = d
	}
	return &r, nil
}

// previousSession is the most recent session other than this one that has not yet been
// reported. Unreported rather than most-recent-full-stop, because the same silent session
// showing up in five consecutive briefs is noise, and the fifth reader has no more power
// to fix it than the first.
func (s *Store) previousSession() (*SessionReport, error) {
	if !s.TableExists("session_log") {
		return nil, nil
	}
	var id string
	err := s.DB.QueryRow("SELECT session FROM session_log WHERE project=? AND session<>? "+
		"AND reported_at IS NULL ORDER BY last_seen DESC LIMIT 1",
		s.Env.Project, s.Env.SessionID()).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return s.sessionReport(id)
}

func (s *Store) markSessionReported(id string) {
	_, _ = s.DB.Exec("UPDATE session_log SET reported_at=? WHERE project=? AND session=?",
		timefmt.Now(), s.Env.Project, id)
}

// commitsSince counts commits made on top of the tree a session started against. Best
// effort and never fatal, like every other use of git here: a number nobody can produce is
// better left out of the sentence than guessed at.
func (s *Store) commitsSince(commit string) int {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := fsguard.GitRead(ctx, s.Env.ProjectRoot(), "rev-list", "--count", commit+"..HEAD")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

type SessionEndArgs struct {
	JSON bool
}

// SessionEnd is the Stop hook. It closes this session's row and states its footprint.
//
// An instruction in CLAUDE.md is a wish and a hook is a mechanism, yet the write path was
// left as a wish: reads were hooked, writes were not, so a session that learned five things
// and stored none was indistinguishable from one that learned nothing. This cannot force a
// write (nothing can), but it ends the symmetry, and
// it does the half that actually pays in Brief, where the NEXT session is told what the
// last one failed to leave behind. A report at the end of a session reaches the session
// that already failed; a report at the start reaches one that can still act.
func (s *Store) SessionEnd(w io.Writer, a SessionEndArgs) error {
	id := s.Env.SessionID()
	if !s.TableExists("session_log") || id == "" {
		return nil
	}
	// A session that never briefed has no row, and inventing one at the end would record a
	// duration of zero for work that may have taken an hour.
	if _, err := s.DB.Exec("UPDATE session_log SET ended_at=?, last_seen=? "+
		"WHERE project=? AND session=?", timefmt.Now(), timefmt.Now(), s.Env.Project, id); err != nil {
		return err
	}
	// A reminder that came due DURING the turn belongs here rather than interrupting a
	// half-finished answer: eight seconds late is better than cutting somebody off.
	due := tasks.New(s.Store).DueBlock(true)

	r, err := s.sessionReport(id)
	if err != nil || r == nil {
		if due != "" {
			fmt.Fprintln(w, due)
		}
		return err
	}
	if a.JSON {
		return store.Out(w, true, "", map[string]interface{}{"session": r, "silent": r.Silent(),
			"due": due})
	}
	if due != "" {
		fmt.Fprintln(w, due)
	}
	switch {
	case r.Silent():
		writeSilentWarning(w, r)
	case r.Unlogged():
		writeUnloggedWarning(w, r)
	}
	// Otherwise silence. A hook that prints on every stop is a hook people mute, and with
	// it the one line that matters.
	return nil
}

func writeSilentWarning(w io.Writer, r *SessionReport) {
	fmt.Fprintf(w, "[brain] this session %s.\n", r.Line())
	fmt.Fprintln(w, "  Nothing was written down, so the next session starts where this one "+
		"did. If anything was learned:")
	fmt.Fprintln(w, "  os-brain remember --type <gotcha|decision|state> --title \"...\" --body \"...\"")
	fmt.Fprintln(w, "  os-brain log --kind <kind> \"<what happened>\"")
}

func writeUnloggedWarning(w io.Writer, r *SessionReport) {
	fmt.Fprintf(w, "[brain] this session %s, and logged nothing.\n", r.Line())
	fmt.Fprintln(w, "  The log is what `os-brain open` reads to answer \"where did "+
		"we leave off\":")
	fmt.Fprintln(w, "  os-brain log --kind <kind> \"<what happened>\"")
}
