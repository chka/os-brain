package person

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// procInfo is one process: its parent, its command name and, where the OS says, its
// session id. Read from /proc on Linux and from the system's ps elsewhere.
type procInfo struct {
	PPID    int
	Comm    string
	Session int
}

// psPaths are where the system's ps lives. Never PATH: on macOS every ancestry decision
// comes from ps, and PATH is one variable an agent can set for one command.
var psPaths = []string{"/bin/ps", "/usr/bin/ps"}

// rootBinary is the first of paths that is a root-owned regular file nobody else can write.
func rootBinary(paths []string) string {
	for _, p := range paths {
		st, err := os.Stat(p)
		if err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o022 == 0 && ownedByRoot(st) {
			return p
		}
	}
	return ""
}

// ps runs the system's ps by absolute path with an empty environment.
func ps(args ...string) ([]byte, error) {
	bin := rootBinary(psPaths)
	if bin == "" {
		return nil, errors.New("no root-owned ps at /bin/ps or /usr/bin/ps")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = []string{}
	cmd.Dir = "/"
	return cmd.Output()
}

func readProc(pid int) (procInfo, bool) {
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		return parseProcStat(string(data))
	}
	return readProcPS(pid)
}

// parseProcStat reads one /proc/<pid>/stat line.
func parseProcStat(s string) (procInfo, bool) {
	// comm is in parentheses and may itself hold spaces or parentheses, so the fields
	// after it are counted from the LAST closing parenthesis.
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return procInfo{}, false
	}
	fields := strings.Fields(s[close+1:])
	// fields[0] is state (field 3); ppid is field 4 and session field 6. A real stat line
	// has some fifty fields, so a short one is not a stat line at all.
	if len(fields) < 20 {
		return procInfo{}, false
	}
	ppid, _ := strconv.Atoi(fields[1])
	sid, _ := strconv.Atoi(fields[3])
	return procInfo{PPID: ppid, Comm: s[open+1 : close], Session: sid}, true
}

// readProcPS asks the system's ps where there is no /proc (macOS). lstart is requested
// although it is not used: it is five words wide, and the comm parsing counts past it.
func readProcPS(pid int) (procInfo, bool) {
	out, err := ps("-o", "ppid=", "-o", "lstart=", "-o", "comm=", "-p", strconv.Itoa(pid))
	if err != nil {
		return procInfo{}, false
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) < 7 {
		return procInfo{}, false
	}
	ppid, _ := strconv.Atoi(f[0])
	return procInfo{PPID: ppid, Comm: filepath.Base(strings.Join(f[6:], " "))}, true
}

// procEnvHas reads a process's starting environment, where the OS exposes it (Linux).
func procEnvHas(pid int, name string) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	for _, kv := range strings.Split(string(raw), "\x00") {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

func selfPID() int { return os.Getpid() }

// ancestorPIDs is this process's chain upward, nearest first, excluding itself and init.
func ancestorPIDs() []int {
	var out []int
	pid := selfPID()
	seen := map[int]bool{}
	for i := 0; i < 64; i++ {
		info, ok := readProc(pid)
		if !ok || info.PPID <= 1 || seen[info.PPID] {
			break
		}
		seen[info.PPID] = true
		out = append(out, info.PPID)
		pid = info.PPID
	}
	return out
}
