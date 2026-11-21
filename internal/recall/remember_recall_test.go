package recall

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/textguard"
)

// A 24 MB body was once stored and recall printed every byte of it. Titles and bodies are
// capped, and recall clips a long body and says where to read the rest.
func TestTitleAndBodyAreCappedAndRecallClips(t *testing.T) {
	s := newTestStore(t)
	if err := s.Remember(io.Discard, RememberArgs{Type: "note", Scope: "global",
		Title: strings.Repeat("t", textguard.MaxTitleRunes+1)}); err == nil {
		t.Error("an over-long title was stored")
	}
	if err := s.Remember(io.Discard, RememberArgs{Type: "note", Scope: "global", Title: "gizmo",
		Body: strings.Repeat("x", textguard.MaxBodyBytes+1)}); err == nil {
		t.Error("an over-long body was stored")
	}
	remember(t, s, RememberArgs{Title: "gizmo specification in full", Body: strings.Repeat("gizmo ", 2000)})
	var out bytes.Buffer
	if err := s.Recall(&out, RecallArgs{Query: "gizmo"}); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 4*RecallBodyRunes || !strings.Contains(out.String(), "os-brain get") {
		t.Errorf("recall printed %d bytes without saying where the rest is", out.Len())
	}
}
