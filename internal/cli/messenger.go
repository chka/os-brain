package cli

import (
	"io"

	"github.com/chka/os-brain/internal/brain"
)

// withMessenger hands fn the store that holds this project's sessions and direct messages.
func withMessenger(cmd string, env brain.Env, fn func(brain.Messenger) error) error {
	return withStore(cmd, env, func(s *brain.Store) error { return fn(s) })
}

// deliverTo runs one hook's delivery and never fails the turn over it.
func deliverTo(w io.Writer, env brain.Env, event string) bool {
	delivered := false
	_ = withMessenger("hook-dm", env, func(m brain.Messenger) error {
		delivered = brain.DeliverHook(w, m, event)
		return nil
	})
	return delivered
}
