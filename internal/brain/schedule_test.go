package brain

import (
	"testing"
	"time"
)

// Task schedules: the plain-English forms `task add --when` accepts, and when a recurring task
// comes due next.

// at parses a "yyyy-mm-dd hh:mm" time in UTC, so every expectation below is free of the
// machine's time zone.
func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The forms people actually type, each with the kind it parses to and its first occurrence
// from a fixed Friday afternoon. A time of day already past today means tomorrow.
func TestScheduleReadsWhatPeopleActuallyType(t *testing.T) {
	now := at(t, "2026-08-07 14:00") // a Friday
	for _, c := range []struct {
		in    string
		kind  string
		first string
	}{
		{"in 3 hours", "once", "2026-08-07 17:00"},
		{"in 30 minutes", "once", "2026-08-07 14:30"},
		{"5pm", "once", "2026-08-07 17:00"},
		{"9am", "once", "2026-08-08 09:00"}, // already past today, so tomorrow
		{"tomorrow 9am", "once", "2026-08-08 09:00"},
		{"2026-08-09 14:00", "once", "2026-08-09 14:00"},
		{"every 5 hours", "every", "2026-08-07 19:00"},
		{"daily 17:00", "daily", "2026-08-07 17:00"},
		{"every monday 9am", "weekly", "2026-08-10 09:00"},
		{"weekly mon,thu 09:00", "weekly", "2026-08-10 09:00"},
		{"monthly 1 09:00", "monthly", "2026-09-01 09:00"},
	} {
		sc, err := ParseSchedule(c.in, now)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if sc.Kind != c.kind {
			t.Errorf("%q: kind %q, want %q", c.in, sc.Kind, c.kind)
		}
		if got := time.Unix(sc.First, 0).UTC().Format("2006-01-02 15:04"); got != c.first {
			t.Errorf("%q: first %s, want %s", c.in, got, c.first)
		}
	}
}

// Refusing is the correct answer to something it cannot read. A reminder set for a time
// nobody meant is worse than an error at the moment of setting it, because the error is
// visible and the wrong time is not.
func TestScheduleRefusesWhatItCannotRead(t *testing.T) {
	now := at(t, "2026-08-07 14:00")
	for _, in := range []string{"in 2 seconds", "next tuesday-ish", "when I get up",
		"every fortnight", "25:00", "monthly 31"} {
		if sc, err := ParseSchedule(in, now); err == nil {
			t.Errorf("%q was accepted as %+v", in, sc)
		}
	}
}

// The catch-up rule: a week of downtime fires once and says what it skipped. Cron gets
// this wrong; anacron is the prior art for getting it right.
func TestCatchUpFiresOnceAndCountsWhatItSkipped(t *testing.T) {
	start := at(t, "2026-08-01 09:00").Unix()
	weekLater := at(t, "2026-08-08 10:00").Unix()
	// Due 1 Aug, nothing ran until the 8th: the 1st is the occurrence being delivered
	// now, and the 2nd through the 8th are the seven that passed unseen.
	next, skipped := NextDue("daily", 0, "", "09:00", "", "UTC", start, weekLater)
	if skipped != 7 {
		t.Errorf("skipped %d, want 7 missed days", skipped)
	}
	if got := time.Unix(next, 0).UTC().Format("2006-01-02 15:04"); got != "2026-08-09 09:00" {
		t.Errorf("next %s, want the following morning", got)
	}
}

// Drift: the next occurrence comes from the SCHEDULED time, not from when it was
// delivered, or "every Monday morning" walks into Wednesday afternoon.
func TestRecurrenceDoesNotDriftWhenDeliveredLate(t *testing.T) {
	scheduled := at(t, "2026-08-10 09:00").Unix() // a Monday
	late := at(t, "2026-08-10 16:30").Unix()      // delivered seven hours late
	next, _ := NextDue("weekly", 0, "", "09:00", "mon", "UTC", scheduled, late)
	if got := time.Unix(next, 0).UTC().Format("2006-01-02 15:04"); got != "2026-08-17 09:00" {
		t.Errorf("next %s, want the following Monday at 09:00", got)
	}
}

// A one-shot task is done once delivered: zero means "no next occurrence", and anything else
// would deliver it again.
func TestOneShotHasNoNextOccurrence(t *testing.T) {
	if next, _ := NextDue("once", 0, "", "", "", "UTC", 1, 2); next != 0 {
		t.Errorf("a one-shot rescheduled itself to %d", next)
	}
}
