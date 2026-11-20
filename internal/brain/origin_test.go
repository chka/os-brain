package brain

// Tests for the origin block: the lines that tell an agent where the brain it is reading
// lives (which edition, which file, which machine).

import (
	"bytes"
	"strings"
	"testing"
)

// The edition is stated in words. Readers used to infer it from a line that was not there,
// and an inference drawn from a missing line does not survive a summariser.
func TestALocalEnvSaysLocalFileInWords(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.EnvBlock(&buf, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"LOCAL FILE", "on this machine only",
		"a local brain has no accounts"} {
		if !strings.Contains(out, want) {
			t.Errorf("a local env does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "HIVE") {
		t.Errorf("a local env called itself a hive:\n%s", out)
	}
}

// The JSON form carries the same facts for programs: the edition, the machine and the file.
func TestEnvJSONCarriesTheEditionAndTheMachine(t *testing.T) {
	s := newTestStore(t)
	facts := s.contextFacts()
	if facts["edition"] != "local" {
		t.Errorf("edition is %v, want local", facts["edition"])
	}
	if facts["machine"] == "" || facts["machine"] == nil {
		t.Error("the facts do not name the machine")
	}
	if _, ok := facts["db"]; !ok {
		t.Error("a local brain must still name its file")
	}
}

// The Origin type also describes a brief computed elsewhere on somebody else's behalf. Such a
// brief must not claim a machine or account: they would be the serving side's, and say
// nothing true about who is reading.
func TestARemoteOriginOmitsTheMachineAndTheUser(t *testing.T) {
	o := Origin{DB: "/srv/brain.db", Machine: "server", User: "root",
		Remote: true}
	for _, line := range o.Lines() {
		if strings.HasPrefix(strings.TrimSpace(line), "you") {
			t.Errorf("a remotely served origin claimed an identity: %q", line)
		}
	}
}

// OS_BRAIN_MACHINE replaces the host name, which is not always a name a person recognises.
func TestMachineNameIsOverridable(t *testing.T) {
	t.Setenv("OS_BRAIN_MACHINE", "the-tablet")
	if got := MachineName(); got != "the-tablet" {
		t.Errorf("OS_BRAIN_MACHINE ignored: %q", got)
	}
}
