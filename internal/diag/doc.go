// Package diag prints the brain's one-line "[brain]" notices to stderr: the nudges a
// command gives without failing (an undeclared scope, a file it left alone). They go to
// stderr, never to the command's own output, so a caller parsing --json is not broken by
// advice meant for a person.
package diag
