//go:build windows

package brain

import "os/exec"

// No process groups here; RunProbe's WaitDelay is what bounds a probe whose child holds
// the pipe open after the parent is killed.
func killGroup(c *exec.Cmd) {}
