package brain

import (
	"fmt"
	"io"
	"strings"
)

// The commands that read and argue with the ladder. The ladder itself (how a memory moves
// from stated to hardened to law) lives in ladder.go; this file is only how it is asked
// for and how it reads back.

// LadderState is stated -> hardened -> law. Derived, never set by hand.
type LadderState struct {
	State       string `json:"state"`
	StatedCount int    `json:"stated_count"`
	HardenedAt  *int64 `json:"hardened_at,omitempty"`
	LawAt       *int64 `json:"law_at,omitempty"`
	Why         string `json:"why,omitempty"`
}

func (s *Store) ObjectCmd(w io.Writer, id int64, why string, asJSON bool) error {
	st, err := s.Object(id, why, "")
	if err != nil {
		return err
	}
	human := fmt.Sprintf("objection recorded against #%d", id)
	if st != nil {
		human += fmt.Sprintf("\n  it is %s (stated %dx)", st.State, st.StatedCount)
		if st.Why != "" {
			human += "\n  " + st.Why
		}
		if st.State == "stated" {
			human += "\n  Restate it and it hardens: the objection keeps its date and its " +
				"reasoning, so nobody re-runs this argument next month."
		}
	}
	return Out(w, asJSON, human, st)
}

func (s *Store) LadderCmd(w io.Writer, id int64, asJSON bool) error {
	st, err := s.LadderOf(id)
	if err != nil {
		return err
	}
	objs, err := s.Objections(id)
	if err != nil {
		return err
	}
	return Out(w, asJSON, renderLadder(id, st, objs),
		map[string]interface{}{"ladder": st, "objections": objs})
}

func renderLadder(id int64, st *LadderState, objs []Objection) string {
	if st == nil {
		return fmt.Sprintf("#%d has no ladder state", id)
	}
	lines := []string{fmt.Sprintf("#%d is %s, stated %dx", id, st.State, st.StatedCount)}
	if st.Why != "" {
		lines = append(lines, "  "+st.Why)
	}
	for _, o := range objs {
		lines = append(lines, fmt.Sprintf("  objected %s: %s", Day(o.RaisedAt), o.Why))
	}
	return strings.Join(lines, "\n")
}
