package store_test

// The external tests seed memories through the ordinary remember command and read them back
// through recall, which both live above the store, so they are a package of their own that
// may import them. These helpers give them the names the rest of the suite uses.

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/recall"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/testutil"
)

// newTestStore is a brain whose every path points into a throwaway directory.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	return testutil.NewStore(t)
}

// newGitTestStore is a throwaway brain in a real git repository with one commit, and the
// repository directory.
func newGitTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	return testutil.NewGitStore(t)
}

func commit(t *testing.T, dir, name string) {
	t.Helper()
	testutil.Commit(t, dir, name)
}

// remember stores a memory through the ordinary command, filling in the defaults the CLI
// would supply, and returns what the command printed.
func remember(t *testing.T, s *store.Store, a memory.RememberArgs) string {
	t.Helper()
	var buf bytes.Buffer
	if a.Type == "" {
		a.Type = "note"
	}
	if a.Scope == "" {
		a.Scope = "global"
	}
	if a.Importance == 0 {
		a.Importance = 3
	}
	if a.Source == "" {
		a.Source = "session"
	}
	if err := memory.New(s).Remember(&buf, a); err != nil {
		t.Fatalf("remember %q: %v", a.Title, err)
	}
	return buf.String()
}

// recallJSON runs recall with --json and returns the decoded envelope.
func recallJSON(t *testing.T, s *store.Store, a recall.RecallArgs) map[string]interface{} {
	t.Helper()
	a.JSON = true
	var buf bytes.Buffer
	if err := recall.New(s).Recall(&buf, a); err != nil {
		t.Fatalf("recall %q: %v", a.Query, err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("recall json: %v (%s)", err, buf.String())
	}
	return out
}

// titles lists the titles of a recall envelope's results, in rank order.
func titles(res map[string]interface{}) []string {
	var out []string
	for _, r := range res["results"].([]interface{}) {
		out = append(out, r.(map[string]interface{})["title"].(string))
	}
	return out
}
