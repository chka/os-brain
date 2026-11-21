// Package memory holds the commands that write, change and read single memories: remember
// and log, update and verify, forget (one memory, or a whole project with --all and
// --everything), history and restore, get, events and stats, and the objection ladder.
// Every guard a memory passes on the way in lives here: the near-duplicate check, the
// citation a confirmed fact needs, the supersede claim that has to name a real row, and the
// check that another session did not just write the same subject.
package memory
