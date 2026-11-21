package recall

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/store"
)

// recall: what it returns on a miss, what it touches, and what rides along with the
// memories it finds.

// Recall fails closed. A recall miss that returned "the closest things" once fed a forget
// loop, which then deleted real memories that had nothing to do with the query.
func TestRecallFailsClosedOnAMiss(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "postgres tuning", Body: "shared buffers"})
	res := recall(t, s, RecallArgs{Query: "kubernetes", Strict: true})
	if res["matched"].(bool) || len(res["results"].([]interface{})) != 0 {
		t.Errorf("strict miss must return nothing, got %v", res)
	}
	// JSON is strict by default: a machine cannot read the "this is a browse" warning.
	res = recall(t, s, RecallArgs{Query: "kubernetes"})
	if len(res["results"].([]interface{})) != 0 {
		t.Error("json recall must be strict unless --browse")
	}
	// A human who asks to browse still gets the scope.
	res = recall(t, s, RecallArgs{Query: "kubernetes", Browse: true})
	if len(res["results"].([]interface{})) == 0 {
		t.Error("--browse should show the scope")
	}
	if res["matched"].(bool) {
		t.Error("a browse fallback must not claim to be a match")
	}
}

// A recall that misses points at the nearest categories of the coverage map: those that
// share a word with the query, or else the largest ones.
func TestNeighbourhoodOfPrefersASharedWordAndFallsBackToTheLargest(t *testing.T) {
	cats := []Category{{Name: "infra", Count: 9}, {Name: "scope:tls:certs", Count: 4},
		{Name: "decision", Count: 2}}
	got := NeighbourhoodOf("tls renewal", cats, 8)
	if len(got) != 1 || got[0].Name != "scope:tls:certs" {
		t.Errorf("near tls gave %+v", got)
	}
	if got := NeighbourhoodOf("nothing shares this", cats, 2); len(got) != 2 || got[0].Name != "infra" {
		t.Errorf("a miss falls back to the largest, gave %+v", got)
	}
}

// An ordinary recall reinforces what it returns (use_count feeds the usage factor), and
// --no-touch must not, so tools and tests can look without changing the ranking.
func TestRecallReinforcesAndNoTouchDoesNot(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the counted memory", Body: "x", Type: "note"})
	count := func() int {
		var n int
		if err := s.DB.QueryRow("SELECT use_count FROM memories WHERE id=1").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	recall(t, s, RecallArgs{Query: "counted", N: 5, NoTouch: true})
	if count() != before {
		t.Errorf("--no-touch incremented use_count from %d to %d", before, count())
	}
	recall(t, s, RecallArgs{Query: "counted", N: 5})
	if count() != before+1 {
		t.Errorf("an ordinary recall did not reinforce: %d -> %d", before, count())
	}
}

// Identity questions ("what is this project") once returned nothing, so the session went
// and read files instead, in a tool whose whole point is that files are not memory. Recall
// answers them from the project's own context in brain.json.
func TestRecallAnswersWhatIsThisProjectFromConfig(t *testing.T) {
	s := newTestStore(t)
	cfg := s.Env.Config
	cfg.Context = map[string]string{"about": "a bookmark manager in Go"}
	if err := store.SaveConfig(s.Env.Here, cfg); err != nil {
		t.Fatal(err)
	}
	s.Env.Config = cfg
	var buf bytes.Buffer
	if err := s.Recall(&buf, RecallArgs{Query: "what is this project"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "a bookmark manager in Go") {
		t.Errorf("identity query answered with nothing:\n%s", buf.String())
	}
	// It survives a strict miss, because it is config and carries no id anything could
	// delete.
	res := mustJSON(t, func(w io.Writer) error {
		return s.Recall(w, RecallArgs{Query: "what is this project", JSON: true, Strict: true})
	})
	if res["identity"] == nil {
		t.Errorf("the identity card did not survive a strict miss: %v", res)
	}
	if res["matched"] != false {
		t.Error("the card must not be reported as a lexical match")
	}
}

// Measures live in their own table. Without this, a series that exists read as never
// recorded to anyone who asked recall about it.
func TestRecallSurfacesAMatchingMeasureAlongsideMemories(t *testing.T) {
	s := newTestStore(t)
	if err := s.Measure(io.Discard, MeasureArgs{Sub: "add", Key: "phosphate", Value: 1.2}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Recall(&buf, RecallArgs{Query: "phosphate"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "MEASURES matching") {
		t.Errorf("a series that exists reads as never recorded:\n%s", buf.String())
	}
}

// --tier takes exactly one known letter. It used to be matched as a substring of "ABCDN",
// so "BCD" was accepted and ran as tier N.
func TestTierIsOneKnownLetter(t *testing.T) {
	s := newTestStore(t)
	for _, tier := range []string{"BCD", "AB", "x"} {
		if err := s.Recall(io.Discard, RecallArgs{Query: "payments", Tier: tier}); err == nil {
			t.Errorf("--tier %s was accepted", tier)
		}
	}
	if err := s.Recall(io.Discard, RecallArgs{Query: "payments", Tier: "n"}); err != nil {
		t.Error(err)
	}
}
