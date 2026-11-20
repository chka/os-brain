package person

import (
	"flag"
	"os"
)

// A test cannot rely on the process tree it happens to run under, and the sweep in tools/
// has to play the person at the terminal. Both turn on test mode with EnvTest=1, and only a
// `go test` binary or a binary built with `-tags sweep` honours it. A release build ignores
// all of these variables, so an agent cannot set one to pass for a person.
const (
	EnvTest         = "OS_BRAIN_TEST"
	EnvTestAgent    = "OS_BRAIN_TEST_AGENT"
	EnvTestDetached = "OS_BRAIN_TEST_DETACHED"
)

func overridesAllowed() bool { return flag.Lookup("test.v") != nil || sweepBuild }

func testOverride() bool { return overridesAllowed() && os.Getenv(EnvTest) == "1" }

// ReplaceTerminal makes every later prompt read from open instead of /dev/tty, and returns
// the function that puts the real terminal back. It is how a test plays the person. Only a
// test or sweep binary may call it: in a release build it panics, so no code path can swap
// the terminal a person confirms on.
func ReplaceTerminal(open func() (TTY, error)) (restore func()) {
	if !overridesAllowed() {
		panic("person.ReplaceTerminal outside a test or sweep binary")
	}
	prev := openDevTTY
	openDevTTY = open
	return func() { openDevTTY = prev }
}
