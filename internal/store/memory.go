package store

// Conflict is recall admitting it cannot choose. Two memories of the same type, in the
// same scope, scoring within a hair of each other, neither superseding the other: from
// the outside that is indistinguishable from two answers to one question, which is what
// two sessions reaching opposite conclusions actually produces.
//
// It deliberately does NOT claim to detect contradiction. Nothing lexical can: "Postgres
// for the job queue" and "keep SQLite everywhere, no server dependency" share almost no
// tokens, and a heuristic confident enough to call that a contradiction would be confident
// enough to call a hundred unrelated pairs one too. What it claims is narrower and true:
// these came out level, and if you were expecting one answer you have two.
type Conflict struct {
	With     []int64  `json:"with"`
	Note     string   `json:"note"`
	Sessions []string `json:"sessions,omitempty"`
}

// ViaRef names a RETRACTED memory whose own text is what the query matched. Recall answers
// with the current version, as it must (a retracted fact never answers as though it stood),
// but it used to say nothing about how it got there, so the original could only be read by
// somebody who already knew its id. That once cost an afternoon: "the note another session
// left" had been retracted three hours after it was written, and every search for its words
// returned the replacement with no sign the original existed.
type ViaRef struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	StoredBy  string `json:"stored_by,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}
