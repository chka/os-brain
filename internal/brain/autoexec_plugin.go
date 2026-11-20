package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenCode runs no hook commands, and neither does Kilo, which forked it. Both load
// JavaScript plugins from the project, so the brain is wired by writing one (OpenCode
// 1.19.2): the brief joins the system prompt once per session, recall on each message rides
// with that turn, and the footprint is recorded when the session goes idle. OpenCode passes
// its session id only to plugins, so the plugin hands it on; without it every call was its
// own pid-keyed session.

// pluginMarker opens every plugin init writes. It is how init and uninstall tell their own
// brain.js from somebody else's, which they never touch.
const pluginMarker = "// Written by `os-brain init`"

// Kilo 7.8 reads .kilo/plugin/ and ignores .opencode/plugins/, so a project wired for both
// is briefed once by each and never twice by either.
var (
	openCodePluginPath = filepath.Join(".opencode", "plugins", "brain.js")
	kiloPluginPath     = filepath.Join(".kilo", "plugin", "brain.js")
	pluginPaths        = []string{openCodePluginPath, kiloPluginPath}
)

// agentPlugin is the plugin source for one agent. The shim path and the auto-recall switch
// are baked in, so the file is regenerated whenever either changes.
func agentPlugin(label, script string, autoRecall bool) string {
	q, _ := json.Marshal(script)
	return pluginMarker + ` for ` + label + `. Delete it, or run ` + "`os-brain uninstall`" + `, to unwire.
// The brief joins the system prompt once per session, recall rides with each message, and
// the session's footprint is recorded when it goes idle.
import { execFileSync } from "node:child_process"

const AUTOEXEC = ` + string(q) + `
const RECALL = ` + fmt.Sprint(autoRecall) + `

const run = (args, sessionID, payload) => {
  try {
    return execFileSync(AUTOEXEC, args, {
      input: JSON.stringify(payload ?? {}),
      env: sessionID ? { ...process.env, OS_BRAIN_SESSION_ID: sessionID } : process.env,
      encoding: "utf8",
      timeout: 15000,
    }).trim()
  } catch {
    return ""
  }
}

// Only a session somebody typed into is briefed. The agent also calls the model for a
// session title (Kilo files it as "title-<session>"), and briefing that paid for the whole
// brief again and left a session that never ended.
export const NsBrain = async () => {
  const typed = new Set()
  const brief = new Map()
  const recall = new Map()
  return {
    "chat.message": async (input, output) => {
      typed.add(input.sessionID)
      if (!RECALL) return
      const prompt = output.parts.filter((p) => p.type === "text").map((p) => p.text).join("\n")
      recall.set(input.sessionID, run(["hook-recall"], input.sessionID, { session_id: input.sessionID, prompt }))
    },
    "experimental.chat.system.transform": async (input, output) => {
      const id = input.sessionID
      if (!typed.has(id)) return
      if (!brief.has(id)) brief.set(id, run(["wakeup"], id, { session_id: id }))
      if (brief.get(id)) output.system.push(brief.get(id))
      const hit = recall.get(id)
      if (hit) output.system.push(hit)
    },
    event: async ({ event }) => {
      if (event.type === "session.idle") {
        const id = event.properties?.sessionID
        run(["hook-stop"], id, { session_id: id })
      }
    },
  }
}
`
}

// pluginWirer writes the plugin at rel, refusing to replace a brain.js init did not write.
func pluginWirer(label, rel string) func(root, dir, script string, autoRecall bool) ([]string, error) {
	return func(root, dir, script string, autoRecall bool) ([]string, error) {
		path := filepath.Join(root, rel)
		want := agentPlugin(label, script, autoRecall)
		if err := CheckNoLinks(root, path); err != nil {
			return nil, err
		}
		if have, err := os.ReadFile(path); err == nil {
			if string(have) == want {
				return nil, nil
			}
			// Somebody else's brain.js is theirs. Ours is regenerated, because the path
			// baked into it moves with the install.
			if !strings.HasPrefix(string(have), pluginMarker) {
				return nil, fmt.Errorf("%s exists and was not written by init, so it was left untouched", path)
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if err := WriteFileSafe(path, []byte(want), WriteOpts{Root: root, Mode: 0o644,
			MkdirMode: 0o755}); err != nil {
			return nil, err
		}
		return []string{label + " plugin -> " + path}, nil
	}
}

func pluginManual(label, rel string) func(root, dir, script string) (string, string) {
	return func(root, dir, script string) (string, string) {
		return rel, agentPlugin(label, script, true)
	}
}
