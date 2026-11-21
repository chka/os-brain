package spine

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

type Axiom struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	Rationale string `json:"rationale"`
	// Who wrote it: project, and the session inside it. Rendered wherever an axiom is,
	// because "this rule binds everything and nobody knows where it came from" is the
	// state that made a bad axiom indistinguishable from a settled one.
	Origin  string `json:"origin"`
	Session string `json:"session"`
	// part of the core set that ships in the binary
	Seeded    bool  `json:"seeded"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
	// The last project and session to reword it. The author stays the author; an edit
	// is recorded beside it, and clears Seeded, because the text is no longer what shipped.
	EditedBy      string `json:"edited_by"`
	EditedSession string `json:"edited_session"`
	EditedAt      int64  `json:"edited_at"`
}

const axiomCols = "id, text, rationale, origin_project, origin_session, seeded, created_at, " +
	"updated_at, edited_project, edited_session, edited_at"

func scanAxiom(sc interface{ Scan(...interface{}) error }) (Axiom, error) {
	var a Axiom
	var seeded int
	err := sc.Scan(&a.ID, &a.Text, &a.Rationale, &a.Origin, &a.Session, &seeded,
		&a.CreatedAt, &a.UpdatedAt, &a.EditedBy, &a.EditedSession, &a.EditedAt)
	a.Seeded = seeded == 1
	return a, err
}

// Provenance is the attribution printed beside a rule wherever an agent reads it: a rule
// that binds everything is only as good as whoever wrote it, and the brief is where that
// is decided.
func (a Axiom) Provenance() string {
	if a.ID == 0 {
		return "  [built in]"
	}
	if a.Seeded {
		return "  [core, ships with the brain]"
	}
	return spineProvenance(a.Origin, a.CreatedAt, a.EditedBy, a.EditedAt)
}

func spineProvenance(origin string, at int64, editor string, editedAt int64) string {
	if origin == "" {
		origin = "an unrecorded author"
	}
	p := "  [by " + textguard.OneLine(origin)
	if at > 0 {
		p += ", " + timefmt.Day(at)
	}
	if editor != "" {
		p += "; edited by " + textguard.OneLine(editor)
		if editedAt > 0 {
			p += ", " + timefmt.Day(editedAt)
		}
	}
	return p + "]"
}

// builtinAxioms is the fallback set, as rows with no id, for when there is no spine.
func builtinAxioms() []Axiom {
	out := make([]Axiom, 0, len(store.DefaultAxioms))
	for _, a := range store.DefaultAxioms {
		out = append(out, Axiom{Text: a[0], Rationale: a[1]})
	}
	return out
}

// Axioms are read live, one copy. Nothing is ever propagated into a project brain: a
// copy is a thing that can drift, and a thing that has to be written somewhere it does
// not belong.
func (s *Store) Axioms() []Axiom {
	g := s.GlobalConnect(false)
	if g == nil {
		return builtinAxioms()
	}
	defer s.CloseGlobal(g)
	rows, err := g.Query("SELECT " + axiomCols + " FROM axioms ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	// No fallback once the spine exists: an empty table means someone removed them
	// deliberately, and quietly resurrecting them would undo that decision.
	var out []Axiom
	for rows.Next() {
		a, err := scanAxiom(rows)
		if err != nil {
			return out
		}
		out = append(out, a)
	}
	sortDefaultsFirst(out)
	return out
}

// sortDefaultsFirst puts the shipped defaults first, in the order the binary lists them,
// then everything written here in the order it was written. Rows are kept by id, so without
// this a default added to an existing spine (the four laws, in v15) would sit below rules
// it outranks.
func sortDefaultsFirst(axioms []Axiom) {
	sort.SliceStable(axioms, func(i, j int) bool {
		di, dj := store.DefaultAxiomIndex(axioms[i].Text), store.DefaultAxiomIndex(axioms[j].Text)
		if di >= 0 && dj >= 0 {
			return di < dj
		}
		return di >= 0 && dj < 0
	})
}

// axiomRows reads the spine's axioms in id order, without the defaults-first sort.
func (s *Store) axiomRows(g *sql.DB) []Axiom {
	rows, err := g.Query("SELECT " + axiomCols + " FROM axioms ORDER BY id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Axiom{}
	for rows.Next() {
		x, err := scanAxiom(rows)
		if err != nil {
			return out
		}
		out = append(out, x)
	}
	return out
}

// builtinAxiomPrint is the fingerprint recorded while a project reads the set compiled
// into the binary.
const builtinAxiomPrint = "0:0:builtin"

// AxiomChanges reports that the rules changed since anyone here last looked.
//
// Notes from other projects are marked untrusted and carry no authority, which is the easy
// half: a note is addressed to one project and has to be read to have any effect. Axioms
// are the opposite: authoritative by design, read live by every project, writable from any
// of them. A wrong one governs everything and announces nothing. This does not decide who
// may write one; it removes the silence, which is what made a bad axiom indistinguishable
// from a settled one.
//
// The fingerprint (axiomFingerprint) is compared against what the last brief in this
// project saw. A removed row leaves nothing to name, so removals are counted rather than
// described, and a change the timestamps cannot place (a removal and an addition that
// cancel out) is still said.
func (s *Store) AxiomChanges(axioms []Axiom, noCursor bool) []string {
	if len(axioms) == 0 || axioms[0].ID == 0 {
		// The built-in fallback set: no spine, no timestamps, nothing that can change
		// without the binary itself changing. Recorded all the same, so the first rule
		// written into a spine created later is announced rather than taken as the baseline.
		if !noCursor && len(axioms) > 0 {
			s.SetSessionString("axiom_fingerprint", builtinAxiomPrint)
		}
		return nil
	}
	cur := axiomFingerprint(axioms)
	prev := s.SessionString("axiom_fingerprint")
	if !noCursor {
		defer s.SetSessionString("axiom_fingerprint", cur)
	}
	// A fingerprint written before the hash was part of it matches on its first two fields.
	if prev == "" || prev == cur || prev == cur[:strings.LastIndex(cur, ":")] {
		return nil
	}
	var prevCount int
	var prevNewest int64
	if _, err := fmt.Sscanf(prev, "%d:%d", &prevCount, &prevNewest); err != nil {
		return nil
	}
	return axiomChangeReport(axioms, prev, prevCount, prevNewest)
}

// axiomFingerprint is "count:newest updated_at:hash", the hash covering every row's id,
// text, attribution and core flag, so an edit that keeps the count and lands in an old
// second is still seen.
func axiomFingerprint(axioms []Axiom) string {
	var newest int64
	h := sha256.New()
	for _, a := range axioms {
		if a.UpdatedAt > newest {
			newest = a.UpdatedAt
		}
		fmt.Fprintf(h, "%d\x00%s\x00%s\x00%s\x00%t\x00", a.ID, a.Text, a.Origin, a.EditedBy, a.Seeded)
	}
	return fmt.Sprintf("%d:%d:%s", len(axioms), newest, hex.EncodeToString(h.Sum(nil))[:16])
}

// axiomChangeReport is the brief's block naming what changed since the previous
// fingerprint.
func axiomChangeReport(axioms []Axiom, prev string, prevCount int, prevNewest int64) []string {
	// An axiom written in the same second as the previous newest one is not "> prevNewest",
	// and a rule that binds everything must not go unannounced because two writes landed
	// inside one tick of a whole-second clock. When the count says something was added,
	// include the rows that share that second: naming one axiom twice is a smaller failure
	// than naming a new one never.
	if len(axioms) > prevCount && prevNewest > 0 {
		prevNewest--
	}
	var lines []string
	for _, a := range axioms {
		// Moving from the built-in set to a real spine is not news about the core set.
		if prev == builtinAxiomPrint && a.Seeded {
			continue
		}
		if a.UpdatedAt > prevNewest {
			lines = append(lines, "  + "+textguard.OneLine(a.Text), "     "+a.Provenance())
		}
	}
	if gone := prevCount - len(axioms); gone > 0 {
		lines = append(lines, fmt.Sprintf("  - %d axiom(s) were removed", gone))
	}
	if len(lines) == 0 {
		lines = append(lines, "  the set changed in a way no timestamp places (a removal and an "+
			"addition together): read the AXIOMS above")
	}
	return append([]string{"", "## AXIOMS CHANGED SINCE THE LAST BRIEF HERE",
		"  These bind every project, and only a person at a terminal can change one.",
		"  Read them; if one looks wrong, tell the user rather than working around it."},
		lines...)
}
