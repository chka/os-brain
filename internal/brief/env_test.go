package brief

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// `env` reprints the identity block at the top of the brief (time, project, host, paths),
// for a session that has been idle long enough for the clock to matter. It calls the
// brief's own code: an earlier hand-rolled version knew nothing about git, the last session
// or the project root.

func TestEnvReprintsTheBriefsIdentityBlock(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.EnvBlock(&buf, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"TESTPROJ — CONTEXT", "  now  ", "  project   testproj",
		"  brain     ", "Re-run `os-brain env` if this session has been idle"} {
		if !strings.Contains(out, want) {
			t.Errorf("env is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "@") {
		t.Errorf("the host line must name the user as user@host:\n%s", out)
	}
}

func TestEnvJSONIsTheSameFactsTheBriefCarries(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.EnvBlock(&buf, true); err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"project", "now", "weekday", "day_part", "timezone", "epoch",
		"project_root", "brain_dir", "db", "host", "user", "platform"} {
		if _, ok := obj[k]; !ok {
			t.Errorf("env JSON is missing %q", k)
		}
	}
}

// The platform is `uname -sr`, not GOOS/GOARCH: the kernel release is the half that
// identifies the machine.
func TestPlatformNameCarriesTheKernelRelease(t *testing.T) {
	name := platformName()
	if name == "" {
		t.Fatal("platformName returned nothing")
	}
	if strings.Contains(name, "amd64") || strings.Contains(name, "arm64") {
		t.Errorf("platform should be `uname -sr`, not the compile-time pair: %q", name)
	}
}
