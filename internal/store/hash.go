package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/chka/os-brain/internal/lexicon"
)

// --- identity --------------------------------------------------------------------

// ContentHash keys identity on project + scope + normalised title. The project has to be
// in the key: two projects may hold the same title for different facts, and in one store
// that would otherwise collide.
func ContentHash(project, scope, title string) string {
	sum := sha256.Sum256([]byte(project + "\x00" + scope + "\x00" + lexicon.NormTitle(title)))
	return hex.EncodeToString(sum[:])
}

// TombstoneHash keys a tombstone on the row's OWN identity instead of recomputing one from
// its title. A correction may carry the same title as the fact it corrects: the retracted
// row holds SupersededHash and the successor holds the name, so recomputing meant
// forgetting the retracted half wrote a tombstone keyed to the LIVE successor, which
// re-remembering then reported as forgotten, and restore then deleted the wrong one.
func TombstoneHash(project string, m *Memory) string {
	if m.Hash != nil && *m.Hash != "" {
		return *m.Hash
	}
	return ContentHash(project, m.Scope, m.Title)
}

// SupersededHash keeps a retracted memory's row unique without letting it hold the name.
// Deriving it from the id rather than nulling the column means the row still has a stable
// identity for history, tombstones and doctor, and two retractions of the same title can
// coexist.
func SupersededHash(id int64, live string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("superseded:%d:%s", id, live)))
	return hex.EncodeToString(sum[:])
}
