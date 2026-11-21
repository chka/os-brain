// Package curate looks after what a brain holds rather than whether the store is intact:
// review reports what has grown into excess (duplicates, stale or cold rows, over-pinning,
// superseded rows, undeclared scopes) and, through omissions.go, the dimensions a project has
// never used at all; promote moves a memory to a wider scope. The brief carries the omissions
// as one line, so it reads them from here.
package curate
