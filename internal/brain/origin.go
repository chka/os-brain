package brain

import (
	"os"
	"strings"
)

// Origin answers the questions a session must never have to infer: is there a brain here,
// what kind it is, and which machine and account is answering. They are stated in words in
// the one output every session reads (the brief, and `env`), because an inference drawn
// from a missing line does not survive a summariser, a subagent boundary, or an MCP client
// that never sees the header at all.
type Origin struct {
	DB      string // the store file
	Machine string
	OS      string
	User    string
	Go      string
	// Remote marks a brief computed for another project over MCP. The machine, the user
	// and the git tree then describe the server process, not the caller, so they are left
	// out rather than reported as if they were true of the caller.
	Remote bool
}

// MachineName is what this box is called. OS_BRAIN_MACHINE overrides the hostname, because a
// hostname is not always a name a person recognises and "which machine wrote this" is asked
// by people. Nothing is guessed from hardware: a guess in the one line whose job is to be
// authoritative is worse than a plain hostname.
func MachineName() string {
	if v := strings.TrimSpace(os.Getenv("OS_BRAIN_MACHINE")); v != "" {
		return v
	}
	host, _ := os.Hostname()
	return host
}

// Lines renders the block. The labels line up with the brief's other keys, and every line
// asserts rather than implies: "LOCAL FILE" is printed, not left to be deduced from the
// absence of a server.
func (o Origin) Lines() []string {
	db := o.DB
	if db == "" {
		db = "(no database resolved)"
	}
	l := []string{
		"  brain     LOCAL FILE · " + db,
		"            on this machine only: no server, no other machine, nobody else's writes",
	}
	if o.Remote {
		return l
	}
	if who := o.whoLine(); who != "" {
		l = append(l, "  you       "+who)
	}
	return l
}

func (o Origin) whoLine() string {
	who := o.User
	if o.Machine != "" {
		if who != "" {
			who += "@"
		}
		who += o.Machine
	}
	if o.OS != "" {
		who += " (" + o.OS + ")"
	}
	if o.Go != "" {
		who += " · " + o.Go
	}
	if who == "" {
		return ""
	}
	return who + " · a local brain has no accounts: every memory here is yours"
}

// OriginFrom reads the same facts map the brief and `env` already carry, so the block and
// the JSON can never describe different brains.
func OriginFrom(facts map[string]interface{}) Origin {
	str := func(k string) string {
		v, _ := facts[k].(string)
		return v
	}
	o := Origin{
		DB:      str("db"),
		Machine: str("machine"),
		OS:      str("platform"),
		User:    str("user"),
		Go:      str("go"),
	}
	o.Remote, _ = facts["remote"].(bool)
	return o
}
