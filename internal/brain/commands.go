package brain

import (
	"embed"
	"os"
	"path/filepath"
)

// The Claude Code slash commands are the front-end to `os-brain sessions` and `os-brain dm`:
// a human types /who or /msg, and the command file only shapes the argv, so there is one
// implementation of the behaviour and nothing project-specific in the binary. They travel
// inside the binary like the agent docs, because a single-binary install has no template
// directory to copy from.
//
//go:embed docs/commands/who.md docs/commands/msg.md docs/commands/rdo.md docs/commands/dnd.md docs/commands/inbox.md docs/commands/accept.md docs/commands/reject.md
var slashCommands embed.FS

// slashCommandFiles is what init writes into .claude/commands/ and uninstall removes.
var slashCommandFiles = []string{"who.md", "msg.md", "rdo.md", "dnd.md", "inbox.md", "accept.md", "reject.md"}

// writeSlashCommands puts the slash commands into <root>/.claude/commands/ and returns the
// project-relative paths it wrote. They belong beside the settings.json wireClaude writes,
// under the project root rather than the brain directory, because that is where Claude Code
// reads them. It overwrites, since these are generated wrappers that belong to the installed
// version: a command pointing at a renamed subcommand is worse than none. It never touches
// anything else in the directory.
func writeSlashCommands(root string) ([]string, error) {
	dir := filepath.Join(root, ".claude", "commands")
	var wrote []string
	for _, name := range slashCommandFiles {
		body, err := slashCommands.ReadFile("docs/commands/" + name)
		if err != nil {
			return wrote, err
		}
		path := filepath.Join(dir, name)
		if old, err := os.ReadFile(path); err == nil && string(old) == string(body) && CheckNoLinks(root, path) == nil {
			continue
		}
		if err := WriteFileSafe(path, body, WriteOpts{Root: root, Mode: 0o644, MkdirMode: 0o755}); err != nil {
			return wrote, err
		}
		wrote = append(wrote, filepath.Join(".claude", "commands", name))
	}
	return wrote, nil
}

// SlashCommand exposes an embedded command file so tests can check it like code, the same
// way AgentDoc does for HOWTO.md and BEST-PRACTICES.md.
func SlashCommand(name string) ([]byte, error) {
	return slashCommands.ReadFile("docs/commands/" + name)
}
