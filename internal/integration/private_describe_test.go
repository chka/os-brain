package integration_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/overview"
	"github.com/chka/os-brain/internal/project"
	"github.com/chka/os-brain/internal/store"
)

// `private` covers memories. The project's NAME and its one-line description are a row in
// the shared index that every other project's brief prints in full, and `private` once said
// nothing about it, so a project told it was private went on broadcasting its description.
// It must now say so and name the way out.
func TestPrivateSaysTheIndexStillCarriesTheDescription(t *testing.T) {
	root, st := projectWithEverything(t)
	if err := project.New(st).Share(devNull{}, project.ShareArgs{Apply: true}); err != nil {
		t.Fatal(err)
	}
	st = reopenShare(t, root)
	if err := overview.New(st).Describe(devNull{}, overview.DescribeArgs{
		Text: "a description naming somebody who did not agree to be named"}); err != nil {
		t.Fatal(err)
	}

	var buf strings.Builder
	if err := project.New(st).Private(&buf, project.PrivateArgs{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "naming somebody who did not agree") {
		t.Errorf("private does not say the index still carries the description:\n%s", out)
	}
	if !strings.Contains(out, "--visibility listed") {
		t.Errorf("private names the exposure without naming the way out of it:\n%s", out)
	}
}

// projectWithEverything is an installed project with rows in more tables than `absorb`
// knows about. The point of `share` is the tables nobody remembers, so the fixture has to
// have some: tombstones, measures, measure limits, tasks and the session log.
func projectWithEverything(t *testing.T) (string, *store.Store) {
	t.Helper()
	root, st := installedProject(t)
	remember(t, st, memory.RememberArgs{Scope: "channel:deploy", Type: "gotcha",
		Title: "the certificate renews on the edge box", Body: "certbot lives there"})
	if err := memory.New(st).Log(devNull{}, memory.LogArgs{Scope: "global", Kind: "deploy", Summary: "shipped v3"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO tombstones(hash,project,scope,title,reason,forgotten_at)
		 VALUES('h1','` + st.Env.Project + `','global','a wrong fact','it was wrong',1)`,
		`INSERT INTO measures(project,scope,key,label,value,unit,taken_on,taken_at,created_at)
		 VALUES('` + st.Env.Project + `','global','latency','p99',42,'ms','2026-08-01',1,1)`,
		`INSERT INTO measure_limits(project,key,low,high,better,unit,why,updated_at)
		 VALUES('` + st.Env.Project + `','latency',0,100,'lower','ms','slo',1)`,
		`INSERT INTO tasks(project,title,created_at,updated_at) VALUES('` + st.Env.Project + `','ship it',1,1)`,
		`INSERT INTO session_log(project,session,started_at,last_seen) VALUES('` + st.Env.Project + `','s1',1,2)`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return root, st
}

// reopenShare opens the project at root the way the next session would, so the test sees
// whichever database brain.json now points at.
func reopenShare(t *testing.T, root string) *store.Store {
	t.Helper()
	s, err := store.Open(store.NewEnv(filepath.Join(root, "brain")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// devNull is an io.Writer that discards everything.
type devNull struct{}

func (devNull) Write(p []byte) (int, error) { return len(p), nil }
