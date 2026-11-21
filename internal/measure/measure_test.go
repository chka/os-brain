package measure

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Measures are numeric series (a reading, a count, a latency) kept beside the memories.
// A series is judged only against a band someone declared, and reading it fails closed
// like recall does.

func TestAMeasureKeyIsOneSeriesHoweverItIsSpelled(t *testing.T) {
	if got := NormMeasureKey("Req/S"); got != "req_s" {
		t.Errorf("Req/S normalised to %q", got)
	}
	if NormMeasureKey("req s") != NormMeasureKey("Req/S") || NormMeasureKey("req-s") != "req_s" {
		t.Error("the same key spelled three ways is not one series")
	}
}

// Most numbers have no right answer, and a tool that invents one starts shouting about
// readings nobody asked it to police.
func TestAKeyWithNoBandIsRecordedAndNeverJudged(t *testing.T) {
	s := newTestStore(t)
	if err := s.Measure(io.Discard, MeasureArgs{Sub: "add", Key: "tmp", Value: 900}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Measure(&buf, MeasureArgs{Sub: "list"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "ABOVE") || strings.Contains(buf.String(), "BELOW") {
		t.Errorf("judged a key with no declared band:\n%s", buf.String())
	}
}

func TestABreachIsReportedOnceABandExists(t *testing.T) {
	s := newTestStore(t)
	low, high := 0.8, 1.5
	if err := s.Measure(io.Discard, MeasureArgs{Sub: "limit", Key: "phosphate",
		Low: &low, High: &high, Better: "down"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Measure(&buf, MeasureArgs{Sub: "add", Key: "phosphate", Value: 2.1,
		Unit: "mmol/L"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "above its declared ceiling") {
		t.Errorf("a breach was not reported:\n%s", buf.String())
	}
}

// Fail closed, the same rule recall follows: an unknown key returns nothing rather than
// every series, and names the keys that do exist.
func TestAnUnknownMeasureKeyReturnsNothingRatherThanEverySeries(t *testing.T) {
	s := newTestStore(t)
	if err := s.Measure(io.Discard, MeasureArgs{Sub: "add", Key: "phosphate", Value: 1.2}); err != nil {
		t.Fatal(err)
	}
	res := mustJSON(t, func(w io.Writer) error {
		return s.Measure(w, MeasureArgs{Sub: "series", Key: "nonsense", JSON: true})
	})
	if res["matched"] != false || res["count"].(float64) != 0 {
		t.Errorf("an unknown key returned %v", res)
	}
	if known, _ := res["known_keys"].([]interface{}); len(known) != 1 {
		t.Errorf("the miss should name the keys that do exist: %v", res["known_keys"])
	}
}

// Keys are ASCII by necessity while the project that owns them may work entirely in another
// language. A search in Greek once missed a whole series because only the key was matched,
// not its label.
func TestMeasuresAreSearchableInTheirOwnLanguage(t *testing.T) {
	s := newTestStore(t)
	if err := s.Measure(io.Discard, MeasureArgs{Sub: "add", Key: "phosphate", Value: 1.2,
		Label: "φώσφορος"}); err != nil {
		t.Fatal(err)
	}
	if hits := s.MeasuresMatching("φωσφορος", ""); len(hits) != 1 {
		t.Errorf("an accent-folded label does not match its series: %d hits", len(hits))
	}
}
