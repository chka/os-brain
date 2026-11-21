package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/textguard"
)

// The output format is part of the contract. The brain's text and JSON output follow the
// original Python implementation byte for byte, because the team edition opens the same
// brain and prints the same things, and scripts parse and diff the output of both. Go's
// defaults differ from Python's in several places; each test pins one of them.

// 2.1 - 1.8 is 0.30000000000000004 in binary floating point. Go's shortest round-trip
// prints all of it; Python's %g prints 0.3, which is what the user should read.
func TestADeltaIsFormattedTheWayPythonFormatsIt(t *testing.T) {
	if got := pyfmt.PyG(2.1 - 1.8); got != "0.3" {
		t.Errorf("delta rendered as %q", got)
	}
	if got := pyfmt.PyG(12); got != "12" {
		t.Errorf("integer rendered as %q", got)
	}
}

// Python's json.dumps escapes non-ASCII and leaves <, > and & alone. Go's encoder does the
// opposite of both, so the same dump differed byte for byte.
func TestJSONIsEscapedTheWayPythonEscapesIt(t *testing.T) {
	out, err := pyfmt.MarshalPy(map[string]string{"t": "φώσφορος & <b>"}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `\u03c6`) {
		t.Errorf("non-ASCII was not escaped: %s", got)
	}
	if strings.Contains(got, `\u0026`) || strings.Contains(got, `\u003c`) {
		t.Errorf("HTML characters were escaped, which Python does not do: %s", got)
	}
}

// Messages quote values the way Python's repr() does: single quotes, unless the value
// itself holds one.
func TestReprMatchesPythonQuoting(t *testing.T) {
	if got := pyfmt.Repr("channel:deploy"); got != "'channel:deploy'" {
		t.Errorf("got %s", got)
	}
	if got := pyfmt.Repr(`it's here`); got != `"it's here"` {
		t.Errorf("got %s", got)
	}
}

// A whole-numbered reading exports as 80.0, as Python's json.dumps writes a float.
func TestPyFloatKeepsTheDecimalPoint(t *testing.T) {
	b, err := json.Marshal(pyfmt.PyFloat(80))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "80.0" {
		t.Errorf("PyFloat(80) marshalled as %s, want 80.0", b)
	}
	if b, _ := json.Marshal(pyfmt.PyFloat(1.25)); string(b) != "1.25" {
		t.Errorf("PyFloat(1.25) marshalled as %s", b)
	}
}

// Every caller re-indents a clipped body, which did nothing while Clip flattened newlines
// first, and the tail said nothing about how much had been cut.
func TestClipKeepsNewlinesAndSaysHowMuchWasCut(t *testing.T) {
	if got := textguard.Clip("one\ntwo", 100); got != "one\ntwo" {
		t.Errorf("a short body must come back untouched, got %q", got)
	}
	got := textguard.Clip(strings.Repeat("a", 50), 10)
	if !strings.Contains(got, "(+40 chars, os-brain get <id>)") {
		t.Errorf("the tail must say how much was cut: %q", got)
	}
}

// The web viewer maps over these arrays. A nil slice marshals to null and the page breaks.
func TestEmptyListsMarshalAsListsNotNull(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := memory.New(s).Events(&buf, memory.EventsArgs{JSON: true}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "[]" {
		t.Errorf("events with no rows must be [], got %q", strings.TrimSpace(buf.String()))
	}
}

// Every JSON payload carries the clock (now, now_epoch) so an agent never has to guess the
// time. Only map payloads used to get it; struct payloads went out without.
func TestStructPayloadsStillCarryTheClock(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "a fact", Body: "x"})
	var buf bytes.Buffer
	if err := memory.New(s).Get(&buf, 1, false, true); err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("get --json is not valid JSON: %v\n%s", err, buf.String())
	}
	for _, k := range []string{"now", "now_epoch", "title", "body"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("get --json is missing %q", k)
		}
	}
}
