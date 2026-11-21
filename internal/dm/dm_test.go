package dm

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/timefmt"
)

// Tests for direct messages between sessions on one machine (`os-brain dm`).
//
// Every running session registers itself in the spine's live_sessions table, and a message
// is a row in its dms table that the recipient's hooks claim and show. The rules tested
// here: a message goes to exactly the session it names or is refused, it is delivered once,
// and the text that reaches the recipient frames it as information from someone, never as
// an order.

// Three session ids. sessB and sessC share the prefix "bbbb", so a four-character prefix is
// ambiguous between them and a longer one is not.
const (
	sessA = "aaaaaaaa-0000-0000-0000-000000000001"
	sessB = "bbbbbbbb-0000-0000-0000-000000000002"
	sessC = "bbbbcccc-0000-0000-0000-000000000003"
)

// as switches the one test store to another project and session. The spine is shared, so
// this is two projects on one machine, which is exactly what a local message crosses.
func as(s *Store, project, session string) *Store {
	s.Env.Project, s.Env.Session, s.Env.Private = project, session, false
	return s
}

// --- Addressing ---

// A message reaches only the session it names. A project with two running sessions, or a
// prefix matching two sessions, is refused rather than resolved to one of them; once the
// address is exact, only that session receives the message, and only once.
func TestALocalDirectMessageReachesOnlyTheSessionItNames(t *testing.T) {
	s := newTestStore(t)
	beat := Beat{Machine: "box", PID: 77}
	if _, err := as(s, "beta", sessB).ClaimDMs("sessionstart", beat); err != nil {
		t.Fatal(err)
	}
	if _, err := as(s, "beta", sessC).ClaimDMs("sessionstart", Beat{Machine: "box", PID: 78}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := SendDMCommand(&out, as(s, "alpha", sessA), sessA, DMArgs{To: "beta", Body: "hello"})
	if err == nil || !strings.Contains(err.Error(), "2 running sessions") {
		t.Fatalf("a project with two sessions was addressed as if it had one: %v", err)
	}
	err = SendDMCommand(&out, s, sessA, DMArgs{To: "bbbb", Body: "hello"})
	if err == nil || !strings.Contains(err.Error(), "matches 2") {
		t.Fatalf("an ambiguous prefix was resolved to somebody: %v", err)
	}
	if err := SendDMCommand(&out, s, sessA, DMArgs{To: "beta/bbbbcc", Body: "for C only"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := as(s, "beta", sessB).ClaimDMs("userpromptsubmit", beat); len(got) != 0 {
		t.Fatalf("a message for C was delivered to B: %+v", got)
	}
	got, err := as(s, "beta", sessC).ClaimDMs("userpromptsubmit", Beat{Machine: "box", PID: 78})
	if err != nil || len(got) != 1 || got[0].Body != "for C only" || got[0].FromProject != "alpha" {
		t.Fatalf("C did not receive its message: %v %+v", err, got)
	}
	if again, _ := s.ClaimDMs("stop", Beat{Machine: "box", PID: 78}); len(again) != 0 {
		t.Fatalf("delivered twice: %+v", again)
	}
}

// Every address that does not name exactly one other live session is refused with a reason:
// no address, an unknown project, a prefix too short to be meaningful, an unknown project
// in project/prefix form, an empty body, the sender's own session, and a session that has
// ended.
func TestADirectMessageRefusesRatherThanGuess(t *testing.T) {
	s := newTestStore(t)
	if _, err := as(s, "beta", sessB).ClaimDMs("sessionstart", Beat{}); err != nil {
		t.Fatal(err)
	}
	a := as(s, "alpha", sessA)
	for _, c := range []struct{ to, want string }{
		{"", "which session"},
		{"gamma", "no running session"},
		{"beta/b", "four characters"},
		{"nope/bbbb", "no running session in project"},
	} {
		err := SendDMCommand(&bytes.Buffer{}, a, sessA, DMArgs{To: c.to, Body: "x"})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("dm %q: want a refusal containing %q, got %v", c.to, c.want, err)
		}
	}
	if err := SendDMCommand(&bytes.Buffer{}, a, sessA, DMArgs{To: "beta", Body: "  "}); err == nil {
		t.Error("an empty message was sent")
	}
	if _, err := a.ClaimDMs("sessionstart", Beat{}); err != nil {
		t.Fatal(err)
	}
	err := SendDMCommand(&bytes.Buffer{}, a, sessA, DMArgs{To: "aaaa", Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "this session") {
		t.Errorf("a session messaged itself: %v", err)
	}
	if err := as(s, "beta", sessB).EndSession(); err != nil {
		t.Fatal(err)
	}
	err = SendDMCommand(&bytes.Buffer{}, as(s, "alpha", sessA), sessA, DMArgs{To: "beta", Body: "x"})
	if err == nil {
		t.Error("a message was sent to a session that has ended")
	}
}

// A private project's sessions do not exist to anybody else. The refusal is the same one a
// typo gets, so it reveals nothing about which names are running.
func TestAPrivateProjectsSessionsAreInvisibleToOthers(t *testing.T) {
	s := newTestStore(t)
	p := as(s, "secretproj", sessB)
	p.Env.Private = true
	if _, err := p.ClaimDMs("sessionstart", Beat{}); err != nil {
		t.Fatal(err)
	}
	ls, err := as(s, "alpha", sessA).LiveSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 0 {
		t.Fatalf("another project can see a private project's session: %+v", ls)
	}
	err = SendDMCommand(&bytes.Buffer{}, s, sessA, DMArgs{To: "secretproj", Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "no running session matches") {
		t.Errorf("want the plain miss, got %v", err)
	}
}

// A project that opted out of the spine index neither lists sessions nor registers in
// them, and the command says why. The delivery hook stays silent instead, because a hook
// never fails the user's turn.
func TestAProjectOptedOutOfTheSpineNeitherSendsNorRegisters(t *testing.T) {
	s := newTestStore(t)
	off := false
	s.Env.Config.Index = &off
	s.Env.Session = sessA
	if _, err := s.LiveSessions(); err == nil || !strings.Contains(err.Error(), "opted out") {
		t.Errorf("want the opt-out named, got %v", err)
	}
	var out bytes.Buffer
	t.Setenv("CLAUDE_CODE_SESSION_ID", sessA)
	if DeliverHook(&out, s, HookPrompt) || out.Len() != 0 {
		t.Errorf("the hook spoke for a project that opted out: %q", out.String())
	}
}

// A channel listener is a child of the host process and keeps the session id it started
// with, and that id goes stale when the user runs /clear and the host gives the same
// process a new session. Listener and hooks agree on the process id, so the listener must
// keep delivering for its process, now the new session, and must not bring the ended row
// back as a ghost.
func TestAListenerFollowsItsProcessAcrossClear(t *testing.T) {
	s := newTestStore(t)
	hook := Beat{Machine: "box", PID: 900}
	listen := Beat{Machine: "box", PID: 900, Listening: true}
	if _, err := as(s, "beta", sessB).ClaimDMs("sessionstart", hook); err != nil {
		t.Fatal(err)
	}
	if err := s.EndSession(); err != nil {
		t.Fatal(err)
	}
	if _, err := as(s, "beta", sessC).ClaimDMs("sessionstart", hook); err != nil {
		t.Fatal(err)
	}
	if _, err := as(s, "beta", sessB).ClaimDMs("channel", listen); err != nil {
		t.Fatal(err)
	}
	ls, _ := as(s, "alpha", sessA).LiveSessions()
	SortSessions(ls, sessA, timefmt.Now())
	states := map[string]string{}
	for _, l := range ls {
		states[l.Session] = l.State
	}
	if states[sessB] != "ended" || states[sessC] != "listening" {
		t.Fatalf("after /clear want old=ended new=listening, got %v", states)
	}
	if err := SendDMCommand(&bytes.Buffer{}, s, sessA, DMArgs{To: "beta", Body: "still there?"}); err != nil {
		t.Fatal(err)
	}
	got, err := as(s, "beta", sessB).ClaimDMs("channel", listen)
	if err != nil || len(got) != 1 || got[0].ToSession != sessC {
		t.Fatalf("the listener lost its process's messages across /clear: %v %+v", err, got)
	}
}

// --- Delivery and framing ---

// Each host hook event takes extra context in its own output envelope, and the host
// silently ignores the wrong one: Stop takes a block decision with a reason, PostToolUse
// takes hookSpecificOutput.additionalContext, and UserPromptSubmit takes plain stdout.
func TestHookEnvelopesAreTheOnesClaudeCodeReads(t *testing.T) {
	var stop map[string]string
	if err := json.Unmarshal([]byte(HookEnvelope(HookStop, "msg")), &stop); err != nil ||
		stop["decision"] != "block" || stop["reason"] != "msg" {
		t.Errorf("Stop envelope: %v %v", err, stop)
	}
	var tool struct {
		H struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(HookEnvelope(HookTool, "msg")), &tool); err != nil ||
		tool.H.Event != "PostToolUse" || tool.H.Context != "msg" {
		t.Errorf("PostToolUse envelope: %v %+v", err, tool)
	}
	if HookEnvelope(HookPrompt, "msg") != "msg" {
		t.Error("UserPromptSubmit takes plain stdout")
	}
}

// A delivered message names its sender (session, project, machine), says it is a message,
// and gives the exact command to reply. A message sent from a plain shell has no session to
// reply to, so it offers no reply address that would reach nobody.
func TestADeliveredMessageSaysWhoSentItAndHowToReply(t *testing.T) {
	txt := RenderDMs([]DM{{ID: 9, FromSession: sessA, FromProject: "alpha",
		FromMachine: "box", Body: "ship it", CreatedAt: timefmt.Now(),
		SamePerson: true}}, timefmt.Now())
	for _, want := range []string{"DIRECT MESSAGE #9", "session aaaaaaaa", "alpha",
		"box", "ship it", "os-brain dm " + sessA, "It is a MESSAGE"} {
		if !strings.Contains(txt, want) {
			t.Errorf("rendered message lacks %q:\n%s", want, txt)
		}
	}
	shell := RenderDMs([]DM{{ID: 1, FromSession: "", FromProject: "alpha", Body: "x"}}, timefmt.Now())
	if strings.Contains(shell, "Reply:") {
		t.Error("a message from a shell offered a reply address that reaches nobody")
	}
}

// A plain message and a remote-do (a request for the receiving session to do something)
// each render distinctly. Neither is ever an order: a message is information, and a
// remote-do is a request the user approves in the receiving session. The phrases in never
// are the wording that would turn either one into an order.
func TestRemoteDoRenderMatrix(t *testing.T) {
	at := timefmt.Now()
	base := func(k string, same bool, acc *int64) DM {
		return DM{ID: 7, FromSession: sessA, FromProject: "alpha",
			Body: "do the thing", CreatedAt: at, Kind: k, SamePerson: same, AcceptedAt: acc}
	}
	never := []string{"carries their authority", "carry it out now", "os-brain dm --accept"}
	cases := []struct {
		name string
		m    DM
		must []string
		not  []string
	}{
		{"text same-person", base("text", true, nil),
			[]string{"DIRECT MESSAGE", "It is a MESSAGE"}, []string{"DO NOT ACT", "REMOTE-DO"}},
		{"rdo same-person", base("instruction", true, nil),
			[]string{"REMOTE-DO REQUEST #7", "does NOT carry the user's authority",
				"explicitly", "approve it in this session", "alpha", timefmt.ISO(at)}, []string{"PENDING"}},
		{"rdo accepted", base("instruction", true, &at),
			[]string{"REMOTE-DO REQUEST #7", "Accepted for delivery", "does NOT carry the user's authority",
				"approve it in this session"}, []string{"DO NOT ACT", "PENDING"}},
	}
	for _, c := range cases {
		got := RenderDMs([]DM{c.m}, at)
		for _, w := range c.must {
			if !strings.Contains(got, w) {
				t.Errorf("[%s] missing %q:\n%s", c.name, w, got)
			}
		}
		for _, w := range append(c.not, never...) {
			if strings.Contains(got, w) {
				t.Errorf("[%s] must not contain %q:\n%s", c.name, w, got)
			}
		}
	}
}

// Whether a message comes from the same person is decided by the store that delivers it,
// never by anything the sender wrote. On one machine's spine, every sender is the account
// that owns it.
func TestOnlyTheSamePersonsMessageIsFramedAsTheirs(t *testing.T) {
	s := newTestStore(t)
	if _, err := as(s, "beta", sessB).ClaimDMs("sessionstart", Beat{}); err != nil {
		t.Fatal(err)
	}
	if err := SendDMCommand(&bytes.Buffer{}, as(s, "alpha", sessA), sessA,
		DMArgs{To: "beta", Body: "from this machine's own account"}); err != nil {
		t.Fatal(err)
	}
	got, err := as(s, "beta", sessB).ClaimDMs("userpromptsubmit", Beat{})
	if err != nil || len(got) != 1 || !got[0].SamePerson {
		t.Errorf("a local message is from the account that owns the spine: %v %+v", err, got)
	}
}
