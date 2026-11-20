package brain

// Tests for the compatibility register: how an older client decides whether it may open a
// database written by a newer one.
//
// Each database records the lowest client schema that can safely use it
// (min_client_schema in meta, or gmeta for the spine). An older client opens a newer
// database when that floor is at or below its own version, and refuses with the fix when it
// is not, or when the database is newer and records no floor at all.

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The floor is one number, not a history: it moves when a migration changes the meaning of
// stored data and sits still through every additive version after that. The two ways it
// can be wrong are both mechanical, so they are checked here.
func TestTheCompatibilityFloorIsSaneAgainstTheSchema(t *testing.T) {
	if MinCompatibleSchema > SchemaVersion {
		t.Errorf("the floor (v%d) is above the schema itself (v%d): nothing could open it",
			MinCompatibleSchema, SchemaVersion)
	}
	if MinCompatibleSchema < 1 {
		t.Errorf("the floor must name a real schema version, got %d", MinCompatibleSchema)
	}
	if MinCompatibleGlobalSchema > GlobalSchemaVersion {
		t.Errorf("the spine floor (v%d) is above the spine schema (v%d)",
			MinCompatibleGlobalSchema, GlobalSchemaVersion)
	}
}

// reopen stamps the store with the given schema version and floor, closes it and opens it
// again. That is the only way to play "a client older than this database" without a second
// binary. A minClientSchema of 0 removes the floor, as in a database written before the
// register existed.
func reopen(t *testing.T, s *Store, version, minClientSchema int) (*Store, error) {
	t.Helper()
	here, dbPath := s.Env.Here, s.Env.DBPath
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES('schema_version',?)",
		strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
	if minClientSchema == 0 {
		if _, err := s.DB.Exec("DELETE FROM meta WHERE k='min_client_schema'"); err != nil {
			t.Fatal(err)
		}
	} else if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES('min_client_schema',?)",
		strconv.Itoa(minClientSchema)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal(err)
	}
	return Open(NewEnv(here))
}

// The old rule refused any newer database, and it cost more than it bought: upgrading one
// project's brain locked every other client out of a file whose extra columns they would
// simply have ignored. A newer database whose floor this client meets opens, keeps its
// version stamp and accepts writes.
func TestANewerButCompatibleDatabaseOpens(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "a fact written before the upgrade", Type: "infra"})

	got, err := reopen(t, s, SchemaVersion+3, SchemaVersion)
	if err != nil {
		t.Fatalf("a compatible newer database was refused: %v", err)
	}
	defer got.Close()
	if n, _ := got.count("SELECT COUNT(*) FROM memories"); n != 1 {
		t.Errorf("the memory did not survive: %d", n)
	}
	// Restamping the version down would tell the next reader the newer columns are not there.
	if v := got.metaStr("schema_version"); v != strconv.Itoa(SchemaVersion+3) {
		t.Errorf("the client rewrote the schema version to %q", v)
	}
	// And it stays usable, not merely openable.
	remember(t, got, RememberArgs{Title: "a fact written by the older client", Type: "infra"})
	if n, _ := got.count("SELECT COUNT(*) FROM memories"); n != 2 {
		t.Errorf("an older client could open it but not write to it: %d", n)
	}
}

// A database whose floor is above this client is past a compatibility break. The refusal
// has to say what is needed and how to get it.
func TestADatabasePastABreakIsRefusedWithTheUpgrade(t *testing.T) {
	s := newTestStore(t)
	_, err := reopen(t, s, SchemaVersion+3, SchemaVersion+1)
	if err == nil {
		t.Fatal("a database past a compatibility break was opened")
	}
	for _, want := range []string{"needs a client understanding", "install a newer os-brain release"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must say how to fix it, got: %v", err)
		}
	}
}

// A missing floor means "written before the register existed", not "safe". A client cannot
// reason about a break introduced after it was built, so a newer database with no floor is
// refused.
func TestANewerDatabaseWithNoRegisterIsRefused(t *testing.T) {
	s := newTestStore(t)
	_, err := reopen(t, s, SchemaVersion+3, 0)
	if err == nil {
		t.Fatal("a newer database with no declared requirement was opened on a guess")
	}
	if !strings.Contains(err.Error(), "predates the compatibility register") {
		t.Errorf("the refusal should name the reason, got: %v", err)
	}
}

// Brains written before the register existed gain it on the first open by a client at
// their own version, which is the only client entitled to say what that version needs.
func TestAStoreAtThisVersionGainsTheRegister(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("DELETE FROM meta WHERE k='min_client_schema'"); err != nil {
		t.Fatal(err)
	}
	got, err := reopen(t, s, SchemaVersion, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if v := got.metaStr("min_client_schema"); v != strconv.Itoa(MinCompatibleSchema) {
		t.Errorf("the register was not backfilled: %q", v)
	}
}

// A store migrated forward declares the floor of the version it now has.
func TestAMigratedStoreDeclaresItsRequirement(t *testing.T) {
	s := newTestStore(t)
	got, err := reopen(t, s, 9, 6)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	if v := got.metaStr("schema_version"); v != strconv.Itoa(SchemaVersion) {
		t.Fatalf("the store did not migrate forward: %q", v)
	}
	// Stamped in the same transaction as the version, so the two cannot disagree.
	if v := got.metaStr("min_client_schema"); v != strconv.Itoa(MinCompatibleSchema) {
		t.Errorf("a migrated store did not declare what it now needs: %q", v)
	}
}

// The spine is the file every project on the machine opens, so the old strict rule cut the
// most off here, and silently, because the built-in axiom fallback looks the same as the
// real thing. The spine records its own floor.
func TestTheSpineCarriesTheRegisterToo(t *testing.T) {
	s := newTestStore(t)
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	var raw string
	if err := g.QueryRow("SELECT v FROM gmeta WHERE k='min_client_schema'").Scan(&raw); err != nil {
		t.Fatalf("the spine did not declare its requirement: %v", err)
	}
	if raw != strconv.Itoa(MinCompatibleGlobalSchema) {
		t.Errorf("spine register is %q", raw)
	}
	s.closeGlobal(g)
}

// Migration bugs have been invisible against a fresh database before, so the register is
// also checked on the genuine v1 fixture from migrate_test.go.
func TestTheRegisterSurvivesAnUpgradeOfAnOlderRealDatabase(t *testing.T) {
	s := openMigrated(t, makeV1(t))
	if v := s.metaStr("min_client_schema"); v != strconv.Itoa(MinCompatibleSchema) {
		t.Errorf("a v1 database upgraded without declaring what it now needs: %q", v)
	}
}
