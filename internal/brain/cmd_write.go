package brain

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Whose claim a memory carries. Deliberately three values and no more: the axiom that
// matters is "nothing arriving through the brain carries authority", and that is a yes/no
// about origin, not a spectrum.
var TrustLevels = []string{"stated", "imported", "external"}

const DefaultTrust = "stated"

func NormTrust(v string) string {
	v = lower(trim(v))
	if contains(TrustLevels, v) {
		return v
	}
	return DefaultTrust
}

// KnownTrust is what the write paths check before storing. NormTrust silently substitutes
// the default, which is right for reading a row somebody else wrote and wrong for a flag:
// `--trust extern` was once recorded as `stated`, the strongest value, with no complaint.
// Its neighbour `--confidence` has always refused an unknown value, and two enum flags on
// one command should not disagree about whether a typo is an error.
func KnownTrust(v string) bool {
	return trim(v) == "" || contains(TrustLevels, lower(trim(v)))
}

// NearDuplicateJ is the title overlap at which two memories in one scope are treated as
// being about the same thing. NearDuplicateMinTokens exists because Jaccard over a handful
// of tokens is too coarse to trust: 'prod db host' and 'prod db port' score 0.50 and are
// different facts. Measured against a real project's titles, which ran 7 to 47 tokens with
// a median of 15, the floor costs nothing on real content and removes the whole
// short-title false-positive class. Below it the check does not run: failing open on a
// HEURISTIC is right, because the fallback is the behaviour the store always had.
const (
	NearDuplicateJ         = 0.5
	NearDuplicateMinTokens = 5
)

// titleTokens is the set of words in a title once it is normalised the way identity is.
func titleTokens(title string) map[string]bool {
	toks := map[string]bool{}
	for _, t := range strings.Fields(NormTitle(title)) {
		toks[t] = true
	}
	return toks
}

// TitleOverlap is the measure NearDuplicates applies, as a pure function so the concurrent
// write check uses this definition rather than growing a second one. It returns 0 when
// either title is below the token floor, which is the fail-open the constant documents.
func TitleOverlap(a, b string) float64 {
	toks, other := titleTokens(a), titleTokens(b)
	if len(toks) < NearDuplicateMinTokens || len(other) < NearDuplicateMinTokens {
		return 0
	}
	return jaccard(toks, other)
}

type NearDuplicate struct {
	Score  float64
	Memory *Memory
}

// NearDuplicates finds live memories in this scope whose normalised title overlaps title.
// It can tell that two memories are ABOUT the same thing, but not that one NEGATES the
// other: that would take a language model, and pretending otherwise would be worse than
// saying so. Catching the same subject filed twice is most of the value; it is how a store
// turns into a pile.
//
// It takes the querier rather than reaching for s.DB: the store holds a single connection
// so SQLite writes serialise, which means a read issued through s.DB while a write
// transaction is open waits for a connection that transaction will never release. That
// deadlocks the process, not just the query.
func (s *Store) NearDuplicates(q querier, scope, title string) ([]NearDuplicate, error) {
	toks := titleTokens(title)
	if len(toks) < NearDuplicateMinTokens {
		return nil, nil
	}
	rows, err := scanMemories(q, "SELECT "+memoryCols+", 0.0 AS bm FROM memories m "+
		"WHERE m.project=? AND m.scope=? AND m.superseded_by IS NULL", s.Env.Project, scope)
	if err != nil {
		return nil, err
	}
	var out []NearDuplicate
	for _, r := range rows {
		other := titleTokens(r.Title)
		if len(other) < NearDuplicateMinTokens {
			continue
		}
		if j := jaccard(toks, other); j >= NearDuplicateJ {
			out = append(out, NearDuplicate{round2(j), r})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out, nil
}

type RememberArgs struct {
	Scope      string
	Type       string
	Title      string
	Body       string
	Source     string
	SourceDate string
	Confidence string
	Refs       []string
	Importance int
	Pin        bool
	Update     bool
	Supersedes int64
	ForkOK     bool
	// Everywhere is always refused here (see Remember). The flag exists because ns-brain's
	// team edition files such a memory above every project, and a local brain has nothing
	// above its one project.
	Everywhere     bool
	Happened       string
	SnoozeUntil    string
	ReviewOn       string
	NoReview       bool
	Trust          string
	ValidFrom      string
	ValidTo        string
	AllowDuplicate bool
	AllowSecret    bool
	// ClaimOK lets a text that SAYS it supersedes #N through without --supersedes N: the
	// sentence is history, or a move is carrying an old body across.
	ClaimOK bool
	JSON    bool
}

// newMemory is a remember request after every flag has been checked and parsed: what will
// be written unless a guard on the store's current contents refuses it.
type newMemory struct {
	title, body, confidence string
	hash                    string
	ts                      int64
	// commit is the tree a volatile claim was true against, empty for other types.
	commit             string
	happened           string
	snooze, review     int64
	reviewDay          string
	reviewDefaulted    bool
	validFrom, validTo string
}

// Remember stores a memory. Identity is project + scope + normalised title, so
// re-remembering a fact refines it instead of duplicating it, and it refuses to replace a
// differing body without --update, because that would destroy what is already there.
func (s *Store) Remember(w io.Writer, a RememberArgs) error {
	m, err := s.prepareMemory(a)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if !a.ClaimOK {
		if err := s.checkSupersedeClaims(tx, m.title, m.body, a.Supersedes); err != nil {
			return err
		}
	}
	if err := s.checkSupersedesTarget(tx, a.Supersedes); err != nil {
		return err
	}
	alert, err := s.concurrentAlert(tx, a.Scope, m.title, a.Type, m.ts)
	if err != nil {
		return err
	}
	if err := clearTombstone(tx, m.hash); err != nil {
		return err
	}
	existing, err := s.titleHolder(tx, m.hash, a.Supersedes)
	if err != nil {
		return err
	}
	if existing != nil {
		return s.refineExisting(w, tx, a, m, existing, alert)
	}
	// --supersedes already declares how the new memory relates to the old one, and
	// --update was handled on the exact-title path above; the near-duplicate guard is only
	// for a caller who has said neither.
	if !a.AllowDuplicate && a.Supersedes == 0 {
		if err := s.refuseNearDuplicate(tx, a.Scope, m.title, alert); err != nil {
			return err
		}
	}
	newID, err := s.insertMemory(tx, a, m)
	if err != nil {
		return err
	}
	sup, superseded, err := s.retractSuperseded(tx, a, newID, m.title, m.ts)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	human := fmt.Sprintf("remembered #%d [%s/%s] %s%s", newID, a.Scope, a.Type, m.title, sup)
	if alert != "" {
		human += "\n" + alert
	}
	if m.reviewDefaulted {
		// Said out loud: a write that silently acquired a review date is a surprise the
		// next session pays for when the brief asks about it.
		human += fmt.Sprintf("\n  review on %s (default for %s, %dd — --review <date> to move "+
			"it, --no-review if it is permanent)", m.reviewDay, a.Type, DefaultReviewDays)
	}
	return Out(w, a.JSON, human, map[string]interface{}{
		"id": newID, "supersedes": nullable(superseded), "concurrent_alert": alertJSON(alert),
		"review_on": nullable(m.review), "review_on_defaulted": m.reviewDefaulted})
}

// prepareMemory checks every flag of a remember and parses the values it will store. The
// checks run in a fixed order, and warnings are printed as they are reached, so a caller
// sees the same refusal and the same warnings for the same input every time.
func (s *Store) prepareMemory(a RememberArgs) (*newMemory, error) {
	// A local brain is one file holding one project's memories with nothing above it.
	// Refusing names what is missing; storing the memory here anyway would tell the caller
	// it reaches every project when it reaches one.
	if a.Everywhere {
		return nil, fmt.Errorf("--everywhere stores a memory at the org so every project reads " +
			"it, and this brain is a local file with no org above it.\n" +
			"Refusing rather than storing it in this project and calling it global.")
	}
	if err := CheckToken(a.Scope, "scope"); err != nil {
		return nil, err
	}
	if !AllTypes()[a.Type] {
		return nil, fmt.Errorf("%s", UnknownType(a.Type))
	}
	title := trim(a.Title)
	if title == "" {
		return nil, fmt.Errorf("--title is required and cannot be blank")
	}
	if err := CheckLines("--title", title, "--source", a.Source, "--source-date", a.SourceDate); err != nil {
		return nil, err
	}
	body := AppendRefs(a.Body, a.Refs)
	if a.Confidence != "" && !KnownConfidence(a.Confidence) {
		return nil, fmt.Errorf("--confidence must be one of confirmed, reported, assumed "+
			"(or high, medium, low), not %s", Repr(a.Confidence))
	}
	if !KnownTrust(a.Trust) {
		return nil, fmt.Errorf("--trust must be one of %s, not %s",
			strings.Join(TrustLevels, ", "), Repr(a.Trust))
	}
	// The top confidence tier is the one thing in a memory that cannot be re-derived later,
	// so it is the one thing that must not be taken on the writer's word.
	confidence, err := EarnedConfidence(NormConfidence(a.Confidence), a.Source, a.Refs, body)
	if err != nil {
		return nil, err
	}
	if err := GuardText(a.AllowSecret, title, body, a.Source, a.SourceDate); err != nil {
		return nil, err
	}
	if err := CheckMemorySize(title, body); err != nil {
		return nil, err
	}
	m := &newMemory{title: title, body: body, confidence: confidence}
	if err := m.parseDates(a); err != nil {
		return nil, err
	}
	s.checkDeclared(a.Scope, "scope")
	warnIfRuleShaped(title, a.Type)

	m.hash = ContentHash(s.Env.Project, a.Scope, title)
	m.ts = Now()
	// Resolved once here rather than at render time, because a fact recorded today is a
	// fact about today's code.
	if VolatileTypes[a.Type] {
		m.commit = s.headCommit()
	}
	m.validFrom, m.validTo, err = ParseValidity(a.ValidFrom, a.ValidTo)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// parseDates reads --date, --snooze-until and --review, and gives a world-owned claim a
// default review date when the caller set none.
func (m *newMemory) parseDates(a RememberArgs) error {
	var err error
	if m.happened, err = ParseDate(a.Happened, "--date"); err != nil {
		return err
	}
	snoozeDay, err := ParseDate(a.SnoozeUntil, "--snooze-until")
	if err != nil {
		return err
	}
	if m.reviewDay, err = ParseDate(a.ReviewOn, "--review"); err != nil {
		return err
	}
	if m.snooze, err = DayToTS(snoozeDay); err != nil {
		return err
	}
	if m.review, err = DayToTS(m.reviewDay); err != nil {
		return err
	}
	if a.NoReview && m.reviewDay != "" {
		// Contradictory, and not worth losing the write over: the date is the specific
		// instruction, so it wins and the opt-out is reported as ignored.
		Warn("--no-review ignored: --review %s asked for a date", m.reviewDay)
	}
	// The default horizon gives the review machinery the brief already runs something to
	// fire on: nobody sets that date by hand, so without it nothing ever dated an infra
	// fact against anything checkable. It applies only to a new row. An existing row keeps
	// whatever it has, because stamping today's horizon on memories written earlier would
	// assert they had all been triaged today.
	if m.review == 0 && m.reviewDay == "" && !a.NoReview {
		if d := defaultReviewDay(a.Type, time.Now()); d != "" {
			if ts, err := DayToTS(d); err == nil && ts > 0 {
				m.reviewDay, m.review, m.reviewDefaulted = d, ts, true
			}
		}
	}
	return nil
}

// warnIfRuleShaped catches a RULE arriving as something that will not be read as one. The
// write is the only moment the caller still holds the text and can change its mind
// cheaply. It warns rather than refuses because it is a heuristic over a title: failing
// open costs a line of noise, failing closed would block a legitimate write over a word.
func warnIfRuleShaped(title, memType string) {
	if RuleShaped(title) && !BindingTypes[memType] {
		Warn("this reads as a standing rule and %s is not a type the brief always "+
			"carries.\n  A rule stored as %s falls out of the brief and then binds "+
			"nobody: --type constraint keeps it in front of every session.", memType, memType)
	}
}

// checkSupersedeClaims refuses a text that says it supersedes a memory when the write does
// not: a relation said only in prose is a relation the store never heard. It runs before
// anything is written, and against THIS project, because an id is per store.
func (s *Store) checkSupersedeClaims(tx *sql.Tx, title, body string, supersedes int64) error {
	for _, claimed := range SupersedeClaims(title, body) {
		state := ClaimMissing
		var sup sql.NullInt64
		err := tx.QueryRow("SELECT superseded_by FROM memories WHERE id=? AND project=?",
			claimed, s.Env.Project).Scan(&sup)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			state = ClaimLive
			if sup.Valid {
				state = ClaimRetracted
			}
		}
		if msg := SupersedeClaimRefusal(claimed, supersedes, state); msg != "" {
			return fmt.Errorf("%s", msg)
		}
	}
	return nil
}

// checkSupersedesTarget refuses --supersedes naming a memory this project does not hold.
// Ignoring it with a warning stored the correction with nothing corrected: the caller was
// told on stderr and the store said nothing.
func (s *Store) checkSupersedesTarget(tx *sql.Tx, supersedes int64) error {
	if supersedes <= 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM memories WHERE id=? AND project=?",
		supersedes, s.Env.Project).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("--supersedes %d: there is no #%d in this project, so nothing "+
			"would be retracted. Ids are per store; find the one you mean with "+
			"os-brain recall", supersedes, supersedes)
	}
	return nil
}

// clearTombstone lets a forgotten fact be stored again, saying so: the tombstone exists so
// a wrong fact is not relearned on a loop, and a deliberate re-remember is not a loop.
func clearTombstone(tx *sql.Tx, hash string) error {
	var reason string
	var forgottenAt int64
	err := tx.QueryRow("SELECT forgotten_at, reason FROM tombstones WHERE hash=?", hash).
		Scan(&forgottenAt, &reason)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	extra := ""
	if reason != "" {
		extra = " (" + reason + ")"
	}
	Warn("note: this was forgotten on %s%s — storing it again", Day(forgottenAt), extra)
	_, err = tx.Exec("DELETE FROM tombstones WHERE hash=?", hash)
	return err
}

// titleHolder is the live memory in this project that already holds the title, or nil.
//
// Only this project's row: a shared store that an older relabel rehashed under one name
// holds other projects' rows under this project's hashes, and the refusal that follows
// prints the stored body it found.
//
// Superseding the memory that holds the title is the ordinary way a fact changes: same
// subject, new truth. Identity is scope plus normalised title and only one live memory can
// hold it, so the name is handed over: the retracted memory gets a stable hash of its own,
// derived from its id, and the successor takes the name. Refusing instead would advise the
// caller to pass --supersedes, which they just did.
func (s *Store) titleHolder(tx *sql.Tx, hash string, supersedes int64) (*Memory, error) {
	row := tx.QueryRow("SELECT "+memoryCols+" FROM memories WHERE hash=? AND project=?", hash,
		s.Env.Project)
	existing, err := scanMemory(row, false)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if existing != nil && supersedes == existing.ID {
		if _, err := tx.Exec("UPDATE memories SET hash=? WHERE id=?",
			supersededHash(existing.ID, hash), existing.ID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return existing, nil
}

// refineExisting is a remember of a title the store already holds: it refines that memory
// in place and commits.
func (s *Store) refineExisting(w io.Writer, tx *sql.Tx, a RememberArgs, m *newMemory,
	existing *Memory, alert string) error {
	replacing := m.body != "" && m.body != existing.Body
	if replacing && !a.Update {
		return fmt.Errorf(alertPrefix(alert)+"#%d already holds this title in %s (titles match once case, "+
			"punctuation and accents are ignored).\n  stored:  %s\n  yours:   %s\n"+
			"Storing this would destroy the existing body. Pass --update to replace it "+
			"(the old version is kept in history), --supersedes to record that the fact "+
			"changed, or pick a distinct title.",
			existing.ID, a.Scope, BodyText(Clip(existing.Body, 300)), BodyText(Clip(m.body, 300)))
	}
	if replacing {
		if err := s.archive(tx, existing, "overwrite", "remember --update"); err != nil {
			return err
		}
	}
	if err := s.rewriteExisting(tx, a, m, existing); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Repetition is authority. Storing the same fact again is a RESTATEMENT, and the ladder
	// is what turns that into standing. It is advanced after the commit rather than inside
	// it, because a ladder that fails must not lose the memory the caller was storing.
	ladder, ladderErr := s.AdvanceLadder(existing.ID, "")

	human := fmt.Sprintf("updated existing memory #%d (same title in %s)", existing.ID, a.Scope)
	if replacing {
		human += "\n  replaced body: " + OneLine(Clip(existing.Body, 160))
	}
	if ladderErr == nil && ladder != nil && ladder.State != "stated" {
		human += fmt.Sprintf("\n  ladder: %s (stated %dx)", ladder.State, ladder.StatedCount)
		if ladder.Why != "" {
			human += " - " + ladder.Why
		}
	}
	return Out(w, a.JSON, human, map[string]interface{}{
		"id": existing.ID, "updated": true, "body_replaced": replacing, "ladder": ladder,
		"previous": map[string]interface{}{
			"title": existing.Title, "body": existing.Body, "mem_type": existing.MemType,
			"importance": existing.Importance, "confidence": existing.Confidence,
			"pinned": existing.Pinned,
		},
	})
}

// rewriteExisting writes a refinement over the memory that holds the title. An empty body,
// source date or commit keeps what the row already has; a pin is only ever added here.
func (s *Store) rewriteExisting(tx *sql.Tx, a RememberArgs, m *newMemory, existing *Memory) error {
	newBody := m.body
	if newBody == "" {
		newBody = existing.Body
	}
	pinned := existing.Pinned
	if a.Pin {
		pinned = 1
	}
	sourceDate := a.SourceDate
	if sourceDate == "" {
		sourceDate = existing.SourceDate
	}
	// Re-stating a claim re-dates it to the tree it was re-stated against. Keeping the old
	// commit would make a fact refined today read as unchecked since it was first written.
	sourceCommit := existing.SourceCommit
	if m.commit != "" {
		sourceCommit = m.commit
	}
	if _, err := tx.Exec("UPDATE memories SET title=?, body=?, mem_type=?, importance=?, "+
		"pinned=?, source=?, confidence=?, source_date=?, source_commit=?, updated_at=? WHERE id=?",
		m.title, newBody, a.Type, ClampImportance(a.Importance), pinned, a.Source,
		m.confidence, sourceDate, sourceCommit, m.ts, existing.ID); err != nil {
		return err
	}
	return s.ftsPut(tx, existing.ID, m.title, newBody)
}

// refuseNearDuplicate refuses a second memory about the same subject in the same scope.
// review has always REPORTED these, but reporting happens after the store has already
// grown a duplicate, and whoever reads the report is not the session that created it. The
// write path is the only place the caller still holds the text and can choose.
func (s *Store) refuseNearDuplicate(tx *sql.Tx, scope, title, alert string) error {
	dupes, err := s.NearDuplicates(tx, scope, title)
	if err != nil {
		return err
	}
	if len(dupes) == 0 {
		return nil
	}
	d := dupes[0]
	return fmt.Errorf(alertPrefix(alert)+"#%d in %s is already about this (%.0f%% title overlap):\n"+
		"  stored:  %s\n  yours:   %s\n"+
		"Storing both would put two answers to one question in the brain. Pick one:\n"+
		"  the fact CHANGED       --supersedes %d\n"+
		"  same fact, better words  os-brain update %d --body ...\n"+
		"  genuinely separate     --allow-duplicate",
		d.Memory.ID, scope, d.Score*100, OneLine(d.Memory.Title), title,
		d.Memory.ID, d.Memory.ID)
}

func (s *Store) insertMemory(tx *sql.Tx, a RememberArgs, m *newMemory) (int64, error) {
	pinned := 0
	if a.Pin {
		pinned = 1
	}
	res, err := tx.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source,"+
		"importance,pinned,confidence,source_date,happened_on,snooze_until,review_on,"+
		"verified_at,source_commit,trust,valid_from,valid_to,session,hash,created_at,updated_at) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		s.Env.Project, a.Scope, a.Type, m.title, m.body, a.Source, ClampImportance(a.Importance),
		pinned, m.confidence, a.SourceDate, m.happened, nullable(m.snooze), nullable(m.review),
		nil, m.commit, NormTrust(a.Trust), m.validFrom, m.validTo, s.Env.SessionID(), m.hash, m.ts, m.ts)
	if err != nil {
		return 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := s.ftsPut(tx, newID, m.title, m.body); err != nil {
		return 0, err
	}
	return newID, nil
}

// retractSuperseded marks the memory named by --supersedes as superseded by newID. It
// returns the suffix for the human output and the id actually retracted, which is what the
// JSON reports: reporting the flag instead once told a machine reader a retraction had
// been recorded when it had not.
func (s *Store) retractSuperseded(tx *sql.Tx, a RememberArgs, newID int64, title string,
	ts int64) (string, int64, error) {
	if a.Supersedes <= 0 {
		return "", 0, nil
	}
	var exists int64
	var prior sql.NullInt64
	var priorSession string
	if err := tx.QueryRow("SELECT id, superseded_by, session FROM memories WHERE id=?",
		a.Supersedes).Scan(&exists, &prior, &priorSession); err != nil {
		return "", 0, err
	}
	forked := prior.Valid && prior.Int64 != newID
	if forked && !a.ForkOK {
		return "", 0, s.forkRefusal(tx, a.Supersedes, prior.Int64, title)
	}
	// The superseded memory loses its pin: a retracted fact must not keep a slot in every
	// brief.
	if _, err := tx.Exec("UPDATE memories SET superseded_by=?, pinned=0, updated_at=? WHERE id=?",
		newID, ts, a.Supersedes); err != nil {
		return "", 0, err
	}
	sup := fmt.Sprintf(" (supersedes #%d)", a.Supersedes)
	if forked {
		sup += fmt.Sprintf(" (fork: #%d also superseded it)", prior.Int64)
	}
	return sup, a.Supersedes, nil
}

// forkRefusal is the error for a second correction of a memory that is already corrected.
// Two sessions correcting the same memory without seeing each other used to be
// last-writer-wins on a single column: the first correction stayed in the store but
// superseded nothing, silently demoted from "the retraction" to one more opinion competing
// at equal weight with the error it retracted. That breaks the founding guarantee that a
// correction cannot sit beside the error, so it refuses and hands the caller the fork.
func (s *Store) forkRefusal(tx *sql.Tx, retracted, by int64, title string) error {
	var t, byTitle, bySession string
	_ = tx.QueryRow("SELECT title FROM memories WHERE id=?", retracted).Scan(&t)
	_ = tx.QueryRow("SELECT title, session FROM memories WHERE id=?",
		by).Scan(&byTitle, &bySession)
	who := "another session"
	if SameSession(bySession, s.Env.SessionID()) {
		who = "this session, earlier"
	}
	return fmt.Errorf("#%d has already been superseded by #%d (%s).\n"+
		"  retracted:  %s\n"+
		"  by #%d:      %s\n"+
		"  yours:      %s\n"+
		"Two corrections to one fact is a fork, and the store can only record one:\n"+
		"  yours is the later correction   --supersedes %d   (chain them)\n"+
		"  both are true, different facts  drop --supersedes\n"+
		"  you mean it, take the branch    --fork-ok",
		retracted, by, who, t, by, byTitle, title, by)
}

// concurrentAlert looks for a live row another session wrote into this scope inside
// ConcurrentWindow that may be about the same thing, and words the alert. It does not
// refuse: nothing here can tell A from not-A, so it tells the agent to ask.
func (s *Store) concurrentAlert(q querier, scope, title, memType string, now int64) (string, error) {
	rows, err := q.Query("SELECT id, title, mem_type, session, updated_at FROM memories "+
		"WHERE project=? AND scope=? AND superseded_by IS NULL AND updated_at >= ? "+
		"ORDER BY updated_at DESC", s.Env.Project, scope, now-ConcurrentWindow)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at int64
		var otherTitle, otherType, session string
		if err := rows.Scan(&id, &otherTitle, &otherType, &session, &at); err != nil {
			return "", err
		}
		if Concurrent(session, s.Env.SessionID(), at, now) &&
			ConcurrentSubject(title, memType, otherTitle, otherType) {
			return ConcurrentAlert(id, otherType, otherTitle, now-at), nil
		}
	}
	return "", rows.Err()
}

func alertJSON(alert string) interface{} {
	if alert == "" {
		return nil
	}
	return alert
}

// alertPrefix puts a concurrent-write alert ahead of a refusal. The refusal text is used
// as a format string, so a '%' in the alert is escaped.
func alertPrefix(alert string) string {
	if alert == "" {
		return ""
	}
	return strings.ReplaceAll(alert, "%", "%%") + "\n\n"
}

type LogArgs struct {
	Scope       string
	Kind        string
	Actor       string
	Summary     string
	Ref         string
	AllowSecret bool
	JSON        bool
	// TS is the event's own timestamp from `log --date`; zero means now.
	TS int64
}

func (s *Store) Log(w io.Writer, a LogArgs) error {
	if err := CheckLine("the summary", a.Summary); err != nil {
		return err
	}
	if err := GuardText(a.AllowSecret, a.Summary, a.Ref); err != nil {
		return err
	}
	if err := CheckToken(a.Kind, "kind"); err != nil {
		return err
	}
	if err := CheckToken(a.Scope, "scope"); err != nil {
		return err
	}
	s.checkDeclared(a.Kind, "kind")
	s.checkDeclared(a.Scope, "scope")
	// Clamped to the past: an event dated in the future would sit at the top of every feed
	// until the clock caught up.
	at := Now()
	if a.TS > 0 && a.TS < at {
		at = a.TS
	}
	res, err := s.DB.Exec("INSERT INTO events(project,ts,scope,kind,actor,summary,ref,session) "+
		"VALUES(?,?,?,?,?,?,?,?)", s.Env.Project, at, a.Scope, a.Kind, a.Actor, a.Summary,
		a.Ref, s.Env.SessionID())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	return Out(w, a.JSON, fmt.Sprintf("logged event #%d [%s] %s", id, a.Kind, a.Summary),
		map[string]interface{}{"id": id})
}

// nullable stores an unset timestamp as NULL rather than as the epoch.
func nullable(ts int64) interface{} {
	if ts == 0 {
		return nil
	}
	return ts
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// tombstoneHash keys a tombstone on the row's OWN identity instead of recomputing one from
// its title. A correction may carry the same title as the fact it corrects: the retracted
// row holds supersededHash and the successor holds the name, so recomputing meant
// forgetting the retracted half wrote a tombstone keyed to the LIVE successor, which
// re-remembering then reported as forgotten, and restore then deleted the wrong one.
func tombstoneHash(project string, m *Memory) string {
	if m.Hash != nil && *m.Hash != "" {
		return *m.Hash
	}
	return ContentHash(project, m.Scope, m.Title)
}

// supersededHash keeps a retracted memory's row unique without letting it hold the name.
// Deriving it from the id rather than nulling the column means the row still has a stable
// identity for history, tombstones and doctor, and two retractions of the same title can
// coexist.
func supersededHash(id int64, live string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("superseded:%d:%s", id, live)))
	return hex.EncodeToString(sum[:])
}

// ParseValidity reads --valid-from and --valid-to as they are stored: days, either may be
// empty, and never a window that ends before it starts.
func ParseValidity(from, to string) (string, string, error) {
	validFrom, err := ParseDate(from, "--valid-from")
	if err != nil {
		return "", "", err
	}
	validTo, err := ParseDate(to, "--valid-to")
	if err != nil {
		return "", "", err
	}
	if validFrom != "" && validTo != "" && validFrom > validTo {
		return "", "", fmt.Errorf("--valid-from %s is after --valid-to %s", validFrom, validTo)
	}
	return validFrom, validTo, nil
}
