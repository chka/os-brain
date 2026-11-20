package brain

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// The machine-wide index: published lessons every project can search, and each project's
// self-description (`describe`), which `global --find` searches and which a project can
// keep to itself.

func TestPublishMakesALessonSearchableAcrossProjects(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "Docker publishes past UFW",
		Body: "DOCKER-USER needs a default drop", Type: "gotcha"})
	if err := s.Publish(io.Discard, PublishArgs{ID: 1, Why: "every host hits this"}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Global(&buf, GlobalArgs{Recall: "docker ufw"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Docker publishes past UFW") {
		t.Errorf("published lesson is not searchable:\n%s", buf.String())
	}
}

// does, provides and keywords were once written to brain.json and dropped on the way to the
// index, so the one question the index exists to answer was asked against columns that
// stayed empty forever.
func TestDescribeReachesTheIndexNotJustBrainJSON(t *testing.T) {
	s := newTestStore(t)
	if err := s.Describe(io.Discard, DescribeArgs{Text: "a bookmark manager",
		Does: "archives web pages", Provides: "full-text search", Keywords: "archive, go",
		Stack: "Go and SQLite"}); err != nil {
		t.Fatal(err)
	}
	g := s.globalConnect(false)
	if g == nil {
		t.Fatal("no spine")
	}
	defer s.closeGlobal(g)
	var does, provides, keywords string
	if err := g.QueryRow("SELECT does, provides, keywords FROM projects WHERE root_id=?",
		hashRoot(s.Env.ProjectRoot())).Scan(&does, &provides, &keywords); err != nil {
		t.Fatal(err)
	}
	if does != "archives web pages" || provides != "full-text search" || keywords != "archive, go" {
		t.Errorf("index has does=%q provides=%q keywords=%q", does, provides, keywords)
	}
	// ...and --find can then actually match on them.
	var buf bytes.Buffer
	if err := s.Global(&buf, GlobalArgs{Find: "archives"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "archives web pages") {
		t.Errorf("--find cannot see the spec it just wrote:\n%s", buf.String())
	}
}

// An honest description of a sensitive project (medical, payroll) is itself sensitive.
// A "listed" project shares only that it exists, and the listing says it is redacted.
func TestAListedProjectSharesOnlyThatItExists(t *testing.T) {
	s := newTestStore(t)
	if err := s.Describe(io.Discard, DescribeArgs{Text: "a salary planner",
		Does: "records pay rises", Visibility: "listed"}); err != nil {
		t.Fatal(err)
	}
	g := s.globalConnect(true)
	if g == nil {
		t.Fatal("no spine")
	}
	// Pose as a different project reading the index. The index stores a hash of the
	// project directory rather than the directory, so "somebody else" is any other hash.
	if _, err := g.Exec("UPDATE projects SET root_id=? WHERE root_id=?",
		hashRoot(t.TempDir()), hashRoot(s.Env.ProjectRoot())); err != nil {
		t.Fatal(err)
	}
	s.closeGlobal(g)
	var buf bytes.Buffer
	if err := s.Global(&buf, GlobalArgs{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "a salary planner") || strings.Contains(out, "records pay rises") {
		t.Errorf("a listed project leaked its spec:\n%s", out)
	}
	if !strings.Contains(out, "listed only") {
		t.Errorf("the listing does not say it is redacted:\n%s", out)
	}
}
