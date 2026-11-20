package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The brain runs git in whatever directory a session opens (status, log, remotes for the brief).
// A repository's own .git/config can name programs for git to run, so a cloned repository
// could execute code the moment an agent looks at it. gitsafe runs git with that config
// neutralised; these tests prove it against a repository built to attack it.

// hostileRepo is a repository whose own .git/config names a program for every hook a plain
// status or log would reach: fsmonitor, a clean filter, signature display. Each touches a
// marker beside the repository.
func hostileRepo(t *testing.T) (repo, marks string) {
	t.Helper()
	base := t.TempDir()
	repo, marks = filepath.Join(base, "repo"), filepath.Join(base, "marks")
	for _, d := range []string{repo, marks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	must(t, os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=ev.il\n"), 0o644))
	run("add", ".")
	run("commit", "-qm", "init")
	gpg := filepath.Join(marks, "gpg.sh")
	must(t, os.WriteFile(gpg, []byte("#!/bin/sh\ntouch "+filepath.Join(marks, "GPG")+"\n"), 0o755))
	run("config", "core.fsmonitor", "touch "+filepath.Join(marks, "FSMONITOR")+"; false")
	run("config", "filter.ev.il.clean", "touch "+filepath.Join(marks, "CLEAN")+"; cat")
	run("config", "log.showSignature", "true")
	run("config", "gpg.program", gpg)
	run("config", "core.hooksPath", marks)
	must(t, os.WriteFile(filepath.Join(marks, "post-index-change"),
		[]byte("#!/bin/sh\ntouch "+filepath.Join(marks, "HOOK")+"\n"), 0o755))
	// Same size, new mtime: git must compare content, which is when it runs the clean filter.
	later := time.Now().Add(2 * time.Second)
	must(t, os.Chtimes(filepath.Join(repo, "f.txt"), later, later))
	return repo, marks
}

// must fails the test on an error from a setup step.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// ran lists which of the hostile programs left their marker, i.e. which ones git executed.
func ran(marks string) []string {
	var got []string
	for _, m := range []string{"FSMONITOR", "CLEAN", "GPG", "HOOK"} {
		if _, err := os.Stat(filepath.Join(marks, m)); err == nil {
			got = append(got, m)
		}
	}
	return got
}

// Every git command the brain issues runs against the hostile repository and none of its
// programs may run. The second half proves the fixture is still an attack: plain git on the
// same repository does run them, so a pass means gitsafe stopped them, not that git changed.
func TestARepositorysOwnConfigRunsNothing(t *testing.T) {
	repo, marks := hostileRepo(t)
	ctx := context.Background()
	for _, args := range [][]string{
		{"rev-parse", "--is-inside-work-tree"},
		{"status", "--porcelain"},
		{"log", "-1", "--format=%h %s"},
		{"log", "-1", "--format=%cr"},
		{"rev-list", "--count", "HEAD"},
		{"check-ignore", "-q", filepath.Join(repo, "f.txt")},
		{"remote"},
	} {
		_, _ = Output(ctx, repo, args...)
	}
	if got := ran(marks); len(got) > 0 {
		t.Fatalf("the repository's own config executed %v", got)
	}

	// The fixture has to be hostile for the assertion above to mean anything.
	cmd := exec.Command("git", "-C", repo, "status", "--porcelain")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	_ = cmd.Run()
	if got := ran(marks); len(got) == 0 {
		t.Fatal("plain git status ran nothing; the fixture no longer reproduces the attack")
	}
}

// GIT_DIR inherited from the environment would point every command at another repository
// (the hostile one here) regardless of the directory asked about. gitsafe clears it, so a
// directory outside any repository never answers as one.
func TestGitDirInTheEnvironmentCannotRedirect(t *testing.T) {
	repo, _ := hostileRepo(t)
	other := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	out, _ := Output(context.Background(), other, "rev-parse", "--is-inside-work-tree")
	if strings.TrimSpace(out) == "true" {
		t.Fatal("GIT_DIR made a directory outside any repository answer as one")
	}
}
