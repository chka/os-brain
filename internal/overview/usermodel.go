package overview

import (
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// The user model: measured signals about how the person at the keyboard works, each with
// the evidence that produced it.
//
// It lives in the SHARED spine, reached through s.GlobalConnect, for the same reason
// axioms and standards do: the person answering is the same across every project, but the
// project is not. A trait recorded from working in one repo ("cites primary sources",
// "corrects the agent and is right") describes how the person works, not how that repo
// works. Filing it per project would mean relearning it in every repo and would let two
// projects disagree about who the person is.
//
// It never leaves the machine and never becomes a published lesson: this is a profile of
// a person, and it stops being safe to keep the moment it is treated as shareable content.
// Nothing enforces that beyond never writing this table anywhere else, so keep it that
// way.

// Trait is one measured signal about how this person works, with the evidence that
// produced it. Every assessment stores its evidence rather than a bare score: a wrong
// assessment reads as condescension and is the error a competent user does not forgive
// twice, so it has to be inspectable and overrulable.
type Trait struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Evidence  string `json:"evidence"`
	UpdatedAt int64  `json:"updated_at"`
}

// The table is created on first use with CREATE TABLE IF NOT EXISTS, the way the spine's
// other tables are guarded, rather than through a migration step.
const userTraitsDDL = `CREATE TABLE IF NOT EXISTS user_traits (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    evidence TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
)`

func ensureUserTraits(g *sql.DB) error {
	_, err := g.Exec(userTraitsDDL)
	return err
}

// Observe records one trait. Evidence is required: a trait with no evidence is exactly the
// unaccountable score the model exists to avoid, so it is refused rather than stored bare.
//
// Observing an existing key updates it in place. The model tracks the CURRENT read on a
// person, not a log of every time it changed its mind, the same identity rule Remember
// uses for memories.
func (s *Store) Observe(key, value, evidence string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	evidence = strings.TrimSpace(evidence)
	if key == "" {
		return fmt.Errorf("a trait needs a key")
	}
	if evidence == "" {
		return fmt.Errorf("Observe %s: evidence is required — a trait with no evidence is "+
			"the bare score this model refuses to store", pyfmt.Repr(key))
	}
	g := s.GlobalConnect(true)
	if g == nil {
		return fmt.Errorf("the shared spine is unavailable; the user model has nowhere to live")
	}
	defer s.CloseGlobal(g)
	if err := store.RetryBusy(func() error { return ensureUserTraits(g) }); err != nil {
		return err
	}
	now := timefmt.Now()
	_, err := g.Exec("INSERT INTO user_traits(key,value,evidence,created_at,updated_at) "+
		"VALUES(?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, "+
		"evidence=excluded.evidence, updated_at=excluded.updated_at",
		key, value, evidence, now, now)
	return err
}

// UserModel returns every trait, with its evidence. It is shown on demand and never
// volunteered, so nothing wires it into brief or wakeup output.
func (s *Store) UserModel() ([]Trait, error) {
	g := s.GlobalConnect(false)
	if g == nil {
		return nil, nil
	}
	defer s.CloseGlobal(g)
	if err := ensureUserTraits(g); err != nil {
		return nil, err
	}
	rows, err := g.Query("SELECT key, value, evidence, updated_at FROM user_traits ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Trait{}
	for rows.Next() {
		var t Trait
		if err := rows.Scan(&t.Key, &t.Value, &t.Evidence, &t.UpdatedAt); err != nil {
			return out, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ForgetTrait removes one trait by key, the same posture as `forget` on a memory: a wrong
// assessment must be correctable by naming it, not by wiping the whole model.
func (s *Store) ForgetTrait(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("which trait? name the key")
	}
	g := s.GlobalConnect(false)
	if g == nil {
		return fmt.Errorf("no shared spine to forget from")
	}
	defer s.CloseGlobal(g)
	if err := ensureUserTraits(g); err != nil {
		return err
	}
	res, err := g.Exec("DELETE FROM user_traits WHERE key=?", key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("no trait %s", pyfmt.Repr(key))
	}
	return nil
}

type WhoamiArgs struct {
	Observe  string
	Evidence string
	Forget   string
	JSON     bool
}

// Whoami shows the model on demand, and records or drops one trait first when asked to.
// Every line carries the evidence that produced it and any line can be dropped by name.
func (s *Store) Whoami(w io.Writer, a WhoamiArgs) error {
	if a.Forget != "" {
		if err := s.ForgetTrait(a.Forget); err != nil {
			return err
		}
		return store.Out(w, a.JSON, "dropped "+pyfmt.Repr(a.Forget),
			map[string]interface{}{"forgot": a.Forget})
	}
	if a.Observe != "" {
		if err := s.observeFlag(a.Observe, a.Evidence); err != nil {
			return err
		}
	}
	traits, err := s.UserModel()
	if err != nil {
		return err
	}
	if len(traits) == 0 {
		return store.Out(w, a.JSON, "nothing measured yet. The model is built from what capture "+
			"sees, and it is shown on demand rather than volunteered.", []Trait{})
	}
	var lines []string
	for _, t := range traits {
		lines = append(lines, fmt.Sprintf("%-18s %s\n    because: %s", t.Key, t.Value, t.Evidence))
	}
	lines = append(lines, "", "Wrong? os-brain whoami --forget <key>. It is a measurement, "+
		"not a verdict.")
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), traits)
}

// observeFlag records the trait named by `--observe key=value`.
func (s *Store) observeFlag(pair, evidence string) error {
	k, v, ok := strings.Cut(pair, "=")
	if !ok {
		return fmt.Errorf("--observe takes key=value, for example " +
			"--observe depth=peer-to-peer --evidence \"corrected the API surface twice\"")
	}
	return s.Observe(strings.TrimSpace(k), strings.TrimSpace(v), evidence)
}
