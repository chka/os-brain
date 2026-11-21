package mcp

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/brain"
	"github.com/chka/os-brain/internal/cli"
	"github.com/chka/os-brain/internal/clitest"
)

// End to end, against the real dispatch, with the coverage claim derived rather than
// asserted. "We tested everything" is a sentence people write when they mean "we tested
// what we thought of", so this file enumerates the surface from the binary itself - the
// same probe the MCP schemas come from - and fails naming whatever it did not reach.
//
// It runs the commands, not mocks of them: one hermetic store, one scripted session, and
// every invocation goes through cli.Run exactly as a shell would call it. What it asserts
// per command is deliberately shallow (it ran, it did not blow up and where it matters
// the output says the right thing); the depth lives in the unit tests. What it is FOR is
// the surface: every command reachable, every flag accepted, nothing quietly orphaned.

// step is one invocation. want/wantErr are substring checks: exact output is asserted in
// the unit tests and repeating it here would make the harness break on rewording rather
// than on behaviour.
type step struct {
	argv    []string
	want    string // stdout must contain this
	wantErr string // the command must fail, with this in the error
	skip    string // why this one cannot run here
}

// e2eEnv makes a fresh brain for project "e2e" in a temporary directory, pins every
// machine-level path beside it and returns the directory.
func e2eEnv(t *testing.T) (root string) {
	t.Helper()
	root = clitest.Brain(t, "e2e")
	// A fixed session id, so the steps that record or read per-session state (capture
	// --session, the session list, direct messages) see one predictable session rather
	// than whatever the environment running the suite happens to export.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "e2e-session-1")
	return root
}

// script is the whole surface, in the order a real session would meet it. Every command
// appears; every flag appears at least once across these steps.
func script(t *testing.T, root string) []step {
	out := filepath.Join(root, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	p := func(name string) string { return filepath.Join(out, name) }
	if err := os.WriteFile(p("body.txt"), []byte("a body read from a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return []step{
		// --- the six that carry the work -------------------------------------------
		{argv: []string{"remember", "nginx terminates TLS on 8443", "--body",
			"certbot renews it, the vhost is /etc/nginx/sites-available/brain",
			"--type", "infra", "--scope", "channel:deploy", "--source", "checked on the box",
			"--confidence", "confirmed", "--importance", "4", "--pin",
			"--source-date", "2026-08-01", "--trust", "stated", "--ref", "https://example.test/vhost",
			"--date", "2026-08-01", "--valid-from", "2026-08-01", "--valid-to", "2027-01-01"},
			want: "remembered"},
		{argv: []string{"remember", "the deploy runs from the makefile", "--body",
			"make deploy tags, pushes and bumps the version file last",
			"--type", "process", "--scope", "channel:deploy"}, want: "remembered"},
		{argv: []string{"remember", "postgres for the job queue", "--body",
			"sqlite locked under fan-out", "--type", "decision",
			"--review", "2026-12-01", "--snooze-until", "2026-08-08"}, want: "remembered"},
		{argv: []string{"remember", "a fact worth superseding later", "--body", "v1",
			"--type", "knowledge", "--allow-duplicate", "--update"}, want: "remembered"},
		// While #1 is still the pinned memory the first step stored: this refusal is
		// about the guard, so it has to run before anything else moves the ids.
		{argv: []string{"forget", "1", "--reason", "trying to delete a pinned memory"},
			wantErr: "pinned"},
		{argv: []string{"recall", "tls"}, want: "nginx"},
		{argv: []string{"recall", "tls", "--json"}, want: `"matched"`},
		{argv: []string{"recall", "--scope", "channel:deploy", "--type", "infra", "-n", "3",
			"--browse", "--no-touch"}, want: "nginx"},
		{argv: []string{"recall", "kubernetes ingress annotations", "--strict"},
			want: "no match"},
		{argv: []string{"recall", "tls", "--all", "--all-projects", "--since", "3650",
			"--tier", "A", "--from", "2020-01-01", "--to", "2030-01-01", "--on", ""}, want: ""},
		{argv: []string{"prefer", "lead with a recommendation, never a neutral menu",
			"--why", "a menu costs the user the time the session was meant to save",
			"--confirmed"}, want: "preference"},
		{argv: []string{"log", "--kind", "deploy", "--summary", "shipped 26080704",
			"--scope", "channel:deploy", "--actor", "claude", "--ref", "26080704"},
			want: "logged"},
		{argv: []string{"events", "-n", "5", "--kind", "deploy", "--scope", "channel:deploy"},
			want: "deploy"},
		{argv: []string{"note", "e2e", "a jittered retry fixed our flaky deploys too",
			"--kind", "note", "--ref", "retry.go", "--expires", "30"}, want: "note"},
		{argv: []string{"note", "--inbox", "-n", "5"}, want: ""},
		{argv: []string{"note", "--elsewhere"}, wantErr: "local file"},

		// --- capture, the ladder, the user model, coverage --------------------------
		{argv: []string{"capture", "push the code", "--session", "e2e-session-1"}},
		{argv: []string{"capture", "from now on always run gofmt before a commit"},
			want: "directive"},
		{argv: []string{"capture", "I like short commit messages"}},
		{argv: []string{"capture", "```\nalways use tabs\n```"}},
		{argv: []string{"utterances", "-n", "10"}, want: "directive"},
		{argv: []string{"utterances", "--class", "taste"}, want: "taste"},
		{argv: []string{"utterances", "--unclassified"}, want: ""},
		{argv: []string{"object", "3", "--why",
			"sqlite handles our volume; postgres is another service to run"},
			want: "objection recorded"},
		{argv: []string{"ladder", "3"}, want: "stated"},
		{argv: []string{"whoami", "--observe", "depth=peer-to-peer", "--evidence",
			"corrected the API surface twice and cited the source"}, want: "peer-to-peer"},
		{argv: []string{"whoami"}, want: "because"},
		{argv: []string{"whoami", "--forget", "depth"}, want: "dropped"},
		{argv: []string{"coverage"}, want: "infra"},
		{argv: []string{"coverage", "--near", "kubernetes"}, want: "nearest"},

		// --- tasks and reminders ----------------------------------------------------
		{argv: []string{"task", "add", "email the proposal", "--body", "the June version",
			"--owner", "user", "--when", "tomorrow 9am", "--scope", "global", "--notify"},
			want: "due"},
		{argv: []string{"task", "add", "run the migration check", "--owner", "agent",
			"--when", "every 5 hours"}, want: "every 5 hours"},
		{argv: []string{"task", "add", "buy milk"}, want: "list item"},
		{argv: []string{"task", "list", "--all", "--state", "open", "--owner", "user"},
			want: "email the proposal"},
		{argv: []string{"task", "due"}, want: ""},
		{argv: []string{"task", "snooze", "1", "--until", "in 3 hours"}, want: "snoozed"},
		{argv: []string{"task", "done", "3"}, want: "done"},
		{argv: []string{"task", "drop", "2"}, want: "dropped"},
		{argv: []string{"task", "drop", "3"}, want: "dropped (was done)"},
		{argv: []string{"task", "reopen", "3"}, want: "open (was dropped)"},
		{argv: []string{"task", "wait", "3"}, want: "waiting (was open)"},
		{argv: []string{"task", "set", "3", "--title", "buy oat milk", "--body", "the barista kind",
			"--when", "none"}, want: "updated (title, body, when)"},
		{argv: []string{"task", "list", "--state", "waiting"}, want: "buy oat milk"},
		{argv: []string{"task", "bogus"}, wantErr: "task has no"},
		{argv: []string{"task", "3", "--state", "dropped"}, wantErr: "after the subcommand"},
		{argv: []string{"task", "done", "3", "--body", "x"}, wantErr: "does not take --body"},
		{argv: []string{"task", "set", "3"}, wantErr: "changes nothing"},
		{argv: []string{"task", "todo", "--out", p("TODO.md")}, want: "wrote"},
		{argv: []string{"task", "todo", "--out", p("TODO.md"), "--allow-outside",
			"--allow-tracked"}, want: "wrote"},
		{argv: []string{"open"}, want: "WHAT IS OPEN"},
		{argv: []string{"open", "--full", "-n", "3"}, want: "OPEN TASKS"},
		{argv: []string{"open", "--compact"}, want: "THAT IS THE ANSWER"},
		// The aliases are the point of the aliases: a session that guesses the wrong verb
		// must not learn that the brain has no answer.
		{argv: []string{"outstanding"}, want: "WHAT IS OPEN"},
		{argv: []string{"status"}, want: "WHAT IS OPEN"},

		// --- measures ----------------------------------------------------------------
		{argv: []string{"measure", "add", "build_seconds", "42", "--unit", "s",
			"--label", "make build", "--date", "2026-08-01", "--ref", "ci#1",
			"--note", "cold cache", "--scope", "global", "--source", "ci",
			"--confidence", "confirmed"}, want: "build_seconds"},
		{argv: []string{"measure", "add", "build_seconds", "31"}, want: "build_seconds"},
		{argv: []string{"measure", "limit", "build_seconds", "--low", "0", "--high", "40",
			"--better", "down", "--why", "over 40s and people stop running it"},
			want: "build_seconds"},
		{argv: []string{"measure", "series", "build_seconds", "--from", "2026-01-01",
			"--to", "2027-01-01", "-n", "10"}, want: "build_seconds"},
		{argv: []string{"measure", "list", "--scope", "global"}, want: "build_seconds"},
		{argv: []string{"measure", "relabel", "1", "--label", "make build (cold)", "--unit", "sec"},
			want: ""},
		{argv: []string{"measure", "forget", "2"}, want: ""},

		// --- corrections and lifecycle ------------------------------------------------
		{argv: []string{"update", "4", "--body", "v2, corrected", "--importance", "5",
			"--confidence", "reported", "--source", "re-read the file", "--scope", "global",
			"--title", "a fact worth superseding later", "--type", "knowledge",
			"--pin", "--allow-secret", "--date", "2026-08-02", "--ref", "x"}, want: "updated"},
		{argv: []string{"verify", "1", "--confidence", "confirmed", "--source",
			"re-checked on the box today"}, want: "verified"},
		{argv: []string{"promote", "2", "--to", "global"}, want: "promoted"},
		{argv: []string{"get", "1"}, want: "nginx"},
		{argv: []string{"get", "1", "--all-projects"}, want: "nginx"},
		// Both are a person's act at a terminal; what they do (the claim and the
		// whole-store refusal without a person) is unit-tested in internal/brain.
		{argv: []string{"relabel", "--claim"}, wantErr: "controlling terminal"},
		{argv: []string{"backup", "--whole-store"}, wantErr: "controlling terminal"},
		{argv: []string{"history", "--id", "4", "-n", "5"}, want: ""},
		{argv: []string{"forget", "4", "--reason", "superseded by the corrected version",
			"--force"}, want: "forgot"},
		{argv: []string{"restore", "1"}, want: ""},

		// --- the store itself ----------------------------------------------------------
		{argv: []string{"brief"}, want: "SESSION BRIEF"},
		// 0 means no trimming, which is what a terminal or the viewer wants; the hook
		// takes the default. Both paths need exercising because the flag decides whether
		// the brief a session actually reads is complete or ranked-and-cut.
		{argv: []string{"brief", "--max-bytes", "0"}, want: "SESSION BRIEF"},
		{argv: []string{"brief", "--json", "--no-cursor"}, want: `"now"`},
		{argv: []string{"wakeup"}, want: "SESSION BRIEF"},
		{argv: []string{"wakeup", "--max-bytes", "3000"}, want: "SESSION BRIEF"},
		{argv: []string{"wakeup", "--no-brief"}, want: ""},
		{argv: []string{"wakeup", "--emit", "gemini"}, want: "hookSpecificOutput"},
		{argv: []string{"wakeup", "--no-tree", "--no-probes", "--timeout", "5", "--chars", "400"},
			want: ""},
		{argv: []string{"env"}, want: "CONTEXT"},
		{argv: []string{"stats", "--json"}, want: `"memories"`},
		{argv: []string{"map"}, want: "SCOPES"},
		{argv: []string{"review", "--stale-days", "30"}, want: ""},
		{argv: []string{"doctor"}, want: "DOCTOR"},
		{argv: []string{"doctor", "--fix"}, want: "DOCTOR"},
		{argv: []string{"reindex"}, want: ""},
		{argv: []string{"vacuum"}, want: ""},
		{argv: []string{"snapshot", "--out", p("SNAPSHOT.md")}, want: ""},
		{argv: []string{"export", "--out", p("export.json")}, want: ""},
		{argv: []string{"backup", "--out", p("backup.json")}, want: "backed up"},
		// The safety copies: make one, list it, verify it and run the restore drill
		// against the copy that was just made. --apply is exercised here too, because a
		// restore path that is only ever dry-run is a restore path nobody has tested -
		// which is the whole failure this feature exists to answer.
		// No `want`: --auto is SILENT when nothing is due and by this point in the script
		// `wakeup` has already made the copy - which is the intended behaviour of both.
		{argv: []string{"backup", "--auto"}, want: ""},
		{argv: []string{"backup", "--list"}, want: "memories"},
		{argv: []string{"backup", "--verify"}, want: "0 failed"},
		{argv: []string{"import", p("export.json"), "--dry-run"}, want: "would"},
		{argv: []string{"prune", "--tier", "D", "--older-than", "365", "--scope", "global",
			"--max-delete", "5"}, want: ""},

		// --- scopes, kinds, context ------------------------------------------------------
		{argv: []string{"scope", "add", "channel:deploy", "--desc", "how it ships"},
			want: "channel:deploy"},
		{argv: []string{"scope", "list"}, want: "channel:deploy"},
		{argv: []string{"kind", "add", "deploy", "--desc", "a release went out"},
			want: "deploy"},
		{argv: []string{"kind", "list"}, want: "deploy"},
		{argv: []string{"context", "set", "prod", "code.example.com"}, want: "prod"},
		{argv: []string{"context", "list"}, want: "prod"},

		// privacy, both directions: on narrows and applies at once, off widens and asks
		{argv: []string{"private"}, want: "private"},
		{argv: []string{"private", "--off"}, wantErr: "--yes"},
		{argv: []string{"private", "--off", "--yes"}, want: "readable again"},

		// --- the shared layer -------------------------------------------------------------
		{argv: []string{"axiom", "list"}, want: "AXIOMS"},
		// Axioms and standards are changed by a person at a terminal, which this harness is
		// not; writing them is covered by internal/cli/spine_person_test.go and the
		// internal/brain tests and every write path here proves the refusal.
		{argv: []string{"axiom", "add", "e2e: a test axiom, removed in the next step",
			"--why", "proving the write path"}, wantErr: "controlling terminal"},
		{argv: []string{"standard", "add", "go", "--category", "stack",
			"--why", "one static binary"}, wantErr: "controlling terminal"},
		{argv: []string{"standard", "list", "--category", "stack"}, want: ""},
		{argv: []string{"describe", "--does", "end to end coverage",
			"--provides", "nothing", "--keywords", "test", "--stack", "go",
			"--status", "active", "--visibility", "listed"}, want: ""},
		{argv: []string{"global"}, want: ""},
		{argv: []string{"global", "--recall", "tls"}, want: ""},
		{argv: []string{"publish", "1", "--why", "other projects hit this too"},
			want: ""},
		{argv: []string{"publish", "9999", "--retract"}, wantErr: "no published lesson"},
		{argv: []string{"relabel", "nonexistent-label", "--expect", "0"}, wantErr: "no memories"},

		// --- version and release -------------------------------------------------------
		{argv: []string{"version"}, want: "os-brain"},
		// A release is a person's act at a terminal, which this harness is not; publishing
		// is covered by internal/cli/release_person_test.go and the internal/brain release
		// tests.
		{argv: []string{"release", brain.Version, "--notes", "the e2e run"}, wantErr: "controlling terminal"},
		{argv: []string{"help"}, want: "remember"},

		{argv: []string{"forget", "999999", "--purge"}, wantErr: "no memory"},

		// --- the sales pipeline, which most projects never touch ---------------------------
		{argv: []string{"lead", "add", "--name", "Joe Bloggs", "--company", "Acme",
			"--role", "CTO", "--status", "contacted", "--segment", "fintech",
			"--channel", "email", "--value-est", "5000", "--url", "https://acme.test",
			"--email", "joe@acme.test", "--notes", "met at a conference",
			"--next-action", "send the proposal", "--next-date", "2026-08-20",
			"--tier", "1"}, want: "Joe"},
		{argv: []string{"lead", "list", "--status", "contacted", "--segment", "fintech",
			"-n", "5"}, want: "Joe"},
		{argv: []string{"lead", "show", "1"}, want: "Joe"},
		{argv: []string{"lead", "update", "1", "--status", "replied",
			"--append-note", "answered on the 8th"}, want: ""},

		// --- install and removal, in a directory of their own -------------------------------
		{argv: []string{"init", "--dir", "brain", "--project", "e2e-second",
			"--agent", "claude", "--no-hook", "--no-auto-recall", "--no-gitignore"},
			want: "", skip: "runs against a second root; see the dedicated case below"},

		// --- hooks ------------------------------------------------------------------------
		{argv: []string{"hook-stop"}, want: ""},
		// Gemini reads AfterAgent's stdout as JSON, so under --emit the hook records and
		// prints nothing at all.
		{argv: []string{"hook-stop", "--emit", "gemini"}, want: ""},

		// --- the rest of the surface: every remaining flag, exercised once ---------------
		//
		// Written from the harness's own gap report rather than from memory, which is the
		// point of deriving the list: these are the flags a hand-written checklist misses.
		{argv: []string{"remember", "--title", "a body from a file", "--body-file",
			p("body.txt"), "--type", "knowledge", "--allow-secret"}, want: "remembered"},
		{argv: []string{"remember", "postgres for the job queue", "--body",
			"corrected: sqlite is fine at this volume", "--type", "decision",
			"--supersedes", "3", "--fork-ok"}, want: ""},
		{argv: []string{"remember", "how the queue decision was reached", "--body",
			"the April note supersedes #9999 in the old tracker, which is history and not " +
				"something this write does", "--type", "knowledge", "--claim-ok"},
			want: "remembered"},
		{argv: []string{"update", "2", "--body-file", p("body.txt"), "--unpin",
			"--source-date", "2026-08-03", "--review", "2027-01-01",
			"--snooze-until", "2026-08-09"}, want: "updated"},
		{argv: []string{"update", "3", "--unsupersede"}, want: ""},
		// Emptying a body is refused unless it is meant, because a shell command that
		// failed to produce the text passes "" and update used to report success.
		{argv: []string{"update", "3", "--body", ""}, wantErr: "--force"},
		{argv: []string{"update", "3", "--body", "", "--force"}, want: "updated"},
		{argv: []string{"prefer", "keep answers short", "--why", "long ones get skimmed",
			"--about", "delivery", "--scope", "global", "--importance", "4", "--pin",
			"--source", "stated by the user", "--update"}, want: ""},
		{argv: []string{"prefer", "keep answers short but complete", "--why", "restated",
			"--supersedes", "9"}, want: ""},
		{argv: []string{"events", "-n", "5", "--since", "30", "--all-projects"}, want: ""},
		{argv: []string{"recall", "tls", "-n", "2"}, want: "nginx"},
		{argv: []string{"utterances", "-n", "3"}, want: ""},
		{argv: []string{"history", "-n", "5"}, want: ""},
		{argv: []string{"map", "--top", "2"}, want: "SCOPES"},
		{argv: []string{"review", "--duplicates", "--max-pairwise", "50", "-n", "5"},
			want: ""},
		{argv: []string{"brief", "--max-pinned", "2", "--max-prefs", "2", "--new-for", "7",
			"--new-project-below", "3", "--no-axioms"}, want: ""},
		{argv: []string{"wakeup", "--no-cursor"}, want: ""},
		{argv: []string{"wakeup", "add", "--name", "tree", "--run", "git status --short"},
			want: ""},
		{argv: []string{"snapshot", "--out", p("snap2.md"), "--per-scope", "1",
			"--allow-outside", "--allow-tracked", "--force"}, want: ""},
		{argv: []string{"export", "--out", p("export2.json"), "--allow-outside",
			"--allow-tracked", "--force"}, want: ""},
		{argv: []string{"backup", "--out", p("backup2.json"), "--allow-outside",
			"--allow-tracked", "--force"}, want: "backed up"},
		{argv: []string{"note", "--inbox", "-n", "3"}, want: ""},
		{argv: []string{"note", "e2e", "a note to this very project", "--allow-secret"},
			want: ""},
		// Ids here belong to the spine and are not predictable from this script; what is
		// being exercised is that the flag is accepted and the miss is refused rather
		// than silently answering about somebody else's note.
		{argv: []string{"note", "--read", "999"}, wantErr: "no note"},
		{argv: []string{"note", "--ack", "999"}, wantErr: "no note"},
		// Nothing is running in this script, so a direct message has nowhere to go and must
		// say so rather than park itself for a session that will never exist.
		{argv: []string{"sessions", "--all"}, want: ""},
		{argv: []string{"dm", "nobody-runs-this", "hello", "--allow-secret"},
			wantErr: "no running session"},
		{argv: []string{"dm", "--sent", "-n", "3"}, want: ""},
		// A remote-do resolves an address the same way a message does, so with nothing running
		// it lands on the same refusal. reject on an unknown id refuses rather than invent one;
		// accept needs a person at a terminal before it looks
		// (internal/cli/dm_accept_person_test.go), and a script has none.
		{argv: []string{"dm", "nobody-runs-this", "do the thing", "--rdo"},
			wantErr: "no running session"},
		{argv: []string{"dm", "--accept", "999"}, wantErr: "controlling terminal"},
		{argv: []string{"dm", "--reject", "999"}, wantErr: "nothing to reject"},
		{argv: []string{"dm", "--inbox"}, want: "inbox"},
		{argv: []string{"dm", "--mute"}, want: "do-not-disturb"},
		{argv: []string{"dm", "--mute", "--everywhere"}, want: "every session"},
		{argv: []string{"dm", "--unmute", "--everywhere"}, want: "do-not-disturb"},
		{argv: []string{"lead", "list", "-n", "3"}, want: ""},
		{argv: []string{"log", "--summary", "a logged line", "--allow-secret"}, want: "logged"},
		{argv: []string{"log", "--date", "yesterday", "a backdated line"}, want: "logged"},
		{argv: []string{"log", "--date", "someday", "x"}, wantErr: "could not read --date"},
		{argv: []string{"measure", "add", "latency_ms", "12", "--allow-secret"}, want: ""},
		{argv: []string{"measure", "series", "latency_ms", "-n", "5"}, want: ""},
		{argv: []string{"task", "add", "a task with the override", "--allow-secret"},
			want: ""},
		{argv: []string{"axiom", "add", "e2e: an axiom to remove", "--why", "coverage"},
			wantErr: "controlling terminal"},
		{argv: []string{"axiom", "rm", "--id", "1"}, wantErr: "controlling terminal"},
		{argv: []string{"axiom", "edit", "--id", "1", "--why", "e2e: reworded rationale"},
			wantErr: "controlling terminal"},
		{argv: []string{"axiom", "edit", "--id", "1", "reworded core text"},
			wantErr: "controlling terminal"},
		{argv: []string{"axiom", "edit", "--why", "no id given"}, wantErr: "controlling terminal"},
		{argv: []string{"standard", "add", "sqlite", "--category", "stack", "--from", "1"},
			wantErr: "controlling terminal"},
		{argv: []string{"standard", "rm", "--id", "1"}, wantErr: "controlling terminal"},
		{argv: []string{"global", "--find", "deploy", "-n", "3"}, want: ""},
		{argv: []string{"describe", "--sync"}, want: ""},
		// A world-owned fact the caller knows is permanent takes no review horizon and a
		// horizon already set can be cleared. --review takes a date, so an empty string
		// cannot say "none": that is what the flag is for.
		{argv: []string{"remember", "the database lives in one region and does not move",
			"--body", "one region, by standing decision", "--type", "infra", "--no-review"},
			want: "remembered"},
		{argv: []string{"update", "3", "--no-review"}, want: "updated"},
		{argv: []string{"restore", "h9999", "--force"}, wantErr: "no history entry h9999"},
		{argv: []string{"axiom", "add", "no-external-artifacts", "never publish outside the repo",
			"--why", "lost once"}, wantErr: "ONE argument"},
		{argv: []string{"remember", "ship the release notes", "--type", "task"},
			wantErr: "os-brain task add"},
		{argv: []string{"remember", "an uncited claim", "--body", "trust me",
			"--confidence", "confirmed"}, wantErr: "refusing 'confirmed'"},
		{argv: []string{"relabel", "e2e", "--apply", "--i-mean-it"},
			wantErr: "already this project"},
		{argv: []string{"release", "99" + brain.Version[2:], "--notes", "a version this binary is not"},
			wantErr: "controlling terminal"},
		{argv: []string{"prune", "--superseded", "--older-than", "365", "--apply",
			"--yes", "--backup", p("prune.json"), "--max-delete", "5", "--force",
			"--allow-outside", "--allow-tracked"}, want: ""},
		{argv: []string{"prune", "--tier", "D", "--older-than", "1", "--all-projects",
			"--i-mean-it"}, want: ""},
		{argv: []string{"forget", "--everything", "--backup", p("wipe.json"),
			"--allow-outside", "--allow-tracked"}, want: "DRY RUN"},
		// Last, because it empties the store: everything above has already run.
		{argv: []string{"forget", "--all", "--apply", "--i-mean-it",
			"--backup", p("wipe2.json"), "--reason", "end of the coverage run"},
			want: "wiped"},

		// --- refusals: the half that has to work when everything else does ----------------
		{argv: []string{"remember", "the key is sk-ABCDEFGHIJKLMNOPQRSTUVWX",
			"--type", "infra"}, wantErr: "refusing to store"},
		{argv: []string{"prune", "--tier", "A", "--older-than", "1"}, wantErr: "never decays"},
		{argv: []string{"prune", "--older-than", "0"}, wantErr: "at least 1 day"},
		{argv: []string{"task", "add", "when nobody can read it", "--when", "in 2 seconds"},
			wantErr: "cannot read"},
		{argv: []string{"task", "snooze", "1"}, wantErr: "--until"},
		{argv: []string{"whoami", "--observe", "depth=peer"}, wantErr: "evidence"},
		{argv: []string{"object", "1"}, wantErr: "reason"},
		{argv: []string{"forget", "--all"}, want: "DRY RUN"},
		{argv: []string{"forget", "--all", "--apply"}, wantErr: "--i-mean-it"},
		{argv: []string{"import", p("export.json"), "--type", "not-a-type"},
			wantErr: "type"},
		{argv: []string{"import", p("export.json"), "--events", "--all-projects",
			"--dry-run", "--allow-secret", "--i-mean-it", "--force"}, want: ""},
		{argv: []string{"get", "99999"}, wantErr: "no memory"},
	}
}

// Runs every step of the script in order against one store, then checks that every
// command the binary offers was run.
func TestEndToEndAcrossTheWholeSurface(t *testing.T) {
	root := e2eEnv(t)
	seen := map[string]bool{} // commands the script names, run or skipped with a reason

	for i, st := range script(t, root) {
		seen[st.argv[0]] = true
		if st.skip != "" {
			continue
		}
		var out bytes.Buffer
		err := cli.Run(append([]string{}, st.argv...), &out)
		got := out.String()
		switch {
		case st.wantErr != "":
			if err == nil {
				t.Errorf("step %d %v: expected a refusal, got:\n%s", i, st.argv, got)
			} else if !strings.Contains(err.Error(), st.wantErr) {
				t.Errorf("step %d %v: refusal was %q, wanted %q", i, st.argv, err.Error(), st.wantErr)
			}
		case err != nil:
			t.Errorf("step %d %v failed: %v\n%s", i, st.argv, err, got)
		case st.want != "" && !strings.Contains(got, st.want):
			t.Errorf("step %d %v: output does not contain %q:\n%s", i, st.argv, st.want, got)
		}
	}

	// The coverage claim, derived from the binary rather than from this file's opinion.
	// Commands this harness cannot reach, each with the reason and where it IS covered.
	// An exemption without a reason is how a coverage claim rots.
	// The hooks that read stdin and the blocking servers are not in commandNames at all
	// (see notATool), so they need no entry here.
	elsewhere := map[string]string{
		"bootstrap": "reads Claude Code transcripts from outside the store",
		"init":      "writes hooks into agent config files; covered by the internal/brain init tests",
		"uninstall": "removes an install; covered by internal/cli/target_scope_test.go and internal/brain",
		"absorb":    "pulls in a second standalone database; covered by internal/brain",
		"share":     "moves the whole store into another file; covered by internal/brain",
	}
	var missing []string
	for _, cmd := range commandNames() {
		if _, ok := elsewhere[cmd]; ok {
			continue
		}
		if !seen[cmd] {
			missing = append(missing, cmd)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d commands were never run: %s", len(missing), strings.Join(missing, " "))
	}
}

// Flags are the other half of the surface and the half that rots quietly: a flag nobody
// passes is a flag nobody notices stopped working. Same derivation as the tool schemas.
func TestEveryFlagIsExercisedSomewhere(t *testing.T) {
	root := t.TempDir()
	passed := map[string]bool{}
	for _, st := range script(t, root) {
		cmd := st.argv[0]
		for _, a := range st.argv[1:] {
			// Go treats -flag and --flag identically and the script uses both.
			if strings.HasPrefix(a, "-") && a != "-" {
				passed[cmd+" "+strings.TrimLeft(a, "-")] = true
			}
		}
	}

	// Flags that cannot be exercised in one hermetic run, each with the reason. Anything
	// not listed here and not passed above is a gap the test reports.
	// As above, the servers (mcp, ui) and the stdin hooks are not in commandNames.
	exempt := map[string]string{
		"init":      "writes hooks into agent config files; covered by the internal/brain init tests",
		"uninstall": "removes an install; covered by internal/cli/target_scope_test.go and internal/brain",
		"bootstrap": "reads Claude Code transcripts from outside the store",
		"absorb":    "pulls in a foreign database; covered by internal/brain",
		"share":     "moves the whole store into another file; covered by internal/brain",
	}

	// Per-FLAG exemptions, because exempting a whole command to excuse two of its flags
	// throws away the coverage of the rest. Same rule as above: a reason and where it is
	// actually covered.
	exemptFlag := map[string]string{
		"remember everywhere": "stores for a whole organisation, which only the team " +
			"edition has; the refusal on a local brain is TestEverywhereRefusesOnALocalBrain " +
			"in internal/brain",
		"backup restore": "needs the path of a copy that does not exist until the run " +
			"makes one; the drill is covered end to end by the internal/brain safety tests",
		"backup apply": "only meaningful with --restore; covered by the internal/brain " +
			"safety tests, which empty a store and put it back",
	}

	var gaps []string
	for _, cmd := range commandNames() {
		if _, ok := exempt[cmd]; ok {
			continue
		}
		if preSwitch[cmd] {
			continue // handled before the dispatch switch, so it registers no flags
		}
		fs, err := cli.ProbeFlags(cmd)
		if err != nil {
			t.Errorf("%s: %v", cmd, err)
			continue
		}
		fs.VisitAll(func(f *flag.Flag) {
			// json is global and passed through the harness on one command only; the
			// rest inherit it from the same dispatch line.
			if f.Name == "json" {
				return
			}
			if _, ok := exemptFlag[cmd+" "+f.Name]; ok {
				return
			}
			if !passed[cmd+" "+f.Name] {
				gaps = append(gaps, cmd+" --"+f.Name)
			}
		})
	}
	sort.Strings(gaps)
	if len(gaps) > 0 {
		t.Errorf("%d flags are never passed by the end-to-end script:\n  %s\n"+
			"Add them to script(), or exempt them with a reason.",
			len(gaps), strings.Join(gaps, "\n  "))
	}
}

// A coverage number nobody prints is a coverage number nobody checks.
func TestPrintTheSurfaceCoverage(t *testing.T) {
	root := t.TempDir()
	cmds := map[string]bool{}
	flags := 0
	for _, st := range script(t, root) {
		cmds[st.argv[0]] = true
		for _, a := range st.argv[1:] {
			if strings.HasPrefix(a, "--") {
				flags++
			}
		}
	}
	total := len(commandNames())
	fmt.Fprintf(os.Stderr, "[e2e] %d/%d commands exercised, %d flag uses across %d steps\n",
		len(cmds), total, flags, len(script(t, root)))
}

// `help <command>` printed the general list for a year, so a session looking one command
// up learned nothing it did not already have. The contrast is the assertion: per-command
// help has to say something bare help does not.
func TestHelpForOneCommandIsNotTheGeneralList(t *testing.T) {
	var general, one strings.Builder
	if err := cli.Run([]string{"help"}, &general); err != nil {
		t.Fatal(err)
	}
	if err := cli.Run([]string{"help", "task"}, &one); err != nil {
		t.Fatal(err)
	}
	if one.String() == general.String() {
		t.Fatal("help task printed the general list")
	}
	// its own flags, which the general list never carries
	for _, want := range []string{"-when", "-owner", "every monday 9am"} {
		if !strings.Contains(one.String(), want) {
			t.Errorf("help task does not mention %q:\n%s", want, one.String())
		}
	}
	if err := cli.Run([]string{"help", "nosuchcommand"}, &one); err == nil {
		t.Error("help for a command that does not exist did not say so")
	}
}
