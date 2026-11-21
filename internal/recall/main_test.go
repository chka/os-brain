package recall

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/chka/os-brain/internal/measure"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/testguard"
	"github.com/chka/os-brain/internal/testutil"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }

// The tests were written inside one package, against one Store with every command on it.
// The names below give them the same vocabulary here, so the test bodies did not change
// when the package was split. Remember and Measure are methods only in the test build: the
// tests seed memories and readings through the ordinary commands, which sit above recall
// and do not import it, so there is no cycle.

type (
	RememberArgs = memory.RememberArgs
	MeasureArgs  = measure.MeasureArgs
)

// seedTitles is the shared fixture of memory titles distinct enough to all be stored.
var seedTitles = testutil.SeedTitles

func (s *Store) Remember(w io.Writer, a RememberArgs) error {
	return memory.New(s.Store).Remember(w, a)
}

func (s *Store) Measure(w io.Writer, a MeasureArgs) error {
	return measure.New(s.Store).Measure(w, a)
}

// newTestStore is testutil.NewStore with recall on top.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(testutil.NewStore(t))
}

// remember stores a memory through the ordinary command, filling in the defaults the CLI
// would supply, and returns what the command printed.
func remember(t *testing.T, s *Store, a RememberArgs) string {
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
	if err := s.Remember(&buf, a); err != nil {
		t.Fatalf("remember %q: %v", a.Title, err)
	}
	return buf.String()
}

// recall runs recall with --json and returns the decoded envelope.
func recall(t *testing.T, s *Store, a RecallArgs) map[string]interface{} {
	t.Helper()
	a.JSON = true
	var buf bytes.Buffer
	if err := s.Recall(&buf, a); err != nil {
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

// mustJSON runs a command that writes --json output and returns the decoded object.
func mustJSON(t *testing.T, fn func(io.Writer) error) map[string]interface{} {
	t.Helper()
	return testutil.MustJSON(t, fn)
}
