package store

// What the external tests (package store_test) need to see that the store does not export.

// MemoisedCommits is how many commit distances a TreeState has worked out, which is how a
// test proves that memories stored at one commit cost one git call between them.
func MemoisedCommits(t *TreeState) int { return len(t.dist) }
