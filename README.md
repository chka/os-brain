# os-brain

Project memory for AI coding agents. One static Go binary over SQLite, with no runtime, no
service and no account.

An agent forgets everything at the end of a session, at every compaction and at every
subagent boundary. A session spends an hour working out why the deploy hangs, the session
ends and three weeks later another session spends the same hour on the same problem.
os-brain keeps what a session learned in a small database inside the project and hands the
next session the part that matters before its first turn.

os-brain is the open source edition of the local brain in ns-brain. It is meant for one
person working on their own machine.

## What it does

A session starts with a brief that the agent's own session hook injects: the wall clock
and the part of the day, the git state read from the working tree (staged, modified and
untracked files by name, unpushed commits, a merge or rebase left half done), the rules
that bind this project, how this person wants to be worked with, open tasks and what
changed since the last session.

During the session the agent stores distilled facts with `remember` and looks things up
with `recall` before it searches files. A second hook runs on every prompt and puts the
memories that match what was just typed into context.

Corrections stick. `remember --supersedes 12` retracts memory 12 in favour of the new one,
so a query worded the old way answers with the corrected fact. Deleting the correction is
refused, because that would bring the error back.

Rules stay in front of the agent. Memories of type `policy` and `constraint` go into every
brief whether they are pinned or not, so a hard rule never loses its place to something
more topical. Axioms and house standards hold in every project on the machine.

Facts carry their confidence and their age. A `confirmed` fact outranks an `assumed` one,
so something said once in passing cannot outrank a measured value by being newer.
`review` lists the facts nobody has checked in a long time.

Numbers get trended. `measure add latency_p95 182` records a reading, `measure series`
shows the history and `measure limit` declares the band the brief should flag.

It fails closed. When the tool cannot tell what the caller meant, it does nothing and says
so. A `recall --json` miss returns nothing rather than the whole scope (a script that fed
recall into forget once destroyed real memories that way). `forget` refuses pinned memories
and guardrails. `prune` is a dry run that backs up before it deletes anything.

## Install

You need Go 1.26 or newer.

```bash
git clone https://github.com/chka/os-brain
cd os-brain
go build -o ~/.local/bin/os-brain ./cmd/os-brain
```

Or `go install github.com/chka/os-brain/cmd/os-brain@latest`. The binary has no cgo
and no dependencies beyond the Go standard library, a pure Go SQLite driver and
golang.org/x/text, so it cross-compiles to a single static file for each platform.

## Start a brain in a project

```bash
cd ~/src/myproject
os-brain init
```

`init` creates `brain/brain.json` (the project's name and settings, meant to be committed)
and `brain/brain.db` (every memory in cleartext, added to `.gitignore`). It writes
`brain/HOWTO.md` and `brain/BEST-PRACTICES.md` for the agent to read. Then it wires the
hooks of the agents it finds in the project. For Claude Code that means five entries in
`.claude/settings.json`:

```
SessionStart      os-brain wakeup        the brief, the working tree and the project's own checks
UserPromptSubmit  os-brain hook-recall   memories that match the prompt
Stop              os-brain hook-stop     what the session did and whether it stored any of it
PostToolUse       os-brain hook-tool     direct messages from other sessions, mid turn
SessionEnd        os-brain hook-end      marks the session gone
```

Codex, Cursor, Gemini, OpenCode, Kilo, Copilot, Devin, Qwen, Hermes and Continue are wired
the same way where they support it (`--agent` picks them by name). When no agent can be
wired, `init` says so and prints what to add by hand rather than pretending.

Nothing is written into `CLAUDE.md` or any other instruction file. A line in a markdown
file is a request the agent may ignore, while a hook runs every time.

The per-prompt hook keeps what you type as an utterance, up to 8000 characters for 30 days
in the local store. `os-brain utterances` lists them and `os-brain init --no-auto-recall`
turns the hook off.

`os-brain uninstall` shows what it would remove and does it with `--apply`. It keeps the
database unless you add `--purge` and it takes a backup first either way.

## The commands that carry the work

```bash
os-brain brief                         # what this project believes, right now
os-brain recall "tls renewal"          # before you search files
os-brain remember "nginx terminates TLS on the edge box" --type decision --body "..."
os-brain prefer "answers first, reasoning after"
os-brain log "deployed 1.4 to staging" --kind deploy
os-brain note other-project "we found the cause of the timeout"
```

The rest is there when it is needed: `task` for what is owed and when (with recurrence such
as `every monday 9am`), `open` for what is outstanding, `measure` for numbers, `map` and
`stats` for an overview, `review` and `doctor` for hygiene, `history` and `restore` for
archived versions, `export`, `import`, `backup` and `snapshot` for copies and `ui` for a
browser view on loopback. `os-brain help` lists every command and `os-brain help <command>`
explains one.

`os-brain mcp` serves the same commands over MCP, on stdio for an agent that starts it or
on Streamable HTTP for a client elsewhere. The tool list is built from the CLI's own flags,
so the two never drift. Every call runs the same code the CLI runs.

## Where things live

Each project keeps its memories in its own `brain/brain.db`. A thin shared store at
`~/.claude/os-brain/brain.db` holds what crosses projects: axioms, house standards, the
project index, notes between projects and lessons a project chose to publish. No project
reads another project's memories unless it asks with `--all-projects`. `private` keeps a
project out of even that. A project can also move into the shared store with `share`.

These variables move things for one run or one shell:

```
OS_BRAIN_HOME        the brain directory, instead of searching upward from here
OS_BRAIN_PROJECT     the project name, instead of brain.json's
OS_BRAIN_DB          the database file
OS_BRAIN_GLOBAL_DB   the shared store
CLAUDE_CONFIG_DIR    where Claude Code keeps its files (~/.claude by default)
```

os-brain never calls home. There is no telemetry, no update check and no account. Nothing
leaves the machine unless you run a command that sends it somewhere.

## Editions

os-brain is the local brain. ns-brain's team edition (Hive) adds a shared server,
organisations, roles and encryption managed per organisation, for teams that want one
brain across people and machines.

The two use the same SQLite format and schema, so a brain moves between them. A project's
`brain/brain.db` opens in either binary as it is. A project kept in a shared store moves
with `export` in one edition and `import` in the other, run under the same project name.
os-brain refuses to run in a project whose `brain.json` points at a Hive server or a
zero-knowledge brain, because it cannot read those and will not answer from a local file
that does not hold the project's memories.

To switch a project from ns-brain, run `ns-brain uninstall --apply` first (it removes
ns-brain's hooks and keeps the database), then `os-brain init`. Otherwise both editions'
hooks stay wired and every session gets two briefs.

os-brain uses its own command name, variables and default paths, so it can sit beside an
installed ns-brain without either one writing into the other's store.

## Development

```bash
go test ./...                    # the unit tests
python3 tools/sweep.py           # the contract: every command and refusal, as real processes
python3 tools/roundtrip.py       # with ns-brain installed: a brain moves between the two
```

`tools/sweep.py` is the same check ns-brain's local edition is held to, run against this
binary in a throwaway install. The checks for features os-brain does not have are listed
at the end of each run with the reason. Both scripts pin every path to a scratch directory
and hash the real stores before and after. The Go tests fail if a run adds anything to a
real store.

Behaviour changes wait until after the first release. Then they come one at a time, each
with a measurement behind it.

## Licence

MIT. See [LICENSE](LICENSE).
