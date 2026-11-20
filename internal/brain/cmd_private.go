package brain

import (
	"fmt"
	"io"
	"strings"
)

// Turning privacy on and off, as a command rather than an edit.
//
// `private: true` in brain.json is the declaration, but two things make hand-editing it
// the wrong route on its own:
//
//   - The declaration has to reach the SHARED INDEX before any other project honours it,
//     because a project cannot read another's brain.json. The index is written on brief,
//     describe and install, so a hand edit does nothing at all until something unrelated
//     happens to run. A rule that takes effect later, silently, is not a rule.
//   - The two directions are not symmetric, and the asymmetry is the whole point. Turning
//     privacy ON is forward-looking: it stops the next read, and cannot unread what has
//     already been read, copied into a note, or published as a lesson. Turning it OFF is
//     retroactive: every memory this project has ever stored becomes searchable by every
//     other project in the file, at once, including the ones written while it was private.
//
// So: on is applied immediately and reports what it cannot undo; off requires --yes and
// says how much history it is about to expose.

type PrivateArgs struct {
	Off  bool
	Yes  bool
	JSON bool
}

// privacyExposure is how much of this project is visible to the others: its live
// memories, and the copies of it that already live in the shared spine.
type privacyExposure struct {
	memories, lessons, notesSent int
}

func (s *Store) Private(w io.Writer, a PrivateArgs) error {
	want := !a.Off
	cur := s.Env.Config.Private
	exp := s.privacyExposure()

	if cur == want {
		state := "readable by other projects in this store"
		if want {
			state = "private: other projects cannot read it"
		}
		return Out(w, a.JSON, fmt.Sprintf("%s is already %s", Repr(s.Env.Project), state),
			map[string]interface{}{"project": s.Env.Project, "private": cur, "changed": false})
	}

	// Off is the widening direction, so it is the one that asks. On narrows, and a
	// narrowing that needed confirmation would just be friction in front of a safer state.
	if !want && !a.Yes {
		return fmt.Errorf("this makes %d memories in %s readable by every other project "+
			"in this store, including everything written while it was private. Retrieval is "+
			"not the only route either: `--all-projects` and the MCP surface both widen.\n"+
			"Add --yes if that is the intent", exp.memories, Repr(s.Env.Project))
	}

	if !s.Env.Shared {
		Warn("%s has its own database, so nothing else can read it anyway. This flag only "+
			"matters in a shared store; setting it now means it is already right if this "+
			"project ever moves into one.", Repr(s.Env.Project))
	}

	if err := s.applyPrivacy(want, cur); err != nil {
		return err
	}

	var lines []string
	if want {
		lines = s.privateOnReport(exp)
	} else {
		lines = append(lines, fmt.Sprintf("%s is readable again: %d memories are now "+
			"searchable from every other project in this store", Repr(s.Env.Project), exp.memories))
		lines = append(lines, "  That includes everything written while it was private.")
	}
	return Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"project": s.Env.Project, "private": want, "changed": true,
		"memories": exp.memories, "published_lessons": exp.lessons, "notes_sent": exp.notesSent,
	})
}

func (s *Store) privacyExposure() privacyExposure {
	var e privacyExposure
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE project=? AND superseded_by IS NULL",
		s.Env.Project).Scan(&e.memories)
	if g := s.globalConnect(false); g != nil {
		_ = g.QueryRow("SELECT COUNT(*) FROM lessons WHERE project=?", s.Env.Project).Scan(&e.lessons)
		_ = g.QueryRow("SELECT COUNT(*) FROM notes WHERE from_project=?", s.Env.Project).
			Scan(&e.notesSent)
		s.closeGlobal(g)
	}
	return e
}

// applyPrivacy writes the shared index first, and immediately rather than on the next
// brief: it is what other projects consult, so until it moves the declaration has changed
// nothing. If the index cannot be written, nothing is saved and the command fails, rather
// than printing "private" over an index that still says readable.
func (s *Store) applyPrivacy(want, cur bool) error {
	prevCfg, prevPriv := s.Env.Config.Private, s.Env.Private
	s.Env.Config.Private, s.Env.Private = want, want
	if err := s.RegisterProject(); err != nil {
		s.Env.Config.Private, s.Env.Private = prevCfg, prevPriv
		return fmt.Errorf("the shared index was not updated, so nothing changed and %s is "+
			"still %s: %v", Repr(s.Env.Project), map[bool]string{true: "private",
			false: "readable"}[cur], err)
	}
	return SaveConfig(s.Env.Here, s.Env.Config)
}

// privateOnReport says what turning privacy on did, and then, plainly rather than left to
// be discovered, what it cannot reach.
func (s *Store) privateOnReport(exp privacyExposure) []string {
	lines := []string{
		fmt.Sprintf("%s is private: its %d memories are no longer "+
			"reachable from another project's --all-projects", Repr(s.Env.Project), exp.memories),
		"  It reads its own store exactly as before.",
	}
	carried := s.copiesOutsidePrivacy(exp)
	if len(carried) == 0 {
		return lines
	}
	lines = append(lines, "", "This is forward-looking. It cannot unread what was "+
		"already read, and these copies live outside this project:")
	for _, c := range carried {
		lines = append(lines, "  - "+c)
	}
	if exp.lessons > 0 {
		lines = append(lines, "  Retract a lesson with: os-brain publish --retract <id>")
	}
	return lines
}

// copiesOutsidePrivacy lists what this project has already put where privacy cannot
// follow it.
func (s *Store) copiesOutsidePrivacy(exp privacyExposure) []string {
	var carried []string
	// The loudest of the three, and the one nobody expects: privacy covers MEMORIES. The
	// project's name and its one-line spec are a row in the shared index, which every other
	// project's brief prints in full under OTHER PROJECTS. Found by testing a real store
	// after several projects moved into it: a private project was broadcasting a
	// description naming a private individual into every sibling's context, having just
	// been told it was private.
	if about := s.projectAbout(); about != "" {
		carried = append(carried, "this project's own one-line description in the "+
			"shared index, which every other project's brief prints:\n      "+
			Clip(about, 160)+"\n    (os-brain describe --visibility listed keeps the "+
			"name and drops the description)")
	}
	if exp.lessons > 0 {
		carried = append(carried, plural(exp.lessons, "published lesson")+
			" (os-brain global --recall finds them; they are copies in the shared index)")
	}
	if exp.notesSent > 0 {
		carried = append(carried, plural(exp.notesSent, "note")+" already sent to other projects")
	}
	return carried
}

// projectAbout is what the shared index publishes about this project, empty when it has
// never described itself or has already dropped to 'listed'. Read from the index rather
// than from brain.json: the index row is what the other projects actually render.
func (s *Store) projectAbout() string {
	g := s.globalConnect(false)
	if g == nil {
		return ""
	}
	defer s.closeGlobal(g)
	var about, visibility string
	if err := g.QueryRow("SELECT description, visibility FROM projects WHERE root_id=?",
		hashRoot(s.Env.ProjectRoot())).Scan(&about, &visibility); err != nil {
		return ""
	}
	if visibility == "listed" {
		return ""
	}
	return about
}
