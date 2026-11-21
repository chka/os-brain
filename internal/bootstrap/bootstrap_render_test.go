package bootstrap

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/store"
)

// bootstrap lists this project's past session transcripts so a new brain can be seeded
// from them, and `bootstrap --read` marks transcripts as done. A mark may only name a
// transcript of this project on this machine: a mark nobody can match to a file would
// report progress that does not exist. The listing renders from the marks it is handed,
// even with none at all.
func TestBootstrapListsAndReadsOnlyThisProjectsTranscripts(t *testing.T) {
	var buf bytes.Buffer
	env := store.Env{Here: filepath.Join(t.TempDir(), "brain"), Project: "p"}
	if err := RenderBootstrap(&buf, env, BootstrapArgs{IncludeMoved: true}, 0, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "BOOTSTRAP") {
		t.Errorf("no listing: %s", buf.String())
	}
	if _, err := BootstrapReadRefs(env, []string{"/not/a/transcript.jsonl"}); err == nil {
		t.Error("a path that is not this project's transcript was accepted")
	}
}
