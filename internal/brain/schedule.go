package brain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The schedule grammar: five closed forms, and deliberately not RFC 5545.
//
//	once      "in 3 hours" · "tomorrow 9am" · "today 5pm" · "5pm" · "2026-08-09 14:00"
//	every     "every 5 hours" · "every 20 minutes"
//	daily     "daily 17:00" · "every day at 9am"
//	weekly    "every monday 9am" · "weekly mon,thu 09:00"
//	monthly   "monthly 1 09:00" · "every month on the 15th"
//
// An RRULE parser is a month of edge cases bought for expressiveness nobody asked for,
// and its failure mode is the worst one available here: a reminder that silently never
// fires. A closed grammar can refuse what it does not understand, which is the whole
// difference.
//
// How the forms are read, and the edge cases that follow from it:
//
//   - Input is trimmed, lowercased and may end in one full stop. Empty input is "no
//     schedule" (a nil Schedule and no error), which is how a task becomes a list item.
//   - Units: m, min, mins, minute, minutes; h, hr, hrs, hour, hours; d, day, days;
//     w, week, weeks. The space between the count and the unit is optional ("in 3h").
//     Seconds are not a unit: nothing here runs on a clock that fine.
//   - Counts are capped at maxScheduleN. "in 0 minutes" is due at once; "every 0 hours"
//     is refused because an interval of nothing would fire on every prompt.
//   - A clock is "9", "9am", "9:30", "17:00" or "5 pm". With am/pm the hour must be 1..12;
//     without, 0..23. Minutes must be 0..59.
//   - "tomorrow" without a clock means 09:00, "today" without a clock means 17:00, and a
//     date, a day, a week or a month without a clock all mean 09:00.
//   - "today 9am" said at 17:00 is accepted and is due immediately: it names a time that
//     has passed, and the person asked for today.
//   - A bare clock ("5pm") is the next time that clock comes round: later today, or
//     tomorrow when it has passed. Same for the first "daily" occurrence.
//   - A date is read as YYYY-MM-DD and passed to time.Date, which normalises an
//     impossible day rather than refusing it: "2026-02-30" lands on 2 March.
//   - Monthly takes a day from 1 to 28 only. Months differ in length, and a reminder that
//     skips February is worse than one that refuses. Default day is the 1st.
//   - Weekly days are separated by commas or spaces and read by their first three letters,
//     so "monday", "mon" and "mondays" are all Monday (and so is anything else that starts
//     "mon"). One unknown day refuses the weekly reading entirely.
//   - The forms are tried in a fixed order (see ParseSchedule) and the first match wins.
//     The order matters: "every 5 hours" must be tried before the weekly form, and
//     "every month" before "every <weekday>".
//
// Every time is computed in the location of `now` and that location's name is stored as
// TZ, so a recurring task keeps its wall-clock time across daylight-saving changes.

type Schedule struct {
	Kind  string // once | every | daily | weekly | monthly
	N     int
	Unit  string // minutes | hours | days | weeks
	At    string // HH:MM
	Days  string // mon,thu  |  day-of-month
	Says  string // what was asked for, rendered back
	TZ    string
	First int64 // first due time
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

var (
	reIn      = regexp.MustCompile(`^in\s+(\d+)\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days|w|week|weeks)$`)
	reEveryN  = regexp.MustCompile(`^every\s+(\d+)\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days|w|week|weeks)$`)
	reDaily   = regexp.MustCompile(`^(?:daily|every\s+day)(?:\s+(?:at\s+)?(.+))?$`)
	reWeekly  = regexp.MustCompile(`^(?:weekly|every)\s+([a-z,\s]+?)(?:\s+(?:at\s+)?([0-9].*))?$`)
	reMonthly = regexp.MustCompile(`^(?:monthly|every\s+month)(?:\s+(?:on\s+the\s+)?(\d{1,2})(?:st|nd|rd|th)?)?(?:\s+(?:at\s+)?(.+))?$`)
	reClock   = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
	reDate    = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:\s+(.+))?$`)
)

// maxScheduleN bounds the count in "in N units" and "every N units". Atoi answers
// MaxInt64 on overflow, and N minutes as a Duration then wraps negative: a reminder due
// before it was set, firing on every prompt. A million of any unit is past any real plan.
const maxScheduleN = 1000000

// unitOf maps any accepted spelling of a unit to its canonical name. The regexps have
// already limited the spelling, so the first letter is enough.
func unitOf(s string) string {
	switch {
	case strings.HasPrefix(s, "m"):
		return "minutes"
	case strings.HasPrefix(s, "h"):
		return "hours"
	case strings.HasPrefix(s, "d"):
		return "days"
	}
	return "weeks"
}

// addUnits steps a time forward. Days and weeks go through AddDate so they keep the
// wall-clock time across a daylight-saving change; minutes and hours are exact durations.
func addUnits(t time.Time, n int, unit string) time.Time {
	switch unit {
	case "minutes":
		return t.Add(time.Duration(n) * time.Minute)
	case "hours":
		return t.Add(time.Duration(n) * time.Hour)
	case "days":
		return t.AddDate(0, 0, n)
	}
	return t.AddDate(0, 0, 7*n)
}

func scheduleN(digits, raw string) (int, error) {
	n, err := strconv.Atoi(digits)
	if err != nil || n > maxScheduleN {
		return 0, fmt.Errorf("%s: the count is past %d, which is not a schedule anybody "+
			"keeps; nothing was set", Repr(raw), maxScheduleN)
	}
	return n, nil
}

// clockOf reads "9am", "17:00", "9", "5 pm". Returns hour, minute, ok.
func clockOf(s string) (int, int, bool) {
	m := reClock.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	h, _ := strconv.Atoi(m[1])
	min := 0
	if m[2] != "" {
		min, _ = strconv.Atoi(m[2])
	}
	// An explicit meridiem constrains the hour to 1..12. "13pm" was once accepted and
	// silently stored as 13:00, which is a reminder set for a time the user did not name:
	// the exact failure the grammar refuses everything else to avoid.
	if m[3] != "" && (h < 1 || h > 12) {
		return 0, 0, false
	}
	switch m[3] {
	case "pm":
		if h < 12 {
			h += 12
		}
	case "am":
		if h == 12 {
			h = 0
		}
	}
	if h > 23 || min > 59 {
		return 0, 0, false
	}
	return h, min, true
}

// atOrDefault normalises an optional clock to HH:MM. An empty clock takes the default;
// one that cannot be read reports false so the caller refuses the whole schedule.
func atOrDefault(s string, dflt string) (string, bool) {
	if strings.TrimSpace(s) == "" {
		return dflt, true
	}
	h, m, ok := clockOf(s)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

// atOn is the clock `at` (already HH:MM) on the calendar day of `day`.
func atOn(day time.Time, at string) time.Time {
	h, m, _ := clockOf(at)
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
}

// nextClock returns HH:MM on the day of `from`, or the day after when that moment is not
// strictly later than `from` or when skipToday asks for tomorrow regardless.
func nextClock(from time.Time, at string, skipToday bool) time.Time {
	t := atOn(from, at)
	if skipToday || !t.After(from) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func cannotReadTime(raw string) error {
	return fmt.Errorf("cannot read a time in %q", raw)
}

// scheduleForm is one form of the grammar. It answers matched=false when the input is not
// its form at all, so the next form is tried; once it has matched, an error refuses the
// whole input rather than letting a later form reinterpret it.
type scheduleForm func(s, raw string, now time.Time, sc *Schedule) (matched bool, err error)

// scheduleForms is the order the forms are tried in. The order is part of the grammar:
// "every 5 hours" is an interval before it could be a weekday list, "every month" is
// monthly before "every <weekday>" sees it, and a bare clock is the last resort.
var scheduleForms = []scheduleForm{
	parseIn,
	parseEveryN,
	parseTomorrow,
	parseToday,
	parseDate,
	parseMonthly,
	parseDaily,
	parseWeekly,
	parseBareClock,
}

// ParseSchedule turns what the user said into the five forms. It refuses anything it does
// not understand rather than guessing: a reminder set for a time nobody meant is worse
// than an error at the moment of setting it, because the error is visible.
func ParseSchedule(spec string, now time.Time) (*Schedule, error) {
	raw := strings.TrimSpace(spec)
	s := strings.ToLower(raw)
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return nil, nil
	}
	sc := &Schedule{Says: raw, TZ: now.Location().String()}
	for _, form := range scheduleForms {
		matched, err := form(s, raw, now, sc)
		if err != nil {
			return nil, err
		}
		if matched {
			return sc, nil
		}
	}
	return nil, fmt.Errorf("cannot read %q as a time.\nUnderstood: \"in 3 hours\", "+
		"\"tomorrow 9am\", \"5pm\", \"2026-08-09 14:00\", \"every 5 hours\", "+
		"\"daily 17:00\", \"every monday 9am\", \"monthly 1 09:00\"", raw)
}

// parseIn reads "in 3 hours": a one-shot that many units from now.
func parseIn(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	m := reIn.FindStringSubmatch(s)
	if m == nil {
		return false, nil
	}
	n, err := scheduleN(m[1], raw)
	if err != nil {
		return true, err
	}
	sc.Kind, sc.N, sc.Unit = "once", n, unitOf(m[2])
	sc.First = addUnits(now, n, sc.Unit).Unix()
	return true, nil
}

// parseEveryN reads "every 5 hours": a fixed interval, first due one interval from now.
func parseEveryN(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	m := reEveryN.FindStringSubmatch(s)
	if m == nil {
		return false, nil
	}
	n, err := scheduleN(m[1], raw)
	if err != nil {
		return true, err
	}
	if n < 1 {
		return true, fmt.Errorf("every %d is not an interval", n)
	}
	sc.Kind, sc.N, sc.Unit = "every", n, unitOf(m[2])
	sc.First = addUnits(now, n, sc.Unit).Unix()
	return true, nil
}

// parseTomorrow reads "tomorrow" with an optional clock, 09:00 by default.
func parseTomorrow(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	if !strings.HasPrefix(s, "tomorrow") {
		return false, nil
	}
	at, ok := atOrDefault(strings.TrimSpace(strings.TrimPrefix(s, "tomorrow")), "09:00")
	if !ok {
		return true, cannotReadTime(raw)
	}
	sc.Kind, sc.At = "once", at
	// The clock on the calendar day after the one that holds (now + 1 day - 24h). Away
	// from a daylight-saving change that is simply tomorrow at `at`.
	sc.First = nextClock(now.AddDate(0, 0, 1).Add(-24*time.Hour), at, true).Unix()
	return true, nil
}

// parseToday reads "today" with an optional clock, 17:00 by default. It does not move a
// past time to tomorrow: the person said today, so a time already gone is due now.
func parseToday(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	if !strings.HasPrefix(s, "today") {
		return false, nil
	}
	at, ok := atOrDefault(strings.TrimSpace(strings.TrimPrefix(s, "today")), "17:00")
	if !ok {
		return true, cannotReadTime(raw)
	}
	sc.Kind, sc.At = "once", at
	sc.First = atOn(now, at).Unix()
	return true, nil
}

// parseDate reads "2026-08-09" with an optional clock, 09:00 by default.
func parseDate(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	m := reDate.FindStringSubmatch(s)
	if m == nil {
		return false, nil
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	at, ok := atOrDefault(m[4], "09:00")
	if !ok {
		return true, cannotReadTime(raw)
	}
	h, mi, _ := clockOf(at)
	sc.Kind, sc.At = "once", at
	sc.First = time.Date(y, time.Month(mo), d, h, mi, 0, 0, now.Location()).Unix()
	return true, nil
}

// parseMonthly reads "monthly 15 09:00" and "every month on the 15th". The first
// occurrence is this month's when it is still ahead, otherwise next month's.
func parseMonthly(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	m := reMonthly.FindStringSubmatch(s)
	// The regexp allows any run of spaces in "every month"; the prefix test admits only
	// the single-space spelling, so other spacings fall through to the later forms.
	if m == nil || !(strings.HasPrefix(s, "monthly") || strings.HasPrefix(s, "every month")) {
		return false, nil
	}
	day := 1
	if m[1] != "" {
		day, _ = strconv.Atoi(m[1])
	}
	if day < 1 || day > 28 {
		return true, fmt.Errorf("monthly needs a day between 1 and 28: months are not the "+
			"same length and a reminder that skips February is worse than one that refuses (%q)", raw)
	}
	at, ok := atOrDefault(m[2], "09:00")
	if !ok {
		return true, cannotReadTime(raw)
	}
	sc.Kind, sc.At, sc.Days = "monthly", at, strconv.Itoa(day)
	h, mi, _ := clockOf(at)
	t := time.Date(now.Year(), now.Month(), day, h, mi, 0, 0, now.Location())
	if !t.After(now) {
		// Safe to add a month: the day is at most 28, so it never overflows into the
		// month after.
		t = t.AddDate(0, 1, 0)
	}
	sc.First = t.Unix()
	return true, nil
}

// parseDaily reads "daily 17:00" and "every day at 9am", 09:00 by default.
func parseDaily(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	m := reDaily.FindStringSubmatch(s)
	if m == nil {
		return false, nil
	}
	at, ok := atOrDefault(m[1], "09:00")
	if !ok {
		return true, cannotReadTime(raw)
	}
	sc.Kind, sc.At = "daily", at
	sc.First = nextClock(now, at, false).Unix()
	return true, nil
}

// parseWeekly reads "every monday 9am" and "weekly mon,thu 09:00", 09:00 by default.
// Input that has the shape but names something other than weekdays ("every fortnight")
// is not weekly, and falls through to the bare clock and then to the refusal.
func parseWeekly(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	m := reWeekly.FindStringSubmatch(s)
	if m == nil {
		return false, nil
	}
	days := weekdayKeys(m[1])
	if len(days) == 0 {
		return false, nil
	}
	at, ok := atOrDefault(m[2], "09:00")
	if !ok {
		return true, cannotReadTime(raw)
	}
	sc.Kind, sc.At, sc.Days = "weekly", at, strings.Join(days, ",")
	sc.First = nextWeekly(now, sc).Unix()
	return true, nil
}

// weekdayKeys turns "mon, thursday" into ["mon", "thu"], or nil when any part is not a
// weekday: a list with one unknown day is refused whole rather than set for the rest.
func weekdayKeys(list string) []string {
	var days []string
	for _, part := range strings.Split(strings.ReplaceAll(list, " ", ","), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key := part
		if len(key) > 3 {
			key = key[:3]
		}
		if _, ok := weekdays[key]; !ok {
			return nil
		}
		days = append(days, key)
	}
	return days
}

// parseBareClock reads "5pm" or "17:00": the next time that clock comes round.
func parseBareClock(s, raw string, now time.Time, sc *Schedule) (bool, error) {
	at, ok := atOrDefault(s, "")
	if !ok || at == "" {
		return false, nil
	}
	sc.Kind, sc.At = "once", at
	sc.First = nextClock(now, at, false).Unix()
	return true, nil
}

// nextWeekly is the earliest of the schedule's weekdays at its clock that is strictly
// after `from`. A weekday that is today but whose time has passed counts from next week.
func nextWeekly(from time.Time, sc *Schedule) time.Time {
	today := atOn(from, sc.At)
	best := time.Time{}
	for _, d := range strings.Split(sc.Days, ",") {
		delta := (int(weekdays[d]) - int(from.Weekday()) + 7) % 7
		t := today.AddDate(0, 0, delta)
		if !t.After(from) {
			t = t.AddDate(0, 0, 7)
		}
		if best.IsZero() || t.Before(best) {
			best = t
		}
	}
	return best
}

// maxCatchUpSteps bounds the walk in NextDue. A schedule that is still behind after this
// many steps is reported as having no next occurrence rather than answered with a time in
// the past.
const maxCatchUpSteps = 10000

// NextDue advances a recurring schedule past `after`. It is computed from the SCHEDULED
// time, not from when the reminder was actually delivered: a weekly task shown four hours
// late still lands next Monday at 09:00, and drift is how "every Monday morning" becomes
// Wednesday afternoons.
//
// It returns the next occurrence strictly after `after`, and how many were skipped
// getting there. One fire on catch-up, with a count: cron gets this wrong and anacron is
// the prior art for getting it right. It always moves at least one step past
// `scheduled`, so marking a recurring task done before it is due still moves it on.
//
// It returns 0 when there is no next occurrence: a one-shot, an interval below 1, or a
// schedule the walk could not bring past `after`.
func NextDue(kind string, n int, unit, at, days, tz string, scheduled, after int64) (int64, int) {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.Local
	}
	t := time.Unix(scheduled, 0).In(loc)
	cut := time.Unix(after, 0).In(loc)
	if kind == "every" && n < 1 {
		return 0, 0
	}
	if kind == "every" && (unit == "minutes" || unit == "hours") && !t.After(cut) {
		return jumpFixedInterval(t, cut, n, unit)
	}
	skipped := 0
	for i := 0; i < maxCatchUpSteps; i++ {
		switch kind {
		case "every":
			t = addUnits(t, n, unit)
		case "daily":
			t = t.AddDate(0, 0, 1)
		case "weekly":
			t = nextWeekly(t, &Schedule{At: at, Days: days})
		case "monthly":
			t = t.AddDate(0, 1, 0)
		default:
			return 0, 0 // a one-shot has no next occurrence
		}
		if t.After(cut) {
			return t.Unix(), skipped
		}
		skipped++
	}
	// Still not past `after`: a time in the past would be due on every prompt from now on.
	return 0, skipped
}

// jumpFixedInterval answers NextDue for minutes and hours arithmetically. A fixed interval
// needs no walk, and walking one stopped short: every 1 minute over a week's absence is
// 10080 steps, past the cap, and the "next" it returned was still in the past, so the
// reminder fired on every prompt after. The answer and the skipped count are the same
// ones the walk would give.
func jumpFixedInterval(t, cut time.Time, n int, unit string) (int64, int) {
	step := time.Duration(n) * time.Minute
	if unit == "hours" {
		step = time.Duration(n) * time.Hour
	}
	k := cut.Sub(t)/step + 1
	return t.Add(k * step).Unix(), int(k) - 1
}

// Describe renders a schedule the way it will behave, which is not always the way it was
// typed. The stored string is what somebody asked for; this is what they get.
func Describe(kind string, n int, unit, at, days string) string {
	switch kind {
	case "every":
		return fmt.Sprintf("every %d %s", n, unit)
	case "daily":
		return "daily at " + at
	case "weekly":
		return "every " + days + " at " + at
	case "monthly":
		return "monthly on the " + days + " at " + at
	}
	return "once"
}
