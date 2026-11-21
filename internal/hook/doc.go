// Package hook is what runs on every turn: hook-recall reads the prompt the host passes and
// answers it from the brain (relevant memories, due tasks, and the open work when the prompt
// asks what is unfinished), prefer stores a stated preference, and capture keeps what the
// user said verbatim and classifies it, failing toward "not a rule" at every decision point.
// It sits above brief, because a prompt asking what is open is answered with the brief's
// open block.
package hook
