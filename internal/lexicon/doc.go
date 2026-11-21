// Package lexicon is how the brain reads words: accent folding, title identity, stopwords,
// the query terms handed to the full-text index, the coverage floor a result must clear,
// and word-set overlap. The index, recall, the duplicate checks and the hook all tokenise
// through here, because two tokenisers drift, and when they did, short queries silently
// found nothing on one side.
//
// (It is not called "terms": that is the name of a local variable in half the callers.)
package lexicon
