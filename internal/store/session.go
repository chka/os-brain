package store

import (
	"fmt"
	"os"
	"strings"
)

// SessionID identifies the agent that is writing, so the store can tell one session
// contradicting itself over time from two sessions disagreeing right now. Those are
// different problems with different answers and until v13 nothing recorded which was
// which: events carried an actor column holding the constant "claude", and memories
// carried nothing at all.
//
// Claude Code exports CLAUDE_CODE_SESSION_ID. Outside it (a shell, cron, a test) there is
// no session, and inventing a stable-looking one would be worse than admitting it: the
// whole point of the column is to be believed. Fall back to the process identity, marked
// so nobody mistakes it for a real session.
func (e Env) SessionID() string {
	if strings.TrimSpace(e.Session) != "" {
		return e.Session
	}
	return SessionID()
}

// sessionEnvVars are where a host exports its session id, os-brain's own name first.
var sessionEnvVars = []string{"OS_BRAIN_SESSION_ID", "CLAUDE_CODE_SESSION_ID"}

func sessionFromEnvVars() string {
	for _, k := range sessionEnvVars {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// SessionFromEnv reports whether the host exported this session's id, so a hook payload's
// own id is not needed.
func SessionFromEnv() bool { return sessionFromEnvVars() != "" }

// SessionID is this process's session: the host's id when it exported one, otherwise the
// process identity marked "shell:".
func SessionID() string {
	if v := sessionFromEnvVars(); v != "" {
		return v
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("shell:%s:%d", host, os.Getpid())
}

// ShortSession is what gets rendered. Session ids are UUIDs and printing one in full
// costs a third of the line while adding nothing: the reader only needs to see whether
// two rows came from the same place.
func ShortSession(id string) string {
	if id == "" {
		return "?"
	}
	if strings.HasPrefix(id, "shell:") {
		return "shell"
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// SameSession answers the only question the column exists for. An unrecorded session is
// never treated as matching anything, including another unrecorded one: two blanks mean
// "we do not know", and reporting that as agreement is the failure this codebase calls
// answering the widest possible way when it cannot tell.
func SameSession(a, b string) bool {
	return a != "" && b != "" && a == b
}

// SessionKey namespaces a meta cursor per session. last_brief_at and last_pref_nudge were
// single global values, so two concurrent sessions clobbered each other: the "events since
// your last brief" window became whoever briefed most recently rather than what this
// session missed, and one session's preference nudge silently consumed the other's
// fifteen-minute budget.
//
// Every write also goes to the project's key (projectKey), which is what a session this
// brain has never seen falls back to: it is how a first brief in a new session still knows
// roughly when the project was last touched.
func (s *Store) SessionKey(base string) string {
	id := s.Env.SessionID()
	if id == "" {
		return base
	}
	return base + ":" + id
}

// SessionCursor prefers this session's own cursor and falls back to the project's, then to
// the bare key, so a session's first brief reports the project's last activity rather than
// the epoch.
func (s *Store) SessionCursor(base string) int64 {
	if v := s.MetaInt(s.SessionKey(base)); v > 0 {
		return v
	}
	if v := s.MetaInt(s.ProjectKey(base)); v > 0 {
		return v
	}
	return s.MetaInt(base)
}

// ProjectKey is the fallback a brand-new session reads, and it is per PROJECT. It was the
// bare key, and the shared store holds every project on the machine in one meta table: a
// session's first brief took "last briefed" from whichever project anybody opened last, so
// work left in one project was marked seen by a session somewhere else.
//
// The bare key is still READ, last, so a brain written before the per-project key existed
// keeps the marker it always had. It is no longer written.
func (s *Store) ProjectKey(base string) string {
	return base + "@" + s.Env.Project
}

// SessionString is sessionCursor for a value that is not a timestamp: this session's own
// record if it has one, otherwise whatever the last session to look left behind.
func (s *Store) SessionString(base string) string {
	if v := s.MetaStr(s.SessionKey(base)); v != "" {
		return v
	}
	if v := s.MetaStr(s.ProjectKey(base)); v != "" {
		return v
	}
	return s.MetaStr(base)
}

func (s *Store) SetSessionString(base, v string) {
	_, _ = s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)", s.SessionKey(base), v)
	_, _ = s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)", s.ProjectKey(base), v)
}

// SetSessionCursor writes both: the per-session key is what this session reads next time,
// the project's key is what a brand-new session in this project falls back to.
func (s *Store) SetSessionCursor(base string, ts int64) error {
	v := fmt.Sprint(ts)
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)", s.SessionKey(base), v); err != nil {
		return err
	}
	_, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)", s.ProjectKey(base), v)
	return err
}
