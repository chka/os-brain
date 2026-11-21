package recall

import (
	"strings"
	"testing"
)

// Scopes nest with ':' and knowledge flows inward only: a session working in
// channel:deploy:prod sees what was said in channel:deploy, channel and global, while a
// session in channel never sees what was said further in. A child's details leaking
// outward would be noise everywhere else.

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
