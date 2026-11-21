package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/install"
)

// `init` without --agent asks the person which agent to wire. The menu takes a number, a
// host key or a host's display name, in any case; an answer that names nothing is asked
// again; and the last number means "I will do it myself", which wires no host.
func TestAskAgentAnswers(t *testing.T) {
	root := t.TempDir()
	self := strconv.Itoa(len(install.Hosts()) + 1)
	cases := []struct {
		in     string
		key    string
		manual bool
	}{
		{"1\n", "claude", false},
		{"2\n", "codex", false},
		{"cursor\n", "cursor", false},
		{"Gemini CLI\n", "gemini", false},
		{"nope\n\n3\n", "cursor", false}, // two answers naming nothing, then a valid one
		{self + "\n", "", true},
	}
	for _, c := range cases {
		var out bytes.Buffer
		key, manual, err := askAgent(strings.NewReader(c.in), &out, root)
		if err != nil || key != c.key || manual != c.manual {
			t.Errorf("%q: got (%q, %v, %v), want (%q, %v)", c.in, key, manual, err, c.key, c.manual)
		}
	}
}

// Running out of input, a number off the menu, or more wrong answers than the prompt allows
// must refuse before init writes anything. Guessing an agent would wire hooks into a host
// the person never chose.
func TestAskAgentNoAnswerRefuses(t *testing.T) {
	for _, in := range []string{"", "0\n", "99\n", strings.Repeat("x\n", agentPromptMax+1)} {
		var out bytes.Buffer
		if _, _, err := askAgent(strings.NewReader(in), &out, t.TempDir()); err == nil {
			t.Errorf("%q: chose an agent from an answer that names none", in)
		}
	}
}
