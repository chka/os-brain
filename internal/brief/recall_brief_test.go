package brief

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/memory"
)

// The list sizes are part of the storage contract: the team edition opens the same brain,
// and the same brain must answer a session the same way whichever binary the hook called.
// Brief caps are 15 pinned and 8 preferences; recall returns 8 results by default.
func TestBriefCapsAndTheRecallDefaultAreFixed(t *testing.T) {
	if BriefMaxPinned != 15 || BriefMaxPrefs != 8 {
		t.Errorf("brief caps drifted: pinned=%d prefs=%d, want 15 and 8",
			BriefMaxPinned, BriefMaxPrefs)
	}
	s := newTestStore(t)
	for i := 0; i < 25; i++ {
		remember(t, s, memory.RememberArgs{Title: strings.Repeat("x", i+1) + " widget", Body: "about nginx"})
	}
	var buf bytes.Buffer
	if err := s.Recall(&buf, RecallArgs{Query: "widget", NoTouch: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(buf.String(), "\n#"); got+1 != 8 {
		t.Errorf("recall returned %d results, want the default of 8:\n%s", got+1, buf.String())
	}
}
