package brain

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `init --dir` once went into every hook command inside double quotes, so a $(...) in it
// ran on every session open, and a project root with a space or a $( in it broke the hook
// or ran something else. --dir is now refused unless it is a plain relative path, before
// any hook is written, and every path placed in a hook command is single-quoted when it
// needs to be.
func TestInitDirCannotInjectIntoAHook(t *testing.T) {
	for _, dir := range []string{"b$(touch${IFS}PWNED)", "b`id`", `b"x`, "../outside", "a//b", "-x", "a b"} {
		root := initEnv(t)
		err := Init(io.Discard, root, InitArgs{Agents: "claude", Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "--dir") {
			t.Errorf("--dir %q was accepted: %v", dir, err)
		}
		if _, serr := os.Stat(filepath.Join(root, ".claude", "settings.json")); serr == nil {
			t.Errorf("--dir %q wrote hooks before refusing", dir)
		}
	}
	root := initEnv(t)
	if err := Init(io.Discard, root, InitArgs{Agents: "claude", Dir: "marketing/brain"}); err != nil {
		t.Fatalf("a plain nested --dir was refused: %v", err)
	}
	for _, c := range []struct{ in, want string }{
		{"/home/u/app/brain/x", "/home/u/app/brain/x"},
		{"/home/u/My App/brain/x", "'/home/u/My App/brain/x'"},
		{"/tmp/$(id)/x", "'/tmp/$(id)/x'"},
		{"/tmp/it's/x", `'/tmp/it'\''s/x'`},
	} {
		if got := invoke(c.in); got != c.want {
			t.Errorf("invoke(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}
