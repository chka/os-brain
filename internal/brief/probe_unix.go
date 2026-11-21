//go:build !windows

package brief

import (
	"os/exec"
	"syscall"
)

// killGroup makes a timed-out probe actually stop. `sh -c "sleep 5; echo done"` with a
// one-second timeout took five seconds: the context killed the shell, but `sleep` inherited
// the pipe and CombinedOutput blocked until it exited on its own. So the timeout was not a
// timeout, and every probe that spawns a child would leak one into the session.
//
// The probe runs in its own process group and the whole group is signalled. WaitDelay in
// RunProbe is the portable backstop for the platform where this is a no-op.
func killGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
