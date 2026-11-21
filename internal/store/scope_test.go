package store_test

import (
	"io"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/diag"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testutil"
)

// Scopes nest with ':' and knowledge flows inward only: a session working in
// channel:deploy:prod sees what was said in channel:deploy, channel and global, while a
// session in channel never sees what was said further in. A child's details leaking
// outward would be noise everywhere else.

func TestScopeAncestorsFlowInwardOnly(t *testing.T) {
	got := store.ScopeAncestors("channel:deploy:prod")
	want := []string{"channel:deploy:prod", "channel:deploy", "channel", "global"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v", got)
	}
	if len(store.ScopeAncestors("global")) != 1 {
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
		if got := store.ScopeMatches(c.scope, c.row); got != c.want {
			t.Errorf("ScopeMatches(%q, %q) = %v", c.scope, c.row, got)
		}
	}
}

// Writing into a scope nobody declared prints a nudge, because a typo in a scope files a
// memory where no later session will look. The nudge is only useful if it says how to
// declare the scope.
func TestUndeclaredScopeNudgeSaysHowToDeclareIt(t *testing.T) {
	s := newTestStore(t)
	cfg := s.Env.Config
	cfg.Scopes = map[string]string{"channel:*": "channels"}
	s.Env.Config = cfg
	out := testutil.CaptureStderr(t, func() { s.CheckDeclared("weird:thing", "scope") })
	if !strings.Contains(out, `brain scope add weird:thing --desc "..."`) {
		t.Errorf("the nudge must name the command that fixes it:\n%s", out)
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

	st.CheckDeclared(store.DefaultScope, "scope")
	if warned.Len() != 0 {
		t.Fatalf("the default scope was reported as undeclared:\n%s", warned.String())
	}
	st.CheckDeclared("channel:nonsense", "scope")
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
	return diag.SetOutput(w)
}
