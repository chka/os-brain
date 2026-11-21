// Package schedule reads when a task is due: a closed grammar of five forms (once, every,
// daily, weekly, monthly) and the next due time after a given moment. The grammar and its
// edge cases are written out at the top of schedule.go; it refuses what it does not
// understand rather than guess, because a reminder that silently never fires is the worst
// failure a schedule can have.
package schedule
