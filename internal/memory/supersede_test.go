package memory

import (
	"strings"
	"testing"
)

// Supersession is the founding guarantee: a correction must not end up competing with the
// error it corrects at equal weight. A query in the outdated wording answers with the
// current fact, exactly once, and says which retracted memory it was reached through.

// A retracted fact must not keep its slot in every brief.
func TestSupersedingUnpinsTheOldFact(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "old rule", Body: "x", Pin: true})
	remember(t, s, RememberArgs{Title: "new rule", Body: "y", Supersedes: 1})
	m, err := s.Own(1, "read")
	if err != nil {
		t.Fatal(err)
	}
	if m.Pinned != 0 {
		t.Error("a retracted fact must not keep its slot in every brief")
	}
}

// get on a superseded memory must say it was replaced, and by what. The JSON always carried
// superseded_by; the human output is what an agent reads, and it showed a retracted fact
// exactly like a current one.
func TestGetMarksARetractedMemory(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "the queue runs on Postgres", Body: "original"})
	remember(t, st, RememberArgs{Scope: "global", Type: "decision",
		Title: "the queue moved to Redis streams", Body: "CORRECTED", Supersedes: 1})
	var buf strings.Builder
	if err := st.Get(&buf, 1, false, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SUPERSEDED", "#2", "the queue moved to Redis streams"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("get on a retracted memory does not say so (%q missing):\n%s", want, buf.String())
		}
	}
	var live strings.Builder
	if err := st.Get(&live, 2, false, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(live.String(), "SUPERSEDED") {
		t.Fatal("a current memory was marked as retracted")
	}
}
