// Package recall answers a question from the brain: full-text search over the memories,
// ranked by how well each one matches and then, inside a bounded band, by its stakes (tier,
// importance, usage, confidence) and how recently it was true. score.go holds that ranking
// and says why each term exists. Recall fails closed: a miss returns nothing rather than
// everything, and names where to look instead (the coverage map in coverage.go). It also
// follows supersession, so a question in outdated wording answers with the current fact,
// and flags two results that contradict each other.
package recall
