package dm

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the session registry and direct messages. It adds them to the
// store underneath and nothing else, so a message is sent and claimed through the spine
// connection, env and config of the command that opened the brain.
type Store struct{ *store.Store }

// New gives the session registry and direct messages of an open brain.
func New(s *store.Store) *Store { return &Store{s} }

// The commands and the hooks take a Messenger, never a Store; this line keeps the one
// implementation honest about it at compile time.
var _ Messenger = (*Store)(nil)
