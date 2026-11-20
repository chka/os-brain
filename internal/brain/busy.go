package brain

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// IsBusy reports a SQLite lock refusal. modernc's driver renders it as a string with the
// result code appended, and there is no exported sentinel to compare against.
func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "database is locked") ||
		strings.Contains(m, "database table is locked") ||
		strings.Contains(m, "sqlite_busy")
}

// retryBusy re-runs work that SQLite refused because someone else held a lock it will not
// wait on. Bounded and short: this covers the milliseconds around creating or migrating a
// brain, not steady-state contention, which busy_timeout and _txlock=immediate handle. A
// non-lock error returns immediately, because retrying a real failure just delays it.
func retryBusy(fn func() error) error {
	delay := 5 * time.Millisecond
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		if err = fn(); !IsBusy(err) {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
	return err
}

// creationToken identifies this process for the one-announcement-per-brain claim. Host and
// pid, not the session id: two sessions can share a pid namespace but never a pid, and a
// shell invocation has no session at all.
func creationToken() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}
