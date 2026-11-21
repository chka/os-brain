package bootstrap

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// `os-brain bootstrap` seeds a new brain from the Claude Code sessions that happened
// before it existed. The reading and the judgement are the agent's: this command finds the
// transcripts, tracks which were read and states the rules, because a heuristic extractor
// would fill a fresh brain with confident noise.

type TranscriptFile struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Modified int64  `json:"modified"`
}

// SlugDirs finds this project's transcript directories. Claude Code stores transcripts as
// JSONL under <config>/projects/<absolute path with separators as dashes>/.
//
// An exact match IS this project. A match on the trailing "-<name>" only might be this
// project at an old path or on another machine, or a different project whose directory
// name ends the same way: lab/api and lab/old-api both produce a slug ending "-api". A real
// session was once handed ten transcripts of which three belonged to the other project, and
// seeding a brain from another project's sessions breaks the one-project axiom through the
// tool rather than the agent. So a suffix match is only a candidate, returned separately
// for each caller to decide. It is a suffix on "-"+name, never a substring, so "-api2" is
// not a candidate for "api".
func SlugDirs(projectRoot string) (exactDirs, candidates []string) {
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		config = store.ExpandHome("~/.claude")
	}
	root := filepath.Join(config, "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil
	}
	exact := strings.ReplaceAll(projectRoot, string(filepath.Separator), "-")
	name := strings.ToLower(filepath.Base(projectRoot))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		switch {
		case e.Name() == exact:
			exactDirs = append(exactDirs, filepath.Join(root, e.Name()))
		case name != "" && strings.HasSuffix(strings.ToLower(e.Name()), "-"+name):
			candidates = append(candidates, filepath.Join(root, e.Name()))
		}
	}
	sort.Strings(exactDirs)
	sort.Strings(candidates)
	return exactDirs, candidates
}

// transcriptDirsFor is the directories to read (the exact ones, plus the candidates when
// --include-moved says so) and the candidates themselves.
func transcriptDirsFor(e store.Env, includeMoved bool) (dirs, candidates []string) {
	dirs, candidates = SlugDirs(e.ProjectRoot())
	if includeMoved {
		dirs = append(dirs, candidates...)
		sort.Strings(dirs)
	}
	return dirs, candidates
}

// transcriptsIn lists the .jsonl files in dirs, oldest first.
func transcriptsIn(dirs []string) []TranscriptFile {
	var files []TranscriptFile
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			files = append(files, TranscriptFile{Path: filepath.Join(d, e.Name()),
				Bytes: info.Size(), Modified: info.ModTime().Unix()})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Modified < files[j].Modified })
	return files
}

// bootstrapReadKey keys progress by PATH, which is the transcript's identity. Size and
// mtime are not: a session that is still running appends to the file it was read from,
// and keying on either would silently un-read every transcript that grew.
const bootstrapReadKey = "bootstrap_read:"

// bootstrapRead is path -> when it was marked read.
func (s *Store) bootstrapRead() map[string]int64 {
	out := map[string]int64{}
	rows, err := s.DB.Query("SELECT k, v FROM meta WHERE k LIKE ?", bootstrapReadKey+"%")
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out
		}
		ts, _ := strconv.ParseInt(v, 10, 64)
		out[strings.TrimPrefix(k, bootstrapReadKey)] = ts
	}
	return out
}

// BootstrapProgress is what the brief needs to say one line and then stay quiet.
type BootstrapProgress struct {
	Read  int  `json:"read"`
	Total int  `json:"total"`
	Open  bool `json:"open"`
}

// BootstrapProgress counts marks laid down since the last --done, against the transcripts
// that are actually this project's. Marks are never deleted, so --again reopens the pass
// naturally: a mark newer than bootstrapped_at is work on the current pass.
func (s *Store) BootstrapProgress() BootstrapProgress {
	marks := s.bootstrapRead()
	if len(marks) == 0 {
		return BootstrapProgress{}
	}
	done := s.BootstrappedAt()
	dirs, _ := transcriptDirsFor(s.Env, false)
	files := transcriptsIn(dirs)
	p := BootstrapProgress{Total: len(files)}
	for _, f := range files {
		if ts, ok := marks[f.Path]; ok && ts > done {
			p.Read++
		}
	}
	p.Open = p.Read > 0 && p.Total > 0
	return p
}

// BootstrappedAt is per project. The bare key was one row in a meta table every project in
// a shared store writes, so one project's --done marked them all bootstrapped. A project's
// own database may still hold the bare key from before, and that one is its own.
func (s *Store) BootstrappedAt() int64 {
	if v := s.MetaInt(s.ProjectKey("bootstrapped_at")); v > 0 {
		return v
	}
	if s.Env.DBPath != s.Env.GlobalDB {
		return s.MetaInt("bootstrapped_at")
	}
	return 0
}

type BootstrapArgs struct {
	Done         bool
	Again        bool
	IncludeMoved bool
	Read         []string
	N            int
	JSON         bool
}

func (s *Store) Bootstrap(w io.Writer, a BootstrapArgs) error {
	if a.N <= 0 {
		a.N = 12
	}
	if len(a.Read) > 0 {
		return s.bootstrapMarkRead(w, a)
	}
	if a.Done {
		return s.bootstrapDone(w, a)
	}
	return RenderBootstrap(w, s.Env, a, s.BootstrappedAt(), s.bootstrapRead())
}

// bootstrapDone records that the pass is finished, as a meta key and an event.
func (s *Store) bootstrapDone(w io.Writer, a BootstrapArgs) error {
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)",
		s.ProjectKey("bootstrapped_at"), fmt.Sprintf("%d", timefmt.Now())); err != nil {
		return err
	}
	if _, err := s.DB.Exec("INSERT INTO events(project,ts,scope,kind,actor,summary,ref,session) "+
		"VALUES(?,?,?,?,?,?,?,?)", s.Env.Project, timefmt.Now(), "global", "session", "claude",
		"bootstrapped the brain from past session transcripts", "", s.Env.SessionID()); err != nil {
		return err
	}
	n, err := s.Count("SELECT COUNT(*) FROM memories WHERE project=?", s.Env.Project)
	if err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("bootstrap recorded; the brain holds %d memories", n),
		map[string]interface{}{"memories": n})
}

// bootstrapListing is what RenderBootstrap works from.
type bootstrapListing struct {
	env            store.Env
	a              BootstrapArgs
	done           int64
	dirs           []string
	candidates     []string
	files          []TranscriptFile
	candidateFiles []TranscriptFile
	marks          map[string]int64
}

func (b *bootstrapListing) isRead(f TranscriptFile) bool {
	ts, ok := b.marks[f.Path]
	return ok && ts > b.done
}

// RenderBootstrap is the bootstrap listing. done is when the last pass finished (0 for
// never) and marks are the transcripts marked read, by path.
func RenderBootstrap(w io.Writer, env store.Env, a BootstrapArgs, done int64, marks map[string]int64) error {
	if a.N <= 0 {
		a.N = 12
	}
	b := &bootstrapListing{env: env, a: a, done: done, marks: marks,
		candidateFiles: []TranscriptFile{}}
	b.dirs, b.candidates = transcriptDirsFor(env, a.IncludeMoved)
	b.files = transcriptsIn(b.dirs)
	if !a.IncludeMoved {
		b.candidateFiles = transcriptsIn(b.candidates)
	}
	nread := 0
	var total int64
	for _, f := range b.files {
		if b.isRead(f) {
			nread++
		}
		total += f.Bytes
	}
	bar := strings.Repeat("=", 72)
	lines := []string{bar, strings.ToUpper(env.Project) + " BRAIN — BOOTSTRAP", bar}

	if done > 0 && !a.Again {
		lines = append(lines, fmt.Sprintf("Already bootstrapped on %s. Re-run with --again "+
			"to go through the transcripts once more (existing memories are kept: same "+
			"scope+title refines rather than duplicates).", timefmt.ISO(done)))
		lines = append(lines, b.excludedLines()...)
		return store.Out(w, a.JSON, strings.Join(lines, "\n"),
			map[string]interface{}{"bootstrapped_at": done, "files": b.files,
				"candidate_dirs": b.candidates, "candidate_files": b.candidateFiles,
				"read": nread, "include_moved": a.IncludeMoved})
	}

	if len(b.files) == 0 {
		lines = append(lines, "No session transcripts found for "+env.ProjectRoot()+".")
		where := "<config>/projects/ (no matching directory)"
		if len(b.dirs) > 0 {
			where = strings.Join(b.dirs, ", ")
		}
		lines = append(lines, "Looked in: "+where)
		lines = append(lines, b.excludedLines()...)
		lines = append(lines, "", "Nothing to seed from. Start storing as you work.")
		return store.Out(w, a.JSON, strings.Join(lines, "\n"),
			map[string]interface{}{"files": []TranscriptFile{}, "count": 0, "read": 0,
				"candidate_dirs": b.candidates, "candidate_files": b.candidateFiles,
				"dirs": b.dirs, "include_moved": a.IncludeMoved})
	}

	lines = append(lines, fmt.Sprintf("%d transcripts, %dKB, %s to %s — %d read, %d to go",
		len(b.files), total/1024, timefmt.Day(b.files[0].Modified), timefmt.Day(b.files[len(b.files)-1].Modified),
		nread, len(b.files)-nread))
	lines = append(lines, b.recentFileLines()...)
	lines = append(lines, b.excludedLines()...)
	lines = append(lines, bootstrapInstructions...)
	var doneVal interface{}
	if done > 0 {
		doneVal = done
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"files": b.files, "count": len(b.files), "bytes": total, "read": nread,
		"candidate_dirs": b.candidates, "candidate_files": b.candidateFiles,
		"dirs": b.dirs, "include_moved": a.IncludeMoved, "bootstrapped_at": doneVal})
}

// recentFileLines lists the newest N transcripts, each marked read or to read.
func (b *bootstrapListing) recentFileLines() []string {
	var lines []string
	start := 0
	if len(b.files) > b.a.N {
		start = len(b.files) - b.a.N
	}
	for _, f := range b.files[start:] {
		mark := "  to read"
		if b.isRead(f) {
			mark = "  READ   "
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %6dKB  %s", mark, timefmt.Day(f.Modified),
			f.Bytes/1024, f.Path))
	}
	if len(b.files) > b.a.N {
		lines = append(lines, fmt.Sprintf("  ... and %d older (--json lists them all)",
			len(b.files)-b.a.N))
	}
	return lines
}

// excludedLines names the near misses. A directory that matches only on the project's
// name is never silently included and never silently dropped: it is printed in full, with
// the flag that would include it.
func (b *bootstrapListing) excludedLines() []string {
	if len(b.candidates) == 0 || b.a.IncludeMoved {
		return nil
	}
	out := []string{"", fmt.Sprintf("EXCLUDED — %d directories match this project's NAME "+
		"but not its path:", len(b.candidates))}
	for _, d := range b.candidates {
		out = append(out, "  "+d)
	}
	return append(out, fmt.Sprintf("  %d transcripts in them. A slug ending %q may be a "+
		"different project, not", len(b.candidateFiles), "-"+strings.ToLower(
		filepath.Base(b.env.ProjectRoot()))),
		"  this one at an old path, and seeding from another project's sessions is the",
		"  one thing this pass must not do. If they really are this project moved:",
		"    os-brain bootstrap --include-moved")
}

// bootstrapInstructions tell the agent how to read the transcripts and what to store.
var bootstrapInstructions = []string{
	"",
	"READ THESE, OLDEST FIRST, AND SEED THE BRAIN.",
	"",
	"Each line is a JSON object. The ones that matter carry type 'user' or",
	"'assistant' and a 'message' with the content; 'timestamp' dates them. Tool",
	"calls and their output are noise for this purpose: what you want is what the",
	"user said and what was concluded.",
	"",
	"Store, as you read:",
	"  - decisions and why they were made          --type decision",
	"  - things that cost time to discover         --type gotcha",
	"  - how the system is wired, hosts, paths     --type infra / access",
	"  - hard rules and boundaries                 --type constraint / policy",
	"  - how the user wants to be worked with      os-brain prefer \"...\" --why \"...\"",
	"",
	"Rules for this pass:",
	"  - Distil. One durable fact per memory, in the words a future session would",
	"    search for. Never paste transcript text.",
	"  - Date it: --source \"session transcript\" --source-date YYYY-MM-DD from the",
	"    timestamp, so the brain knows how old the claim is.",
	"  - Confidence: --confidence confirmed only for what was verified in the",
	"    session. Everything discussed but not confirmed is --confidence assumed.",
	"  - Superseded by later sessions? Store the LATEST version and skip the old one,",
	"    or store both with --supersedes so the correction wins.",
	"  - Skip anything already true in the repo, in git history, or in CLAUDE.md.",
	"  - Aim for tens of memories, not hundreds. A bootstrap that dumps everything",
	"    produces a brain nobody trusts.",
	"",
	"Mark each one as you finish it, so an interrupted pass resumes instead of",
	"starting over:  os-brain bootstrap --read <path>",
	"When finished:  os-brain bootstrap --done",
}

// bootstrapMarkRead records progress one transcript at a time. Before this the only state
// was a single flag written at the end, so a pass interrupted after one of ten files left
// nothing behind and the next run reprinted all ten.
func (s *Store) bootstrapMarkRead(w io.Writer, a BootstrapArgs) error {
	marked, err := BootstrapReadRefs(s.Env, a.Read)
	if err != nil {
		return err
	}
	now := timefmt.Now()
	for _, p := range marked {
		if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)",
			bootstrapReadKey+p, fmt.Sprintf("%d", now)); err != nil {
			return err
		}
	}
	p := s.BootstrapProgress()
	return store.Out(w, a.JSON, fmt.Sprintf("%d marked read · %d of %d transcripts done, %d to go",
		len(marked), p.Read, p.Total, p.Total-p.Read),
		map[string]interface{}{"marked": marked, "read": p.Read, "total": p.Total})
}

// BootstrapReadRefs is what `bootstrap --read` may record: each path made absolute, and
// every one of them a transcript of this project on this machine, or nothing at all. A
// mark nobody can match to a file would report progress that does not exist.
func BootstrapReadRefs(env store.Env, refs []string) ([]string, error) {
	dirs, candidates := transcriptDirsFor(env, false)
	known := map[string]bool{}
	for _, f := range transcriptsIn(dirs) {
		known[f.Path] = true
	}
	// Marking is deliberately not gated on --include-moved: a file that was listed under
	// that flag can be marked without repeating it.
	for _, f := range transcriptsIn(candidates) {
		known[f.Path] = true
	}
	var marked []string
	for _, p := range refs {
		abs, err := filepath.Abs(store.ExpandHome(p))
		if err != nil {
			return nil, err
		}
		if !known[abs] {
			return nil, fmt.Errorf("not a transcript of this project: %s\n"+
				"  %d transcripts are known here; os-brain bootstrap lists them by path.\n"+
				"  Nothing was recorded.", abs, len(known))
		}
		marked = append(marked, abs)
	}
	return marked, nil
}
