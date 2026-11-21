package textguard

import (
	"strings"
	"testing"
)

// The secret scan also knows the team edition's access-token format (prefix hive_). A user
// who moves between editions holds such tokens, and one pasted into a memory would sit in
// plain text in a store that is exported, snapshotted and shared like any other.
func TestTheSecretScanCatchesTeamEditionTokens(t *testing.T) {
	hits := ScanSecrets("the CI credential is hive_8Fk2LmQpXr4TzWn7Ba9CdEfGhJkLmNoPqRs")
	if len(hits) == 0 {
		t.Fatal("a team-edition token stored in a memory is not flagged")
	}
}

// Judged marks a refusal as the caller's to fix, and the mark must not be part of what the
// caller reads. The first version used fmt.Errorf("%w%s", ErrJudged, msg), which renders the
// wrapped error's own text first, so the product printed "judgedrefusing to store what
// looks like an AWS access key id". Every test stayed green, because the newer ones checked
// the status code and the older ones matched substrings that were all still there. So the
// text is asserted here, on the one function every refusal goes through.
func TestAJudgedRefusalReadsAsItsMessage(t *testing.T) {
	err := Judged("#%d is pinned. Pass force to delete it anyway", 3)
	const want = "#3 is pinned. Pass force to delete it anyway"
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
	// And the guards that go through it say only what they mean.
	if got := GuardText(false, "AKIAIOSFODNN7EXAMPLE"); got == nil {
		t.Fatal("the secret scan let an AWS key through")
	} else if !strings.HasPrefix(got.Error(), "refusing to store") {
		t.Errorf("the secret refusal reads %q", got.Error())
	}
	if got := CheckToken("not a scope!!", "scope"); got == nil {
		t.Fatal("CheckToken accepted a malformed scope")
	} else if !strings.HasPrefix(got.Error(), "invalid scope") {
		t.Errorf("the token refusal reads %q", got.Error())
	}
}
