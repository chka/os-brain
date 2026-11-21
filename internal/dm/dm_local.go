package dm

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// The registry of live sessions and the messages between them are kept in the spine, the one
// file every project on the machine opens. A project that has opted out of the shared layer
// takes no part: it neither registers nor sends, because either would put its name into a
// file every other project reads, which is the thing the opt-out exists to stop.

// dmSpine opens the spine for writing, or refuses for a project that opted out of it or
// whose name another project already holds there.
func (s *Store) dmSpine() (*sql.DB, error) {
	if !s.GlobalEnabled() || !s.IndexEnabled() {
		return nil, fmt.Errorf("direct messages go through the shared spine, and this project " +
			"has opted out of it (\"global\" or \"index\" is false in brain.json), so it can " +
			"neither send nor be reached")
	}
	g, err := s.WritableSpine()
	if err != nil {
		return nil, err
	}
	if err := s.OwnsName(g); err != nil {
		s.CloseGlobal(g)
		return nil, err
	}
	return g, nil
}

// Heartbeat records that this session is alive. A shell is never registered (see
// ClaudeSession).
func (s *Store) Heartbeat(b Beat) error {
	id := s.Env.SessionID()
	if !ClaudeSession(id) {
		return nil
	}
	g, err := s.dmSpine()
	if err != nil {
		return err
	}
	defer s.CloseGlobal(g)
	if b.Listening {
		return s.listenerBeat(g, id, b)
	}
	// A hook's beat revives an ended row: on resume the same id comes back in a new process,
	// and a session that is plainly running must not stay marked gone.
	now := timefmt.Now()
	_, err = g.Exec("INSERT INTO live_sessions(session,project,private,machine,pid,started_at,"+
		"last_seen) VALUES(?,?,?,?,?,?,?) ON CONFLICT(session) DO UPDATE SET "+
		"project=excluded.project, private=excluded.private, machine=excluded.machine, "+
		"pid=CASE WHEN excluded.pid>0 THEN excluded.pid ELSE live_sessions.pid END, "+
		"last_seen=excluded.last_seen, ended_at=NULL",
		id, s.Env.Project, store.BoolToInt(s.Env.Private), b.Machine, b.PID, now, now)
	return err
}

// listenerBeat says the PROCESS is listening, never that a session is alive: after /clear its
// own session id has ended, and reviving that row would list a ghost. It inserts its row only
// if no hook has registered it yet, and otherwise stamps every open session of its process.
func (s *Store) listenerBeat(g *sql.DB, id string, b Beat) error {
	now := timefmt.Now()
	if _, err := g.Exec("INSERT INTO live_sessions(session,project,private,machine,pid,"+
		"started_at,last_seen,listening_at) VALUES(?,?,?,?,?,?,?,?) "+
		"ON CONFLICT(session) DO NOTHING", id, s.Env.Project, store.BoolToInt(s.Env.Private),
		b.Machine, b.PID, now, now, now); err != nil {
		return err
	}
	_, err := g.Exec("UPDATE live_sessions SET listening_at=? WHERE ended_at IS NULL AND "+
		"(session=? OR (? > 0 AND session IN "+samePIDSessions+"))",
		now, id, b.PID, b.Machine, b.PID, s.Env.Project, id)
	return err
}

// LiveSessions is every session this project may address: its own, and any other project's
// that is not private. A private project's sessions are simply absent to everybody else, so a
// message to one refuses exactly as a typo does and reveals nothing about which names exist.
func (s *Store) LiveSessions() ([]LiveSession, error) {
	g, err := s.dmSpine()
	if err != nil {
		return nil, err
	}
	defer s.CloseGlobal(g)
	cutoff := timefmt.Now() - LiveHorizon
	// The account-wide flag mutes every session on this spine. Read it BEFORE the cursor
	// below: SQLite gives this handle one connection, and a second query while the rows are
	// open would wait for a connection it is holding - a deadlock.
	dnd := int64(0)
	if s.localDND(g) {
		dnd = timefmt.Now()
	}
	rows, err := g.Query("SELECT session, project, machine, pid, started_at, last_seen, "+
		"COALESCE(listening_at,0), COALESCE(ended_at,0), COALESCE(muted_at,0) FROM live_sessions "+
		"WHERE (last_seen >= ? OR listening_at >= ?) AND (private=0 OR project=?)",
		cutoff, cutoff, s.Env.Project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveSession
	for rows.Next() {
		var l LiveSession
		if err := rows.Scan(&l.Session, &l.Project, &l.Machine, &l.PID, &l.StartedAt,
			&l.LastSeen, &l.ListeningAt, &l.EndedAt, &l.MutedAt); err != nil {
			return nil, err
		}
		if l.MutedAt == 0 {
			l.MutedAt = dnd
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SendDM stores one message for a session. A sender that is not a Claude session (a shell)
// is recorded with no session, so the recipient is never offered a reply address that
// reaches nobody.
func (s *Store) SendDM(to LiveSession, body, kind string, allowSecret bool) (int64, error) {
	if err := textguard.GuardText(allowSecret, body); err != nil {
		return 0, err
	}
	if kind == "" {
		kind = "text"
	}
	g, err := s.dmSpine()
	if err != nil {
		return 0, err
	}
	defer s.CloseGlobal(g)
	s.sweepDMs(g)
	from := s.Env.SessionID()
	if !ClaudeSession(from) {
		from = ""
	}
	res, err := g.Exec("INSERT INTO dms(to_session,to_project,from_session,from_project,"+
		"from_machine,body,created_at,kind) VALUES(?,?,?,?,?,?,?,?)",
		to.Session, to.Project, from, s.Env.Project, store.MachineName(), body, timefmt.Now(), kind)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// samePIDSessions is the /clear fallback: the sessions of the Claude Code process a listener
// belongs to, whose own session id went stale when /clear handed the process a new one. A
// pid is not an identity - a crashed session's row stays open and its pid comes back on
// another process - so a row only counts while it is open, in the claimer's own project, and
// seen since the claimer's session started. A row whose process died before the claimer's
// process existed cannot pass the last: a reused pid needs its first owner dead.
// Parameters: machine, pid, project, claimer's session.
const samePIDSessions = "(SELECT session FROM live_sessions WHERE machine=? AND pid=? " +
	"AND ended_at IS NULL AND project=? AND last_seen >= " +
	"(SELECT started_at FROM live_sessions WHERE session=?))"

func (s *Store) ClaimDMs(via string, b Beat) ([]DM, error) {
	id := s.Env.SessionID()
	if !ClaudeSession(id) {
		return nil, nil
	}
	if err := s.Heartbeat(b); err != nil {
		return nil, err
	}
	g, err := s.dmSpine()
	if err != nil {
		return nil, err
	}
	defer s.CloseGlobal(g)
	now := timefmt.Now()
	muted := 0
	if s.localMuted(g, id) {
		muted = 1
	}
	// Claimed and returned in one statement, so two hooks racing on the same session (a tool
	// hook and the listener, say) cannot both deliver one message. A rejected message is never
	// delivered; a remote-do always is, as a request the user approves in that session; a
	// plain message is held for the inbox when the session is muted.
	rows, err := g.Query("UPDATE dms SET delivered_at=?, delivered_via=? WHERE delivered_at "+
		"IS NULL AND created_at > ? AND rejected_at IS NULL AND (kind='instruction' OR ?=0) "+
		"AND to_project=? AND (to_session=? OR (? > 0 AND to_session IN "+samePIDSessions+")) "+
		"RETURNING id, to_session, to_project, from_session, from_project, from_machine, "+
		"body, created_at, kind, accepted_at, rejected_at",
		now, via, now-int64(DMExpiryDays)*86400, muted, s.Env.Project, id, b.PID,
		b.Machine, b.PID, s.Env.Project, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DM
	for rows.Next() {
		d, err := scanClaimedDM(rows)
		if err != nil {
			return nil, err
		}
		at := now
		d.DeliveredAt, d.DeliveredVia = &at, via
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, rows.Err()
}

// scanClaimedDM reads one row of ClaimDMs' RETURNING clause.
func scanClaimedDM(rows *sql.Rows) (DM, error) {
	var d DM
	var accepted, rejected sql.NullInt64
	if err := rows.Scan(&d.ID, &d.ToSession, &d.ToProject, &d.FromSession, &d.FromProject,
		&d.FromMachine, &d.Body, &d.CreatedAt, &d.Kind, &accepted, &rejected); err != nil {
		return DM{}, err
	}
	if accepted.Valid {
		v := accepted.Int64
		d.AcceptedAt = &v
	}
	if rejected.Valid {
		v := rejected.Int64
		d.RejectedAt = &v
	}
	// Every session on this spine runs as the account that owns it (the file is 0600). That
	// says who could write the row, not that the person asked for it: RenderDMs delivers a
	// remote-do as a request either way.
	d.SamePerson = true
	return d, nil
}

// SentDMs is what this project sent, newest first, for `dm --sent`.
func (s *Store) SentDMs(n int) ([]DM, error) {
	g, err := s.dmSpine()
	if err != nil {
		return nil, err
	}
	defer s.CloseGlobal(g)
	rows, err := g.Query("SELECT id, to_session, to_project, from_session, from_project, "+
		"from_machine, body, created_at, delivered_at, delivered_via FROM dms "+
		"WHERE from_project=? ORDER BY id DESC LIMIT ?", s.Env.Project, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DM
	for rows.Next() {
		var d DM
		if err := rows.Scan(&d.ID, &d.ToSession, &d.ToProject, &d.FromSession, &d.FromProject,
			&d.FromMachine, &d.Body, &d.CreatedAt, &d.DeliveredAt, &d.DeliveredVia); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// EndSession marks this session gone, so `sessions` stops offering it as an address.
func (s *Store) EndSession() error {
	id := s.Env.SessionID()
	if !ClaudeSession(id) {
		return nil
	}
	g, err := s.dmSpine()
	if err != nil {
		return err
	}
	defer s.CloseGlobal(g)
	_, err = g.Exec("UPDATE live_sessions SET ended_at=?, listening_at=NULL WHERE session=?",
		timefmt.Now(), id)
	return err
}

// localMuted reports do-not-disturb for one session: its own muted_at, or the account-wide flag.
func (s *Store) localMuted(g *sql.DB, session string) bool {
	var n int
	_ = g.QueryRow("SELECT COUNT(*) FROM live_sessions WHERE session=? AND muted_at IS NOT NULL",
		session).Scan(&n)
	return n > 0 || s.localDND(g)
}

// localDND reports the account-wide do-not-disturb flag.
func (s *Store) localDND(g *sql.DB) bool {
	var v string
	_ = g.QueryRow("SELECT v FROM gmeta WHERE k='dm_dnd'").Scan(&v)
	return v == "1"
}

// AcceptDM and RejectDM complete the Messenger the `dm --accept/--reject` flags call. On a
// single-account spine nothing is held for an accept: a remote-do is delivered as a request
// the user approves where it lands, so there is nothing to decide, and saying so is the
// fail-closed answer.
func (s *Store) AcceptDM(id int64) error { return s.resolveLocal(id, true) }
func (s *Store) RejectDM(id int64) error { return s.resolveLocal(id, false) }

func (s *Store) resolveLocal(id int64, accept bool) error {
	verb := "accept"
	if !accept {
		verb = "reject"
	}
	return fmt.Errorf("nothing to %s: #%d — on a single-user brain a remote-do is never held "+
		"for an accept; it arrives as a request, and the user approves or ignores it in the "+
		"session it reaches", verb, id)
}

// SetMute turns do-not-disturb on or off: for this session, or with everywhere for the whole
// account (every session on this spine).
func (s *Store) SetMute(everywhere, on bool) error {
	g, err := s.dmSpine()
	if err != nil {
		return err
	}
	defer s.CloseGlobal(g)
	if everywhere {
		v := "0"
		if on {
			v = "1"
		}
		_, err := g.Exec("INSERT OR REPLACE INTO gmeta(k,v) VALUES('dm_dnd',?)", v)
		return err
	}
	var at interface{}
	if on {
		at = timefmt.Now()
	}
	_, err = g.Exec("UPDATE live_sessions SET muted_at=? WHERE session=?", at, s.Env.SessionID())
	return err
}

// DMInbox is what mute held back: plain messages to this session, undelivered, newest first.
func (s *Store) DMInbox() ([]DM, error) {
	g, err := s.dmSpine()
	if err != nil {
		return nil, err
	}
	defer s.CloseGlobal(g)
	rows, err := g.Query("SELECT id, to_session, to_project, from_session, from_project, "+
		"from_machine, body, created_at, kind FROM dms WHERE to_session=? AND delivered_at "+
		"IS NULL AND rejected_at IS NULL ORDER BY id DESC", s.Env.SessionID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DM
	for rows.Next() {
		var d DM
		if err := rows.Scan(&d.ID, &d.ToSession, &d.ToProject, &d.FromSession, &d.FromProject,
			&d.FromMachine, &d.Body, &d.CreatedAt, &d.Kind); err != nil {
			return nil, err
		}
		d.SamePerson = true
		out = append(out, d)
	}
	return out, rows.Err()
}

// sweepDMs keeps the two tables the size of what is live: a delivered message has done its
// job, and a session unseen for a week is not coming back under that id. A remote-do gets no
// exemption: nothing here is ever held for an accept, and sparing an unaccepted one would
// keep every remote-do ever sent.
func (s *Store) sweepDMs(g *sql.DB) {
	week := timefmt.Now() - int64(DMExpiryDays)*86400
	_, _ = g.Exec("DELETE FROM dms WHERE created_at < ? OR delivered_at < ?", week, week)
	_, _ = g.Exec("DELETE FROM live_sessions WHERE last_seen < ?", week)
}
