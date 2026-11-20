package brain

import "testing"

// --- recall's term coverage -------------------------------------------------------

// Recall's coverage rule: how many of the query's terms a row must contain to count as a
// match, and how much a partial match is worth. Requiring every term makes a long query miss
// rows that hold most of it, so the floor loosens, but only from three terms up: with one or
// two terms, every term is the whole question.
func TestCoverageFloorLoosensOnlyFromThreeTerms(t *testing.T) {
	for n, want := range map[int]int{0: 0, 1: 1, 2: 2, 3: 2, 4: 3, 5: 3, 6: 4, 10: 6} {
		if got := CoverageFloor(n); got != want {
			t.Errorf("CoverageFloor(%d) = %d, want %d", n, got, want)
		}
	}
	if CoverageWeight(4, 4) != 1.0 || CoverageWeight(3, 4) != 0.75 {
		t.Errorf("CoverageWeight: full must be 1 and 3 of 4 must be 0.75")
	}
	// A row the engine returned always held something; zero is a counting failure and
	// must not zero the score.
	if CoverageWeight(0, 4) <= 0 {
		t.Errorf("CoverageWeight(0, n) must stay positive")
	}
}

// --- what the brain holds, by category ---------------------------------------------

// `coverage` counts live memories per type. A superseded memory is history, not knowledge,
// so it is not counted, and the largest category comes first.
func TestCoverageCountsByTypeExcludesSupersededOrdersByCount(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "infra", Scope: "global", Title: "prod db host is 10.0.0.5"})
	remember(t, s, RememberArgs{Type: "infra", Scope: "global", Title: "prod db port is 5432"})
	remember(t, s, RememberArgs{Type: "gotcha", Scope: "global", Title: "docker bypasses ufw"})

	// A superseded memory must not be counted: remember a fact then supersede it with a
	// second remember citing --supersedes.
	remember(t, s, RememberArgs{Type: "decision", Scope: "global", Title: "use rust for the client"})
	var oldID int64
	if err := s.DB.QueryRow("SELECT id FROM memories WHERE title=? AND project=?",
		NormTitle("use rust for the client"), s.Env.Project).Scan(&oldID); err != nil {
		t.Fatalf("finding the memory to supersede: %v", err)
	}
	remember(t, s, RememberArgs{Type: "decision", Scope: "global", Title: "use go for the client",
		Supersedes: oldID})

	cats, err := s.Coverage()
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	byName := map[string]Category{}
	for _, c := range cats {
		byName[c.Name] = c
	}
	if byName["infra"].Count != 2 {
		t.Fatalf("want 2 infra, got %+v", byName["infra"])
	}
	if byName["gotcha"].Count != 1 {
		t.Fatalf("want 1 gotcha, got %+v", byName["gotcha"])
	}
	// The superseded "use rust" row must not inflate decision's count beyond the one live
	// successor.
	if byName["decision"].Count != 1 {
		t.Fatalf("superseded memory was counted: decision=%+v", byName["decision"])
	}
	// Ordered by count, descending.
	for i := 1; i < len(cats); i++ {
		if cats[i].Count > cats[i-1].Count {
			t.Fatalf("Coverage not ordered by count: %+v", cats)
		}
	}
}

// An empty project has no categories, rather than a list of zero counts.
func TestCoverageEmptyProject(t *testing.T) {
	s := newTestStore(t)
	cats, err := s.Coverage()
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	if len(cats) != 0 {
		t.Fatalf("want no categories on an empty project, got %+v", cats)
	}
}

// A miss must never come back empty: an agent told "nothing" concludes the brain knows
// nothing on the subject, when it may hold the answer under words the query did not use.
// Neighbourhood exists to show what the brain does hold, so a total miss still lists it.
func TestNeighbourhoodReturnsSomethingOnATotalMiss(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "infra", Scope: "global", Title: "prod db host is 10.0.0.5"})
	remember(t, s, RememberArgs{Type: "gotcha", Scope: "global", Title: "docker bypasses ufw"})

	near, err := s.Neighbourhood("xqzzy plonk wibble", 5)
	if err != nil {
		t.Fatalf("Neighbourhood: %v", err)
	}
	if len(near) == 0 {
		t.Fatal("a miss must still return the categories held here, not nothing")
	}
}

// The nearest category is the one whose name matches the query's words, even when another
// category holds more rows: size is a tie-breaker, not the answer.
func TestNeighbourhoodPrefersLexicalMatch(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Type: "infra", Scope: "global", Title: "prod db host is 10.0.0.5"})
	remember(t, s, RememberArgs{Type: "infra", Scope: "global", Title: "prod db port is 5432"})
	remember(t, s, RememberArgs{Type: "gotcha", Scope: "global", Title: "docker bypasses ufw"})
	remember(t, s, RememberArgs{Type: "gotcha", Scope: "global", Title: "redis was hijacked"})
	remember(t, s, RememberArgs{Type: "gotcha", Scope: "global", Title: "a third gotcha to outweigh infra by count"})

	near, err := s.Neighbourhood("infra facts", 1)
	if err != nil {
		t.Fatalf("Neighbourhood: %v", err)
	}
	if len(near) != 1 || near[0].Name != "infra" {
		t.Fatalf("want infra as the nearest category despite gotcha having more rows, got %+v", near)
	}
}
