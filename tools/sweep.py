#!/usr/bin/env python3
"""Full functional sweep of os-brain: every command, every deliberate refusal, the spine, the
UI and MCP transports, run as real processes against a throwaway install.

    python3 tools/sweep.py [scratch dir]
    NSB=/path/to/os-brain   (a binary built with -tags sweep; by default the sweep builds one)

This is os-brain's tools/local_sweep.py, the contract the local edition of os-brain keeps,
pointed at os-brain. os-brain promises the same behaviour, so the checks are the same; the
only differences are the binary's name, the OS_BRAIN_ variable names, and the checks for
features os-brain does not have (Hive, zero-knowledge brains, feedback to the maintainers,
self-upgrade). Those are listed in SKIPPED below with the reason, and printed at the end.

Everything happens under the scratch dir: a project installed with `os-brain init`, a
neighbour project for the cross-project commands, and a decoy spine (CLAUDE_CONFIG_DIR and
OS_BRAIN_GLOBAL_DB) that the real one is never asked to be. The real stores of both os-brain
and an installed os-brain are hashed before and after: the run must not touch them. Where a
check fails, fix the code.
"""
import datetime, hashlib, http.client, json, os, re, shutil, socket, subprocess, sys, tempfile, time

HOME = os.path.expanduser("~")
SCRATCH = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "os-brain-sweep")
def build_sweep_binary():
    """The binary under test is built from this checkout with -tags sweep: the only build
    that honours the scripted terminal (OS_BRAIN_SWEEP_TTY) the person-only commands need.
    A release build never carries it."""
    # A directory of its own per run, so two sweeps run side by side (from two worktrees,
    # say) never test each other's binary.
    out = os.path.join(tempfile.mkdtemp(prefix="os-brain-sweep-bin-"), "os-brain")
    src = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    r = subprocess.run(["go", "build", "-tags", "sweep", "-o", out, "./cmd/os-brain"], cwd=src, capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit("building the sweep binary failed:\n" + r.stdout + r.stderr)
    return out

NSB = os.environ.get("NSB") or build_sweep_binary()
VERSION = subprocess.run([NSB, "version"], capture_output=True, text=True).stdout.split()[1]
LTEST = "localtest-sweep"
NEIGH = "neighbour-sweep"
PROJ = os.path.join(SCRATCH, "proj")
BRAIN = os.path.join(PROJ, "brain")
NPROJ = os.path.join(SCRATCH, "neigh")
NBRAIN = os.path.join(NPROJ, "brain")
CFG = os.path.join(SCRATCH, "cfg")
SPINE = os.path.join(CFG, "brain", "brain.db")

# The real stores a run must leave untouched: both editions' default shared stores, plus any
# project stores of your own you want proven untouched, colon-separated in
# OS_BRAIN_SWEEP_GUARD (for example a project whose brain you use every day).
REAL = [os.path.join(HOME, ".claude/os-brain/brain.db"),
        os.path.join(HOME, ".claude/brain/brain.db")] + \
       [p for p in os.environ.get("OS_BRAIN_SWEEP_GUARD", "").split(":") if p]

def sha(p):
    h = hashlib.sha256()
    for f in [p, p + "-wal"]:
        if os.path.exists(f):
            h.update(open(f, "rb").read())
    return h.hexdigest()

shutil.rmtree(SCRATCH, ignore_errors=True)
os.makedirs(PROJ); os.makedirs(NPROJ); os.makedirs(os.path.join(CFG, "brain"))
for d in (PROJ, NPROJ):
    subprocess.run(["git", "init", "-q", d])
    open(os.path.join(d, "README.md"), "w").write("# throwaway\n")
    subprocess.run(["git", "-C", d, "add", "."])
    subprocess.run(["git", "-C", d, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"])

ENV = dict(os.environ, CLAUDE_CONFIG_DIR=CFG, OS_BRAIN_GLOBAL_DB=SPINE,
           # Test mode, honoured only by a -tags sweep build: the process tree this sweep runs
           # under (often an agent's) must not decide whether a scripted person is a person.
           OS_BRAIN_TEST="1")
# The operator's own Claude Code session must never leak into the sweep: a hook run here with
# it would register THAT session under the sweep's throwaway project and deliver its messages
# here. Every check that wants a session names one explicitly.
for _k in ("CLAUDE_CODE_SESSION_ID", "CLAUDE_PID", "OS_BRAIN_SESSION_ID", "CLAUDECODE"):
    ENV.pop(_k, None)
ENV.pop("OS_BRAIN_HOME", None); ENV.pop("OS_BRAIN_PROJECT", None)
before = {p: sha(p) for p in REAL}
results = []

# Checks of os-brain's local sweep that do not apply to os-brain, and why. Each is recorded
# where it would have run, so the list below and the run agree.
SKIPPED = []
def skip(name, why):
    SKIPPED.append((name, why))
    print("  SKIP  " + name + "  (" + why + ")")

def run(*args, stdin=None, cwd=PROJ, home=None, env_extra=None):
    env = dict(ENV)
    if home:
        env["OS_BRAIN_HOME"] = home
    if env_extra:
        env.update(env_extra)
    r = subprocess.run([NSB, *args], capture_output=True, text=True, env=env, cwd=cwd, input=stdin, timeout=120)
    return r.returncode, r.stdout, r.stderr

def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("  PASS  " if ok else "  FAIL  ") + name + ("" if ok else "\n          " + detail.strip()[:500]))

def js(*args, **kw):
    rc, out, err = run(*args, "--json", **kw)
    try:
        return rc, json.loads(out), err
    except Exception:
        return rc, None, out + err

def section(t): print("\n=== " + t)

# ------------------------------------------------------------------ A. init
section("A. init: the install")
rc, out, err = run("init", "--project", LTEST, "--dir", "brain")
check("init creates the brain", rc == 0 and os.path.exists(os.path.join(BRAIN, "brain.db")) and os.path.exists(os.path.join(BRAIN, "brain.json")), out + err)
check("init writes HOWTO.md and BEST-PRACTICES.md out of the binary", os.path.exists(os.path.join(BRAIN, "HOWTO.md")) and os.path.exists(os.path.join(BRAIN, "BEST-PRACTICES.md")), "")
_cmds = {n: os.path.join(PROJ, ".claude", "commands", n + ".md") for n in
         ("who", "msg", "rdo", "dnd", "inbox", "accept", "reject")}
check("init writes all seven slash commands into .claude/commands",
      all(os.path.exists(p) for p in _cmds.values()), str([n for n, p in _cmds.items() if not os.path.exists(p)]))
check("/who wraps sessions, /msg wraps dm, /rdo wraps dm --rdo",
      "os-brain sessions" in open(_cmds["who"]).read()
      and "os-brain dm" in open(_cmds["msg"]).read()
      and "--rdo" in open(_cmds["rdo"]).read(), "")
check("accept, reject, rdo, msg and dnd are the person's to type, and no heredoc ends on a fixed word",
      all("\ndisable-model-invocation: true\n" in open(_cmds[n]).read() for n in ("accept", "reject", "rdo", "msg", "dnd"))
      and not any("OS_BRAIN_DM_EOF" in open(p).read() for p in _cmds.values())
      and not any("!`" in open(_cmds[n]).read() for n in ("inbox", "who")), "")
check("/accept tells the person to run the accept in their own terminal and lets the model run nothing",
      "allowed-tools" not in open(_cmds["accept"]).read() and "terminal of my own" in open(_cmds["accept"]).read(), "")
_inj = tempfile.mkdtemp(dir=SCRATCH, prefix="inject-")
rc, out, err = run("init", "--project", LTEST + "-inject", "--dir", "b$(touch${IFS}PWNED)", cwd=_inj)
check("init refuses a --dir that would run in every hook command, and writes nothing",
      rc != 0 and "--dir" in err and not os.path.exists(os.path.join(_inj, ".claude")) and not os.path.exists(os.path.join(_inj, "PWNED")), out + err)
st = json.load(open(os.path.join(PROJ, ".claude", "settings.json")))
hooks = {ev: [h["hooks"][0]["command"] for h in hs] for ev, hs in st.get("hooks", {}).items()}
check("init wires SessionStart, UserPromptSubmit and Stop, one hook each", all(len(hooks.get(ev, [])) == 1 and "os-brain" in hooks[ev][0] for ev in ("SessionStart", "UserPromptSubmit", "Stop")), json.dumps(hooks))
check("init does not edit CLAUDE.md (retired: the hooks carry the brief)", not os.path.exists(os.path.join(PROJ, "CLAUDE.md")), "")
check("init gitignores the database", "brain.db" in open(os.path.join(PROJ, ".gitignore")).read(), "")
rc, out, err = run("init", "--project", LTEST, "--dir", "brain")
check("init is idempotent", rc == 0 and len(json.load(open(os.path.join(PROJ, ".claude", "settings.json")))["hooks"]["SessionStart"]) == 1, out + err)
rc, out, err = run("init", "--project", NEIGH, "--dir", "brain", cwd=NPROJ)
check("a second project installs beside it", rc == 0, out + err)
rc, out, err = run("version")
check(f"client is {VERSION}", VERSION in out, out)
rc, out, err = run("env")
check("env answers the clock, paths and git", rc == 0 and LTEST in out and "CONTEXT" in out and "git" in out.lower(), out + err)
check("env says LOCAL FILE in words, names the file and says nobody else writes here",
      "LOCAL FILE" in out and os.path.join(BRAIN, "brain.db") in out and "on this machine only" in out, out + err)
check("env names this machine and says a local brain has no accounts",
      "@" in out and "no accounts" in out and "HIVE" not in out, out + err)
rc, out, err = run("env", "--json")
_env = json.loads(out)
check("env --json carries the edition, the machine and the file",
      _env.get("edition") == "local" and _env.get("machine") and _env.get("db"), out[:300] + err)
skip("hive status fails loudly: no credential, not attached", "Hive is the team edition; os-brain has no hive command")

# ------------------------------------------------------------------ B. remember / recall
section("B. remember and recall")
rc, d, err = js("remember", "LTEST alpha: the primary colour of this throwaway is vermilion", "--body",
                "written by the full local sweep on 2026-09-18", "--type", "knowledge", "--scope", "global")
check("remember stores", rc == 0 and d and d.get("id"), str(d) + err)
ID1 = d.get("id") if d else 0
rc, d, err = js("remember", "LTEST beta: the deploy host is decoy.example.test", "--body",
                "a fact in a channel scope", "--type", "infra", "--scope", "channel:deploy")
ID2 = d.get("id") if d else 0
check("remember into a channel scope", rc == 0 and ID2, str(d) + err)
rc, d, err = js("remember", "LTEST gamma: the state of the sweep is running", "--body", "tier D state row",
                "--type", "state", "--importance", "5", "--confidence", "confirmed", "--pin",
                "--source", "stated by the user")
ID3 = d.get("id") if d else 0
check("remember --pin with importance and confidence", rc == 0 and ID3, str(d) + err)
rc, d, err = js("remember", "LTEST alpha: the primary colour of this throwaway is vermilion", "--body",
                "a DIFFERENT body under the same title")
check("same title, different body is refused (no silent replace)", rc != 0, str(d) + err)
rc, d, err = js("remember", "LTEST epsilon: the sweep's update target keeps one title", "--body", "the first body")
IDU = d.get("id") if d else 0
rc, d, err = js("remember", "LTEST epsilon: the sweep's update target keeps one title", "--body", "the second body", "--update")
check("remember --update replaces the body in place", rc == 0 and d and d.get("id") == IDU, str(d) + err)
rc, d, err = js("get", str(IDU))
check("the replaced body is the one stored", rc == 0 and d and d.get("body") == "the second body", str(d)[:200] + err)
rc, d, err = js("history", str(IDU))
check("the body --update replaced is in history", rc == 0 and isinstance(d, dict) and any(v.get("body") == "the first body" for v in d.get("history") or []), str(d)[:300] + err)
rc, out, err = run("remember", "LTEST zeta: a task is not a memory", "--type", "task")
check("remember --type task is refused and names the task command", rc != 0 and "task add" in err, out + err)
rc, out, err = run("remember", "LTEST eta: an uncited claim", "--body", "trust me", "--confidence", "confirmed")
check("remember --confidence confirmed without a citation is refused", rc != 0 and "--source" in err, out + err)
rc, d, err = js("remember", "LTEST alpha: the primary colour of this throwaway is scarlet", "--body",
                "same subject, overlapping title")
check("second memory about the same subject is blocked with the ways out", rc != 0 and ("--supersedes" in err or "--allow-duplicate" in err or "update" in err), str(d) + err)
rc, d, err = js("remember", "LTEST delta: a token AKIAIOSFODNN7EXAMPLE lives here", "--body", "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
check("secret guard refuses a credential-looking body", rc != 0 and "secret" in (err + str(d)).lower(), str(d) + err)
rc, d, err = js("remember", "LTEST delta: a token AKIAIOSFODNN7EXAMPLE lives here", "--body",
                "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "--allow-secret")
ID4 = d.get("id") if d else 0
check("--allow-secret stores it deliberately", rc == 0 and ID4, str(d) + err)
rc, d, err = js("remember", "LTEST alpha v2: the primary colour is now cobalt", "--body",
                "supersedes alpha", "--type", "knowledge", "--supersedes", str(ID1))
ID5 = d.get("id") if d else 0
check("remember --supersedes retracts the old row", rc == 0 and ID5, str(d) + err)

rc, d, err = js("recall", "vermilion", "--no-touch")
check("recall of a retracted row answers with the successor", rc == 0 and d and d.get("matched") and any(r["id"] == ID5 for r in d["results"]) and not any(r["id"] == ID1 for r in d["results"]), str(d)[:300] + err)
via = [v.get("id") for r in (d or {}).get("results", []) if r.get("id") == ID5 for v in (r.get("matched_via") or [])]
check("the successor names the RETRACTED memory the query really matched (matched_via)", ID1 in via, str(d)[:400])
rcv, outv, errv = run("recall", "vermilion", "--no-touch")
check("and says so in text, with the id to read it by", "RETRACTED #%d" % ID1 in outv and "get %d" % ID1 in outv, outv + errv)
rc, d, err = js("recall", "vermilion", "--no-touch", "--all")
check("recall --all returns the retracted row itself, marked with its successor", rc == 0 and d and any(r["id"] == ID1 and r.get("superseded_by") == ID5 for r in d["results"]), str(d)[:300])
rc, d, err = js("recall", "zzzqqqxnonsense", "--no-touch")
check("a miss FAILS CLOSED: matched false, no results", rc == 0 and d is not None and d.get("matched") is False and not d.get("results"), str(d)[:300] + err)
rc, d, err = js("recall", "zzzqqqxnonsense", "--no-touch", "--browse")
check("--browse on a miss shows the scope with matched false", rc == 0 and d and d.get("matched") is False and d.get("results"), str(d)[:300])
rc, d, err = js("recall", "zzzqqqxnonsense", "--no-touch", "--browse", "--strict")
check("--strict overrides --browse", rc == 0 and d is not None and not d.get("results"), str(d)[:300])
rc, d, err = js("recall", "decoy.example.test", "--no-touch", "--scope", "channel:deploy")
check("recall --scope sees the channel and what flows inward from global, nothing else", rc == 0 and d and d.get("matched") and all(r["scope"] in ("channel:deploy", "global") for r in d["results"]) and any(r["scope"] == "channel:deploy" for r in d["results"]), str(d)[:300])
rc, d, err = js("recall", "LTEST", "--no-touch", "--type", "infra")
check("recall --type filters", rc == 0 and d and d.get("matched") and all(r["type"] == "infra" for r in d["results"]), str(d)[:300])
rc, d, err = js("recall", "LTEST", "--no-touch", "--tier", "D")
check("recall --tier D returns the state row only", rc == 0 and d and d.get("matched") and all(r["type"] == "state" for r in d["results"]), str(d)[:300])
rc, d, err = js("recall", "LTEST", "--no-touch", "--type", "nosuchtype")
check("a typo'd --type is an error, not an empty result", rc != 0, str(d) + err)
rc, d, err = js("recall", "LTEST", "--no-touch", "--since", "1")
check("recall --since", rc == 0 and d and d.get("matched"), str(d)[:200])
rc, d, err = js("remember", "LTEST Πιλότος Χαλανδρίου (Ευάγγελος Κούρης)", "--body", "ελληνικό κείμενο για τον έλεγχο", "--type", "knowledge")
IDG = d.get("id") if d else 0
rc, d, err = js("recall", "Κούρης", "--no-touch")
check("a Greek query hits a Greek title", rc == 0 and d and d.get("matched") and any(r["id"] == IDG for r in d["results"]), str(d)[:300])
rc, out, err = run("recall", "LTEST", "--no-touch")
check("recall human rendering", rc == 0 and "#" in out and "LTEST" in out, out + err)
# One extra word must not remove the best row. Candidacy was all-terms-or-nothing, so on
# 2026-09-18 a memory titled with three of a query's four terms vanished because weaker rows
# held all four somewhere in a body. The epsilon row is that weaker row.
rc, d, err = js("remember", "LTEST epsilon: marsupials seen on site", "--body",
                "a quokka near the deploy host turned out to be a decoy", "--type", "knowledge")
IDE = d.get("id") if d else 0
rc, d, err = js("recall", "quokka deploy host decoy", "--no-touch")
got = [r["id"] for r in (d or {}).get("results", [])]
check("a row holding 3 of 4 query terms stays a candidate beside one holding all 4", rc == 0 and d and d.get("matched") and ID2 in got and IDE in got, str(got) + err)
rc, d, err = js("recall", "vermilion quokka", "--no-touch", "--all")
got = [r["id"] for r in (d or {}).get("results", [])]
check("two terms still need both before any-term is tried", rc == 0 and d and d.get("matched") and ID1 in got and IDE in got, str(got) + err)
rc, out, err = run("recall", "--browse", "--no-touch")
check("recall --browse renders rows as text, not a miss", rc == 0 and "#" in out and "nothing matched" not in out, out[:300] + err)
# Saying "supersedes #N" in a body retracts nothing; only the flag does. 2026-09-18: a
# decision stored that way left the rule it killed live, in every brief, and obeyed.
rc, out, err = run("remember", "LTEST zeta: the colour question is closed", "--body", "Supersedes #%d. Stop asking." % ID5, "--type", "decision")
check("a body that says it supersedes a LIVE memory is refused without the flag", rc != 0 and "--supersedes %d" % ID5 in (out + err), out + err)
rc, out, err = run("remember", "LTEST zeta: the colour question is closed", "--body", "Supersedes #99999999. Stop asking.", "--type", "decision")
check("a body that says it supersedes an id this project does not hold says so", rc != 0 and "no #99999999" in (out + err), out + err)
rc, out, err = run("remember", "LTEST zeta: the colour question is closed", "--body", "x", "--type", "decision", "--supersedes", "99999999")
check("--supersedes on an id this project does not hold is refused, not ignored", rc != 0 and "no #99999999" in (out + err), out + err)
rc, d, err = js("remember", "LTEST zeta: the colour question is closed", "--body", "the April note supersedes #99999999 in the old tracker", "--type", "decision", "--claim-ok")
IDZ = d.get("id") if d else 0
check("--claim-ok stores a sentence that is history", rc == 0 and IDZ, str(d) + err)
# Absence has to be measured against a THIN brain. This sweep's main project uses scopes,
# tasks, the log and the index by construction, so it is the one shape that legitimately has
# no omissions - and a check asserting otherwise there tests nothing.
THIN = os.path.join(SCRATCH, "thin")
THIN_NAME = "thin-sweep"
os.makedirs(THIN, exist_ok=True)
subprocess.run(["git", "init", "-q", THIN])
rc, out, err = run("init", "--project", THIN_NAME, "--dir", "brain", cwd=THIN)
check("a third, deliberately thin project installs", rc == 0, out + err)
for i, t in enumerate(["the deploy runbook", "how billing is wired", "why postgres here",
                       "what the api returns", "where the certs live", "the nightly job"]):
    run("remember", f"{t} {i}", "--body", f"a body long enough to be a real memory about {t}.", cwd=THIN)
rc, out, err = run("review", cwd=THIN)
check("review tells a thin brain which dimensions it has never used",
      "NEVER SCOPED" in out and "NEVER LOGGED" in out and "NOT IN THE SHARED INDEX" in out, out[:900] + err)
check("review does not call a brain that has never used half the product clean",
      "nothing to clean up" not in out, out[:600] + err)
rc, out, err = run("brief", cwd=THIN)
check("the brief carries the one-line summary of what has never been used",
      "THIS BRAIN HAS NEVER USED" in out, out[:900] + err)
rc, d, err = js("review", cwd=THIN)
check("review --json carries the omissions as data",
      rc == 0 and d and len(d.get("omissions") or []) >= 3, str(d)[:300] + err)
rc, out, err = run("doctor")
check("doctor reports the unrecorded claim and names both ids", "#%d says it supersedes #99999999" % IDZ in (out + err), (out + err)[-400:])
# Two sessions, one project, under two minutes: the second is told to ask which stands.
r = subprocess.run([NSB, "remember", "LTEST eta: the colour question is reopened after all", "--body", "says the other session", "--type", "decision"],
                   capture_output=True, text=True, cwd=PROJ, env=dict(ENV, OS_BRAIN_SESSION_ID="LTEST-another-session", CLAUDE_CODE_SESSION_ID=""))
check("a second session writing a decision inside two minutes is alerted and told to ask", r.returncode == 0 and "ALERT" in (r.stdout + r.stderr) and "ask the user" in (r.stdout + r.stderr), r.stdout + r.stderr)
rc, out, err = run("remember", "LTEST theta: an unrelated note about lunch", "--body", "same session as most of this sweep", "--type", "knowledge")
check("an unrelated fact does not alert", rc == 0 and "ALERT" not in (out + err), out + err)

# ------------------------------------------------------------------ C. get / update / verify / forget / history / restore
section("C. get, update, verify, forget, history, restore")
rc, d, err = js("get", str(ID5))
check("get by id", rc == 0 and d and d.get("id") == ID5 and "cobalt" in d.get("title", ""), str(d)[:200] + err)
rc, d, err = js("get", str(ID1))
check("get of a retracted row carries superseded_by (json)", rc == 0 and d and d.get("superseded_by") == ID5, str(d)[:300])
rc, out, err = run("get", str(ID1))
check("get of a retracted row says SUPERSEDED in the human text", rc == 0 and "SUPERSEDED" in out and f"#{ID5}" in out, out + err)
rc, out, err = run("update", str(ID5), "--body", "supersedes alpha, edited once by the sweep")
check("update --body", rc == 0, out + err)
rc, out, err = run("update", str(ID5), "--body", "")
check("update with a blank body is refused without --force", rc != 0 and "refusing to blank" in (out + err), out + err)
rc, out, err = run("update", str(ID5), "--title", "HTEST alpha v2: the primary colour is now cobalt blue", "--force")
rc2, d2, _ = js("get", str(ID5))
check("--force on a title-only update does not blank the body", rc == 0 and d2 and d2.get("body"), out + err + str(d2)[:200])
rc, out, err = run("update", str(ID5), "--title", "LTEST alpha v2: the primary colour is now cobalt blue")
check("update --title", rc == 0, out + err)
rc, out, err = run("update", str(ID2), "--scope", "channel:deploy:prod")
rc2, d2, _ = js("get", str(ID2))
check("update --scope refiles", rc == 0 and d2 and d2.get("scope") == "channel:deploy:prod", out + err + str(d2)[:200])
run("update", str(ID2), "--scope", "channel:deploy")
rc, out, err = run("update", str(ID2), "--pin")
check("update --pin", rc == 0, out + err)
rc, out, err = run("update", str(ID2), "--unpin")
check("update --unpin", rc == 0, out + err)
rc, out, err = run("update", str(ID2), "--importance", "5", "--confidence", "confirmed")
check("update --confidence confirmed without a citation is refused", rc != 0 and "--source" in err, out + err)
rc, out, err = run("update", str(ID2), "--importance", "5", "--confidence", "confirmed", "--source", "stated by the user")
check("update importance and confidence", rc == 0, out + err)
rc, d, err = js("history", "--id", str(ID5))
vs = d.get("history") if isinstance(d, dict) else None
rcp, dp, errp = js("history", str(ID5))
check("history <id> is the same answer as history --id <id>", rcp == 0 and isinstance(dp, dict) and dp.get("history") == vs, str(dp)[:200] + errp)
rcn, outn, errn = run("history", "notanid")
check("history refuses a positional that is not an id", rcn != 0 and "not one" in (outn + errn), outn + errn)
check("history lists the archived versions under the history key, newest first", rc == 0 and vs and len(vs) >= 2 and all(v.get("mem_id") == ID5 for v in vs) and vs[0]["id"] > vs[-1]["id"], str(d)[:300] + err)
VER = vs[-1]["id"] if vs else None  # oldest: history is newest-first on both editions
rc, out, err = run("restore", str(VER)) if VER else (1, "", "no version id")
check("restore refuses to roll back a LIVE memory without --force", rc != 0 and "still exists" in (out + err), out + err)
rc, out, err = run("restore", "h%d" % VER, "--force") if VER else (1, "", "no version id")
rc2, d2, _ = js("get", str(ID5))
check("restore --force rolls the live memory back", rc == 0 and d2 and d2.get("body") == "supersedes alpha", out + err + str(d2)[:200])
rc, d, err = js("remember", "LTEST omega: a fact to forget and bring back", "--body", "the way back from forget", "--type", "knowledge")
IDO = d.get("id") if d else 0
run("forget", str(IDO), "--reason", "sweep: by mistake")
rc, d, err = js("history", "--id", str(IDO))
HO = (d.get("history") or [{}])[0].get("id") if isinstance(d, dict) else None
rc, out, err = run("restore", str(HO)) if HO else (1, "", "no version")
rc2, d2, _ = js("recall", "way back from forget", "--no-touch")
check("restore brings back a FORGOTTEN memory, tombstone cleared", rc == 0 and d2 and d2.get("matched") and d2["results"][0]["id"] != IDO, out + err + str(d2)[:200])
rc, out, err = run("verify", str(ID5))
check("verify marks a row re-checked", rc == 0, out + err)
rc, out, err = run("verify", str(ID5), "--confidence", "confirmed")
check("verify --confidence confirmed without a citation is refused", rc != 0 and "--source" in err, out + err)
rc, out, err = run("verify", str(ID5), "--confidence", "confirmed", "--source", "stated by the user")
check("verify --confidence", rc == 0, out + err)
rc, out, err = run("forget", str(ID3))
check("forget refuses a pinned row without --force", rc != 0 and "pin" in (out + err).lower(), out + err)
rc, d, err = js("remember", "LTEST policy: the sweep never deletes a real memory", "--body", "a guardrail-type row", "--type", "policy")
IDP = d.get("id") if d else 0
check("a policy row can be stored locally", rc == 0 and IDP, str(d) + err)
rc, out, err = run("forget", str(IDP))
check("forget refuses a guardrail-type row without --force", rc != 0 and ("guardrail" in (out + err).lower() or "policy" in (out + err).lower()), out + err)
rc, out, err = run("forget", str(ID5))
check("forget refuses a row that supersedes others", rc != 0 and "supersed" in (out + err).lower(), out + err)
rc, out, err = run("forget", str(ID4), "--reason", "sweep cleanup")
check("forget of an ordinary row works", rc == 0, out + err)
rc, d, err = js("get", str(ID4))
check("a forgotten row is gone", rc != 0 or not d or d.get("id") != ID4, str(d)[:200])

# ------------------------------------------------------------------ D. prefer / whoami / capture / object / ladder
section("D. prefer, whoami, capture, utterances, object, ladder")
rc, d, err = js("prefer", "LTEST preference: sweeps report in tables", "--why", "said on 2026-09-18")
check("prefer stores a preference", rc == 0 and d and d.get("id"), str(d) + err)
rc, out, err = run("whoami")
check("whoami renders the user model", rc == 0, out + err)
rc, out, err = run("whoami", "--observe", "reporting=tables", "--evidence", "LTEST sweep")
check("whoami --observe records a trait", rc == 0, out + err)
rc, out, err = run("whoami")
check("the trait is visible", rc == 0 and "tables" in out, out + err)
rc, out, err = run("whoami", "--forget", "reporting")
check("whoami --forget drops it", rc == 0, out + err)
rc, out, err = run("capture", "LTEST utterance: from now on always run the sweep before a release")
check("capture stores an utterance", rc == 0, out + err)
rc, out, err = run("utterances")
check("utterances lists it with a classification", rc == 0 and "sweep" in out, out + err)
rc, out, err = run("object", str(ID5), "--why", "the sweep disagrees for the sake of testing")
check("object records an objection", rc == 0, out + err)
rc, out, err = run("ladder", str(ID5))
check("ladder reports where the memory stands", rc == 0 and ("stated" in out.lower() or "hardened" in out.lower() or "law" in out.lower()), out + err)

# ------------------------------------------------------------------ E. log / events / tasks / open / measure / lead
section("E. log, events, tasks, open, measure, lead")
rc, out, err = run("log", "LTEST: the sweep logged an event", "--kind", "note")
check("log appends an event", rc == 0, out + err)
rc, d, err = js("events", "-n", "5")
check("events lists it", rc == 0 and d is not None and "sweep logged" in json.dumps(d), str(d)[:300] + err)
rc, d, err = js("task", "add", "LTEST task: owed by the agent", "--owner", "agent", "--when", "tomorrow 9am")
TID = d.get("id") if d else None
check("task add with a schedule", rc == 0 and TID, str(d)[:300] + err)
rc, d, err = js("task", "add", "LTEST task: owed by Pat", "--owner", "user", "--when", "every monday 9am")
TID2 = d.get("id") if d else None
check("task add recurring", rc == 0 and TID2, str(d)[:300] + err)
rc, out, err = run("task", "list")
check("task list shows both", rc == 0 and "owed by the agent" in out and "owed by Pat" in out, out + err)
rc, out, err = run("open")
check("open answers from tasks and the log", rc == 0 and "owed by the agent" in out and "sweep logged" in out, out + err)
rc, out, err = run("task", "snooze", str(TID), "--until", "in 3 days")
check("task snooze", rc == 0, out + err)
rc, out, err = run("task", "done", str(TID))
check("task done", rc == 0, out + err)
rc, out, err = run("task", "drop", str(TID2))
check("task drop", rc == 0, out + err)
rc, out, err = run("task", "list", "--all")
check("task list --all shows the closed ones", rc == 0 and "owed by the agent" in out, out + err)
rc, out, err = run("task", "list")
check("task list hides the closed ones", rc == 0 and "owed by the agent" not in out, out + err)
# states, set and typo refusals, on the done task so the export's task count does not move
rc, out, err = run("task", "reopen", str(TID))
check("task reopen brings a done task back", rc == 0 and "open (was done)" in out, out + err)
rc, out, err = run("task", "wait", str(TID))
check("task wait", rc == 0 and "waiting (was open)" in out, out + err)
rc, out, err = run("task", "list")
check("a waiting task stays on the default list, marked", rc == 0 and "[~]" in out and "owed by the agent" in out, out + err)
rc, out, err = run("task", "set", str(TID), "--title", "LTEST task: owed by the agent, edited", "--body", "sweep: awaiting a reply")
check("task set edits title and body in place", rc == 0 and "updated (title, body)" in out, out + err)
rc, d, err = js("task", "list", "--state", "waiting")
rows = d if isinstance(d, list) else []
check("task list --state waiting returns the edit", rc == 0 and any(t.get("id") == TID and t.get("body") == "sweep: awaiting a reply" and t.get("title", "").endswith("edited") for t in rows), str(d)[:300] + err)
rc, out, err = run("task", "bogus")
check("an unknown task subcommand is refused", rc != 0 and "task has no" in err, out + err)
rc, out, err = run("task", str(TID), "--state", "dropped")
check("task <id> --state is refused, not answered with the list", rc != 0 and "after the subcommand" in err, out + err)
rc, out, err = run("task", "done", str(TID), "--body", "x")
check("a flag the subcommand ignores is refused", rc != 0 and "does not take --body" in err, out + err)
rc, out, err = run("task", "list", "--state", "closed")
check("an unknown --state is refused", rc != 0 and "--state is one of" in err, out + err)
rc, out, err = run("note", "--elsewhere")
check("note --elsewhere answers (other orgs' mailboxes on a hive, a refusal on a local file)", rc != 0 and "local file" in err, out + err)
skip("feedback refuses a one-word body as a likely mistyped subcommand", "feedback sends to the maintainers' server; os-brain never calls home")
skip("feedback from an unregistered local brain says how to register", "feedback sends to the maintainers' server; os-brain never calls home")
rc, out, err = run("task", "done", str(TID))
check("task done from waiting", rc == 0 and "done (was waiting)" in out, out + err)
rc, out, err = run("task", "drop", str(TID))
check("task drop on a done task", rc == 0 and "dropped (was done)" in out, out + err)
rc, out, err = run("measure", "add", "sweep_latency_ms", "42", "--unit", "ms", "--why", "LTEST")
check("measure add", rc == 0, out + err)
run("measure", "add", "sweep_latency_ms", "40", "--unit", "ms", "--why", "LTEST")
rc, out, err = run("measure", "series", "sweep_latency_ms")
check("measure series renders the series", rc == 0 and "42" in out and "40" in out, out + err)
rc, out, err = run("measure", "list")
check("measure list", rc == 0 and "sweep_latency_ms" in out, out + err)
rc, out, err = run("lead", "add", "--name", "Ada Lovelace", "--company", "Analytical Engines", "--status", "engaged", "--value-est", "12000", "--next-action", "send the proposal", "--next-date", "2026-09-25")
check("lead add", rc == 0, out + err)
rc, out, err = run("lead", "list")
check("lead list", rc == 0 and "Lovelace" in out, out + err)
rc, out, err = run("lead", "pipeline")
check("lead pipeline sums the funnel", rc == 0 and "engaged" in out and "$12,000" in out, out + err)
rc, out, err = run("lead", "hot")
check("lead hot", rc == 0 and "Lovelace" in out, out + err)
rc, out, err = run("lead", "next")
check("lead next", rc == 0 and "send the proposal" in out, out + err)
rc, d, err = js("lead", "list")
LID = d[0]["id"] if isinstance(d, list) and d else (d.get("leads") or [{}])[0].get("id") if isinstance(d, dict) else 0
rc, out, err = run("lead", "show", str(LID))
check("lead show", rc == 0 and "Analytical" in out, out + err)
rc, out, err = run("lead", "update", str(LID), "--status", "contacted", "--append-note", "sweep pinged her")
check("lead update --status --append-note", rc == 0, out + err)
rc, out, err = run("lead", "show", str(LID))
check("lead update landed, note dated and appended", "contacted" in out and "pinged" in out, out + err)

# ------------------------------------------------------------------ F. reports and files
section("F. brief, wakeup, hooks, map, coverage, review, stats, doctor, snapshot, export, backup")
rc, out, err = run("brief")
check("brief renders with the project name", rc == 0 and LTEST.upper() in out, out[:400] + err)
check("brief states the edition and the store, not just the path",
      "LOCAL FILE" in out and "on this machine only" in out, out[:800] + err)
check("brief names the user and the machine", "  you       " in out and "@" in out, out[:800] + err)
check("brief opens with the pinned row", ID3 and f"#{ID3}" in out, out[:800])
rc, out, err = run("brief", "--max-bytes", "1500")
check("brief --max-bytes truncates", rc == 0 and len(out.encode()) <= 1700, f"{len(out.encode())} bytes")
rc, out, err = run("backup", "--list")
check("backup --list before any copy says where they will land", rc == 0 and "no safety copies yet" in out and CFG in out, out + err)
rc, out, err = run("wakeup", "--no-tree", "--no-probes")
check("wakeup renders the brief", rc == 0 and LTEST.upper() in out, out[:300] + err)
check("wakeup took the day's safety copy", "SAFETY COPY" in out and "safety copy:" in out, out[-400:])
rc, out, err = run("wakeup", "add", "--name", "sweep-probe", "--run", "echo probe-output-here")
check("wakeup add registers a probe", rc == 0, out + err)
rc, out, err = run("wakeup", "list")
check("wakeup list shows it", rc == 0 and "sweep-probe" in out, out + err)
rc, out, err = run("wakeup", "--no-tree", "--no-brief")
check("wakeup runs the probe", rc == 0 and ("probe-output-here" in out or "sweep-probe" in out), out[-400:] + err)
rc, out, err = run("wakeup", "rm", "--name", "sweep-probe")
check("wakeup rm removes it", rc == 0, out + err)
rc, out, err = run("hook-recall", stdin=json.dumps({"prompt": "is the colour cobalt?"}))
check("hook-recall injects the matching memory", rc == 0 and "cobalt" in out, out[:400] + err)
check("hook-recall names the project and the edition on every message",
      f"[{LTEST} brain \u00b7 local file]" in out, out[:300] + err)
rc, out, err = run("hook-recall", stdin=json.dumps({"prompt": "zzzqqqx nothing here"}))
check("hook-recall on a miss injects nothing", rc == 0 and "LTEST" not in out, out[:300] + err)
rc, out, err = run("hook-stop", stdin=json.dumps({"session_id": "localtest-session"}))
check("hook-stop reports what this session wrote", rc == 0, out[:300] + err)
rc, out, err = run("map")
check("map indexes scopes and types", rc == 0 and "channel:deploy" in out, out[:400] + err)
rc, out, err = run("scope", "add", "channel:deploy", "--desc", "deploys of the throwaway")
check("scope declares a scope in brain.json", rc == 0 and json.load(open(os.path.join(BRAIN, "brain.json")))["scopes"].get("channel:deploy") == "deploys of the throwaway", out + err)
rc, out, err = run("kind", "add", "sweep", "--desc", "an event the sweep logs")
check("kind declares an event kind in brain.json", rc == 0 and json.load(open(os.path.join(BRAIN, "brain.json")))["kinds"].get("sweep") == "an event the sweep logs", out + err)
rc, out, err = run("context", "set", "about", "a throwaway for the local sweep")
check("context sets a project fact", rc == 0 and json.load(open(os.path.join(BRAIN, "brain.json")))["context"].get("about", "").startswith("a throwaway"), out + err)
rc, out, err = run("coverage")
check("coverage by category", rc == 0, out[:300] + err)
rc, out, err = run("remember", "never run this by hand", "--type", "decision", "--body",
                   "LTEST: a standing rule stored as something that does not bind, to prove the write path says so.")
check("remember warns when a standing rule is stored as a type the brief does not carry",
      rc == 0 and "standing rule" in (out + err) and "--type constraint" in (out + err), out + err)
rc, out, err = run("review")
check("review hygiene report", rc == 0, out[:300] + err)
check("review reports ABSENCE, not only excess: the rule stored as a decision is named",
      "RULES THAT DO NOT BIND" in out and "--type constraint" in out, out[:900] + err)
rc, out, err = run("review", "--duplicates")
check("review --duplicates", rc == 0, out[:300] + err)
rc, d, err = js("stats")
check("stats counts this project only", rc == 0 and d and 0 < d.get("memories", 0) < 30, str(d) + err)
rc, out, err = run("doctor")
check("doctor runs clean", rc == 0, out[:400] + err)
rc, out, err = run("doctor", "--fix")
check("doctor --fix", rc == 0, out[:400] + err)
rc, out, err = run("reindex")
check("reindex rebuilds FTS", rc == 0, out + err)
rc, d, err = js("recall", "cobalt", "--no-touch")
check("recall still hits after reindex", rc == 0 and d and d.get("matched"), str(d)[:200])
rc, out, err = run("vacuum")
check("vacuum checkpoints and compacts", rc == 0, out + err)
snap = os.path.join(BRAIN, "snapshot.md")
rc, out, err = run("snapshot", "--out", snap)
check("snapshot refuses an un-ignored path inside a git repo (cleartext of every memory)", rc != 0 and not os.path.exists(snap) and "gitignore" in (out + err), out + err)
snap = os.path.join(SCRATCH, "snapshot.md")
rc, out, err = run("snapshot", "--out", snap, "--allow-outside")
check("snapshot writes markdown", rc == 0 and os.path.exists(snap) and "cobalt" in open(snap).read(), out + err)
tracked = os.path.join(PROJ, "README.md")
rc, out, err = run("snapshot", "--out", tracked)
check("snapshot refuses a git-tracked path", rc != 0 and open(tracked).read().startswith("# throwaway"), out + err)
xp = os.path.join(SCRATCH, "x.json")
rc, out, err = run("export", "--out", xp)
check("export --out outside the project is refused without --allow-outside", rc != 0, out + err)
rc, out, err = run("export", "--out", xp, "--allow-outside")
check("export --out writes the dump, mode 600", rc == 0 and os.path.exists(xp) and oct(os.stat(xp).st_mode)[-3:] == "600" and "memories" in json.load(open(xp)), out + err)
open(os.path.join(SCRATCH, "notes.json"), "w").write('{"mine": true}')
rc, out, err = run("export", "--out", os.path.join(SCRATCH, "notes.json"), "--allow-outside")
check("export --out refuses to overwrite a file it did not write", rc != 0 and json.load(open(os.path.join(SCRATCH, "notes.json"))).get("mine"), out + err)
rc, out, err = run("export")
try:
    doc = json.loads(out)
except Exception:
    doc = {}
check("export to stdout carries memories, tasks and events", rc == 0 and doc.get("memories") and isinstance(doc.get("tasks"), list) and len(doc["tasks"]) == 2 and "events" in doc, (out + err)[:200])
check("export carries type, superseded_by and pinned on every row", all(("type" in m or "mem_type" in m) and "superseded_by" in m and "pinned" in m for m in doc.get("memories", [])), str(doc.get("memories", [{}])[0])[:300])
bp = os.path.join(SCRATCH, "backup.json")
rc, out, err = run("backup", "--out", bp, "--allow-outside")
check("backup writes a full dump, every table", rc == 0 and os.path.exists(bp) and "measures" in json.load(open(bp)).get("counts", json.load(open(bp))), out + err)
rc, out, err = run("backup", "--auto")
check("backup --auto after wakeup's copy: silent unless the store moved since", rc == 0 and (out.strip() == "" or "safety copy:" in out), out + err)
rc, out, err = run("backup", "--auto")
check("a second --auto with nothing written in between is silent", rc == 0 and out.strip() == "", out + err)
rc, d, err = js("backup", "--list")
check("backup --list shows the copies, verified, under the decoy config dir", rc == 0 and d and d.get("count") >= 1 and all(c["ok"] and c["memories"] > 0 for c in d["copies"]) and d["dir"].startswith(CFG), str(d)[:300] + err)
COPY = d["copies"][0]["path"] if d and d.get("copies") else ""
rc, out, err = run("backup", "--verify")
check("backup --verify opens every copy", rc == 0 and "ok" in out, out + err)
rc, out, err = run("backup", "--restore", COPY)
check("backup --restore is a dry run", rc == 0 and ("DRY" in out.upper() or "would" in out) , out + err)

# ------------------------------------------------------------------ G. import / prune / promote
section("G. import, prune, promote")
rc, d2, _ = js("get", str(ID5))
dump = {"generated_by": "sweep", "schema_version": 1, "memories": [
    {"id": 1, "title": "LTEST imported: a fact from a dump", "body": "imported by the sweep", "type": "gotcha",
     "scope": "global", "created_at": 1700000000, "updated_at": 1700000000, "pinned": False, "superseded_by": None},
    {"id": 2, "title": d2["title"], "body": "x", "type": "knowledge", "scope": "global"}]}
dp = os.path.join(SCRATCH, "dump.json"); json.dump(dump, open(dp, "w"))
rc, out, err = run("import", dp, "--dry-run")
check("import --dry-run reports without writing", rc == 0 and "1" in out and ("dry" in out.lower() or "would" in out.lower()), out + err)
rc, out, err = run("import", dp)
check("import writes the one new row and skips the identity already there", rc == 0 and "1" in out, out + err)
rc, d, err = js("recall", "imported dump", "--no-touch")
check("the imported row kept its 2023 created_at", d and d.get("results") and "1700000000" in json.dumps(d), str(d)[:300])
bad = dict(dump); bad["memories"] = [dict(dump["memories"][0], title="LTEST bad type", type="nosuch")]
json.dump(bad, open(dp, "w"))
rc, out, err = run("import", dp, "--dry-run")
check("import refuses an unknown type", rc != 0 and "unknown type" in (out + err), out + err)
rc, out, err = run("prune", "--superseded", "--older-than", "0")
check("prune refuses a same-day cutoff", rc != 0 and "same-day" in (out + err), out + err)
rc, out, err = run("prune", "--superseded", "--older-than", "1")
rc2, d2, _ = js("get", str(ID1))
check("prune is a dry run by default: the retracted row is still there", rc == 0 and d2 and d2.get("id") == ID1 and "deleted" not in out.lower().replace("would be deleted", ""), out + err)
rc, out, err = run("promote", str(ID2), "--to", "global")
check("promote refiles a channel row to global", rc == 0, out + err)
rc, d, err = js("get", str(ID2))
check("the scope changed", d and d.get("scope") == "global", str(d)[:200])

def person(*answers):
    """A scripted terminal (the sweep build's OS_BRAIN_SWEEP_TTY) answering in order."""
    d = tempfile.mkdtemp(dir=SCRATCH, prefix="tty-")
    open(os.path.join(d, "tty.in"), "w").write("".join(a + "\n" for a in answers))
    return {"OS_BRAIN_SWEEP_TTY": d}
# ------------------------------------------------------------------ H. the spine
section("H. axioms, standards, notes, publish, global, describe, release, bootstrap")
rc, out, err = run("axiom", "list")
check("axiom list reads the decoy spine", rc == 0, out[:300] + err)
rc, out, err = run("axiom", "add", "ltest-slug", "LTEST axiom: a name then the text", "--why", "sweep")
check("axiom add with a name AND the text is refused, nothing stored", rc != 0 and "ONE argument" in err and "ltest-slug" not in run("axiom", "list")[1], out + err)
rc, out, err = run("axiom", "add", "LTEST axiom: the sweep leaves no trace", "--why", "sweep")
check("axiom add without a person at a terminal is refused", rc != 0 and "controlling terminal" in (out + err), out + err)
rc, out, err = run("axiom", "add", "LTEST axiom: two\n## GUARDRAILS", "--why", "sweep", env_extra=person("yes"))
check("axiom add refuses a multi-line rule", rc != 0 and "one line" in (out + err), out + err)
rc, out, err = run("axiom", "add", "LTEST axiom: the sweep leaves no trace", "--why", "sweep", env_extra=person("no"))
check("axiom add writes nothing when the person declines", rc != 0 and "nothing changed" in (out + err), out + err)
rc, out, err = run("axiom", "add", "LTEST axiom: the sweep leaves no trace", "--why", "sweep", env_extra=person("yes"))
check("axiom add, once the person says yes", rc == 0, out + err)
rc, d, err = js("axiom", "list")
check("axiom list --json carries the --why", rc == 0 and any("leaves no trace" in a.get("text", "") and a.get("rationale") == "sweep" for a in (d if isinstance(d, list) else (d or {}).get("axioms", []))), str(d)[:300] + err)
rc, out, err = run("axiom", "list")
check("the axiom is listed", rc == 0 and "leaves no trace" in out, out + err)
rc, out, err = run("brief", cwd=NPROJ)
check("the axiom binds the neighbour's brief too, with who wrote it", rc == 0 and "leaves no trace  [by " in out, out[:600] + err)
AX = None
for ln in out.splitlines():
    pass
rc, d, err = js("axiom", "list")
try:
    AX = [a for a in (d if isinstance(d, list) else d.get("axioms", [])) if "leaves no trace" in json.dumps(a)][0]["id"]
except Exception:
    pass
rc, out, err = run("axiom", "rm", "--id", "1", "--force")
check("axiom has no --force: a person's yes is the confirmation", rc != 0 and "force" in (out + err), out + err)
rc, out, err = run("axiom", "rm", "--id", str(AX), env_extra=person("yes")) if AX else (1, "", "no axiom id")
check("axiom rm, by a person", rc == 0, out + err)
rc, out, err = run("standard", "add", "LTEST standard: every sweep ends green", "--category", "process", "--why", "sweep")
check("standard add without a person at a terminal is refused", rc != 0 and "controlling terminal" in (out + err), out + err)
rc, out, err = run("standard", "add", "LTEST standard: every sweep ends green", "--category", "process", "--why", "sweep", env_extra=person("yes"))
check("standard add, by a person", rc == 0, out + err)
rc, out, err = run("standard", "list")
check("standard list", rc == 0 and "ends green" in out, out + err)
rc, d, err = js("standard", "list")
SD = None
try:
    SD = [a for a in (d if isinstance(d, list) else d.get("standards", [])) if "ends green" in json.dumps(a)][0]["id"]
except Exception:
    pass
rc, out, err = run("standard", "rm", "--id", str(SD), env_extra=person("yes")) if SD else (1, "", "no standard id")
check("standard rm, by a person", rc == 0, out + err)
# A message has a recipient that exists, or it is not sent. The local edition used to park
# a note for any string and report it "delivered"; one is still in a live spine addressed to
# a whole sentence.
for bad in ("no-such-project-xyz", "a sentence about LLM routing, not a project"):
    rcb, outb, errb = run("note", bad, "--", "LTEST this must reach nobody")
    check("a note to a recipient that does not exist is refused, never reported as sent: " + bad[:24], rcb != 0 and "delivered" not in outb and "sent #" not in outb, outb + errb)
# Flags go before "--": every word after it is text, --json included (audit B-11).
rcb, outb, errb = run("note", "--json", "--", NEIGH.upper(), "LTEST note addressed in the wrong case")
try:
    db = json.loads(outb)
except Exception:
    db, errb = None, outb + errb
check("a recipient that differs only by case reaches the real project", rcb == 0 and db and db.get("id"), str(db) + errb)
if db and db.get("id"):
    run("note", "--ack", str(db["id"]), cwd=NPROJ)  # so the inbox checks below start from empty
rc, out, err = run("note", NEIGH, "--", "LTEST note from the sweep to the neighbour")
check("note to another project is accepted", rc == 0, out + err)
rc, out, err = run("brief", cwd=NPROJ)
check("the note lands in the neighbour's brief", rc == 0 and "to the neighbour" in out, out[:800] + err)
rc, d, err = js("note", "--inbox", cwd=NPROJ)
NID = 0
try:
    NID = [n for n in (d.get("notes") if isinstance(d, dict) else d) if "to the neighbour" in n.get("body", "")][0]["id"]
except Exception:
    pass
check("note --inbox lists it with an id", rc == 0 and NID, str(d)[:300] + err)
rc, out, err = run("note", "--read", str(NID), cwd=NPROJ)
check("note --read shows the body", rc == 0 and "to the neighbour" in out, out + err)
rc, out, err = run("note", "--ack", str(NID), cwd=NPROJ)
check("note --ack clears it", rc == 0, out + err)
rc, out, err = run("note", "--inbox", cwd=NPROJ)
check("the acked note is gone from the inbox", "to the neighbour" not in out, out + err)
rc, d, err = js("note", "--inbox", cwd=NPROJ)
check("an empty inbox is [] in JSON, never null", rc == 0 and d is not None and not isinstance(d, dict) or (isinstance(d, dict) and d.get("notes") == []), str(d) + err)
check("an empty inbox says what a note is not", "recall --browse" in out, out)

# ------------------------------------------------------------------ direct messages
# A DM goes to one RUNNING session. Sessions are simulated the way Claude Code identifies
# them to a hook: CLAUDE_CODE_SESSION_ID and CLAUDE_PID in the environment.
SB, SC = "bbbbbbbb-5eed-0000-0000-000000000002", "cccccccc-5eed-0000-0000-000000000003"
def as_session(sid, pid):
    return {"CLAUDE_CODE_SESSION_ID": sid, "CLAUDE_PID": str(pid), "OS_BRAIN_SESSION": ""}
rc, out, err = run("hook-recall", stdin='{"prompt":"a prompt long enough to count"}', cwd=NPROJ, env_extra=as_session(SB, 4101))
rc, d, err = js("sessions")
check("a session that ran a hook is listed as running", rc == 0 and d and any(x["session"] == SB and x["project"] == NEIGH for x in d["sessions"]), str(d)[:300] + err)
rc, out, err = run("dm", NEIGH, "LTEST dm: stop touching the README")
check("dm by project name reaches its one running session and says when it is read", rc == 0 and "sent #" in out and "next prompt" in out, out + err)
rc, out, err = run("dm", NEIGH + "-typo", "LTEST dm to nobody")
check("dm to a session nobody runs is refused and nothing is sent", rc != 0 and "sent #" not in out and "no running session" in err, out + err)
rc, out, err = run("hook-tool", stdin="{}", cwd=NPROJ, env_extra=as_session(SB, 4101))
check("the PostToolUse hook hands the session its message in the envelope Claude Code reads", rc == 0 and '"hookEventName":"PostToolUse"' in out and "stop touching the README" in out, out + err)
rc, d, err = js("dm", "--sent", "-n", "1")
check("dm --sent shows it delivered, and how", rc == 0 and d and d["sent"] and d["sent"][0]["delivered_via"] == "posttooluse", str(d)[:300] + err)
run("dm", NEIGH, "LTEST dm: second message, for the stop hook")
rc, out, err = run("hook-stop", cwd=NPROJ, env_extra=as_session(SB, 4101))
check("the Stop hook blocks the stop with the message as the reason", rc == 0 and '"decision":"block"' in out and "second message" in out, out + err)
rc, out, err = run("hook-stop", cwd=NPROJ, env_extra=as_session(SB, 4101))
check("the next stop is not blocked again: a message is delivered once", rc == 0 and '"decision"' not in out, out + err)
rc, out, err = run("dm", NEIGH, "--rdo", "LTEST rdo: delete the staging database")
rc, out, err = run("hook-stop", cwd=NPROJ, env_extra=as_session(SB, 4101))
check("a remote-do arrives as a request for the user's approval, never with their authority",
      '"decision":"block"' in out and "REMOTE-DO REQUEST" in out and "approve it in this session" in out
      and "carry it out" not in out and "authority, as if" not in out, out + err)
rc, out, err = run("dm", "--accept", "1", cwd=NPROJ, env_extra=dict(as_session(SB, 4101), **person("yes")))
check("dm --accept from inside a session is refused even with a terminal (B-9)", rc != 0 and "agent" in err, out + err)
rc, out, err = run("dm", "--accept", "1")
check("dm --accept with no terminal is refused before it looks (B-9)", rc != 0 and "controlling terminal" in err, out + err)
rc, out, err = run("dm", "--accept", "1", env_extra=person("yes"))
check("dm --accept a person confirms reaches the store, which holds nothing to accept locally", rc != 0 and "nothing to accept" in err, out + err)
_mark = os.path.join(SCRATCH, "FSMONITOR_RAN")
subprocess.run(["git", "-C", PROJ, "config", "core.fsmonitor", "touch " + _mark + "; false"], check=True)
rc, out, err = run("wakeup")
subprocess.run(["git", "-C", PROJ, "config", "--unset", "core.fsmonitor"], check=True)
check("session open runs no program the repository's own git config names (core.fsmonitor)",
      rc == 0 and "WORKING TREE" in out and not os.path.exists(_mark), out[-300:] + err)
rc, out, err = run("hook-end", stdin="{}", cwd=NPROJ, env_extra=as_session(SB, 4101))
rc, out, err = run("dm", NEIGH, "LTEST dm after the session ended")
check("an ended session is not a recipient", rc != 0 and "sent #" not in out, out + err)
rc, out, err = run("channel", stdin="", env_extra={"CLAUDE_CODE_SESSION_ID": "", "OS_BRAIN_SESSION_ID": ""})
check("the channel refuses outside a Claude Code session", rc != 0 and "CLAUDE_CODE_SESSION_ID" in err, out + err)
chan_env = dict(ENV, **as_session(SC, 0))
chan = subprocess.Popen([NSB, "channel"], cwd=NPROJ, env=chan_env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
chan.stdin.write(json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}}) + "\n"); chan.stdin.flush()
init = json.loads(chan.stdout.readline())
check("the channel declares Claude Code's channel capability", "claude/channel" in json.dumps(init.get("result", {}).get("capabilities", {})), json.dumps(init)[:300])
chan.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n"); chan.stdin.flush()
time.sleep(1.5)
rc, d, err = js("sessions")
st = [x["state"] for x in (d or {}).get("sessions", []) if x["session"] == SC]
check("a session with a live channel is listed as listening", st == ["listening"], str(d)[:300] + err)
rc, out, err = run("dm", NEIGH, "LTEST dm: wake up")
check("the sender is told a listening session reads it now", rc == 0 and "read now" in out, out + err)
import select as _select
ready, _, _ = _select.select([chan.stdout], [], [], 10)
ev = json.loads(chan.stdout.readline()) if ready else {}
check("the channel pushes the message as a notifications/claude/channel event", ev.get("method") == "notifications/claude/channel" and "wake up" in ev.get("params", {}).get("content", ""), json.dumps(ev)[:300])
chan.kill(); chan.wait()

rc, d, err = js("publish", str(ID5), "--why", "sweep test")
LES = d.get("id") if d else 0
check("publish shares a lesson and names it (id = lesson, published = memory)", rc == 0 and LES and d.get("published") == ID5, str(d) + err)
rc, out, err = run("global", "--recall", "cobalt", cwd=NPROJ)
check("the lesson is recallable from the neighbour", rc == 0 and "cobalt" in out, out + err)
rc, out, err = run("publish", str(LES), "--retract")
check("publish --retract <lesson id> takes it back", rc == 0, out + err)
rc, d, err = js("global", "--recall", "cobalt", cwd=NPROJ)
check("the retracted lesson is gone from global", rc == 0 and d is not None and not (d.get("lessons") or d.get("results")), str(d)[:300] + err)
rc, out, err = run("global")
check("global renders axioms, the index and lessons", rc == 0 and LTEST in out, out[:400] + err)
rc, out, err = run("global", "--find", "neighbour")
check("global --find finds the neighbour by name", rc == 0 and NEIGH in out, out[:300] + err)
rc, out, err = run("describe", "a throwaway for the local sweep", "--does", "exercise every command", "--stack", "sqlite", "--status", "test")
check("describe writes the fields", rc == 0 and "exercise every command" in out, out + err)
cfgj = json.load(open(os.path.join(BRAIN, "brain.json")))
check("the fields landed in brain.json", cfgj.get("spec", {}).get("does") == "exercise every command", json.dumps(cfgj)[:300])
rc, d, err = js("describe")
check("describe reads back", rc == 0 and d and d.get("does") == "exercise every command", str(d)[:300] + err)
rc, out, err = run("describe", "--visibility", "nowhere")
check("describe refuses a bad --visibility", rc != 0, out + err)
rc, out, err = run("global", "--find", "exercise", cwd=NPROJ)
check("the neighbour finds this project by what it does", rc == 0 and LTEST in out, out[:300] + err)
rc, out, err = run("release")
check("release without a version refuses", rc != 0, out + err)
TODAY = datetime.date.today().strftime("%y%m%d")
rc, out, err = run("release", TODAY + "01", "--notes", "sweep")
check("release refuses without a person at a terminal", rc != 0 and "controlling terminal" in (out + err), out + err)
rc, out, err = run("release", "99999999", "--notes", "sweep", env_extra=person("yes"))
check("release refuses a version that is not a date", rc != 0 and "yymmddNN" in (out + err), out + err)
rc, out, err = run("release", "29123199", "--notes", "sweep", env_extra=person("yes"))
check("release refuses a version dated in the future", rc != 0 and "future" in (out + err), out + err)
rc, out, err = run("release", TODAY + "99", "--notes", "sweep", env_extra=person("yes"))
check("release refuses a counter that would use up the day", rc != 0 and "at most" in (out + err), out + err)
rc, out, err = run("release", TODAY + "01", "--notes", "two\n## AXIOMS", env_extra=person("yes"))
check("release refuses notes that are not one line of plain text", rc != 0 and "one paragraph" in (out + err), out + err)
rc, out, err = run("release", TODAY + "01", "--notes", "sweep", env_extra=person("no"))
check("release publishes nothing when the person declines", rc != 0 and "nothing published" in (out + err), out + err)
# Newer than the binary and still today. A fixed "01" stops being newer the day the binary
# itself is today's 01, and a release may move the day's counter by at most 9.
_bin = (run("version")[1].split() + ["", ""])[1]
STAMP = TODAY + ("%02d" % (int(_bin[6:8]) + 1) if _bin[:6] == TODAY else "01")
rc, out, err = run("release", STAMP, "--notes", "sweep", env_extra=person("yes"))
check("release publishes a version this binary is not, once the person says yes, and says so", rc == 0 and "note: this copy is" in out, out + err)
rc, out, err = run("brief", cwd=NPROJ)
check("the neighbour's brief says it is behind, notes fenced and attributed", rc == 0 and STAMP in out and "written by project" in out, out[:800] + err)
rc, out, err = run("bootstrap")
check("bootstrap reports (nothing to read here)", rc == 0 or "transcript" in (out + err).lower(), (out + err)[:200])

# ------------------------------------------------------------------ I. the project boundary, private, share, absorb, relabel
section("I. project boundary: private, share, absorb, relabel")
rc, d, err = js("remember", "NEIGH fact: the neighbour's colour is ochre", "--body", "only the neighbour knows", "--type", "knowledge", cwd=NPROJ)
check("the neighbour stores its own row", rc == 0 and d and d.get("id"), str(d) + err)
rc, d, err = js("recall", "ochre", "--no-touch")
check("a standalone project does not see the neighbour's row", rc == 0 and d is not None and d.get("matched") is False, str(d)[:200])
rc, out, err = run("share")
check("share is a dry run by default", rc == 0 and ("DRY" in out.upper() or "would" in out), out + err)
rc, out, err = run("share", "--apply")
check("share --apply moves this project into the shared store", rc == 0 and json.load(open(os.path.join(BRAIN, "brain.json"))).get("shared") is True, out + err)
rc, d, err = js("recall", "cobalt", "--no-touch")
check("recall answers from the shared store after the move", rc == 0 and d and d.get("matched"), str(d)[:200])
rc, out, err = run("share", "--apply", cwd=NPROJ)
check("the neighbour moves in too", rc == 0, out + err)
rc, d, err = js("recall", "ochre", "--no-touch")
check("one shared file, still no cross-project read", rc == 0 and d is not None and d.get("matched") is False, str(d)[:200])
rc, d, err = js("recall", "ochre", "--no-touch", "--all-projects")
check("--all-projects reads the neighbour deliberately, marked as theirs", rc == 0 and d and d.get("matched") and any(r.get("project") == NEIGH for r in d["results"]), str(d)[:300])
rc, out, err = run("private", "--yes", cwd=NPROJ)
check("private marks the neighbour", rc == 0, out + err)
rc, d, err = js("recall", "ochre", "--no-touch", "--all-projects")
check("a private project is invisible even to --all-projects", rc == 0 and d is not None and not any(r.get("project") == NEIGH for r in (d.get("results") or [])), str(d)[:300])
rc, out, err = run("private", "--off", "--yes", cwd=NPROJ)
check("private --off", rc == 0, out + err)
rc, out, err = run("share", "--off", "--apply", cwd=NPROJ)
check("share --off brings the neighbour back to its own file", rc == 0 and json.load(open(os.path.join(NBRAIN, "brain.json"))).get("shared") is not True, out + err)
rc, d, err = js("recall", "ochre", "--no-touch", cwd=NPROJ)
check("the neighbour still answers from its own file", rc == 0 and d and d.get("matched"), str(d)[:200])
rc, out, err = run("share", "--off", "--apply")
check("share --off brings this project back too", rc == 0, out + err)
rc, d, err = js("recall", "cobalt", "--no-touch")
check("and it still answers", rc == 0 and d and d.get("matched"), str(d)[:200])
rc, out, err = run("absorb", os.path.join(NBRAIN, "brain.db"), "--dry-run")
check("absorb refuses another project's label unless named twice", rc != 0 and "--i-mean-it" in (out + err), out + err)
rc, out, err = run("absorb", os.path.join(NBRAIN, "brain.db"), "--project", NEIGH, "--i-mean-it", "--dry-run")
check("absorb is a dry run when asked", rc == 0 and "would absorb" in out, out + err)
rc, out, err = run("absorb", os.path.join(NBRAIN, "brain.db"), "--project", NEIGH, "--i-mean-it")
check("absorb pulls a standalone file in under its own label", rc == 0, out + err)
rc, out, err = run("relabel", NEIGH)
check("relabel refuses a label that is a live registered project", rc != 0 and "live project" in (out + err), out + err)
rc, out, err = run("absorb", os.path.join(NBRAIN, "brain.db"), "--project", "old-name-of-this-project", "--force-label", "--i-mean-it")
check("absorb again under an old label", rc == 0, out + err)
rc, out, err = run("relabel", "old-name-of-this-project")
check("relabel is a dry run by default", rc == 0 and ("DRY" in out.upper() or "would" in out), out + err)
rc, out, err = run("relabel", "old-name-of-this-project", "--apply", "--expect", "1", "--i-mean-it")
check("relabel --apply claims the absorbed row as this project's", rc == 0, out + err)
rc, d, err = js("recall", "ochre", "--no-touch")
check("the claimed row now answers here", rc == 0 and d and d.get("matched"), str(d)[:200])

# ------------------------------------------------------------------ J. transports
section("J. ui and mcp")
def free_port():
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); return p
port = free_port()
ui = subprocess.Popen([NSB, "ui", "--no-open", "--port", str(port)], cwd=PROJ, env=ENV, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
# The banner's first line carries the link. The link carries a single-use nonce, never the
# key: the first visit spends it for an HttpOnly cookie, and the page that cookie opens
# carries the key the reads need, so another local user on loopback reads nothing.
banner = ui.stdout.readline()
link = next((w for w in banner.split() if w.startswith("http://")), "")
nonce = link.split("?k=", 1)[1] if "?k=" in link else ""
def ui_get(path, token=None, cookie=None):
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    h = {}
    if token:
        h["X-Brain-Token"] = token
    if cookie:
        h["Cookie"] = cookie
    c.request("GET", path, headers=h)
    r = c.getresponse()
    return r.status, r.read().decode(), r.getheader("Set-Cookie") or "", r.getheader("Location") or ""
bcode, cookie, loc = 0, "", ""
for _ in range(40):
    time.sleep(0.25)
    try:
        bcode, _, cookie, loc = ui_get("/?k=" + nonce); break
    except Exception:
        continue
cookie = cookie.split(";", 1)[0]
check("ui's link is spent for an HttpOnly SameSite=Strict cookie", bcode == 303 and loc == "/" and cookie.startswith("brain_ui_"), f"{bcode} {loc} {cookie} {banner}")
code, page, _, _ = ui_get("/", cookie=cookie)
m = re.search(r'name="brain-token" content="([0-9a-f]+)"', page)
key = m.group(1) if m else ""
check("ui serves the page from the binary", code == 200 and "brain" in page.lower() and key and key != nonce and key not in link, f"{code} {banner} {page[:200]}")
scode, _, _, _ = ui_get("/?k=" + nonce)
check("ui refuses a link that was already used", scode == 403, str(scode))
bcode, _, _, _ = ui_get("/")
check("ui refuses the page without the cookie", bcode == 403, str(bcode))
rcode, _, _, _ = ui_get("/api/brief")
check("ui refuses a read without the per-process token", rcode == 403, str(rcode))
mcode, meta, _, _ = ui_get("/api/meta", key)
check("ui /api/meta answers with the token and never returns it", mcode == 200 and key not in meta, f"{mcode} {meta[:200]}")
qcode, qbody, _, _ = ui_get("/api/memories?q=--help", key)
check("ui searches for a query that looks like a flag rather than obeying it", qcode == 200, f"{qcode} {qbody[:200]}")
try:
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=5); c.request("POST", "/api/forget", body=json.dumps({"id": ID5}), headers={"Content-Type": "application/json", "Origin": "https://evil.example"}); r = c.getresponse(); fcode = r.status
except Exception as e:
    fcode = 0
check("ui refuses a cross-origin forget without the per-process token", fcode in (400, 401, 403, 404, 405), str(fcode))
rc, d, err = js("get", str(ID5))
check("the memory survived the refused forget", d and d.get("id") == ID5, str(d)[:200])
ui.kill()

mcp = subprocess.Popen([NSB, "mcp"], cwd=PROJ, env=ENV, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
def rpc(obj):
    mcp.stdin.write(json.dumps(obj) + "\n"); mcp.stdin.flush()
    line = mcp.stdout.readline()
    return json.loads(line) if line.strip() else None
init = rpc({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "sweep", "version": "0"}}})
check("mcp initialize over stdio", init and init.get("result", {}).get("serverInfo", {}).get("name") == "os-brain", str(init)[:200])
tools = rpc({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
names = [t["name"] for t in (tools or {}).get("result", {}).get("tools", [])]
check("mcp lists the command surface as tools", "brain_recall" in names and "brain_remember" in names and len(names) > 40, str(names)[:300])
res = rpc({"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "brain_recall", "arguments": {"args": "cobalt", "no-touch": True}}})
text = json.dumps(res)
check("mcp fails closed when the store holds two projects and none is named", "pass _project" in text, text[:300])
res = rpc({"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "brain_recall", "arguments": {"args": "cobalt", "no-touch": True, "_project": LTEST}}})
text = json.dumps(res)
check("mcp brain_recall answers for the named project", "cobalt" in text, text[:300])
mcp.kill()

# ------------------------------------------------------------------ K. upgrade and uninstall
section("K. upgrade and uninstall")
skip("upgrade is a dry run by default", "upgrade downloads from our release server; os-brain never calls home")
rc, out, err = run("uninstall", cwd=NPROJ)
check("uninstall is a dry run by default", rc == 0 and os.path.exists(os.path.join(NBRAIN, "brain.db")) and ("DRY" in out.upper() or "would" in out), out + err)
rc, out, err = run("uninstall", "--apply", cwd=NPROJ)
st = json.load(open(os.path.join(NPROJ, ".claude", "settings.json"))) if os.path.exists(os.path.join(NPROJ, ".claude", "settings.json")) else {}
check("uninstall --apply removes the hooks and brain.json and keeps the database", rc == 0 and os.path.exists(os.path.join(NBRAIN, "brain.db")) and not os.path.exists(os.path.join(NBRAIN, "brain.json")) and not any("os-brain" in json.dumps(v) for v in st.get("hooks", {}).values()), out + err + json.dumps(st)[:200])
rc, out, err = run("init", "--project", NEIGH, "--dir", "brain", cwd=NPROJ)
rc2, d2, _ = js("recall", "ochre", "--no-touch", cwd=NPROJ)
check("init over a kept database migrates in place and keeps its memories", rc == 0 and d2 and d2.get("matched"), out + err + str(d2)[:200])
rc, out, err = run("uninstall", "--apply", "--purge", cwd=NPROJ)
check("uninstall --purge removes the database too, after a backup", rc == 0 and not os.path.exists(os.path.join(NBRAIN, "brain.db")) and "backup" in (out + err).lower(), out + err)

# ------------------------------------------------------------------ L. the guard
section("L. nothing real was touched")
skip("every check in section M (zero-knowledge: init --brain, the bound directory, no plaintext copies)", "zero-knowledge brains are a team-edition feature")
skip("unchanged: the real zero-knowledge key directory", "os-brain has no zero-knowledge key directory")
after = {p: sha(p) for p in REAL}
for p in REAL:
    check(f"unchanged: {p.replace(HOME, '~')}", before[p] == after[p], "hash moved")
import sqlite3
n = sqlite3.connect(SPINE).execute("select count(*) from projects where name not in (?,?,?)", (LTEST, NEIGH, THIN_NAME)).fetchone()[0]
check("the decoy spine indexes only the sweep's three projects", n == 0, f"{n} strangers")
for _real in (".claude/os-brain/brain.db", ".claude/brain/brain.db"):
    _p = os.path.join(HOME, _real)
    if not os.path.exists(_p):
        continue
    n = sqlite3.connect("file:" + _p + "?mode=ro", uri=True).execute("select count(*) from projects where name in (?,?)", (LTEST, NEIGH)).fetchone()[0]
    check(f"the real spine (~/{_real}) never heard of the sweep's projects", n == 0, f"{n} rows")

n = len(results); ok = sum(1 for r in results if r[1])
print(f"\n=== SUMMARY {ok}/{n} passed, {len(SKIPPED)} skipped")
for name, why in SKIPPED:
    print("  skipped:", name, "--", why)
for name, good, detail in results:
    if not good:
        print("  FAILED:", name)
