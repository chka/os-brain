package brain

import (
	"bytes"
	"strings"
	"testing"
)

// Several projects can share one store file. Each project reads and writes its own rows;
// another project's rows are readable only on request, only when that project registered
// and is not private, and never writable.

// Every refusal reads exactly like a missing id, so the error is not an oracle for what
// other projects hold.
func TestAnotherProjectsMemoryIsReadableButNotWritable(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.DB.Exec("INSERT INTO memories(project,scope,mem_type,title,body,source,"+
		"importance,pinned,confidence,source_date,happened_on,hash,created_at,updated_at) "+
		"VALUES('otherproj','global','note','theirs','x','session',3,0,'reported','','',?,?,?)",
		"otherhash", Now(), Now()); err != nil {
		t.Fatal(err)
	}
	missing := NoMemory(1).Error()
	if _, err := s.Readable(1, true); err == nil || err.Error() != missing {
		t.Errorf("an unregistered project's memory was readable: %v", err)
	}
	other := *s
	other.Env.Project = "otherproj"
	if err := other.RegisterProject(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Readable(1, true); err != nil {
		t.Errorf("a registered project's memory should be readable across projects: %v", err)
	}
	if _, err := s.Own(1, "read"); err == nil || err.Error() != missing {
		t.Errorf("a by-id read without asking across projects reached another project: %v", err)
	}
	if _, err := s.Own(1, "delete"); err == nil || err.Error() != missing {
		t.Errorf("another project's memory must not be deletable, and must answer as missing: %v", err)
	}
	if _, err := s.Own(99, "delete"); err == nil || err.Error() != NoMemory(99).Error() {
		t.Errorf("a missing id answers differently: %v", err)
	}
	if n := len(recall(t, s, RecallArgs{Query: "theirs", Browse: true})["results"].([]interface{})); n != 0 {
		t.Errorf("project filter leaked %d rows", n)
	}
}

// --all-projects is an allow-list: a project must have registered and not be private. The
// first version was a deny-list ("is this project marked private?"), which is silently
// false for a project that has not yet written its index row, and a project only writes
// that row when it runs brief or describe. A store somebody had just absorbed was
// unprotected exactly while nobody had looked at it.
func TestAPrivateProjectIsNotReadableByAWidenedSearch(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "the ordinary work fact", Body: "x", Type: "infra"})

	other := *s
	other.Env.Project = "sensitive"
	other.Env.Private = true
	remember(t, &other, RememberArgs{Title: "salary bands and the payroll schedule",
		Body: "payroll detail", Type: "knowledge"})
	other.RegisterProject()
	s.RegisterProject()

	got := titles(recall(t, s, RecallArgs{Query: "salary payroll", AllProjects: true,
		N: 10, Strict: true, NoTouch: true}))
	for _, tl := range got {
		if strings.Contains(tl, "salary") {
			t.Errorf("a private project was readable through --all-projects: %v", got)
		}
	}
	mine := titles(recall(t, &other, RecallArgs{Query: "salary", N: 5, NoTouch: true}))
	if len(mine) != 1 {
		t.Errorf("a private project cannot read its own memories: %v", mine)
	}

	// An unregistered project is not readable either: "no row yet" is not "open".
	third := *s
	third.Env.Project = "unregistered"
	remember(t, &third, RememberArgs{Title: "written before anybody looked", Body: "y",
		Type: "note"})
	got = titles(recall(t, s, RecallArgs{Query: "before anybody", AllProjects: true,
		N: 10, Strict: true, NoTouch: true}))
	for _, tl := range got {
		if strings.Contains(tl, "before anybody") {
			t.Errorf("a project with no index row was readable through --all-projects: %v", got)
		}
	}
}

// In a shared store, another project's preferences used to silence this project's
// "standing preference" nudge, and the idle clock was whichever project had been typed in
// last. The prompt hook reads only this project's rows.
func TestThePromptHookReadsOnlyThisProject(t *testing.T) {
	s := newTestStore(t)
	home := s.Env.Project
	s.Env.Project = "elsewhere"
	if err := s.Prefer(&bytes.Buffer{}, PreferArgs{
		Text: "always run the tests before committing", Why: "theirs"}); err != nil {
		t.Fatal(err)
	}
	_ = hook(t, s, "a prompt in the other project, long enough", HookRecallArgs{})
	s.Env.Project = home
	if _, err := s.DB.Exec("INSERT OR REPLACE INTO meta(k,v) VALUES(?,?)",
		s.projectKey(s.sessionKey("last_prompt_at")), Now()-7200); err != nil {
		t.Fatal(err)
	}
	out := hook(t, s, "from now on always run the tests before committing anything", HookRecallArgs{})
	if !strings.Contains(out, "standing preference") {
		t.Errorf("another project's preference silenced this project's nudge:\n%s", out)
	}
	if !strings.Contains(out, "clock has moved") {
		t.Errorf("the other project's prompt reset this project's idle clock:\n%s", out)
	}
}
