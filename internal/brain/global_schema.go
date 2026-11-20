package brain

// globalSchemaDDL is the spine at the current GlobalSchemaVersion. Every statement is
// CREATE ... IF NOT EXISTS, so it runs on every open; columns added to an existing table
// arrive through migrateGlobal instead.
const globalSchemaDDL = `
CREATE TABLE IF NOT EXISTS gmeta (k TEXT PRIMARY KEY, v TEXT NOT NULL);

-- Every default axiom this spine has been offered, by its text. A default is inserted the
-- first time a binary that ships it opens the spine and never again, so one an owner removed
-- stays removed and one added in a later release still arrives (offerDefaultAxioms).
CREATE TABLE IF NOT EXISTS axioms_offered (text TEXT PRIMARY KEY, offered_at INTEGER NOT NULL);

-- The one authoritative, propagating layer in the system: read live by every project, so a
-- wrong row governs all of them at once. Any session in any project can write here, which
-- is a deliberate trade (one developer, no review board to convene) and not a reason to
-- leave it anonymous. origin_project/origin_session say who, and seeded marks the core set
-- that ships with the binary so removing one takes an explicit --force rather than an id.
CREATE TABLE IF NOT EXISTS axioms (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    text TEXT NOT NULL UNIQUE,
    rationale TEXT NOT NULL DEFAULT '',
    origin_project TEXT NOT NULL DEFAULT '',
    origin_session TEXT NOT NULL DEFAULT '',
    seeded INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    edited_project TEXT NOT NULL DEFAULT '',
    edited_session TEXT NOT NULL DEFAULT '',
    edited_at INTEGER NOT NULL DEFAULT 0
);

-- The index answers "who might already have solved this?". It carries each project's own
-- high-level spec and never any memory contents.
CREATE TABLE IF NOT EXISTS projects (
    -- an opaque hash of the project directory, never the directory. Identity is all the
    -- index needs; the path is one client's layout in a file another client can read.
    root_id TEXT PRIMARY KEY,
    -- declared by the project itself: keep my rows out of everybody else's
    -- --all-projects. Held here because the projects that must honour it cannot read
    -- this project's brain.json.
    private INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    does TEXT NOT NULL DEFAULT '',
    provides TEXT NOT NULL DEFAULT '',
    keywords TEXT NOT NULL DEFAULT '',
    stack TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT '',
    -- 'described' shares the spec; 'listed' shares only that the project exists and
    -- where. A project whose purpose is itself sensitive sets listed: an honest
    -- description of a medical brain is medical information.
    visibility TEXT NOT NULL DEFAULT 'described',
    scopes TEXT NOT NULL DEFAULT '',
    memories INTEGER NOT NULL DEFAULT 0,
    spec_updated_at INTEGER NOT NULL DEFAULT 0,
    last_seen INTEGER NOT NULL
);

-- Defaults a new project inherits. Unlike axioms these are overridable locally; unlike
-- project preferences they outlive the project.
CREATE TABLE IF NOT EXISTS standards (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    category TEXT NOT NULL DEFAULT 'general',
    text TEXT NOT NULL UNIQUE,
    rationale TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    edited_by TEXT NOT NULL DEFAULT '',
    edited_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    to_project TEXT NOT NULL,
    from_project TEXT NOT NULL,
    body TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'note',
    created_at INTEGER NOT NULL,
    read_at INTEGER,
    acked_at INTEGER,
    expires_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_note_to ON notes(to_project, read_at);

-- A broadcast (to_project '*') is one row every project reads, so each recipient's read,
-- ack and accept is its own row here; on the note it was the first recipient's, for all.
CREATE TABLE IF NOT EXISTS note_reads (
    note_id INTEGER NOT NULL,
    project TEXT NOT NULL,
    read_at INTEGER,
    acked_at INTEGER,
    accepted_as INTEGER,
    PRIMARY KEY (note_id, project)
);

CREATE TABLE IF NOT EXISTS lessons (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project TEXT NOT NULL,
    project_root TEXT NOT NULL DEFAULT '',
    mem_type TEXT NOT NULL DEFAULT 'note',
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    confidence TEXT NOT NULL DEFAULT 'reported',
    why TEXT NOT NULL DEFAULT '',
    hash TEXT UNIQUE,
    published_at INTEGER NOT NULL
);

-- v10: sessions that are running now, and messages addressed to one of them. Here rather
-- than in a project store because a direct message crosses projects on this machine the way
-- a note does, and the spine is the one file every project opens. Keyed by the Claude Code
-- session id. pid is the Claude Code process, which outlives a session id across /clear.
CREATE TABLE IF NOT EXISTS live_sessions (
    session TEXT PRIMARY KEY,
    project TEXT NOT NULL,
    private INTEGER NOT NULL DEFAULT 0,
    machine TEXT NOT NULL DEFAULT '',
    pid INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL,
    last_seen INTEGER NOT NULL,
    listening_at INTEGER,
    ended_at INTEGER,
    muted_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_live_pid ON live_sessions(machine, pid);

CREATE TABLE IF NOT EXISTS dms (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    to_session TEXT NOT NULL,
    to_project TEXT NOT NULL,
    from_session TEXT NOT NULL DEFAULT '',
    from_project TEXT NOT NULL DEFAULT '',
    from_machine TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    delivered_at INTEGER,
    delivered_via TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'text',
    accepted_at INTEGER,
    rejected_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_dms_to ON dms(to_session) WHERE delivered_at IS NULL;
`

// globalFTSDDL is the search index over published lessons. The text is written into it
// explicitly, accent-stripped, for the same reason mem_fts is: unicode61 does not fold
// Greek tonos at any remove_diacritics setting, and a trigger cannot call StripAccents. It
// is kept out of globalSchemaDDL because splitting on semicolons would break a virtual
// table definition.
const globalFTSDDL = `CREATE VIRTUAL TABLE IF NOT EXISTS lesson_fts USING fts5(
    title, body, tokenize="unicode61 remove_diacritics 2")`
