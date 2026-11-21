package memory

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// Review dates. A fact the outside world owns (a host, an access path) can go stale
// without anything in the project changing, so a new memory of such a type gets a date on
// which the brief asks for it to be rechecked. Without it, a host fact read once reads as
// exactly as current weeks later.

// reviewOf returns the review_on column of memory id (nil when it has none).
func reviewOf(t *testing.T, s *Store, id int64) *int64 {
	t.Helper()
	var ts *int64
	if err := s.DB.QueryRow("SELECT review_on FROM memories WHERE id=?", id).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

// wantDay fails unless ts is set and falls on the day want (YYYY-MM-DD).
func wantDay(t *testing.T, ts *int64, want string, what string) {
	t.Helper()
	if ts == nil {
		t.Fatalf("%s: no review date", what)
	}
	if got := timefmt.Day(*ts); got != want {
		t.Errorf("%s: review is %s, want %s", what, got, want)
	}
}

func TestAWorldOwnedMemoryAsksToBeRecheckedByDefault(t *testing.T) {
	s := newTestStore(t)
	want := time.Now().AddDate(0, 0, store.DefaultReviewDays).Format("2006-01-02")
	for _, memType := range []string{"infra", "access"} {
		out := remember(t, s, RememberArgs{Type: memType, Scope: memType,
			Title: "the " + memType + " box at 192.0.2.10 is staging"})
		wantDay(t, reviewOf(t, s, lastID(t, s)), want, memType)
		// A write that silently acquired a review date is a surprise the next session pays for.
		if !strings.Contains(out, "review on ") || !strings.Contains(out, "--no-review") {
			t.Errorf("%s: the write did not say it set a review date: %q", memType, out)
		}
	}
}

// A decision is falsified by the user, not by the calendar, and nagging about one invites
// re-arguing a settled call. A gotcha is dated against the tree by source_commit instead,
// and the documentation types are documentation.
func TestATypeTimeCannotFalsifyGetsNoReviewHorizon(t *testing.T) {
	s := newTestStore(t)
	for _, memType := range []string{"decision", "constraint", "commitment", "gotcha",
		"state", "incident", "preference", "note", "policy"} {
		out := remember(t, s, RememberArgs{Type: memType, Title: memType + " about the queue"})
		if ts := reviewOf(t, s, lastID(t, s)); ts != nil {
			t.Errorf("%s acquired a review date of %s", memType, timefmt.Day(*ts))
		}
		if strings.Contains(out, "review on ") {
			t.Errorf("%s: output claims a review date: %q", memType, out)
		}
	}
}

func TestAnExplicitReviewDateBeatsTheDefault(t *testing.T) {
	s := newTestStore(t)
	want := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	remember(t, s, RememberArgs{Type: "infra", Title: "the jump host is 198.51.100.7",
		ReviewOn: want})
	ts := reviewOf(t, s, lastID(t, s))
	if ts == nil || timefmt.Day(*ts) != want {
		t.Errorf("explicit --review %s did not win", want)
	}
}

// --no-review is the escape hatch for a fact the caller knows is permanent. It must not
// refuse the write, and it must not leave a date behind.
func TestNoReviewStoresAWorldOwnedFactWithNoHorizon(t *testing.T) {
	s := newTestStore(t)
	out := remember(t, s, RememberArgs{Type: "infra",
		Title: "the datacentre region is fixed and will not move", NoReview: true})
	if ts := reviewOf(t, s, lastID(t, s)); ts != nil {
		t.Errorf("--no-review still stored %s", timefmt.Day(*ts))
	}
	if strings.Contains(out, "review on ") {
		t.Errorf("--no-review output mentions a review date: %q", out)
	}
}

// Contradictory flags are not worth losing a write over: the date is the more specific
// instruction, so it wins, the memory is stored and the contradiction is reported.
func TestNoReviewAndAnExplicitDateKeepsTheDateAndStoresTheMemory(t *testing.T) {
	s := newTestStore(t)
	var warned bytes.Buffer
	defer captureWarnings(&warned)()
	want := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	remember(t, s, RememberArgs{Type: "infra", Title: "the vault lives on the dev box",
		ReviewOn: want, NoReview: true})
	ts := reviewOf(t, s, lastID(t, s))
	if ts == nil || timefmt.Day(*ts) != want {
		t.Fatalf("the explicit date did not win: %v", ts)
	}
	if !strings.Contains(warned.String(), "--no-review ignored") {
		t.Errorf("the contradiction was not reported: %q", warned.String())
	}
}

// Backfilling a date onto an existing row would assert that every old infra fact had been
// triaged today, which is the same lie as backfilling source_commit. Restating, editing and
// verifying an existing row must all leave a null review date null.
func TestAMemoryWrittenBeforeThisExistedIsNeverStampedWithAHorizon(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "infra", Title: "the prod box is 203.0.113.5",
		Body: "first", NoReview: true})
	id := lastID(t, s)

	remember(t, s, RememberArgs{Type: "infra", Title: "the prod box is 203.0.113.5",
		Body: "second", Update: true})
	if ts := reviewOf(t, s, id); ts != nil {
		t.Errorf("restating stamped %s onto an existing row", timefmt.Day(*ts))
	}
	var buf bytes.Buffer
	body := "third"
	if err := s.Update(&buf, UpdateArgs{ID: id, Body: &body}); err != nil {
		t.Fatal(err)
	}
	if ts := reviewOf(t, s, id); ts != nil {
		t.Errorf("update stamped %s onto an existing row", timefmt.Day(*ts))
	}
	if err := s.Verify(&buf, VerifyArgs{ID: id}); err != nil {
		t.Fatal(err)
	}
	if ts := reviewOf(t, s, id); ts != nil {
		t.Errorf("verify stamped %s onto an existing row", timefmt.Day(*ts))
	}
}

// The brief tells a session that is done with a review to clear the date, so update must be
// able to. --review takes a date, and an empty string cannot be told apart from the flag
// being absent, so clearing needs --no-review.
func TestUpdateCanMoveAndClearAReviewDate(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "access", Title: "the deploy key lives in vault at kv/deploy"})
	id := lastID(t, s)
	var buf bytes.Buffer

	moved := time.Now().AddDate(0, 0, 90).Format("2006-01-02")
	if err := s.Update(&buf, UpdateArgs{ID: id, ReviewOn: moved}); err != nil {
		t.Fatal(err)
	}
	if ts := reviewOf(t, s, id); ts == nil || timefmt.Day(*ts) != moved {
		t.Errorf("update --review did not move the date to %s", moved)
	}
	if err := s.Update(&buf, UpdateArgs{ID: id, NoReview: true}); err != nil {
		t.Fatal(err)
	}
	if ts := reviewOf(t, s, id); ts != nil {
		t.Errorf("update --no-review left %s in place", timefmt.Day(*ts))
	}
}
