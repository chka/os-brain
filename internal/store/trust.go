package store

import (
	"sort"
	"strings"
)

// TrustKey is the identity the approvals belong to: the project AND the directory it is
// bound to. Keyed by name alone, a checkout declaring the same name inherited every
// approval and ran its own Makefile under it.
func (s *Store) TrustKey() string {
	return "wakeup_trust:" + s.Env.Project + ":" + HashRoot(s.Env.ProjectRoot())
}

// LegacyTrustKey is where approvals were recorded before the key carried the directory.
func (s *Store) LegacyTrustKey() string { return "wakeup_trust:" + s.Env.Project }

// Trusted is the set of probe hashes approved on this machine. The database is the anchor
// because it is gitignored in both layouts and never leaves the machine.
func (s *Store) Trusted() map[string]bool {
	out := map[string]bool{}
	v := s.MetaStr(s.TrustKey())
	// Approvals recorded by name alone still count where the name is proven to be this
	// directory's: opening the shared store already proved this directory holds the name,
	// and a project store is this directory's own file. A project named by an MCP caller
	// proves neither, so it gets no legacy approvals.
	if v == "" && !s.Env.Named && !s.Env.Remote {
		v = s.MetaStr(s.LegacyTrustKey())
	}
	for _, h := range strings.Fields(v) {
		out[h] = true
	}
	return out
}

// SetTrusted records the approved hashes under the directory-bound key and drops the
// legacy name-only key, so an old approval cannot outlive a new decision.
func (s *Store) SetTrusted(hashes []string) error {
	sort.Strings(hashes)
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)",
		s.TrustKey(), strings.Join(hashes, " ")); err != nil {
		return err
	}
	_, err := s.DB.Exec("DELETE FROM meta WHERE k=?", s.LegacyTrustKey())
	return err
}
