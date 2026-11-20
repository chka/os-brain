package brain

import (
	"io"
	"strings"
	"testing"
)

// Scopes nest with ':' and knowledge flows inward only: a session working in
// channel:deploy:prod sees what was said in channel:deploy, channel and global, while a
// session in channel never sees what was said further in. A child's details leaking
// outward would be noise everywhere else.

func TestScopeAncestorsFlowInwardOnly(t *testing.T) {
	got := ScopeAncestors("channel:deploy:prod")
	want := []string{"channel:deploy:prod", "channel:deploy", "channel", "global"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v", got)
	}
	if len(ScopeAncestors("global")) != 1 {
		t.Error("global should not gain ancestors")
	}
}

// ScopeMatches is the one filter every scoped read uses: ancestors match, children do not,
// and a trailing * asks for a whole subtree.
func TestScopeMatchesSeesAncestorsAndSubtreesButNotChildren(t *testing.T) {
	cases := []struct {
		scope, row string
		want       bool
	}{
		{"global", "global", true}, {"global", "ops", false},
		{"ops:tls", "ops", true}, {"ops:tls", "global", true}, {"ops:tls", "web", false},
		{"ops:tls", "ops:tls:certs", false},
		{"ops*", "ops:tls:certs", true}, {"ops*", "web", false}, {"ops*", "global", true},
	}
	for _, c := range cases {
		if got := ScopeMatches(c.scope, c.row); got != c.want {
			t.Errorf("ScopeMatches(%q, %q) = %v", c.scope, c.row, got)
		}
	}
}

// The scope that was asked for ranks first among the ancestors it sees.
func TestScopesFlowInwardOnlyInRecall(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Scope: "global", Title: "global fact", Body: "deploy from global"})
	remember(t, s, RememberArgs{Scope: "channel", Title: "channel fact", Body: "deploy from channel"})
	remember(t, s, RememberArgs{Scope: "channel:deploy", Title: "inner fact", Body: "deploy from inner"})

	inner := titles(recall(t, s, RecallArgs{Query: "deploy", Scope: "channel:deploy", N: 10}))
	if len(inner) != 3 {
		t.Errorf("an inner scope sees its ancestors, got %v", inner)
	}
	if !strings.Contains(inner[0], "inner") {
		t.Errorf("the scope that was asked for must rank first, got %v", inner)
	}
	outer := titles(recall(t, s, RecallArgs{Query: "deploy", Scope: "channel", N: 10}))
	for _, ti := range outer {
		if strings.Contains(ti, "inner") {
			t.Errorf("an outer scope must not see its children, got %v", outer)
		}
	}
}

// The same rule holds for a browse, three levels deep.
func TestScopeNestingFlowsInwardOnly(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the global fact", Body: "x", Type: "note", Scope: "global"})
	remember(t, s, RememberArgs{Title: "the channel fact", Body: "x", Type: "note",
		Scope: "channel:deploy"})
	remember(t, s, RememberArgs{Title: "the inner fact", Body: "x", Type: "note",
		Scope: "channel:deploy:prod"})

	inner := titles(recall(t, s, RecallArgs{Query: "fact", Scope: "channel:deploy:prod",
		N: 10, Browse: true, NoTouch: true}))
	if len(inner) != 3 {
		t.Errorf("an inner scope should see its parents and global, got %v", inner)
	}
	outer := titles(recall(t, s, RecallArgs{Query: "fact", Scope: "channel:deploy",
		N: 10, Browse: true, NoTouch: true}))
	for _, tl := range outer {
		if strings.Contains(tl, "inner") {
			t.Errorf("a parent scope saw a child's memory: %v", outer)
		}
	}
}

// --- declared scopes ---------------------------------------------------------------------

// Writing into a scope nobody declared prints a nudge, because a typo in a scope files a
// memory where no later session will look. The nudge is only useful if it says how to
// declare the scope.
func TestUndeclaredScopeNudgeSaysHowToDeclareIt(t *testing.T) {
	s := newTestStore(t)
	cfg := s.Env.Config
	cfg.Scopes = map[string]string{"channel:*": "channels"}
	s.Env.Config = cfg
	out := captureStderr(t, func() { s.checkDeclared("weird:thing", "scope") })
	if !strings.Contains(out, `brain scope add weird:thing --desc "..."`) {
		t.Errorf("the nudge must name the command that fixes it:\n%s", out)
	}
}

// Declaring '*' would match every scope and silently switch the undeclared-scope check off.
func TestRegistryRefusesAPatternThatMatchesEverything(t *testing.T) {
	s := newTestStore(t)
	if err := s.Registry(io.Discard, RegistryArgs{Sub: "add", Name: "*"}); err == nil {
		t.Error("declared '*', which disables the undeclared-scope check entirely")
	}
}

// `global` is the default scope and the root every other scope hangs off, so it is
// declared by construction. A real install declared five channels and then had its very
// first memory, which went to the default scope, flagged as undeclared. A warning that
// fires on correct usage teaches people to ignore warnings.
func TestTheDefaultScopeIsNeverReportedAsUndeclared(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	st.Env.Config.Scopes = map[string]string{
		"channel:deploy": "...", "channel:security": "...",
	}
	var warned strings.Builder
	restore := captureWarnings(&warned)
	defer restore()

	st.checkDeclared(DefaultScope, "scope")
	if warned.Len() != 0 {
		t.Fatalf("the default scope was reported as undeclared:\n%s", warned.String())
	}
	st.checkDeclared("channel:nonsense", "scope")
	out := warned.String()
	if !strings.Contains(out, "channel:nonsense") {
		t.Fatalf("a genuinely undeclared scope stopped warning:\n%s", out)
	}
	// The nudge only works if it names a command that exists.
	if !strings.Contains(out, "os-brain scope add") {
		t.Fatalf("the hint does not name the real command:\n%s", out)
	}
}

// captureWarnings sends package warnings to w until the returned function is called.
func captureWarnings(w io.Writer) func() {
	prev := warnTo
	warnTo = w
	return func() { warnTo = prev }
}
