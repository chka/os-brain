package store

import (
	"strings"
)

// SchemaDDL builds a new brain.db at the current SchemaVersion; on an older file it creates
// only the tables that are missing, and migrate adds the rest. SQLite keeps each CREATE
// statement's text, comments included, in sqlite_master, and the file format is shared with
// ns-brain so a brain can move between the editions: change this text only together with a
// migration step, never to tidy it.
const SchemaDDL = `
CREATE TABLE IF NOT EXISTS meta (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scope TEXT NOT NULL DEFAULT 'global',
    mem_type TEXT NOT NULL DEFAULT 'note',
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'session',
    importance INTEGER NOT NULL DEFAULT 3,
    pinned INTEGER NOT NULL DEFAULT 0,
    use_count INTEGER NOT NULL DEFAULT 0,
    last_used INTEGER,
    project TEXT NOT NULL DEFAULT '',
    superseded_by INTEGER,
    -- when the thing HAPPENED, which is not when it was written down. An incident
    -- recorded a week later, or years later, is the normal case rather than the odd one.
    happened_on TEXT NOT NULL DEFAULT '',
    snooze_until INTEGER,
    review_on INTEGER,
    confidence TEXT NOT NULL DEFAULT 'reported',
    source_date TEXT NOT NULL DEFAULT '',
    verified_at INTEGER,
    -- the commit this claim was true against. confidence rates the source; this dates the
    -- claim to a tree, so a later session can prove nothing has moved instead of
    -- re-deriving the whole fact from the files.
    source_commit TEXT NOT NULL DEFAULT '',
    -- WHOSE claim this is, which is a different question from how good the source was.
    -- confidence discounts hearsay; trust records that a memory arrived from somewhere
    -- other than this project's own work, and recall says so rather than silently
    -- downranking it. Never multiplied into the score: confidence already occupies that
    -- axis, and stacking two would double-count.
    trust TEXT NOT NULL DEFAULT 'stated',
    -- When the claim STOPS being true, where that is known in advance. Distinct from
    -- snooze_until (hide it until) and review_on (nag me about it): after valid_to the
    -- claim is simply not the case any more.
    valid_from TEXT NOT NULL DEFAULT '',
    valid_to TEXT NOT NULL DEFAULT '',
    -- Which session wrote this. Two agents working one project is the normal case, and
    -- without this the store cannot tell one agent contradicting itself over time from
    -- two agents disagreeing right now. Empty means written before sessions were tracked.
    session TEXT NOT NULL DEFAULT '',
    -- The ladder (v16). Repetition is authority: stated once is a position, restated
    -- after a RECORDED objection is hardened, restated in a different session is law.
    -- All three are derived from what the store can show and none is ever set by hand.
    -- Defaults describe a memory written before the ladder existed: stated once, never
    -- argued, and last_stated_at 0 meaning unknown rather than a backfilled guess.
    stated_count INTEGER NOT NULL DEFAULT 1,
    last_stated_at INTEGER NOT NULL DEFAULT 0,
    hardened_at INTEGER,
    law_at INTEGER,
    -- the utterance this was distilled from: provenance a later reader can re-derive
    from_utterance INTEGER,
    -- what was NOT chosen, and why. The field every repository loses and everybody wants
    -- six months later.
    rejected TEXT NOT NULL DEFAULT '',
    hash TEXT UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS leads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    company TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT '',
    channel TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    segment TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'new',
    tier TEXT NOT NULL DEFAULT '',
    value_est INTEGER NOT NULL DEFAULT 0,
    next_action TEXT NOT NULL DEFAULT '',
    next_date TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL DEFAULT '',
    ts INTEGER NOT NULL,
    scope TEXT NOT NULL DEFAULT 'global',
    kind TEXT NOT NULL DEFAULT 'note',
    actor TEXT NOT NULL DEFAULT 'claude',
    summary TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    -- actor says WHAT wrote it and has only ever held 'claude'; session says WHICH one.
    session TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS mem_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL DEFAULT '',
    mem_id INTEGER NOT NULL,
    scope TEXT NOT NULL,
    mem_type TEXT NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    source_date TEXT NOT NULL DEFAULT '',
    source_commit TEXT NOT NULL DEFAULT '',
    confidence TEXT NOT NULL DEFAULT 'reported',
    importance INTEGER NOT NULL DEFAULT 3,
    pinned INTEGER NOT NULL DEFAULT 0,
    superseded_by INTEGER,
    action TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    archived_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tombstones (
    hash TEXT PRIMARY KEY,
    project TEXT NOT NULL DEFAULT '',
    scope TEXT NOT NULL,
    title TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    forgotten_at INTEGER NOT NULL
);

-- Numbers that only mean something as a series. A memory answers "what is true";
-- a measure answers "what was the value, and which way is it going".
CREATE TABLE IF NOT EXISTS measures (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL DEFAULT '',
    scope TEXT NOT NULL DEFAULT 'global',
    key TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    value REAL NOT NULL,
    unit TEXT NOT NULL DEFAULT '',
    taken_on TEXT NOT NULL DEFAULT '',
    taken_at INTEGER NOT NULL,
    source TEXT NOT NULL DEFAULT 'session',
    confidence TEXT NOT NULL DEFAULT 'reported',
    ref TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

-- One row per session that has opened this project's brain. It exists so that a session
-- which learned things and stored none stops being INVISIBLE: without it, that session and
-- one that learned nothing look identical, and the difference is the whole failure mode the
-- write path has. Counts are never stored here, they are queried from memories and events
-- at read time, so the row cannot drift from what the store actually holds.
CREATE TABLE IF NOT EXISTS session_log (
    project TEXT NOT NULL DEFAULT '',
    session TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    last_seen INTEGER NOT NULL,
    -- git HEAD when the session first briefed, so its work can be counted in commits
    start_commit TEXT NOT NULL DEFAULT '',
    ended_at INTEGER,
    -- when a later session was told about this one. Reporting the same silent session
    -- every morning for a week is how a warning becomes wallpaper.
    reported_at INTEGER,
    PRIMARY KEY (project, session)
);

-- v2 step 2: what the user said, verbatim, before the turn was answered. Append only and
-- NEVER deduped: the repetition is the signal the ladder reads. Fenced out of ordinary
-- ranking so raw speech cannot outrank a distilled fact.
CREATE TABLE IF NOT EXISTS utterances (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL DEFAULT '',
    ts INTEGER NOT NULL,
    text TEXT NOT NULL,
    -- command | taste | directive | exhibit. The classifier is lexical, narrow and fails
    -- toward not-a-rule: storing a one-off AS a rule silently rewrites every future
    -- session, while missing one costs a single restatement the ladder then catches.
    class TEXT NOT NULL DEFAULT 'command',
    class_why TEXT NOT NULL DEFAULT '',
    -- inference is English-only, and this column is what makes that reviewable rather
    -- than a standing assumption: it counts what could not be classified.
    lang TEXT NOT NULL DEFAULT 'en',
    session TEXT NOT NULL DEFAULT ''
);

-- v2 step 3: an objection is a row with a date and an author, not a comment. Recording
-- one is the price of arguing, and it is what stops the third session reconstructing an
-- argument that was already won twice.
CREATE TABLE IF NOT EXISTS objections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL DEFAULT '',
    memory_id INTEGER NOT NULL,
    why TEXT NOT NULL,
    raised_at INTEGER NOT NULL,
    session TEXT NOT NULL DEFAULT '',
    answered_at INTEGER,
    answer TEXT NOT NULL DEFAULT ''
);

-- Tasks: what is owed, by whom, and when. A memory is a fact and a task is not - it has
-- state, an owner, an order and a clock, and none of those belong to something that is
-- true. The commitment memory type stays for the rows that already use it; new work
-- files here.
--
-- Delivery is PULL. Nothing in this schema is read by a scheduler, because there is not
-- one: the hooks that already run on session open, on every user message and at turn end
-- ask what is due, so a reminder reaches a session that is running without anything
-- acting unattended. Latency is bounded by the next keystroke, not by the clock.
CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    -- what the agent owes is the session's opening instruction; what the user owes is
    -- context in front of them. Same row, different rendering.
    owner TEXT NOT NULL DEFAULT 'user',        -- user | agent
    scope TEXT NOT NULL DEFAULT 'global',
    state TEXT NOT NULL DEFAULT 'open',        -- open | done | dropped
    position REAL NOT NULL DEFAULT 0,
    -- The schedule, five closed forms. An RRULE parser is a month of edge cases whose
    -- failure mode is a reminder that silently never fires.
    sched_kind TEXT NOT NULL DEFAULT '',       -- once | every | daily | weekly | monthly
    sched_n INTEGER NOT NULL DEFAULT 0,
    sched_unit TEXT NOT NULL DEFAULT '',       -- minutes | hours | days | weeks
    sched_at TEXT NOT NULL DEFAULT '',         -- 'HH:MM' local
    sched_days TEXT NOT NULL DEFAULT '',       -- 'mon,thu', or a day of the month
    sched_says TEXT NOT NULL DEFAULT '',       -- what was asked for, rendered back
    -- stamped at creation and never inferred later: '17:00' is a different instant after
    -- a flight, and recomputing in today's zone would silently move every reminder.
    tz TEXT NOT NULL DEFAULT '',
    due_at INTEGER,
    snooze_until INTEGER,
    -- delivery bookkeeping, so lateness and the nag rule can be honest
    last_fired_at INTEGER,
    fire_count INTEGER NOT NULL DEFAULT 0,
    offers_since_ack INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0,
    notify INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    closed_at INTEGER,
    session TEXT NOT NULL DEFAULT ''
);

-- Optional per-key band. Absent means a series is recorded but never judged: the tool
-- says nothing about whether a number is good, because most keys have no such answer.
CREATE TABLE IF NOT EXISTS measure_limits (
    project TEXT NOT NULL DEFAULT '',
    key TEXT NOT NULL,
    low REAL,
    high REAL,
    better TEXT NOT NULL DEFAULT '',
    unit TEXT NOT NULL DEFAULT '',
    why TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (project, key)
);
`

// The index is an own-content FTS5 table written explicitly by FTSPut/FTSDel, not an
// external-content table driven by triggers: unicode61 does not fold Greek tonos at any
// remove_diacritics setting, and a SQL trigger cannot call StripAccents. Consequence:
// every write must go through this package.
const ftsDDL = `CREATE VIRTUAL TABLE IF NOT EXISTS mem_fts USING fts5(
    title, body, tokenize="unicode61 remove_diacritics 2"
)`

// Kept out of SchemaDDL on purpose. An index names columns, so it can only be created
// once the schema is known to have them: CREATE INDEX IF NOT EXISTS still evaluates its
// column list, and an index over superseded_by once made a v1 database fail to open,
// taking down with it the migration that would have added the column.
var indexes = []string{
	"CREATE INDEX IF NOT EXISTS idx_mem_project ON memories(project)",
	"CREATE INDEX IF NOT EXISTS idx_event_project ON events(project)",
	"CREATE INDEX IF NOT EXISTS idx_mem_scope ON memories(scope)",
	"CREATE INDEX IF NOT EXISTS idx_mem_type ON memories(mem_type)",
	"CREATE INDEX IF NOT EXISTS idx_mem_live ON memories(superseded_by)",
	"CREATE INDEX IF NOT EXISTS idx_lead_status ON leads(status)",
	"CREATE INDEX IF NOT EXISTS idx_event_ts ON events(ts)",
	"CREATE INDEX IF NOT EXISTS idx_event_scope ON events(scope)",
	"CREATE INDEX IF NOT EXISTS idx_measure_key ON measures(project, key, taken_at)",
	"CREATE INDEX IF NOT EXISTS idx_mem_session ON memories(project, session)",
	"CREATE INDEX IF NOT EXISTS idx_session_seen ON session_log(project, last_seen)",
	"CREATE INDEX IF NOT EXISTS idx_task_due ON tasks(project, state, due_at)",
	"CREATE INDEX IF NOT EXISTS idx_utterance_ts ON utterances(project, ts)",
	"CREATE INDEX IF NOT EXISTS idx_objection_mem ON objections(project, memory_id)",
}

// historyCols are the memory columns archive copies into mem_history before a change.
var historyCols = []string{"scope", "mem_type", "title", "body", "source", "source_date",
	"source_commit", "confidence", "importance", "pinned", "superseded_by"}

// splitOnSemicolon splits a DDL block into statements, stripping `--` line comments first.
// Splitting naively is wrong: a schema comment once contained a semicolon, which cut a
// CREATE TABLE in half. The shared schema then failed to build, the shared store would not
// open, and the brief silently fell back to the built-in axioms with no error anywhere,
// the class of failure this codebase treats as worse than a crash.
func splitOnSemicolon(s string) []string {
	var clean strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		clean.WriteString(line)
		clean.WriteString("\n")
	}
	return strings.Split(clean.String(), ";")
}
