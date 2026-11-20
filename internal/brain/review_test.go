package brain

import (
	"bytes"
	"strings"
	"testing"
)

// `review` reports what needs a person's attention (near-duplicates, load-bearing facts
// nobody has verified in a while). It is a report only.

func TestReviewNeverChangesAnything(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "nginx on 8443", Type: "infra"})
	remember(t, s, RememberArgs{Title: "nginx on 8443 tls", Type: "infra"})
	before, _ := s.count("SELECT COUNT(*) FROM memories")
	var buf bytes.Buffer
	if err := s.Review(&buf, ReviewArgs{}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.count("SELECT COUNT(*) FROM memories")
	if before != after {
		t.Errorf("review changed the store: %d -> %d", before, after)
	}
	if !strings.Contains(buf.String(), "NEAR-DUPLICATE") {
		t.Errorf("two near-identical titles were not flagged:\n%s", buf.String())
	}
}

// --stale-days 0 is a request ("everything unverified"), not an omission: folding it into
// the default answered a different question while looking like a clean brain.
func TestReviewStaleDaysZeroMeansEverythingUnverified(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "gotcha", Title: "a load bearing fact here", Body: "x"})
	// A minute old, not this second: the comparison is strictly less-than, so a row created
	// inside the current second is not yet stale at any window.
	if _, err := s.DB.Exec("UPDATE memories SET created_at=created_at-60"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Review(&buf, ReviewArgs{StaleDays: 0}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "UNVERIFIED >0d") {
		t.Errorf("--stale-days 0 must list every unverified tier A/B memory:\n%s", buf.String())
	}
	buf.Reset()
	if err := s.Review(&buf, ReviewArgs{StaleDays: 180}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "UNVERIFIED") {
		t.Error("a fact written a minute ago is not 180 days unverified")
	}
}
