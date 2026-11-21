package install

import (
	"embed"
	"os"
	"path/filepath"

	"github.com/chka/os-brain/internal/fsguard"
)

// The agent-facing docs travel inside the binary, because a single binary has no template
// directory to copy them from. Without them an install leaves an agent with a working brain,
// correct hooks and nothing telling it the commands, the scopes or the session contract,
// and that is the half of "installed" that decides whether the brain gets used.
//
//go:embed docs/HOWTO.md docs/BEST-PRACTICES.md
var agentDocs embed.FS

// writeAgentDocs puts HOWTO.md and BEST-PRACTICES.md beside the database and returns the
// names it wrote. It overwrites, because these are generated files that belong to the
// installed version: a stale HOWTO teaching a retired command is worse than none, and
// nothing here is authored in place. It never touches anything else in the directory.
func writeAgentDocs(dir string) ([]string, error) {
	root := filepath.Dir(dir)
	var wrote []string
	for _, name := range []string{"HOWTO.md", "BEST-PRACTICES.md"} {
		body, err := agentDocs.ReadFile("docs/" + name)
		if err != nil {
			return wrote, err
		}
		path := filepath.Join(dir, name)
		if old, err := os.ReadFile(path); err == nil && string(old) == string(body) && fsguard.CheckNoLinks(root, path) == nil {
			continue
		}
		if err := fsguard.WriteFileSafe(path, body, fsguard.WriteOpts{Root: root, Mode: 0o644}); err != nil {
			return wrote, err
		}
		wrote = append(wrote, name)
	}
	return wrote, nil
}

// AgentDoc exposes an embedded document so tests can check it like code: the docs ship
// inside the binary, so doc drift is a test failure rather than something a reader
// discovers later.
func AgentDoc(name string) ([]byte, error) { return agentDocs.ReadFile("docs/" + name) }
