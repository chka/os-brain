// Package safety keeps automatic whole-store copies beside the shared store and puts one
// back on request. A copy is a database taken with VACUUM INTO, not a JSON dump, so taking
// one never stops writers and restoring one is copying a file back. Rotation never discards
// the copy holding the most memories, and restore copies what it is about to replace first,
// because the one command whose job is recovery must never be the moment data is lost.
package safety
