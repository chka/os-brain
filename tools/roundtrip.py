#!/usr/bin/env python3
"""Round trip between os-brain and ns-brain: a brain written by one opens in the other with
its memories, types, scopes, history and supersede links intact, and an export from one
imports into the other.

    python3 tools/roundtrip.py [scratch dir]
    NSB=/path/to/ns-brain        an installed ns-brain (default: the one on PATH)
    NSB_SRC=/path/to/source      or build ns-brain from a source checkout instead

Why this exists: os-brain keeps ns-brain's SQLite format and schema version on purpose, so a
person can move from the free local edition to ns-brain or its team edition without losing
anything, and back. That promise is only as good as a check that runs both binaries
against the same files.

Both binaries run against a scratch directory only: their shared stores, config dirs and
credentials are pinned there, ns-brain's update check is switched off, and the real stores
of both editions are hashed before and after.
"""
import hashlib, json, os, shutil, sqlite3, subprocess, sys, tempfile

HOME = os.path.expanduser("~")
REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRATCH = sys.argv[1] if len(sys.argv) > 1 else os.path.join(tempfile.gettempdir(), "os-brain-roundtrip")
NSB_SRC = os.environ.get("NSB_SRC")
CFG = os.path.join(SCRATCH, "cfg")

REAL = [os.path.join(HOME, ".claude", "os-brain", "brain.db"),
        os.path.join(HOME, ".claude", "brain", "brain.db")]

results = []


def check(name, ok, detail=""):
    results.append((name, ok))
    print(("  PASS  " if ok else "  FAIL  ") + name + ("" if ok else "\n          " + str(detail).strip()[:600]))


def sha(path):
    h = hashlib.sha256()
    for f in (path, path + "-wal"):
        if os.path.exists(f):
            h.update(open(f, "rb").read())
    return h.hexdigest()


def build(src, out):
    r = subprocess.run(["go", "build", "-o", out, "."], cwd=src, capture_output=True, text=True)
    if r.returncode != 0:
        sys.exit(f"building {src} failed:\n{r.stdout}{r.stderr}")
    return out


shutil.rmtree(SCRATCH, ignore_errors=True)
os.makedirs(CFG)
OSB = build(REPO, os.path.join(SCRATCH, "bin", "os-brain"))
if NSB_SRC:
    NSB = build(NSB_SRC, os.path.join(SCRATCH, "bin", "ns-brain"))
else:
    NSB = os.environ.get("NSB") or shutil.which("ns-brain")
    if not NSB:
        sys.exit("needs ns-brain: install it, or set NSB to its path or NSB_SRC to a source checkout")

BASE = {k: v for k, v in os.environ.items()
        if k not in ("CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CLAUDE_PID", "BRAIN_HOME", "BRAIN_PROJECT",
                     "BRAIN_DB", "BRAIN_SESSION_ID", "OS_BRAIN_HOME", "OS_BRAIN_PROJECT", "OS_BRAIN_DB",
                     "OS_BRAIN_SESSION_ID")}
BASE["CLAUDE_CONFIG_DIR"] = CFG
ENV = {
    # Each edition has its own shared store; both live in the scratch dir.
    OSB: dict(BASE, OS_BRAIN_GLOBAL_DB=os.path.join(CFG, "os-spine.db")),
    NSB: dict(BASE, BRAIN_GLOBAL_DB=os.path.join(CFG, "ns-spine.db"),
              NS_BRAIN_CREDENTIALS=os.path.join(SCRATCH, "no-such-credentials.json"),
              NS_BRAIN_NO_UPDATE_CHECK="1",
              NS_BRAIN_QUEUE_DIR=os.path.join(SCRATCH, "queue"),
              NS_BRAIN_RELEASE_TRUST=os.path.join(SCRATCH, "release-trust.json")),
}
NAME = {OSB: "os-brain", NSB: "ns-brain"}


def run(binary, cwd, *args, stdin=None):
    r = subprocess.run([binary, *args], cwd=cwd, env=ENV[binary], capture_output=True, text=True,
                       input=stdin, timeout=120)
    return r.returncode, r.stdout, r.stderr


def js(binary, cwd, *args):
    rc, out, err = run(binary, cwd, *args, "--json")
    try:
        return json.loads(out)
    except ValueError:
        raise SystemExit(f"{NAME[binary]} {' '.join(args)} --json did not answer JSON (rc {rc}):\n{out}{err}")


def project(name):
    d = os.path.join(SCRATCH, name)
    os.makedirs(d)
    subprocess.run(["git", "init", "-q", d], check=True)
    return d


def populate(binary, cwd):
    """Writes every kind of row a round trip has to carry and returns the memory ids."""
    ids = {}

    def remember(key, title, *extra):
        out = js(binary, cwd, "remember", title, *extra)
        ids[key] = out["id"]

    remember("decision", "nginx terminates TLS on the edge box", "--type", "decision", "--scope", "infra",
             "--body", "the origin speaks plain HTTP only on loopback")
    remember("correction", "TLS now terminates on the origin as well as the edge", "--type", "decision",
             "--scope", "infra", "--body", "mTLS between edge and origin",
             "--supersedes", str(ids["decision"]))
    remember("gotcha", "the migration tool silently skips views", "--type", "gotcha", "--scope", "db",
             "--body", "recreate views by hand after a migrate", "--importance", "5", "--pin")
    remember("preference", "answers first then the reasoning", "--type", "preference",
             "--body", "the user reads the first line and skips the rest when it is long")
    remember("knowledge", "the staging database restores nightly from production", "--type", "knowledge",
             "--scope", "db", "--body", "anything written to staging is gone by morning",
             "--confidence", "reported")
    # A title edit archives the old version, which is the history a round trip must keep.
    rc, out, err = run(binary, cwd, "update", str(ids["knowledge"]), "--title",
                       "the staging database restores nightly from a production snapshot")
    if rc != 0:
        raise SystemExit(f"{NAME[binary]} update failed:\n{out}{err}")
    for args in (["log", "deployed the edge config", "--kind", "deploy"],
                 ["task", "add", "rotate the edge certificate", "--when", "tomorrow 9am"]):
        rc, out, err = run(binary, cwd, *args)
        if rc != 0:
            raise SystemExit(f"{NAME[binary]} {args[0]} failed:\n{out}{err}")
    return ids


def table_rows(db, table):
    con = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
    try:
        rows = con.execute(f"SELECT * FROM {table} ORDER BY 1").fetchall()
    finally:
        con.close()
    return rows


def schema_version(db):
    con = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
    try:
        row = con.execute("SELECT v FROM meta WHERE k='schema_version'").fetchone()
    finally:
        con.close()
    return row[0] if row else None


VOLATILE = {"now", "now_epoch", "use_count", "last_used"}


def same_memory(a, b):
    """Two `get --json` answers describe the same memory, ignoring use counters and the clock."""
    da = {k: v for k, v in a.items() if k not in VOLATILE}
    db = {k: v for k, v in b.items() if k not in VOLATILE}
    return da == db, {k: (da.get(k), db.get(k)) for k in set(da) | set(db) if da.get(k) != db.get(k)}


def cross_open(writer, reader, name):
    print(f"\n=== {NAME[writer]} writes, {NAME[reader]} reads ({name})")
    cwd = project(name)
    rc, out, err = run(writer, cwd, "init", "--project", name, "--agent", "claude")
    check(f"{NAME[writer]} init", rc == 0, out + err)
    ids = populate(writer, cwd)
    db = os.path.join(cwd, "brain", "brain.db")
    tables = ("memories", "mem_history", "events", "tasks", "tombstones")
    before = {t: table_rows(db, t) for t in tables}
    written = {k: js(writer, cwd, "get", str(i)) for k, i in ids.items()}

    rc, out, err = run(reader, cwd, "doctor")
    check(f"{NAME[reader]} doctor finds the {NAME[writer]} store clean", rc == 0 and "no problems found" in out,
          out + err)
    for key, i in ids.items():
        ok, diff = same_memory(written[key], js(reader, cwd, "get", str(i)))
        check(f"{NAME[reader]} reads the {key} memory exactly as written", ok, diff)
    check("the supersede link survives in both directions",
          written["decision"]["superseded_by"] == ids["correction"]
          and js(reader, cwd, "get", str(ids["decision"]))["superseded_by"] == ids["correction"], written["decision"])
    hist = js(reader, cwd, "history", "--id", str(ids["knowledge"]))
    check(f"{NAME[reader]} sees the archived title from before the edit",
          any("nightly from production" in json.dumps(v) for v in hist.get("history", [])), hist)
    rec = js(reader, cwd, "recall", "TLS", "--no-touch")
    got = [r["id"] for r in rec.get("results", [])]
    check(f"{NAME[reader]} recall ranks the live correction and hides the retracted fact",
          rec.get("matched") and ids["correction"] in got and ids["decision"] not in got, rec)
    rec_all = js(reader, cwd, "recall", "TLS", "--no-touch", "--all")
    check(f"{NAME[reader]} recall --all still finds the retracted fact",
          ids["decision"] in [r["id"] for r in rec_all.get("results", [])], rec_all)
    tasks = js(reader, cwd, "task", "list")
    check(f"{NAME[reader]} lists the task {NAME[writer]} set", "rotate the edge certificate" in json.dumps(tasks), tasks)
    after = {t: table_rows(db, t) for t in tables}
    check(f"reading through {NAME[reader]} changed no row", before == after,
          [t for t in tables if before[t] != after[t]])

    # Now the reader writes into the same file, and the writer reads that back.
    new = js(reader, cwd, "remember", "the edge certificate renews through ACME", "--type", "decision",
             "--scope", "infra", "--body", "renewal runs weekly",
             "--supersedes", str(ids["correction"]))["id"]
    back = js(writer, cwd, "get", str(new))
    check(f"{NAME[writer]} reads what {NAME[reader]} wrote into its store",
          back.get("title") == "the edge certificate renews through ACME" and back.get("scope") == "infra", back)
    check(f"{NAME[writer]} sees the supersede {NAME[reader]} made",
          js(writer, cwd, "get", str(ids["correction"]))["superseded_by"] == new)
    rc, out, err = run(writer, cwd, "doctor")
    check(f"{NAME[writer]} doctor still clean after {NAME[reader]} wrote", rc == 0 and "no problems found" in out,
          out + err)
    check("one schema version on both sides", schema_version(db) is not None
          and run(OSB, cwd, "version")[1].split("schema v")[1].split(")")[0]
          == run(NSB, cwd, "version")[1].split("schema v")[1].split(")")[0], schema_version(db))


def export_import(writer, reader):
    print(f"\n=== {NAME[writer]} export, {NAME[reader]} import")
    src = project(f"export-{NAME[writer]}")
    run(writer, src, "init", "--project", f"src-{NAME[writer]}", "--agent", "claude")
    ids = populate(writer, src)
    dump = os.path.join(src, "brain", "roundtrip-backup-export.json")  # matches the gitignore init writes
    rc, out, err = run(writer, src, "export", "--out", dump)
    check(f"{NAME[writer]} export", rc == 0 and os.path.exists(dump), out + err)
    dst = project(f"import-{NAME[reader]}")
    # The same project moving between editions, which is the case this exists for: import
    # keeps only rows of the project it runs in, by design.
    run(reader, dst, "init", "--project", f"src-{NAME[writer]}", "--agent", "claude")
    shutil.copy(dump, os.path.join(dst, "brain", "import.json"))
    rc, out, err = run(reader, dst, "import", os.path.join(dst, "brain", "import.json"))
    check(f"{NAME[reader]} imports the {NAME[writer]} export", rc == 0, out + err)
    rec = js(reader, dst, "recall", "staging database", "--no-touch")
    titles = [r.get("title") for r in rec.get("results", [])]
    check("the imported memory carries its edited title",
          "the staging database restores nightly from a production snapshot" in titles, rec)
    rec = js(reader, dst, "recall", "TLS", "--no-touch", "--all")
    by_title = {r.get("title"): r for r in rec.get("results", [])}
    old = by_title.get("nginx terminates TLS on the edge box")
    new = by_title.get("TLS now terminates on the origin as well as the edge")
    check("the imported correction still supersedes the fact it corrected",
          old is not None and new is not None and old.get("superseded_by") == new.get("id"), rec)
    got = js(reader, dst, "recall", "migration tool", "--no-touch")
    # Import drops pins on purpose in both editions: an imported row may not put itself
    # first in every brief. Importance, type and scope travel; the row is marked imported.
    check("importance, type and scope survive the import, and the pin is dropped by design",
          any(r.get("importance") == 5 and r.get("mem_type") == "gotcha" and r.get("scope") == "db"
              and not r.get("pinned") and r.get("trust") == "imported"
              for r in got.get("results", [])), got)


before = {p: sha(p) for p in REAL}
cross_open(OSB, NSB, "os-to-ns")
cross_open(NSB, OSB, "ns-to-os")
export_import(OSB, NSB)
export_import(NSB, OSB)

print("\n=== nothing real was touched")
for p in REAL:
    check(f"unchanged: {p.replace(HOME, '~')}", before[p] == sha(p), "hash moved")

ok = sum(1 for _, good in results if good)
print(f"\n=== SUMMARY {ok}/{len(results)} passed")
for name, good in results:
    if not good:
        print("  FAILED:", name)
sys.exit(0 if ok == len(results) else 1)
