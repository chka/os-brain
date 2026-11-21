package project

import (
	"io"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/timefmt"
)

// relabel recovers this project's own rows filed under an old name. Moving another
// project's memories into this one through a recovery command would break the rule that a
// project writes only its own rows, so relabel refuses a live project's label and needs
// --i-mean-it for one nothing can identify.

func TestRelabelRefusesToClaimALiveProjectsRows(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "theirs", Type: "note"})
	if _, err := s.DB.Exec("UPDATE memories SET project='liveproj'"); err != nil {
		t.Fatal(err)
	}
	g := s.GlobalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	if _, err := g.Exec("INSERT INTO projects(root_id,name,last_seen) VALUES(?,?,?)",
		"/somewhere/live", "liveproj", timefmt.Now()); err != nil {
		t.Fatal(err)
	}
	s.CloseGlobal(g)
	err := s.Relabel(io.Discard, RelabelArgs{Old: "liveproj", Apply: true})
	if err == nil || !strings.Contains(err.Error(), "live project") {
		t.Errorf("relabel accepted another project's rows: %v", err)
	}
}

func TestRelabelIsADryRunUntilApplied(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, memory.RememberArgs{Title: "mine under an old name", Type: "note"})
	if _, err := s.DB.Exec("UPDATE memories SET project='oldname'"); err != nil {
		t.Fatal(err)
	}
	res := mustJSON(t, func(w io.Writer) error {
		return s.Relabel(w, RelabelArgs{Old: "oldname", JSON: true})
	})
	if res["applied"] != false {
		t.Error("relabel applied without --apply")
	}
	// For an unregistered label nothing can tell a mislabelled copy of your own store from
	// a live project that never ran `describe`, so it has to be claimed deliberately.
	// Without --i-mean-it, one command with --apply once moved somebody else's whole store
	// into this project.
	if err := s.Relabel(io.Discard, RelabelArgs{Old: "oldname", Apply: true, Expect: 1}); err == nil {
		t.Error("an unregistered label was claimed without --i-mean-it")
	}
	if err := s.Relabel(io.Discard, RelabelArgs{Old: "oldname", Apply: true, Expect: 1,
		IMeanIt: true}); err != nil {
		t.Fatal(err)
	}
	if got := titles(recallJSON(t, s, RecallArgs{Query: "old name"})); len(got) != 1 {
		t.Errorf("claimed rows are not visible to this project: %v", got)
	}
}
