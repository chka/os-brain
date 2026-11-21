package brief

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/lead"
	"github.com/chka/os-brain/internal/measure"
	"github.com/chka/os-brain/internal/memory"
)

// The sections of the session brief. The brief is the only output every session is
// guaranteed to read, so anything that needs acting on has to reach it, in the text a
// person reads and in the JSON an agent reads.

// A reading outside its band once never reached the session that could act on it, because
// the brief had no measures block. The block leads with the breach count.
func TestBriefShowsMeasuresAndFlagsABreach(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	for _, v := range []float64{1.8, 2.1} {
		if err := s.Measure(&buf, measure.MeasureArgs{Sub: "add", Key: "phosphate", Value: v,
			Unit: "mmol/L", Scope: "global", Source: "session"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Measure(&buf, measure.MeasureArgs{Sub: "limit", Key: "phosphate",
		Low: ptr(0.8), High: ptr(1.5), Better: "down"}); err != nil {
		t.Fatal(err)
	}
	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "## MEASURES  (1 outside its band)") {
		t.Errorf("the brief must lead the measures block with the breach count:\n%s", out)
	}
	if !strings.Contains(out, "ABOVE 1.5") {
		t.Errorf("a reading outside its band must say so in the brief:\n%s", out)
	}
	if !strings.Contains(out, "brain measure series <key> for the history") {
		t.Errorf("the measures block must say how to read the history:\n%s", out)
	}
}

// A key with no declared band is recorded and never judged, so it appears without a mark.
func TestBriefShowsAnUnjudgedMeasureWithoutABreachCount(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Measure(&buf, measure.MeasureArgs{Sub: "add", Key: "weight", Value: 80, Unit: "kg",
		Scope: "global", Source: "session"}); err != nil {
		t.Fatal(err)
	}
	out := brief(t, s, BriefArgs{})
	if !strings.Contains(out, "## MEASURES\n") {
		t.Errorf("an unjudged series still belongs in the brief:\n%s", out)
	}
	if strings.Contains(out, "outside its band") {
		t.Error("a key with no band must not be reported as breaching one")
	}
}

func TestBriefShowsThePipelineAndWhatIsDueOnIt(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Lead(&buf, lead.LeadArgs{Sub: "add", Name: "Jane", Company: "Acme",
		Status: ptr("demo"), ValueEst: ptr(int64(50000)), NextAction: ptr("call her"),
		NextDate: ptr("2026-08-01")}); err != nil {
		t.Fatal(err)
	}
	out := brief(t, s, BriefArgs{})
	for _, want := range []string{"## PIPELINE", "demo", "$50,000", "## HOT LEADS",
		"## NEXT ACTIONS", "Jane @ Acme: call her (2026-08-01)"} {
		if !strings.Contains(out, want) {
			t.Errorf("brief is missing %q:\n%s", want, out)
		}
	}
}

// A won lead is not a next action, and a lead nobody has to do anything about is not hot.
func TestBriefLeavesClosedLeadsOutOfNextActions(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Lead(&buf, lead.LeadArgs{Sub: "add", Name: "Bob", Company: "Beta",
		Status: ptr("won"), NextAction: ptr("invoice him")}); err != nil {
		t.Fatal(err)
	}
	out := brief(t, s, BriefArgs{})
	if strings.Contains(out, "## NEXT ACTIONS") {
		t.Errorf("a won lead must not appear as a next action:\n%s", out)
	}
	if strings.Contains(out, "## HOT LEADS") {
		t.Errorf("a won lead is not hot:\n%s", out)
	}
}

// Every list key is present on every brief and is an empty list, not absent and not null,
// so a consumer that iterates them does not have to nil-check first.
func TestBriefJSONCarriesTheMeasureAndLeadKeys(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Brief(&buf, BriefArgs{JSON: true}); err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"measures", "pipeline", "hot", "next_actions",
		"due_for_review", "pinned", "guardrails", "preferences", "scopes", "recent"} {
		v, ok := obj[k]
		if !ok {
			t.Errorf("brief JSON is missing %q", k)
			continue
		}
		if v == nil {
			t.Errorf("brief JSON %q is null; it must be []", k)
		}
	}
}

// The coverage map answers "you cannot search for what you do not know exists", so it has
// to reach every consumer of the brief, not only the human one.
func TestTheBriefCarriesCoverageInJSONToo(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "how the deploy works", Body: "x", Type: "infra"})
	var buf bytes.Buffer
	if err := s.Brief(&buf, BriefArgs{JSON: true, NoCursor: true}); err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	cats, ok := out["coverage"].([]interface{})
	if !ok || len(cats) == 0 {
		t.Fatal("brief --json carries no coverage map")
	}
	first := cats[0].(map[string]interface{})
	for _, k := range []string{"name", "count", "last"} {
		if _, ok := first[k]; !ok {
			t.Errorf("a coverage category has no %q", k)
		}
	}
}

// Project context set with `context set` (hosts, URLs, conventions) rides along in every
// brief.
func TestContextRidesAlongInTheBrief(t *testing.T) {
	s := newTestStore(t)
	if err := s.Context(io.Discard, ContextArgs{Sub: "set", Key: "prod",
		Value: "prod.example.com"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Brief(&buf, BriefArgs{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "prod.example.com") {
		t.Errorf("project context did not reach the brief:\n%s", buf.String())
	}
}

// --new-for 0 is a request ("only what is new since the last brief"), not an omission.
// Folding zero into the one-day default answered a different question while looking like a
// quiet brain.
func TestBriefNewForZeroMeansSinceTheLastBriefOnly(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "written twelve hours ago", Body: "x"})
	// Backdated behind the cursor: with --new-for 0 it falls outside the window, with
	// --new-for 1 the window opens a day and it is back in.
	if _, err := s.DB.Exec("UPDATE memories SET updated_at=updated_at-43200"); err != nil {
		t.Fatal(err)
	}
	brief(t, s, BriefArgs{}) // moves last_brief_at to now
	if out := brief(t, s, BriefArgs{NewFor: 0}); strings.Contains(out, "written twelve hours ago") {
		t.Errorf("--new-for 0 must not widen the window back to a day:\n%s", out)
	}
	if out := brief(t, s, BriefArgs{NewFor: 1}); !strings.Contains(out, "written twelve hours ago") {
		t.Errorf("--new-for 1 must reach back a day:\n%s", out)
	}
}
