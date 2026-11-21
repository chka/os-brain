package doctor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/store"
)

// The remember guard stops new unrecorded claims; doctor finds the ones stored before the
// guard existed. It reports both kinds (target still live, target missing) and repairs none,
// because only a person knows whether the prose or the store is right.
func TestDoctorReportsASupersedeClaimTheStoreNeverRecorded(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	for _, a := range []RememberArgs{
		{Type: "preference", Title: "remind me about the audit every session", Body: "until killed"},
		{Type: "decision", Title: "the audit is killed", Body: "Supersedes preference #1.", ClaimOK: true},
		{Type: "decision", Title: "the grant hunt is killed too", Body: "Supersedes #375.", ClaimOK: true},
	} {
		a.Scope = store.DefaultScope
		if err := s.Remember(&bytes.Buffer{}, a); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := s.Doctor(&out, DoctorArgs{}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "#2 says it supersedes #1, and #1 is still live") {
		t.Errorf("the live target is not reported:\n%s", got)
	}
	if !strings.Contains(got, "#3 says it supersedes #375, and there is no #375") {
		t.Errorf("the missing target is not reported:\n%s", got)
	}
	var live int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM memories WHERE superseded_by IS NULL").Scan(&live)
	if live != 3 {
		t.Errorf("doctor must report and change nothing; live rows = %d", live)
	}
}
