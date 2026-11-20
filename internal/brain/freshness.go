package brain

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// VolatileTypes are the memory types whose claims a commit can falsify: they describe
// what the code does right now. Everything else is falsified by the user changing their
// mind, not by the tree moving, and telling a session that a settled decision is N
// commits old only invites it to re-litigate a call that was already made.
//
// Narrow on purpose. A freshness line that fires on every memory is a freshness line
// everyone learns to skip, which is how the brief's other signals earn their slot.
var VolatileTypes = set("gotcha", "state", "incident")

// WorldOwnedTypes are the memory types whose truth belongs to the world rather than to
// the user or to this repository. A host moves, an IP is reassigned, a key is rotated, and
// nothing in the project records that it happened: there is no commit to count and no user
// to contradict it, so the claim reads exactly as current on the day it goes wrong. A
// session once named a box as staging from a 25-day-old note; it had been production for a
// fortnight.
//
// Deliberately two. `gotcha` is tier A as well but is a claim about the CODE, which
// source_commit already dates against the tree. `decision`, `constraint` and `commitment`
// are falsified by the user, not by time, and nagging about a settled call only invites
// re-arguing it (the same reason VolatileTypes stays narrow). `state` decays in days on its
// own. `incident` never decays and never expires either: what happened to a system is fixed
// once it has happened, so there is nothing for a review date to re-check. Everything else
// is documentation.
var WorldOwnedTypes = set("infra", "access")

// DefaultReviewDays is how far ahead a world-owned memory asks to be re-checked when the
// caller sets no horizon of their own. Thirty days because a month is the shortest window
// that does not fire during ordinary week-to-week work on one project (a horizon that fires
// every week is a DUE FOR REVIEW block everybody learns to scroll past), and it is
// comfortably inside the 25 days it took the note above to go stale unnoticed.
const DefaultReviewDays = 30

// defaultReviewDay is the day a world-owned memory should ask to be re-checked, or "" when
// nothing honest can be said. Fails open by construction: any other type sets no date and
// the memory is stored regardless.
func defaultReviewDay(memType string, now time.Time) string {
	if !WorldOwnedTypes[memType] {
		return ""
	}
	return now.AddDate(0, 0, DefaultReviewDays).Format("2006-01-02")
}

// Freshness answers the one question a session currently spends a dozen shell commands
// answering by hand: has the tree moved since this was written down?
type Freshness struct {
	StoredAt  string `json:"stored_at"` // short sha the claim was recorded against
	Head      string `json:"head"`      // short sha now
	Commits   int    `json:"commits"`   // commits from StoredAt to Head
	Unchanged bool   `json:"unchanged"` // nothing has moved: the claim still stands
	// Always serialised, never omitempty, so every verdict carries the same keys and a
	// consumer never has to tell "false" from "missing".
	Unknown bool   `json:"unknown"` // stored commit is not in this tree
	Note    string `json:"note"`    // the rendered one-liner
}

// TreeState resolves HEAD once and memoises the distance to each stored commit, so a
// recall returning six memories costs at most one git call per distinct commit, and
// zero for the case that matters most, where the memory was written at the current HEAD.
type TreeState struct {
	root string
	head string
	ok   bool
	dist map[string]int
	gone map[string]bool
}

func (s *Store) TreeState() *TreeState {
	t := &TreeState{root: s.Env.ProjectRoot(), dist: map[string]int{}, gone: map[string]bool{}}
	if t.git("rev-parse", "--is-inside-work-tree") != "true" {
		return t
	}
	t.head = t.git("rev-parse", "HEAD")
	t.ok = t.head != ""
	return t
}

func (t *TreeState) git(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := gitRead(ctx, t.root, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Head is the current commit, empty outside a work tree. Writers stamp this.
func (t *TreeState) Head() string { return t.head }

// For computes the verdict for one memory, or nil when there is nothing honest to say:
// no git, no stored commit, a type a commit cannot falsify, or a memory belonging to
// another project, whose claims were recorded against a tree that is not this one.
func (t *TreeState) For(m *Memory, project string) *Freshness {
	if !t.ok || m.SourceCommit == "" || !VolatileTypes[m.MemType] {
		return nil
	}
	if m.Project != "" && m.Project != project {
		return nil
	}
	f := &Freshness{StoredAt: short(m.SourceCommit), Head: short(t.head)}
	if m.SourceCommit == t.head {
		f.Unchanged = true
		f.Note = "tree unchanged since stored (" + f.StoredAt + ")"
		return f
	}
	if t.gone[m.SourceCommit] {
		return notInTree(f)
	}
	n, seen := t.dist[m.SourceCommit]
	if !seen {
		if t.git("cat-file", "-e", m.SourceCommit+"^{commit}") == "" &&
			t.git("rev-parse", "--verify", "--quiet", m.SourceCommit+"^{commit}") == "" {
			// Rebased, garbage-collected or from a branch that no longer exists. Say so
			// rather than report a distance computed from nothing.
			t.gone[m.SourceCommit] = true
			return notInTree(f)
		}
		raw := t.git("rev-list", "--count", m.SourceCommit+"..HEAD")
		n, _ = strconv.Atoi(raw)
		t.dist[m.SourceCommit] = n
	}
	f.Commits = n
	if n == 0 {
		// Reachable but not an ancestor of HEAD: a sibling branch. Distance zero here
		// would read as "unchanged", which is the one thing it must never say wrongly.
		f.Unknown = true
		f.Note = "stored at " + f.StoredAt + ", off this branch — re-check against source"
		return f
	}
	plural := "s"
	if n == 1 {
		plural = ""
	}
	f.Note = fmt.Sprintf("%d commit%s since stored (%s) — re-check against source", n, plural, f.StoredAt)
	return f
}

func notInTree(f *Freshness) *Freshness {
	f.Unknown = true
	f.Note = "stored at " + f.StoredAt + ", which is not in this tree — re-check against source"
	return f
}

// StampFreshness fills in the computed verdict for a batch about to be rendered. It is a
// no-op for a brain outside a work tree, so nothing depends on git being present.
func (s *Store) StampFreshness(ms []*Memory) {
	if len(ms) == 0 {
		return
	}
	relevant := false
	for _, m := range ms {
		if m.SourceCommit != "" && VolatileTypes[m.MemType] {
			relevant = true
			break
		}
	}
	if !relevant {
		return // do not shell out to git to answer a question nobody asked
	}
	t := s.TreeState()
	for _, m := range ms {
		m.Fresh = t.For(m, s.Env.Project)
	}
}

// headCommit is what a writer stamps. Best-effort and never fatal: a brain in a directory
// that is not a git repo stores an empty string and simply renders no verdict.
func (s *Store) headCommit() string { return s.TreeState().Head() }

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
