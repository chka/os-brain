package brief

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/timefmt"
)

const (
	treeMaxFiles   = 10 // names rendered per category
	treeKeepFiles  = 50 // names kept for the JSON envelope
	treeMaxCommits = 10
)

// TreeFacts is the detailed read the brief's one git line cannot carry: what is staged
// versus merely modified versus untracked, what is unpushed, how stale the remote
// comparison is, and whether an operation is half-finished.
//
// Everything here is best-effort and never fatal, for the same reason gitFacts is: a
// session open that fails because git is absent is worse than one without a tree block.
type TreeFacts struct {
	Branch     string   `json:"branch,omitempty"`
	Detached   bool     `json:"detached,omitempty"`
	Commit     string   `json:"commit,omitempty"`
	Committed  string   `json:"committed,omitempty"`
	Upstream   string   `json:"upstream,omitempty"`
	Remote     string   `json:"remote,omitempty"`
	RemoteURL  string   `json:"remote_url,omitempty"`
	Ahead      int      `json:"ahead"`
	Behind     int      `json:"behind"`
	Unpushed   []string `json:"unpushed,omitempty"`
	FetchedAt  int64    `json:"fetched_at,omitempty"`
	Staged     []string `json:"staged,omitempty"`
	StagedN    int      `json:"staged_count"`
	Modified   []string `json:"modified,omitempty"`
	ModifiedN  int      `json:"modified_count"`
	Untracked  []string `json:"untracked,omitempty"`
	UntrackedN int      `json:"untracked_count"`
	Conflicted []string `json:"conflicted,omitempty"`
	Stash      int      `json:"stash"`
	Operation  string   `json:"operation,omitempty"`
}

// TreeFacts reads the working tree, or returns nil outside a git checkout. A brain served
// over MCP for another project would be looking at the SERVER's checkout, and reporting
// that as the caller's tree is the same lie contextFacts already refuses to tell.
func (s *Store) TreeFacts() *TreeFacts {
	if s.Env.Remote {
		return nil
	}
	return readTree(s.Env.ProjectRoot())
}

// treeGit runs read-only git commands in one checkout.
type treeGit struct{ root string }

// raw trims trailing newlines only. `status --porcelain` encodes staged-versus-unstaged in
// the first TWO columns, and the first is a space for an unstaged edit, so trimming the
// whole output strips exactly that column off the first line and files a modified file as
// a staged one.
func (g treeGit) raw(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := fsguard.GitRead(ctx, g.root, args...)
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, "\n")
}

func (g treeGit) run(args ...string) string { return strings.TrimSpace(g.raw(args...)) }

func readTree(root string) *TreeFacts {
	git := treeGit{root}
	if git.run("rev-parse", "--is-inside-work-tree") != "true" {
		return nil
	}
	t := &TreeFacts{
		Branch:    currentBranch(git),
		Commit:    git.run("log", "-1", "--format=%h %s"),
		Committed: git.run("log", "-1", "--format=%cr"),
		Upstream:  git.run("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"),
	}
	if t.Branch == "HEAD" {
		t.Detached, t.Branch = true, git.run("rev-parse", "--short", "HEAD")
	}
	t.readRemote(git)
	t.readStatus(git.raw("status", "--porcelain"))
	t.Stash = len(splitLines(git.run("stash", "list")))

	if dir := git.run("rev-parse", "--absolute-git-dir"); dir != "" {
		// ahead/behind come from the remote-tracking ref, so they are as of the last fetch.
		// Reporting them without their age would let old truth read exactly like current
		// truth.
		if fi, err := os.Stat(filepath.Join(dir, "FETCH_HEAD")); err == nil {
			t.FetchedAt = fi.ModTime().Unix()
		}
		t.Operation = gitOperation(dir)
	}
	return t
}

// currentBranch falls back to symbolic-ref because rev-parse fails on an unborn HEAD (a
// repository with no commit yet). The error used to be swallowed into an empty branch
// name, so `os-brain init` in a fresh repo opened every session with a blank where the
// branch goes.
func currentBranch(git treeGit) string {
	if branch := git.run("rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
		return branch
	}
	return git.run("symbolic-ref", "--short", "HEAD")
}

// readRemote fills the remote, its URL, and the ahead/behind counts against the upstream.
func (t *TreeFacts) readRemote(git treeGit) {
	if remotes := strings.Fields(git.run("remote")); len(remotes) > 0 {
		t.Remote = remotes[0]
		if strings.Contains(t.Upstream, "/") {
			t.Remote = strings.SplitN(t.Upstream, "/", 2)[0]
		}
		t.RemoteURL = git.run("remote", "get-url", t.Remote)
	}
	if t.Upstream == "" {
		return
	}
	if c := strings.Fields(git.run("rev-list", "--left-right", "--count", "HEAD...@{u}")); len(c) == 2 {
		t.Ahead, _ = strconv.Atoi(c[0])
		t.Behind, _ = strconv.Atoi(c[1])
	}
	if t.Ahead > 0 {
		t.Unpushed = append(t.Unpushed, splitLines(git.run("log", "--format=%h %s",
			"-"+strconv.Itoa(treeMaxCommits), "@{u}..HEAD"))...)
	}
}

// readStatus sorts `git status --porcelain` lines into untracked, conflicted, staged and
// modified. A file can be both staged and modified; it is then counted in each.
func (t *TreeFacts) readStatus(porcelain string) {
	for _, ln := range splitLines(porcelain) {
		if len(ln) < 4 {
			continue
		}
		x, y, path := ln[0], ln[1], strings.TrimSpace(ln[2:])
		switch {
		case x == '?' && y == '?':
			t.UntrackedN++
			t.Untracked = keep(t.Untracked, path)
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			t.Conflicted = keep(t.Conflicted, path)
		default:
			if x != ' ' {
				t.StagedN++
				t.Staged = keep(t.Staged, string(x)+" "+path)
			}
			if y != ' ' {
				t.ModifiedN++
				t.Modified = keep(t.Modified, string(y)+" "+path)
			}
		}
	}
}

// gitOperation names a half-finished operation, which is the one thing in this whole block
// that changes what the session is allowed to do next.
func gitOperation(gitDir string) string {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(gitDir, name))
		return err == nil
	}
	switch {
	case exists("rebase-merge"), exists("rebase-apply"):
		return "rebase"
	case exists("MERGE_HEAD"):
		return "merge"
	case exists("CHERRY_PICK_HEAD"):
		return "cherry-pick"
	case exists("REVERT_HEAD"):
		return "revert"
	case exists("BISECT_LOG"):
		return "bisect"
	}
	return ""
}

// keep appends while the list is under treeKeepFiles, so a huge untracked directory
// cannot bloat the JSON envelope; the counts stay exact.
func keep(list []string, v string) []string {
	if len(list) >= treeKeepFiles {
		return list
	}
	return append(list, v)
}

func splitLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func treeLines(t *TreeFacts) []string {
	if t == nil {
		return nil
	}
	lines := []string{"", "## WORKING TREE (read from git just now, not from memory)",
		"  branch    " + branchDescription(t)}
	if t.Commit != "" {
		lines = append(lines, fmt.Sprintf("  head      %s (%s)", t.Commit, t.Committed))
	}
	if t.RemoteURL != "" {
		fetched := "never fetched here"
		if t.FetchedAt > 0 {
			fetched = "fetched " + timefmt.HumanGap(timefmt.Now()-t.FetchedAt)
		}
		lines = append(lines, fmt.Sprintf("  remote    %s %s · %s", t.Remote, t.RemoteURL, fetched))
	}
	lines = append(lines, syncLines(t)...)
	lines = append(lines, treeFileGroup("staged", t.StagedN, t.Staged)...)
	lines = append(lines, treeFileGroup("modified", t.ModifiedN, t.Modified)...)
	lines = append(lines, treeFileGroup("untracked", t.UntrackedN, t.Untracked)...)
	lines = append(lines, treeFileGroup("CONFLICT", len(t.Conflicted), t.Conflicted)...)
	if t.StagedN+t.ModifiedN+t.UntrackedN+len(t.Conflicted) == 0 {
		lines = append(lines, "  clean     nothing staged, modified or untracked")
	}
	if t.Stash > 0 {
		lines = append(lines, fmt.Sprintf("  stash     %s — work parked here is invisible "+
			"everywhere else", timefmt.Plural(t.Stash, "entry")))
	}
	if t.Operation != "" {
		lines = append(lines, fmt.Sprintf("  UNFINISHED  a %s is in progress. Finish or abort "+
			"it before you commit anything", t.Operation))
	}
	return lines
}

func branchDescription(t *TreeFacts) string {
	switch {
	case t.Detached:
		return t.Branch + " (detached HEAD — commits here belong to no branch)"
	case t.Upstream != "":
		return t.Branch + " -> " + t.Upstream
	}
	return t.Branch + " — no upstream: nothing on this branch is pushed anywhere"
}

// syncLines says what is unpushed and what is behind the upstream.
func syncLines(t *TreeFacts) []string {
	var lines []string
	if t.Ahead > 0 {
		lines = append(lines, fmt.Sprintf("  unpushed  %s — this work exists only here",
			timefmt.Plural(t.Ahead, "commit")))
		for _, c := range t.Unpushed {
			lines = append(lines, "              "+c)
		}
	}
	if t.Behind > 0 {
		lines = append(lines, fmt.Sprintf("  behind    %s: someone has been here since. "+
			"Pull before you work", timefmt.Plural(t.Behind, "commit")))
	}
	if t.Ahead > 0 || t.Behind > 0 {
		// The counts are only as current as the last fetch, and nothing here fetches: a
		// network call at session open can block, hang the hook, or prompt for credentials.
		lines = append(lines, "            (both counts are as of the last fetch — "+
			"`git fetch` if the number has to be current)")
	}
	return lines
}

func treeFileGroup(label string, n int, files []string) []string {
	if n == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf("  %-9s %s", label, timefmt.Plural(n, "file"))}
	for i, f := range files {
		if i == treeMaxFiles {
			lines = append(lines, fmt.Sprintf("              ... and %d more", n-treeMaxFiles))
			break
		}
		lines = append(lines, "              "+f)
	}
	return lines
}
