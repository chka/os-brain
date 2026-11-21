package lead

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Leads are a small sales pipeline kept in the brain: a status from a known set, notes that
// only grow, and three views (pipeline, hot, next) that the brief also draws.

func TestLeadStatusIsCheckedAgainstTheKnownSet(t *testing.T) {
	s := newTestStore(t)
	bogus := "banana"
	if err := s.Lead(io.Discard, LeadArgs{Sub: "add", Name: "Jane", Status: &bogus}); err == nil {
		t.Error("accepted an unknown lead status")
	}
}

// --append-note adds to the history; it must never replace what was already there.
func TestLeadAppendNoteKeepsWhatWasAlreadyThere(t *testing.T) {
	s := newTestStore(t)
	notes := "first contact at the conference"
	if err := s.Lead(io.Discard, LeadArgs{Sub: "add", Name: "Jane", Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if err := s.Lead(io.Discard, LeadArgs{Sub: "update", ID: 1,
		AppendNote: "followed up by email"}); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := s.DB.QueryRow("SELECT notes FROM leads WHERE id=1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "first contact") || !strings.Contains(got, "followed up") {
		t.Errorf("append-note lost history: %q", got)
	}
}

// The three views the brief draws are also commands, rendered by the same code as the
// brief, so the command and the brief cannot disagree. They once were refused as unknown
// words, and an unknown action must name every action that exists.
func TestLeadPipelineHotAndNextAreCommands(t *testing.T) {
	s := newTestStore(t)
	engaged, v := "engaged", int64(12000)
	next, when := "send the proposal", "2026-09-25"
	if err := s.Lead(io.Discard, LeadArgs{Sub: "add", Name: "Jane", Company: "Acme",
		Status: &engaged, ValueEst: &v, NextAction: &next, NextDate: &when}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Lead(&out, LeadArgs{Sub: "pipeline"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "engaged") || !strings.Contains(out.String(), "$12,000") ||
		!strings.Contains(out.String(), "1 leads, pipeline value $12,000") {
		t.Errorf("pipeline rendering: %q", out.String())
	}
	out.Reset()
	if err := s.Lead(&out, LeadArgs{Sub: "hot"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#1 [engaged] Jane @ Acme $12,000 | send the proposal") {
		t.Errorf("hot rendering: %q", out.String())
	}
	out.Reset()
	if err := s.Lead(&out, LeadArgs{Sub: "next"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#1 Jane @ Acme: send the proposal (2026-09-25)") {
		t.Errorf("next rendering: %q", out.String())
	}
	if err := s.Lead(io.Discard, LeadArgs{Sub: "banana"}); err == nil ||
		!strings.Contains(err.Error(), "pipeline, hot, next") {
		t.Errorf("an unknown action should name every action: %v", err)
	}
}
