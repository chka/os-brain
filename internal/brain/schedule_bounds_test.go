package brain

import (
	"testing"
	"time"
)

// Schedule counts are bounded. "every 99999999999999999999 minutes" once parsed to
// MaxInt64, wrapped negative and fired on every prompt. And the next due time is always in
// the future: a one-minute interval a week overdue used to stop its walk short and return a
// time in the past.
func TestScheduleCountsAreBounded(t *testing.T) {
	now := time.Now()
	for _, spec := range []string{"every 99999999999999999999 minutes", "in 99999999999999999999 hours",
		"every 1000001 minutes"} {
		if sc, err := ParseSchedule(spec, now); err == nil {
			t.Errorf("%q was accepted: %+v", spec, sc)
		}
	}
	if _, err := ParseSchedule("every 1000000 minutes", now); err != nil {
		t.Errorf("the bound itself refused: %v", err)
	}
	next, _ := NextDue("every", 1, "minutes", "", "", "UTC", now.Add(-7*24*time.Hour).Unix(), now.Unix())
	if next <= now.Unix() {
		t.Errorf("next due %d is not after now %d", next, now.Unix())
	}
	if next, _ := NextDue("every", -5, "minutes", "", "", "UTC", now.Unix()-60, now.Unix()); next > 0 && next <= now.Unix() {
		t.Errorf("a stored negative interval came back due in the past: %d", next)
	}
}
