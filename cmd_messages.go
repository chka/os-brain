package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

func cmdNote(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.NoteArgs{}
	fs.StringVar(&a.Ref, "ref", "", "")
	fs.StringVar(&a.Kind, "kind", "note", "note | prompt")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "store it anyway; it is a false positive")
	expires := fs.Int("expires", 0, "days until it drops out of their brief")
	fs.Int64Var(&a.Read, "read", 0, "read one in full")
	fs.Int64Var(&a.Ack, "ack", 0, "done with it")
	fs.BoolVar(&a.Inbox, "inbox", false, "")
	fs.BoolVar(&a.Elsewhere, "elsewhere", false, "notes for this project in the other organisations this machine has a credential for")
	fs.IntVar(&a.N, "n", 10, "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	// Notes between projects live in this machine's store. Other organisations' mailboxes
	// are a team-edition feature, so the flag exists only to say so.
	if a.Elsewhere {
		return fmt.Errorf("--elsewhere reads other organisations on ns-brain's team edition, " +
			"and this project's brain is a local file")
	}
	if flagSet(fs, "expires") {
		a.HasExp, a.Expires = true, *expires
	}
	if len(pos) > 0 {
		a.To = pos[0]
	}
	if len(pos) > 1 {
		a.Body = strings.Join(pos[1:], " ")
	}
	if a.To != "" && a.Read == 0 && a.Ack == 0 && !a.Inbox && !a.Elsewhere {
		b, err := readBody(a.Body, "")
		if err != nil {
			return err
		}
		if b == "" || b == "-" {
			piped, err := readStdinText()
			if err != nil {
				return err
			}
			b = piped
		}
		a.Body = b
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Note(stdout, a) })
}

func cmdSessions(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.SessionsArgs{}
	fs.BoolVar(&a.All, "all", false, "include sessions that have ended")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return withMessenger(c.cmd, c.env, func(m brain.Messenger) error {
		return brain.SessionsCommand(stdout, m, brain.SessionID(), a)
	})
}

func cmdDM(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.DMArgs{}
	fs.BoolVar(&a.Sent, "sent", false, "what this project sent, and whether each was read")
	fs.IntVar(&a.N, "n", 10, "")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "send it anyway; it is a false positive")
	fs.BoolVar(&a.Rdo, "rdo", false, "a remote-do (an instruction), not a plain message")
	fs.Int64Var(&a.Accept, "accept", 0, "release a pending remote-do addressed to you, by id")
	fs.Int64Var(&a.Reject, "reject", 0, "refuse a pending remote-do addressed to you, by id")
	fs.BoolVar(&a.Mute, "mute", false, "do-not-disturb: hold plain messages for the inbox")
	fs.BoolVar(&a.Unmute, "unmute", false, "turn do-not-disturb off")
	fs.BoolVar(&a.Everywhere, "everywhere", false, "with --mute/--unmute: you, on every session")
	fs.BoolVar(&a.Inbox, "inbox", false, "what do-not-disturb held back")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		a.To = pos[0]
	}
	if len(pos) > 1 {
		a.Body = strings.Join(pos[1:], " ")
	}
	// Only a send reads stdin. --inbox, --mute and the rest used to block on a terminal
	// waiting for a body they never use.
	sending := !a.Sent && !a.Inbox && !a.Mute && !a.Unmute && a.Accept == 0 && a.Reject == 0
	if sending && a.To != "" {
		if a.Body == "" || a.Body == "-" {
			piped, err := readStdinText()
			if err != nil {
				return err
			}
			a.Body = piped
		}
	}
	if a.Accept > 0 && a.Reject == 0 {
		// A remote-do is another person's instruction. The pending notice tells the model
		// not to accept it; this is what makes that hold.
		if c.via.transport != viaCLI.transport {
			return errors.New("dm --accept is refused over MCP: it releases another person's " +
				"instruction into this session, a person's decision made at a terminal. Ask the " +
				"person to run `os-brain dm --accept <id>` themselves, in a terminal of their own")
		}
		if a.Confirm, err = confirmAtTerminal("accepting a remote-do", "Accept it? Type yes: "); err != nil {
			return err
		}
	}
	a.JSON = c.json()
	return withMessenger(c.cmd, c.env, func(m brain.Messenger) error {
		return brain.SendDMCommand(stdout, m, brain.SessionID(), a)
	})
}
