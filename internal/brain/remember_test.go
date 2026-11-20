package brain

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// remember: identity, size limits, validity windows and the ladder a restated memory
// climbs.

// A title that normalises to an existing one is the same fact. Silently replacing its body
// would lose what was there, so remember refuses and names --update as the way to do it on
// purpose.
func TestRememberRefusesToReplaceADifferingBody(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "nginx sits in front", Body: "original"})
	err := s.Remember(io.Discard, RememberArgs{Scope: "global", Type: "note",
		Title: "NGINX, sits in front!", Body: "different", Importance: 3})
	if err == nil {
		t.Fatal("expected a refusal on a normalised-title collision")
	}
	if !strings.Contains(err.Error(), "--update") {
		t.Errorf("the refusal must name the way out, got %v", err)
	}
}

// Two projects may hold the same title for different facts, and in a shared store their
// identities would otherwise collide.
func TestContentHashIsPerProject(t *testing.T) {
	if ContentHash("a", "global", "same title") == ContentHash("b", "global", "same title") {
		t.Error("hash collides across projects")
	}
}

// A 24 MB body was once stored and recall printed every byte of it. Titles and bodies are
// capped, and recall clips a long body and says where to read the rest.
func TestTitleAndBodyAreCappedAndRecallClips(t *testing.T) {
	s := newTestStore(t)
	if err := s.Remember(io.Discard, RememberArgs{Type: "note", Scope: "global",
		Title: strings.Repeat("t", MaxTitleRunes+1)}); err == nil {
		t.Error("an over-long title was stored")
	}
	if err := s.Remember(io.Discard, RememberArgs{Type: "note", Scope: "global", Title: "gizmo",
		Body: strings.Repeat("x", MaxBodyBytes+1)}); err == nil {
		t.Error("an over-long body was stored")
	}
	remember(t, s, RememberArgs{Title: "gizmo specification in full", Body: strings.Repeat("gizmo ", 2000)})
	var out bytes.Buffer
	if err := s.Recall(&out, RecallArgs{Query: "gizmo"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 4*RecallBodyRunes || !strings.Contains(out.String(), "os-brain get") {
		t.Errorf("recall printed %d bytes without saying where the rest is", out.Len())
	}
}

// --valid-from and --valid-to: a window that ends before it starts can never be true.
func TestParseValidityRefusesAWindowThatEndsBeforeItStarts(t *testing.T) {
	if f, to, err := ParseValidity("2026-01-01", "2026-02-01"); err != nil || f != "2026-01-01" || to != "2026-02-01" {
		t.Errorf("gave %q %q %v", f, to, err)
	}
	if _, _, err := ParseValidity("2026-02-01", "2026-01-01"); err == nil {
		t.Error("a window ending before it starts was accepted")
	}
}

// --- the ladder ---------------------------------------------------------------------------
//
// A memory stated again in a different session climbs toward law; repetition within one
// session is not authority. These tests drive the ladder through the ordinary remember
// command rather than the internal call.

func TestTheLadderMovesThroughRememberAcrossSessions(t *testing.T) {
	s := newTestStore(t)
	state := func() (int, bool, bool) {
		var n int
		var hardened, law interface{}
		if err := s.DB.QueryRow("SELECT stated_count, hardened_at, law_at FROM memories "+
			"WHERE id=1").Scan(&n, &hardened, &law); err != nil {
			t.Fatal(err)
		}
		return n, hardened != nil, law != nil
	}
	args := RememberArgs{Title: "the rule under test", Body: "same body",
		Type: "decision", Scope: "global"}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-one")
	s.Env.Session = ""
	remember(t, s, args)
	if n, h, l := state(); n != 1 || h || l {
		t.Errorf("a fresh memory is stated once: got count=%d hardened=%v law=%v", n, h, l)
	}
	if err := s.Remember(&bytes.Buffer{}, args); err != nil {
		t.Fatal(err)
	}
	if _, _, law := state(); law {
		t.Error("restating in the SAME session reached law")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-two")
	if err := s.Remember(&bytes.Buffer{}, args); err != nil {
		t.Fatal(err)
	}
	n, _, law := state()
	if !law {
		t.Errorf("restating in a DIFFERENT session did not reach law (count=%d)", n)
	}
	if n != 3 {
		t.Errorf("stated_count is %d after three statements", n)
	}
}

// A memory that reached law without ever being objected to must not acquire a hardened_at
// LATER than its law_at when somebody objects afterwards. Hardening is a step below law, so
// that timestamp would assert a transition that never happened, on a ladder whose whole
// claim is that each state is reached once and in order.
func TestObjectingToALawMemoryDoesNotBackdateHardening(t *testing.T) {
	s := newTestStore(t)
	args := RememberArgs{Title: "go for the new service", Body: "one static binary",
		Type: "decision", Scope: "global"}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-one")
	remember(t, s, args)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "session-two")
	if err := s.Remember(&bytes.Buffer{}, args); err != nil { // -> law
		t.Fatal(err)
	}
	var hardened, law interface{}
	if err := s.DB.QueryRow("SELECT hardened_at, law_at FROM memories WHERE id=1").
		Scan(&hardened, &law); err != nil {
		t.Fatal(err)
	}
	if law == nil {
		t.Fatal("a restatement in a second session did not reach law")
	}
	if hardened != nil {
		t.Fatal("it hardened without an objection")
	}

	if _, err := s.Object(1, "rust would give us the same binary with better guarantees",
		"session-two"); err != nil {
		t.Fatal(err)
	}
	st, err := s.AdvanceLadder(1, "session-three")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT hardened_at, law_at FROM memories WHERE id=1").
		Scan(&hardened, &law); err != nil {
		t.Fatal(err)
	}
	if hardened != nil {
		t.Errorf("a law memory acquired hardened_at %v, later than its law_at %v: that "+
			"asserts a hardened state it was never in", hardened, law)
	}
	if st.State != "law" {
		t.Errorf("state is %q after objecting to law", st.State)
	}
	if strings.Contains(st.Why, "hardened") {
		t.Errorf("the reason reads as a contradiction: %q", st.Why)
	}
	if !strings.Contains(st.Why, "objection") {
		t.Errorf("the objection is not mentioned at all: %q", st.Why)
	}
}
