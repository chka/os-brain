package cli

import (
	"io"

	"github.com/chka/os-brain/internal/dm"
	"github.com/chka/os-brain/internal/store"
)

// withMessenger hands fn the store that holds this project's sessions and direct messages.
func withMessenger(cmd string, env store.Env, fn func(dm.Messenger) error) error {
	return withStore(cmd, env, func(s *store.Store) error { return fn(dm.New(s)) })
}

// deliverTo runs one hook's delivery and never fails the turn over it.
func deliverTo(w io.Writer, env store.Env, event string) bool {
	delivered := false
	_ = withMessenger("hook-dm", env, func(m dm.Messenger) error {
		delivered = dm.DeliverHook(w, m, event)
		return nil
	})
	return delivered
}
