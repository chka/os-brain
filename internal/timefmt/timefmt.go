package timefmt

import (
	"fmt"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/textguard"
)

func Now() int64 { return time.Now().Unix() }

// DayPart names the part of the day an hour falls in, for the greeting a brief opens with.
func DayPart(hour int) string {
	switch {
	case hour < 5:
		return "night"
	case hour < 12:
		return "morning"
	case hour < 18:
		return "afternoon"
	case hour < 22:
		return "evening"
	}
	return "night"
}

// HumanGap renders an age in seconds the way a person says it: "just now", "5m ago".
func HumanGap(seconds int64) string {
	if seconds < 90 {
		return "just now"
	}
	mins := seconds / 60
	if mins < 90 {
		return fmt.Sprintf("%dm ago", mins)
	}
	hours := mins / 60
	if hours < 36 {
		return fmt.Sprintf("%dh ago", hours)
	}
	return fmt.Sprintf("%dd ago", hours/24)
}

// ISO renders a timestamp as local date and minute; 0 is "".
func ISO(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Local().Format("2006-01-02 15:04")
}

// Day renders a timestamp as a local date; 0 is "".
func Day(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Local().Format("2006-01-02")
}

// Provenance is the part of `get` that says when and from where. It prints every date a
// memory carries, because a date that `update --date` can set and nothing can read back is
// a date nobody can check.
func Provenance(created, updated int64, verified *int64, source, sourceDate, happened,
	validFrom, validTo string, reviewOn, snooze *int64) string {
	v := "never"
	if verified != nil && *verified > 0 {
		v = ISO(*verified)
	}
	source = textguard.OneLine(source)
	if sourceDate != "" {
		source += " (" + textguard.OneLine(sourceDate) + ")"
	}
	out := fmt.Sprintf("created %s  updated %s  verified %s\nsource %s",
		ISO(created), ISO(updated), v, source)
	var dates []string
	if happened != "" {
		dates = append(dates, "happened "+textguard.OneLine(happened))
	}
	if validFrom != "" || validTo != "" {
		dates = append(dates, "valid "+textguard.OneLine(validFrom)+".."+textguard.OneLine(validTo))
	}
	if reviewOn != nil && *reviewOn > 0 {
		dates = append(dates, "review "+Day(*reviewOn))
	}
	if snooze != nil && *snooze > 0 {
		dates = append(dates, "snoozed until "+Day(*snooze))
	}
	if len(dates) > 0 {
		out += "\n" + strings.Join(dates, "  ")
	}
	return out
}

// ParseDate accepts 2026-07-22, 22/07/2026, 22-07-2026 and 22/07 (this year), plus "today"
// and "yesterday" in English and Greek. Day first, because that is how people outside the
// US write dates, and an ISO date is unambiguous either way.
func ParseDate(text, label string) (string, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", nil
	}
	switch strings.ToLower(t) {
	case "today", "σημερα", "σήμερα":
		return time.Now().Format("2006-01-02"), nil
	case "yesterday", "χθες":
		return time.Now().AddDate(0, 0, -1).Format("2006-01-02"), nil
	}
	for _, f := range []string{"2006-01-02", "02/01/2006", "02-01-2006", "2006/01/02"} {
		if d, err := time.ParseInLocation(f, t, time.Local); err == nil {
			return d.Format("2006-01-02"), nil
		}
	}
	// A bare 22/07 means this year; supply it rather than letting the parser guess.
	year := time.Now().Year()
	for _, f := range []struct{ layout, sep string }{{"02/01/2006", "/"}, {"02-01-2006", "-"}} {
		if d, err := time.ParseInLocation(f.layout, fmt.Sprintf("%s%s%d", t, f.sep, year), time.Local); err == nil {
			return d.Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("could not read %s %s. Use 2026-07-22, 22/07/2026 or 22/07", label, pyfmt.Repr(text))
}

// DayToTS turns a YYYY-MM-DD into local midnight, for snooze and review columns.
func DayToTS(d string) (int64, error) {
	if d == "" {
		return 0, nil
	}
	t, err := time.ParseInLocation("2006-01-02", d, time.Local)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

func Plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "y") {
		return fmt.Sprintf("%d %sies", n, strings.TrimSuffix(word, "y"))
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func HumanDuration(seconds int64) string {
	if seconds < 90 {
		return fmt.Sprintf("%ds", seconds)
	}
	mins := seconds / 60
	if mins < 90 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh%02dm", mins/60, mins%60)
}
