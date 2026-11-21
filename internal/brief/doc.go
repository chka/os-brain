// Package brief is what a session reads before its first turn and whenever it asks what is
// open: the brief (pinned memories, preferences, axioms, standards, notes, due tasks, leads,
// measures, omissions and the warnings that must not be missed), wakeup (the brief plus the
// working tree and the project's own probes, in the envelope each agent host reads), the
// session report the stop hook prints, and open. budget.go keeps the brief inside the space
// a host gives it. The probe files carry build tags because stopping a timed-out probe takes
// a process group on Unix and has no equivalent on Windows.
package brief
