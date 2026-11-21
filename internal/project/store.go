package project

import "github.com/chka/os-brain/internal/store"

// Store is a brain opened for the commands that act on a project as a whole: its context,
// its scope registry, relabel, absorb, share and private. It adds them to the store
// underneath and nothing else, so each one acts on the very store the command opened.
type Store struct{ *store.Store }

// New gives the project commands of an open brain.
func New(s *store.Store) *Store { return &Store{s} }

// open opens the brain env names with the project commands on top. share opens the store
// it moves a project into through it, so both ends of the move are the same kind of Store.
func open(env store.Env) (*Store, error) {
	s, err := store.Open(env)
	if err != nil {
		return nil, err
	}
	return New(s), nil
}
