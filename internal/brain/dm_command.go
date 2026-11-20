package brain

import (
	"fmt"
	"io"
	"strings"
)

// DMArgs is `os-brain dm`.
type DMArgs struct {
	To          string
	Body        string
	Sent        bool
	N           int
	AllowSecret bool
	JSON        bool
	// Rdo makes this a remote-do (an instruction) rather than a plain message.
	Rdo bool
	// Accept/Reject decide on a remote-do held for an accept. The flags behave as they do in
	// ns-brain so a brain moves between the two unchanged; a local brain never holds one, so
	// the store refuses both (see resolveLocal).
	Accept int64
	Reject int64
	// Confirm is the person's typed yes at a terminal for an Accept, set only by the CLI. An
	// accept releases another person's instruction into this session, so the session that
	// instruction may have steered must not be able to give it; without Confirm it refuses.
	Confirm func(summary string) (bool, error)
	// Mute/Unmute set do-not-disturb; Everywhere makes it the person's, not the session's.
	Mute       bool
	Unmute     bool
	Everywhere bool
	// Inbox lists what was queued while muted.
	Inbox bool
}

// SendKind maps the --rdo flag to the stored kind.
func (a DMArgs) SendKind() string {
	if a.Rdo {
		return "instruction"
	}
	return "text"
}

// SendDMCommand is `dm`: one flag picks a listing or a setting; with none of them it resolves
// the address, guards the body, sends, and says when the message will be read.
func SendDMCommand(w io.Writer, m Messenger, self string, a DMArgs) error {
	switch {
	case a.Sent:
		return showSentDMs(w, m, a)
	case a.Inbox:
		return showDMInbox(w, m, a)
	case a.Mute || a.Unmute:
		return setDMMute(w, m, a)
	case a.Accept > 0 || a.Reject > 0:
		return decideRemoteDo(w, m, a)
	}
	return sendDM(w, m, self, a)
}

func showSentDMs(w io.Writer, m Messenger, a DMArgs) error {
	n := a.N
	if n <= 0 {
		n = 10
	}
	ms, err := m.SentDMs(n)
	if err != nil {
		return err
	}
	if ms == nil {
		ms = []DM{}
	}
	lines := []string{}
	for _, d := range ms {
		state := "waiting"
		if d.DeliveredAt != nil {
			state = fmt.Sprintf("delivered %s via %s", ISO(*d.DeliveredAt), d.DeliveredVia)
		}
		lines = append(lines, fmt.Sprintf("  #%d to %s/%s, %s · %s\n%s", d.ID,
			OneLine(d.ToProject), OneLine(ShortSession(d.ToSession)), ISO(d.CreatedAt),
			OneLine(state), strings.Join(Fenced("      ", Clip(d.Body, 200)), "\n")))
	}
	human := "(nothing sent from this project)"
	if len(lines) > 0 {
		human = strings.Join(lines, "\n")
	}
	return Out(w, a.JSON, human, map[string]interface{}{"sent": ms})
}

func showDMInbox(w io.Writer, m Messenger, a DMArgs) error {
	ms, err := m.DMInbox()
	if err != nil {
		return err
	}
	if ms == nil {
		ms = []DM{}
	}
	human := "(inbox empty)"
	if len(ms) > 0 {
		human = RenderDMs(ms, Now())
	}
	return Out(w, a.JSON, human, map[string]interface{}{"inbox": ms})
}

func setDMMute(w io.Writer, m Messenger, a DMArgs) error {
	if err := m.SetMute(a.Everywhere, a.Mute); err != nil {
		return err
	}
	scope := "this session"
	if a.Everywhere {
		scope = "you, on every session and machine"
	}
	state := "on do-not-disturb: messages wait in the inbox, a remote-do still surfaces"
	if a.Unmute {
		state = "off do-not-disturb: messages arrive again"
	}
	return Out(w, a.JSON, fmt.Sprintf("%s is %s", scope, state),
		map[string]interface{}{"muted": a.Mute, "everywhere": a.Everywhere})
}

// decideRemoteDo is --accept and --reject. An accept asks the person at their terminal
// first, because it is theirs to give and never the session's.
func decideRemoteDo(w io.Writer, m Messenger, a DMArgs) error {
	id, accept := a.Accept, true
	if a.Reject > 0 {
		id, accept = a.Reject, false
	}
	var err error
	if accept {
		if a.Confirm == nil {
			return fmt.Errorf("accepting remote-do #%d is the person's decision, made in a "+
				"terminal of their own: os-brain dm --accept %d", id, id)
		}
		ok, cerr := a.Confirm(fmt.Sprintf("Remote-do #%d is an instruction from ANOTHER person. "+
			"Accepting it delivers it to your session as a request, which you still approve "+
			"there before anything is done.", id))
		if cerr != nil {
			return cerr
		}
		if !ok {
			return fmt.Errorf("nothing accepted: remote-do #%d is still pending", id)
		}
		err = m.AcceptDM(id)
	} else {
		err = m.RejectDM(id)
	}
	if err != nil {
		return err
	}
	human := fmt.Sprintf("rejected remote-do #%d: it will not be delivered", id)
	if accept {
		human = fmt.Sprintf("accepted remote-do #%d: it arrives on this session's next turn "+
			"as a request, to act on once you approve it there", id)
	}
	return Out(w, a.JSON, human, map[string]interface{}{"ok": true, "id": id, "accepted": accept})
}

func sendDM(w io.Writer, m Messenger, self string, a DMArgs) error {
	body := strings.TrimSpace(a.Body)
	if body == "" {
		return fmt.Errorf("a direct message needs a body: os-brain dm <session|project> \"...\", " +
			"or - to read it from stdin")
	}
	if err := GuardText(a.AllowSecret, body); err != nil {
		return err
	}
	cands, err := m.LiveSessions()
	if err != nil {
		return err
	}
	SortSessions(cands, self, Now())
	to, err := ResolveSession(a.To, self, cands)
	if err != nil {
		return err
	}
	id, err := m.SendDM(to, body, a.SendKind(), a.AllowSecret)
	if err != nil {
		return err
	}
	where := to.Project
	if to.Machine != "" {
		where += " on " + to.Machine
	}
	what := "#" + fmt.Sprint(id)
	if a.Rdo {
		what = "remote-do " + what
	}
	human := fmt.Sprintf("sent %s to session %s (%s). It is read %s.\n"+
		"  os-brain dm --sent shows when it was delivered", what, ShortSession(to.Session), where,
		Reaches(to.State))
	return Out(w, a.JSON, human, map[string]interface{}{"id": id, "to": to,
		"reaches": Reaches(to.State)})
}

type SessionsArgs struct {
	All  bool
	JSON bool
}

// SessionsCommand is `os-brain sessions`: the running sessions this brain can address.
func SessionsCommand(w io.Writer, m Messenger, self string, a SessionsArgs) error {
	ls, err := m.LiveSessions()
	if err != nil {
		return err
	}
	now := Now()
	SortSessions(ls, self, now)
	if !a.All {
		ls = notEnded(ls)
	}
	if ls == nil {
		ls = []LiveSession{}
	}
	human := "(no running sessions this brain can reach)"
	if len(ls) > 0 {
		human = RenderSessionLines(ls, now) + "\n\n  listening = wakes on a message now; " +
			"active/idle = reads it at its next prompt, tool call or turn end\n" +
			"  os-brain dm <id-prefix|project|project/id-prefix> \"...\""
	}
	return Out(w, a.JSON, human, map[string]interface{}{"sessions": ls})
}
