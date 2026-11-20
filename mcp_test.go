package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/chka/os-brain/internal/brain"
)

// The MCP server: every CLI command offered as a tool, called through the ordinary CLI
// path and routed to the right project. The HTTP transport's access checks are in
// mcp_security_test.go, serving off loopback in mcp_net_test.go and OAuth in
// mcp_oauth_test.go.

// mcpEnv makes a fresh single-project brain ("mcptest") in a temporary directory, pins every
// machine-level path beside it and names it through OS_BRAIN_HOME. Most tests in this
// package that need a brain start here.
func mcpEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"mcptest"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "cfg", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_PROJECT", "mcptest")
}

// rpc sends one JSON-RPC request straight to the server's handler and returns the result
// as a map. A protocol error fails the test; a notification returns nil.
func rpc(t *testing.T, s *mcpServer, method string, params any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	resp := s.handle(&rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"),
		Method: method, Params: raw}, "", "")
	if resp == nil {
		return nil
	}
	if resp.Error != nil {
		t.Fatalf("%s: rpc error %d %s", method, resp.Error.Code, resp.Error.Message)
	}
	out, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// callText calls a tool and returns the text it answered with and whether the call was a
// tool error.
func callText(t *testing.T, s *mcpServer, tool string, args map[string]any) (string, bool) {
	t.Helper()
	r := rpc(t, s, "tools/call", map[string]any{"name": tool, "arguments": args})
	content := r["content"].([]any)
	return content[0].(map[string]any)["text"].(string), r["isError"].(bool)
}

// dispatchCases reads the commands the switch actually accepts, straight from the source.
// Deriving the tool list from `usage` is only safe if usage cannot fall behind the switch,
// and this is what checks that.
func dispatchCases(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	body = body[strings.Index(body, "\tswitch cmd {"):]
	re := regexp.MustCompile(`(?m)^\tcase ((?:"[a-z-]+"(?:, )?)+):`)
	out := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		for _, name := range strings.Split(m[1], ", ") {
			out[strings.Trim(name, `"`)] = true
		}
	}
	// These two are handled before the switch, so the regexp cannot see them.
	out["help"], out["version"] = true, true
	return out
}

// The requirement is 100% of the CLI, not a curated subset: for a client that reaches the
// brain only over MCP, anything the CLI can do and MCP cannot is something that client
// simply cannot do. Only the commands in notATool (servers that block, hooks that read
// stdin) are left out, each for a stated reason.
func TestEveryCLICommandIsAnMCPTool(t *testing.T) {
	mcpEnv(t)
	tools, err := buildTools()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range tools {
		have[commandFor(tl.Name)] = true
	}
	var missing []string
	for cmd := range dispatchCases(t) {
		if notATool[cmd] {
			continue
		}
		if !have[cmd] {
			missing = append(missing, cmd)
		}
	}
	if len(missing) > 0 {
		t.Errorf("dispatch commands with no MCP tool: %v", missing)
	}
}

// The reverse direction: a tool that maps to no command would fail on every call and a
// tool with no description gives the model nothing to choose it by.
func TestEveryMCPToolIsARealCommand(t *testing.T) {
	mcpEnv(t)
	tools, err := buildTools()
	if err != nil {
		t.Fatal(err)
	}
	cases := dispatchCases(t)
	for _, tl := range tools {
		cmd := commandFor(tl.Name)
		if cmd == "" || !cases[cmd] {
			t.Errorf("tool %q maps to no dispatch command", tl.Name)
		}
		if tl.Description == "" {
			t.Errorf("tool %q has no description", tl.Name)
		}
	}
}

// The anti-drift guarantee and the reason the schema is derived rather than authored: a
// flag added to the CLI becomes an MCP input on the next build, with no second edit. The
// only flags held back are the person-only ones (personOnlyFlags), which an agent must
// never be offered.
func TestEveryCLIFlagAppearsInItsToolSchema(t *testing.T) {
	mcpEnv(t)
	for _, cmd := range commandNames() {
		if preSwitch[cmd] {
			continue
		}
		fs, err := probeFlags(cmd)
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		schema, err := schemaFor(cmd)
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		props := schema["properties"].(map[string]any)
		fs.VisitAll(func(f *flag.Flag) {
			if _, ok := props[f.Name]; !ok && !personOnlyFlags[cmd][f.Name] {
				t.Errorf("%s --%s is not exposed as an MCP input", cmd, f.Name)
			}
		})
		for _, always := range []string{"json", "args"} {
			if _, ok := props[always]; !ok {
				t.Errorf("%s is missing the %q input", cmd, always)
			}
		}
	}
}

// A client that reaches the brain only over MCP must be able to set a project up and init
// must not be marked destructive: it creates what is missing and leaves the rest, so a
// client that asks a person before destructive calls would ask for nothing.
func TestInitIsATool(t *testing.T) {
	mcpEnv(t)
	tools, err := buildTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if commandFor(tl.Name) == "init" {
			if tl.Annotations.DestructiveHint {
				t.Error("init is additive: it creates what is missing and leaves the rest")
			}
			return
		}
	}
	t.Error("init is a dispatch command with no MCP tool")
}

// MCP clients read a tool's annotations to decide where to put a human in the loop, so a
// command that deletes must say so, a command that only reads must say so and only a
// command that really reaches outside the store may claim an open world.
func TestDestructiveCommandsAreAnnotatedAsSuch(t *testing.T) {
	mcpEnv(t)
	tools, err := buildTools()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]toolDef{}
	for _, tl := range tools {
		byName[commandFor(tl.Name)] = tl
	}
	for _, cmd := range []string{"forget", "prune", "import", "restore", "absorb", "relabel"} {
		a := byName[cmd].Annotations
		if a == nil || !a.DestructiveHint || a.ReadOnlyHint {
			t.Errorf("%s is not annotated destructive: %+v", cmd, a)
		}
	}
	for _, cmd := range []string{"recall", "get", "brief", "stats", "review"} {
		a := byName[cmd].Annotations
		if a == nil || !a.ReadOnlyHint {
			t.Errorf("%s is not annotated read-only: %+v", cmd, a)
		}
	}
	// A local brain is a closed world with one exception: wakeup runs commands the project
	// declared, so its effects are not confined to the store. Any other tool claiming an
	// open world is noise that trains clients to ignore the hint.
	openWorldOK := map[string]bool{"brain_wakeup": true}
	for _, tl := range tools {
		if tl.Annotations.OpenWorldHint && !openWorldOK[tl.Name] {
			t.Errorf("%s claims an open world; a brain is closed by definition", tl.Name)
		}
	}
	if a := byName["wakeup"].Annotations; a == nil || !a.OpenWorldHint || a.ReadOnlyHint {
		t.Errorf("wakeup executes project-declared commands and must say so: %+v", a)
	}
}

// A tool call is not a parallel implementation: it builds the same argv and runs the same
// dispatch as the CLI, so guards and output cannot diverge between the two surfaces.
func TestAToolCallGoesThroughTheOrdinaryCLIPath(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := callText(t, s, "brain_remember", map[string]any{
		"title": "the deploy pipeline needs the vault unsealed first", "type": "gotcha",
		"body": "learned the hard way",
	})
	if isErr || !strings.Contains(out, "remembered #1") {
		t.Fatalf("remember: isError=%v %q", isErr, out)
	}
	out, isErr = callText(t, s, "brain_recall", map[string]any{"args": "vault", "json": true})
	if isErr {
		t.Fatalf("recall failed: %s", out)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("recall did not return the JSON envelope: %v", err)
	}
	if env["matched"] != true || env["count"].(float64) != 1 {
		t.Errorf("recall envelope: %v", env)
	}
}

// The near-duplicate guard, the tombstone and the pinned-forget refusal all belong to the
// CLI. Over MCP a refusal must arrive whole, as a readable tool error rather than a
// transport fault: the text, which names the way forward, is the whole point of a guard.
// The near-duplicate guard stands in for all of them here.
func TestTheBrainsGuardsSurviveTheMCPSurface(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	if _, isErr := callText(t, s, "brain_remember", map[string]any{
		"title": "the deploy pipeline needs the vault unsealed first", "type": "gotcha",
	}); isErr {
		t.Fatal("setup write failed")
	}
	out, isErr := callText(t, s, "brain_remember", map[string]any{
		"title": "deploy pipeline requires the vault to be unsealed", "type": "gotcha",
	})
	if !isErr {
		t.Fatal("the near-duplicate guard did not fire through MCP")
	}
	for _, want := range []string{"already about this", "--supersedes", "--allow-duplicate"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal lost %q on the way out:\n%s", want, out)
		}
	}
}

// A command that fails is an answer the model should read (isError with the reason), not a
// JSON-RPC error, which clients surface as a broken connection.
func TestAFailedCommandIsAToolErrorNotAProtocolError(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	out, isErr := callText(t, s, "brain_get", map[string]any{"args": "999"})
	if !isErr || !strings.Contains(out, "999") {
		t.Errorf("expected a tool error naming the id, got isError=%v %q", isErr, out)
	}
}

// An unknown tool, or an argument the tool's schema does not offer, is refused rather than
// ignored: a dropped argument would run a different command from the one the model asked
// for.
func TestAnUnknownToolIsRefused(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callText(t, s, "brain_nonsense", nil); !isErr ||
		!strings.Contains(out, "unknown tool") {
		t.Errorf("got isError=%v %q", isErr, out)
	}
	if _, isErr := callText(t, s, "brain_recall", map[string]any{"nonsense": "x"}); !isErr {
		t.Error("an unknown argument was accepted")
	}
}

// A tool's args string is split the way a shell would split it. It was once split with
// strings.Fields: a quoted note body lost its newlines and any word in it starting with
// "-" became a flag, so a reply that mentioned --title was refused. Now quotes hold
// words together (an apostrophe inside a word is still an apostrophe), escapes work inside
// double quotes, an empty quoted string is an argument and an unterminated quote is refused
// rather than guessed at. The quoted body then reaches the command whole, after "--".
func TestArgsSplitLikeTheCLIPromises(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{`what's the nginx port`, []string{"what's", "the", "nginx", "port"}},
		{"acme \"use --title and --body\nthen wait\"",
			[]string{"acme", "use --title and --body\nthen wait"}},
		{`mtest 'it's fine'`, []string{"mtest", "it's fine"}},
		{`x "say \"hi\" now"`, []string{"x", `say "hi" now`}},
		{`x "a\b"`, []string{"x", `a\b`}},
		{`x ""`, []string{"x", ""}},
		{`  spaced   out  `, []string{"spaced", "out"}},
	} {
		got, err := splitArgs(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") || len(got) != len(c.want) {
			t.Errorf("%q split to %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := splitArgs(`mtest "never closed`); err == nil {
		t.Error("an unterminated quote was guessed at instead of refused")
	}
	argv, err := argvFor("note", map[string]any{"args": `acme "pass --title to task set"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 4 || argv[1] != "--" || argv[3] != "pass --title to task set" {
		t.Errorf("a quoted body did not reach the command whole: %q", argv)
	}
}

// initialize answers with the protocol version the client asked for, identifies the server,
// and gives instructions that warn the model recall fails closed: a session that does not
// know an empty answer can mean "refused" reads it as "nothing stored".
func TestInitializeReportsTheNegotiatedVersionAndInstructions(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	r := rpc(t, s, "initialize", map[string]any{"protocolVersion": ProtocolVersion})
	if r["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion %v", r["protocolVersion"])
	}
	if !strings.Contains(r["instructions"].(string), "FAILS CLOSED") {
		t.Error("instructions do not warn about the fail-closed recall envelope")
	}
	info := r["serverInfo"].(map[string]any)
	if info["name"] != "os-brain" || info["version"] == "" {
		t.Errorf("serverInfo %v", info)
	}
}

// A connector's displayed name is whatever the person who added it typed and its tool
// names carry no address. A session holding two of them could not tell which brain either
// one reached without making a call, so initialize says it.
func TestInitializeSaysWhichBrainThisServerFronts(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	resp := s.handle(&rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"),
		Method: "initialize"}, "", "")
	res, _ := resp.Result.(map[string]any)
	ins, _ := res["instructions"].(string)
	for _, want := range []string{"WHERE THIS BRAIN IS", "LOCAL FILE", "on this machine only"} {
		if !strings.Contains(ins, want) {
			t.Errorf("initialize does not say %q:\n%s", want, ins)
		}
	}
	info, _ := res["serverInfo"].(map[string]any)
	if title, _ := info["title"].(string); !strings.Contains(title, "local") {
		t.Errorf("serverInfo title does not name the edition: %q", title)
	}
}

// The stdio transport requires that stdout carry only MCP messages, one per line. Every
// warning in this codebase goes to stderr and it must stay that way or the stream
// corrupts. A notification gets no response at all.
func TestStdioSpeaksNewlineDelimitedJSONAndNothingElse(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := s.serveStdio(in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses (the notification takes none), got %d:\n%s",
			len(lines), out.String())
	}
	for i, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Errorf("line %d is not a JSON-RPC message: %v", i, err)
		}
	}
}

// sharedServer fronts a brain marked "shared" in brain.json: one store holding several
// projects, served over HTTP to a developer's other machines. Its own project label is
// "hosted" and OS_BRAIN_PROJECT is cleared so nothing names the caller's project.
func sharedServer(t *testing.T) *mcpServer {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"hosted","shared":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "cfg", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_PROJECT", "")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// MCP carries no notion of the client's working directory. A server that assumed its own
// label filed every other project's memories under that name, silently: that is how 73
// memories once became invisible to the project they belonged to, with no error. A shared
// server refuses a write that does not name its project and says how to name it.
func TestAHostedBrainRefusesToGuessWhichProjectIsCalling(t *testing.T) {
	s := sharedServer(t)
	out, isErr := callText(t, s, "brain_remember", map[string]any{
		"title": "newproject uses flask and gunicorn", "type": "infra"})
	if !isErr {
		t.Fatalf("a shared server guessed the project instead of refusing: %s", out)
	}
	for _, want := range []string{"_project", "shared brain", "already here"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, out)
		}
	}
}

// Gating the guard above on "more than one project is present" would be wrong: a freshly
// hosted brain holds exactly one, so the FIRST session from another machine would be the
// one mislabelled. Shared means multi-project whether or not anyone has arrived yet.
func TestTheGuardFiresBeforeASecondProjectExists(t *testing.T) {
	s := sharedServer(t)
	if len(s.known) > 1 {
		t.Fatalf("this test needs a store with a single project, has %v", s.known)
	}
	if !s.multi {
		t.Fatal("a shared store must require _project even while it holds one project")
	}
}

// With _project given, a write lands under that project and a recall for it sees only its
// own memories, not another project's on the same server.
func TestNamingTheProjectFilesItThereAndKeepsItSeparate(t *testing.T) {
	s := sharedServer(t)
	for _, p := range []string{"alpha", "beta"} {
		if out, isErr := callText(t, s, "brain_remember", map[string]any{
			"_project": p, "title": p + " runs nginx in front of gunicorn", "type": "infra",
		}); isErr {
			t.Fatalf("%s: %s", p, out)
		}
	}
	out, isErr := callText(t, s, "brain_recall", map[string]any{
		"_project": "alpha", "args": "nginx", "json": true})
	if isErr {
		t.Fatal(out)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	results := env["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("alpha sees %d memories, should see only its own", len(results))
	}
	if got := results[0].(map[string]any)["project"]; got != "alpha" {
		t.Errorf("alpha's recall returned a memory belonging to %v", got)
	}
}

// The local case must not get harder: one brain, one project, stdio and no _project.
func TestASingleProjectServerStillNeedsNoProject(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	if s.multi {
		t.Fatal("a plain local brain should not demand _project")
	}
	if out, isErr := callText(t, s, "brain_remember", map[string]any{
		"title": "solo project stores without naming itself", "type": "infra"}); isErr {
		t.Fatalf("the local case regressed: %s", out)
	}
}

// A stdio server that a client launches for one repository is configured with
// OS_BRAIN_PROJECT. Even on a shared brain, being told the project outright is not the same
// as having to guess, so no _project is demanded.
func TestAnExplicitBrainProjectEnvIsNotAmbiguous(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "brain")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "brain.json"),
		[]byte(`{"project":"hosted","shared":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OS_BRAIN_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "cfg"))
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "cfg", "brain", "brain.db"))
	t.Setenv("OS_BRAIN_PROJECT", "namedbyenv")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	if s.multi {
		t.Error("OS_BRAIN_PROJECT names the project outright; that is not ambiguous")
	}
	if out, isErr := callText(t, s, "brain_remember", map[string]any{
		"title": "stored without naming the project again", "type": "infra"}); isErr {
		t.Errorf("refused despite an explicit OS_BRAIN_PROJECT: %s", out)
	}
}

// _project is how a caller names its project, so every tool must offer it. It belongs to
// the transport and is not a CLI flag, so it must never reach the argv: absorb already has
// a --project flag meaning something else and passing ours through would collide.
func TestProjectIsOfferedOnEveryToolAndNeverReachesTheArgv(t *testing.T) {
	mcpEnv(t)
	tools, err := buildTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		props := tl.InputSchema["properties"].(map[string]any)
		if _, ok := props["_project"]; !ok {
			t.Errorf("%s does not offer _project", tl.Name)
		}
	}
	argv, err := argvFor("remember", map[string]any{"_project": "x", "title": "y"})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range argv {
		if strings.Contains(a, "_project") {
			t.Errorf("_project leaked into the argv: %v", argv)
		}
	}
}

// A client can name its project once, in a header set in its config, instead of the model
// passing _project on every call: config cannot forget and a model can. The argument still
// wins when given, so one connection can reach across projects deliberately. With neither
// header nor argument, the write is still refused.
func TestAClientCanDeclareItsProjectOnceInAHeader(t *testing.T) {
	s := sharedServer(t)
	srv := httptest.NewServer(s.httpHandler(httpOpts{addr: "127.0.0.1:0", token: "sekrit"}))
	defer srv.Close()

	post := func(body, project string, session *string) map[string]any {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sekrit")
		if project != "" {
			req.Header.Set(ProjectHeader, project)
		}
		if session != nil && *session != "" {
			req.Header.Set("Mcp-Session-Id", *session)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if session != nil && *session == "" {
			*session = resp.Header.Get("Mcp-Session-Id")
		}
		var out struct {
			Result map[string]any `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Result
	}
	sid := ""
	post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "alpha", &sid)

	write := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"brain_remember",
		"arguments":{"title":"alpha runs nginx in front of gunicorn","type":"infra"}}}`
	r := post(write, "alpha", &sid)
	if r["isError"] == true {
		t.Fatalf("the header did not satisfy the project guard: %v", r["content"])
	}
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "remembered") {
		t.Fatalf("write failed: %s", text)
	}
	// It really landed under alpha, not under the server's own label.
	out, isErr := callText(t, s, "brain_recall", map[string]any{
		"_project": "alpha", "args": "nginx", "json": true})
	if isErr {
		t.Fatal(out)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env["count"].(float64) != 1 {
		t.Errorf("alpha should hold exactly its own memory, envelope: %v", env)
	}
	// No header and no argument is still a refusal.
	r = post(write, "", &sid)
	if r["isError"] != true {
		t.Error("a call with neither header nor _project was accepted")
	}
}

// The git tree, host and paths on the server say nothing true about the caller. A brief
// that reports them with a straight face is worse than one that omits them: the whole
// promise of the brief is that a session can trust it. So a remotely served brief says it
// is remote and leaves the server's machine out.
func TestARemotelyServedBriefDoesNotReportTheServersMachine(t *testing.T) {
	s := sharedServer(t)
	out, isErr := callText(t, s, "brain_brief", map[string]any{"_project": "elsewhere"})
	if isErr {
		t.Fatal(out)
	}
	if !strings.Contains(out, "served remotely") {
		t.Errorf("the brief does not say it is remote:\n%s", out)
	}
	for _, leak := range []string{"  git       ", "  you       "} {
		if strings.Contains(out, leak) {
			t.Errorf("a remotely served brief leaked the server's %q:\n%s",
				strings.TrimSpace(leak), out)
		}
	}
}

// A shared server whose own label was mtest described itself as "for project mtest". A
// session asking about another project on it read that as a mismatch and spent a turn
// hunting a problem that did not exist: its calls were already reaching the right project.
// A shared server must say it serves every project, list them and say how to choose one,
// in its instructions and in its title. A single-project server still names its project.
func TestASharedServerSaysEveryProject(t *testing.T) {
	env := brain.Env{Project: "mtest"}
	ins := whereThisBrainIs(env, true, []string{"acme", "mtest"})
	for _, want := range []string{"EVERY project", "_project", ProjectHeader, "acme, mtest"} {
		if !strings.Contains(ins, want) {
			t.Errorf("a shared server does not say %q:\n%s", want, ins)
		}
	}
	if strings.Contains(ins, `for project "mtest"`) {
		t.Errorf("a shared server still reads as bound to its home project:\n%s", ins)
	}
	s := &mcpServer{defaultProject: "mtest", multi: true}
	if title := s.title(); strings.Contains(title, "mtest") || !strings.Contains(title, "every project") {
		t.Errorf("a shared server is titled %q", title)
	}
	one := whereThisBrainIs(env, false, nil)
	if !strings.Contains(one, `for project "mtest"`) || strings.Contains(one, "EVERY project") {
		t.Errorf("a single-project server lost its project:\n%s", one)
	}
}

// One server writes on behalf of many sessions. Taking the session id from the process
// environment would stamp every one of them with the SERVER's id and the record of which
// session wrote what would collapse to a single value: the same failure as _project, one
// axis over and worst on a shared server. The caller's _session wins, then the MCP
// connection's id and only with neither does the process's own id apply.
func TestResolveSessionPrefersCallerThenConnection(t *testing.T) {
	cases := []struct {
		name       string
		args       map[string]any
		mcpSession string
		want       string
	}{
		{"explicit _session wins", map[string]any{"_session": "ci-runner-7"}, "conn-abc", "ci-runner-7"},
		{"connection id when unstated", map[string]any{}, "conn-abc", "mcp:conn-abc"},
		{"blank _session falls through", map[string]any{"_session": "  "}, "conn-abc", "mcp:conn-abc"},
		{"nothing at all defers to the process", map[string]any{}, "", ""},
		{"non-string _session ignored", map[string]any{"_session": 42}, "conn-abc", "mcp:conn-abc"},
	}
	for _, c := range cases {
		if got := resolveSession(c.args, c.mcpSession); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// _session is transport plumbing and must never reach the CLI as a flag, exactly like
// _project. If it leaked through, every call would fail on an unknown flag.
func TestSessionIsNotForwardedAsAFlag(t *testing.T) {
	argv, err := argvFor("remember", map[string]any{
		"title": "nginx holds the wildcard certificate", "body": "x",
		"_session": "ci-runner-7", "_project": "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range argv {
		if strings.Contains(a, "_session") || strings.Contains(a, "ci-runner-7") ||
			strings.Contains(a, "_project") {
			t.Fatalf("transport argument leaked into argv: %v", argv)
		}
	}
}

// Every tool must offer _session, or a client cannot identify itself on the tools that
// write.
func TestEveryToolAcceptsSession(t *testing.T) {
	tools, err := buildTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		props, _ := tool.InputSchema["properties"].(map[string]any)
		if _, ok := props["_session"]; !ok {
			t.Errorf("%s does not accept _session", tool.Name)
		}
	}
}
