// Package dump moves a brain's contents through files: export writes a JSON dump, import
// reads one back under the same rules remember applies to a row written here, backup writes
// a dump and runs the safety copies, and prune archives what has decayed. It also holds the
// per-project building blocks (list a project's tables, dump them, purge them) that wipe,
// share and uninstall use, so every command that removes a project writes its dump the same
// way first. The dump format is the one ns-brain writes, so a dump moves between editions.
package dump
