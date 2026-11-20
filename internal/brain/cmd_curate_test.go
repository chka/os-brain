package brain

import (
	"io"
	"strings"
	"testing"
)

// Curation at write time: a near-duplicate is refused before it is stored, and every memory
// records where it came from (trust) and when it holds (the validity window).

// --- near-duplicate blocking -------------------------------------------------------

// `review` reports duplicates, but a report arrives after the store has already grown one,
// and the session reading it is not the session that made it. So the write itself refuses a
// second memory about the same subject in the same scope, and names the two ways past.
func TestASecondMemoryAboutTheSameSubjectIsRefused(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the staging deploy needs the vault unsealed first",
		Type: "gotcha"})
	err := s.Remember(io.Discard, RememberArgs{
		Title: "staging deploy requires the vault to be unsealed", Type: "gotcha",
		Scope: "global", Importance: 3, Source: "session"})
	if err == nil {
		t.Fatal("stored two memories about the same subject in one scope")
	}
	for _, want := range []string{"already about this", "--supersedes", "--allow-duplicate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not offer %q:\n%v", want, err)
		}
	}
	if n, _ := s.count("SELECT COUNT(*) FROM memories"); n != 1 {
		t.Errorf("a refused write still stored something: %d rows", n)
	}
}

// --supersedes already declares the relationship; --allow-duplicate is the explicit
// "yes, I mean it". Neither should be blocked.
func TestSupersedesAndAllowDuplicateBothGetPast(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the staging deploy needs the vault unsealed first",
		Type: "gotcha"})
	remember(t, s, RememberArgs{Title: "staging deploy requires the vault to be unsealed",
		Type: "gotcha", Supersedes: 1})
	remember(t, s, RememberArgs{Title: "staging deploy also wants the vault unsealed early",
		Type: "gotcha", AllowDuplicate: true})
	if n, _ := s.count("SELECT COUNT(*) FROM memories"); n != 3 {
		t.Errorf("expected 3 rows, got %d", n)
	}
}

// Scopes exist so the same subject can be tracked separately per channel, so the guard only
// compares memories within one scope.
func TestTheGuardIsPerScopeNotGlobal(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the staging deploy needs the vault unsealed first",
		Type: "gotcha", Scope: "channel:a"})
	remember(t, s, RememberArgs{Title: "the staging deploy needs the vault unsealed first",
		Type: "gotcha", Scope: "channel:b"})
	if n, _ := s.count("SELECT COUNT(*) FROM memories"); n != 2 {
		t.Errorf("the guard reached across scopes: %d rows", n)
	}
}

// 'prod db host' and 'prod db port' overlap by half and are different facts: on a short
// title word overlap says little. Below the token floor the check does not run at all.
func TestShortTitlesAreExemptBecauseOverlapIsUnreliableThere(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "prod db host", Type: "infra"})
	remember(t, s, RememberArgs{Title: "prod db port", Type: "infra"})
	if n, _ := s.count("SELECT COUNT(*) FROM memories"); n != 2 {
		t.Errorf("blocked two short distinct titles: %d rows", n)
	}
	dupes, err := s.NearDuplicates(s.DB, "global", "prod db host")
	if err != nil {
		t.Fatal(err)
	}
	if len(dupes) != 0 {
		t.Errorf("the token floor did not apply: %v", dupes)
	}
}

// The store keeps ONE connection so SQLite writes serialise. A read issued through
// s.DB while the write transaction is open waits for a connection that transaction
// will never release, and the process hangs (not the query: the process). The guard
// runs inside Remember's transaction, so it must use the transaction.
//
// This test passes by completing at all: -timeout turns the regression into a failure.
func TestTheGuardDoesNotDeadlockAgainstItsOwnWriteTransaction(t *testing.T) {
	s := newTestStore(t)
	for i, title := range []string{
		"the first durable fact about how deployment works here",
		"a second unrelated claim concerning database connection pooling",
		"a third statement regarding the certificate renewal procedure",
	} {
		if err := s.Remember(io.Discard, RememberArgs{Title: title, Type: "infra",
			Scope: "global", Importance: 3, Source: "session"}); err != nil {
			t.Fatalf("write %d hung or failed: %v", i, err)
		}
	}
	if n, _ := s.count("SELECT COUNT(*) FROM memories"); n != 3 {
		t.Errorf("expected 3 rows, got %d", n)
	}
}

// A superseded memory is no longer live, so it must not block a later memory on the same
// subject (here, the fact that replaced it being restated).
func TestTheGuardIgnoresSupersededMemories(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the staging deploy needs the vault unsealed first",
		Type: "gotcha"})
	remember(t, s, RememberArgs{Title: "staging deploy no longer needs any vault unsealing",
		Type: "gotcha", Supersedes: 1})
	dupes, err := s.NearDuplicates(s.DB, "global", "the staging deploy needs the vault unsealed first")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dupes {
		if d.Memory.ID == 1 {
			t.Error("a superseded memory is still blocking writes")
		}
	}
}

// --- trust and the validity window --------------------------------------------------

// Trust records where a memory came from, so a reader can tell what this project established
// from what it was told. Unset, it is "stated"; a value that was passed is kept.
func TestTrustDefaultsToStatedAndRecordsOrigin(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "something we established here ourselves", Type: "infra"})
	remember(t, s, RememberArgs{Title: "a claim that arrived from another project entirely",
		Type: "infra", Trust: "external"})
	var stated, external string
	if err := s.DB.QueryRow("SELECT trust FROM memories WHERE id=1").Scan(&stated); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRow("SELECT trust FROM memories WHERE id=2").Scan(&external); err != nil {
		t.Fatal(err)
	}
	if stated != "stated" || external != "external" {
		t.Errorf("trust stored as %q and %q", stated, external)
	}
}

// A trust value is a small fixed vocabulary that readers filter on, so an unknown word becomes
// the default and case is folded, rather than storing a value no filter will ever match.
func TestAnUnknownTrustValueFallsBackRatherThanStoringNonsense(t *testing.T) {
	if got := NormTrust("banana"); got != DefaultTrust {
		t.Errorf("NormTrust(banana) = %q", got)
	}
	if got := NormTrust("IMPORTED"); got != "imported" {
		t.Errorf("NormTrust is case-sensitive: %q", got)
	}
}

// Confidence already discounts hearsay multiplicatively. Stacking trust on the same
// axis would double-count, and hide provenance behind a number instead of showing it.
func TestTrustDoesNotTouchTheScore(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "an external claim about nginx configuration here",
		Type: "infra", Trust: "external"})
	remember(t, s, RememberArgs{Title: "a locally stated claim about nginx tuning instead",
		Type: "infra", Trust: "stated", AllowDuplicate: true})
	res := recall(t, s, RecallArgs{Query: "nginx"})
	rows := res["results"].([]interface{})
	if len(rows) != 2 {
		t.Fatalf("expected both, got %d", len(rows))
	}
	a := rows[0].(map[string]interface{})["score"].(float64)
	b := rows[1].(map[string]interface{})["score"].(float64)
	if a != b {
		t.Errorf("trust changed the ranking: %v vs %v", a, b)
	}
}

// A fact with an end date ("until the end of March") stores that date, and a window whose
// start falls after its end is refused, because it would describe a fact that is never true.
func TestTheValidityWindowIsStoredAndOrderChecked(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the free tier lasts until the end of March",
		Type: "constraint", ValidTo: "2026-03-31"})
	var to string
	if err := s.DB.QueryRow("SELECT valid_to FROM memories WHERE id=1").Scan(&to); err != nil {
		t.Fatal(err)
	}
	if to != "2026-03-31" {
		t.Errorf("valid_to stored as %q", to)
	}
	err := s.Remember(io.Discard, RememberArgs{Title: "a window that runs backwards in time",
		Type: "constraint", Scope: "global", Importance: 3, Source: "session",
		ValidFrom: "2026-06-01", ValidTo: "2026-01-01"})
	if err == nil || !strings.Contains(err.Error(), "after") {
		t.Errorf("accepted a valid_from later than valid_to: %v", err)
	}
}

// Upgrading an old brain adds the trust and validity columns without guessing at values: an
// existing row IS 'stated', and an empty valid_to means "no known end", not "expired".
func TestMigratedRowsGetStatedTrustAndNoValidityWindow(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	cols, err := s.columnsOf("memories")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"trust", "valid_from", "valid_to"} {
		if !cols[c] {
			t.Fatalf("column %q was never added", c)
		}
	}
	n, err := s.count("SELECT COUNT(*) FROM memories WHERE trust='stated' AND valid_to=''")
	if err != nil {
		t.Fatal(err)
	}
	total, _ := s.count("SELECT COUNT(*) FROM memories")
	if n != total {
		t.Errorf("%d of %d migrated rows were not left as stated/open-ended", total-n, total)
	}
}
