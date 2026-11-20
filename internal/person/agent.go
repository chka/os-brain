package person

import (
	"os"
	"strings"
)

// Some commands change what every project reads as binding (axioms, standards, a release,
// probes that run shell commands), and those are a person's decision, never an agent's. This
// file answers "does this process look like it was started by an agent?". Every signal here
// is one a process of the same user can fake or drop: `script` gives it a terminal and
// `setsid -f` takes it out of the session's tree. It is a speed bump, documented as one.

func hasControllingTerminal() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// LooksLikeAgent reports whether this process looks like an agent's: the agent's own
// variables, a claude process above it, no controlling terminal, or a process tree an agent
// leaves when it takes itself out of its session.
func LooksLikeAgent() bool {
	if testOverride() {
		return os.Getenv("CLAUDE_CODE_SESSION_ID") != "" || os.Getenv(EnvTestAgent) == "1" ||
			os.Getenv(EnvTestDetached) == "1"
	}
	return agentByProcess() || detachedFrom(selfPID())
}

func agentByProcess() bool {
	if os.Getenv("CLAUDECODE") != "" || strings.TrimSpace(os.Getenv("CLAUDE_CODE_SESSION_ID")) != "" {
		return true
	}
	if suspiciousLineage(selfPID()) {
		return true
	}
	for _, pid := range ancestorPIDs() {
		if info, ok := readProc(pid); ok && strings.Contains(strings.ToLower(info.Comm), "claude") {
			return true
		}
	}
	return !hasControllingTerminal()
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true,
	"ksh": true, "env": true}

// subreapers adopt whatever leaves its parent: init, and the per-user service managers.
var subreapers = map[string]bool{"systemd": true, "launchd": true, "init": true}

// detachers are what a process that took itself out of a session's tree leaves at the top of
// its own chain: a shell or a wrapper the init system adopted, where a person's chain has a
// terminal emulator, sshd or login. Interpreters are here because each of them can do what
// `script` does in one line.
var detachers = map[string]bool{"script": true, "expect": true, "unbuffer": true, "socat": true,
	"setsid": true, "nohup": true, "timeout": true, "daemonize": true, "at": true, "atd": true,
	"cron": true, "crond": true, "perl": true, "ruby": true}

// ptyWrappers allocate a terminal for a command they drive themselves, so a session whose
// leader one of them started is a terminal nobody is sitting at.
var ptyWrappers = map[string]bool{"script": true, "expect": true, "unbuffer": true, "socat": true,
	"perl": true, "ruby": true, "empty": true}

func isPython(comm string) bool { return strings.HasPrefix(strings.ToLower(comm), "python") }

// detachedFrom reports whether pid's chain tops out, below init or a service manager, at a
// process no person sits behind: a shell, a wrapper, an interpreter, or pid itself. That is
// what `setsid -f`, a double fork, `systemd-run`, `at` and `nohup ... &` leave behind.
// tmux and screen servers are not caught: a person's own terminal looks the same.
func detachedFrom(pid int) bool {
	info, ok := readProc(pid)
	if !ok {
		return true
	}
	cur := pid
	for i := 0; i < 64; i++ {
		if info.PPID <= 1 {
			break
		}
		parent, ok := readProc(info.PPID)
		if !ok || subreapers[strings.ToLower(parent.Comm)] {
			break
		}
		cur, info = info.PPID, parent
	}
	return cur == pid || shells[info.Comm] || detachers[strings.ToLower(info.Comm)] || isPython(info.Comm)
}

// suspiciousLineage looks for an agent's marker in the starting environment of pid or any
// ancestor still readable, a chain that was re-parented away from every terminal, or a
// session whose leader a pty wrapper started.
func suspiciousLineage(pid int) bool {
	cur := pid
	for i := 0; i < 64; i++ {
		if procEnvHas(cur, "CLAUDECODE") || procEnvHas(cur, "CLAUDE_CODE_SESSION_ID") {
			return true
		}
		info, ok := readProc(cur)
		if !ok || info.PPID <= 1 {
			break
		}
		cur = info.PPID
	}
	if detachedFrom(pid) {
		return true
	}
	if self, ok := readProc(pid); ok && self.Session > 0 {
		if leader, ok := readProc(self.Session); ok {
			if p, ok := readProc(leader.PPID); ok && (ptyWrappers[strings.ToLower(p.Comm)] || isPython(p.Comm)) {
				return true
			}
		}
	}
	return false
}
