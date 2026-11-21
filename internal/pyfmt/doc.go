// Package pyfmt prints values the way the first, Python, implementation of the brain did:
// repr() quoting, Python's float and bool spelling, and JSON with ensure_ascii escaping and
// Python's separators. os-brain promises the same bytes on every command, and a brain
// written by one edition is read by the other, so these rules live in one place below
// everything that prints.
package pyfmt
