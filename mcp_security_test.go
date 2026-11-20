package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The checks the MCP HTTP transport makes on every request. The endpoint serves every
// memory in the brain and "it only listens on localhost" protects nothing on its own:
// every local process can reach loopback and so can a web page through DNS rebinding,
// where a hostname the attacker controls is re-pointed at 127.0.0.1 after the page loads.
// So a request needs a bearer token, an allowed Origin and Host, a JSON body and, after
// initialize, the session id it was given.

// mcpPost sends body to url's /mcp with the given headers ("Host" sets the request's Host)
// and returns the response with its body closed.
func mcpPost(t *testing.T, url string, hdr map[string]string, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// initBody is the smallest request every MCP session starts with.
const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

// Whether an address is loopback decides how much the server demands before it listens,
// so the wildcard binds (0.0.0.0, ::, an empty host) and LAN addresses must never pass.
func TestLoopbackDetection(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7777", "localhost:7777", "[::1]:7777"} {
		if !isLoopback(addr) {
			t.Errorf("%s should be loopback", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:7777", "192.168.1.10:7777", ":7777", "[::]:7777"} {
		if isLoopback(addr) {
			t.Errorf("%s is NOT loopback", addr)
		}
	}
}

// A loopback bind with no --token once served the whole store to every process on the
// machine. Without a bearer the handler now answers 401, even on loopback.
func TestLoopbackWithoutATokenStillNeedsOne(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.httpHandler(httpOpts{addr: "127.0.0.1:0"}))
	defer srv.Close()
	r := mcpPost(t, srv.URL, map[string]string{"Content-Type": "application/json"}, initBody)
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated loopback initialize got %d, want 401", r.StatusCode)
	}
}

// So a loopback server started without --token mints one: long enough not to be guessed,
// written to a file only the owner can read (which is how the owner's client finds it), and
// fresh for every server process, so one leaked token does not open the next run.
func TestALoopbackServerMintsItsOwnTokenAndWritesItPrivately(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OS_BRAIN_GLOBAL_DB", filepath.Join(dir, "cfg", "brain.db"))
	o := httpOpts{addr: "127.0.0.1:7801"}
	path, err := mintHTTPToken(&o)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.token) < 32 {
		t.Fatalf("minted token %q is too short", o.token)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != o.token {
		t.Errorf("the token file holds %q, the server checks %q", got, o.token)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v, want 0600", fi.Mode().Perm())
	}
	o2 := httpOpts{addr: "127.0.0.1:7801"}
	if _, err := mintHTTPToken(&o2); err != nil || o2.token == o.token {
		t.Errorf("a second process reused the first one's token (%v)", err)
	}
}

// A browser sends the page's Origin with every request, so checking it stops a web page
// driving a localhost server. By default only loopback origins pass; an allow-list
// replaces that default rather than adding to it.
func TestOriginIsValidatedAgainstDNSRebinding(t *testing.T) {
	if originAllowed("https://evil.example", "") {
		t.Error("a foreign origin was allowed by default")
	}
	if !originAllowed("http://localhost:3000", "") {
		t.Error("localhost should be allowed by default")
	}
	if !originAllowed("https://my.app", "https://my.app") {
		t.Error("an explicitly allowed origin was refused")
	}
	if originAllowed("https://evil.example", "https://my.app") {
		t.Error("the allow-list did not exclude others")
	}
}

// The default Origin check was once a prefix match, so any hostname that merely STARTS
// with a loopback name (localhost.evil.com, 127.0.0.1.nip.io) passed it. The host is now
// parsed and compared whole; "null", file:// and userinfo tricks are refused too.
func TestOriginLookalikesOfLoopbackAreRefused(t *testing.T) {
	for _, o := range []string{"http://localhost.evil.com", "http://127.0.0.1.nip.io",
		"http://localhostXYZ.attacker.tld", "https://localhost.evil.com:443", "null",
		"file://localhost", "http://127.0.0.1@evil.com"} {
		if originAllowed(o, "") {
			t.Errorf("%q passed the default origin check", o)
		}
	}
	for _, o := range []string{"http://localhost", "http://localhost:3000", "http://127.0.0.1:8080",
		"https://localhost", "http://[::1]:7777"} {
		if !originAllowed(o, "") {
			t.Errorf("%q is loopback and was refused", o)
		}
	}
}

// The whole attack as a rebound web page would send it: a lookalike Origin, the attacker's
// own Host, a text/plain body (which a browser sends cross-origin without asking first), no
// credential and no session. Neither initialize nor a write may succeed.
func TestADriveByRequestFromARebindingPageIsRefused(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.httpHandler(httpOpts{addr: "127.0.0.1:0"}))
	defer srv.Close()
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"brain_remember",` +
		`"arguments":{"title":"injected rule from a web page","type":"guardrail"}}}`
	for _, body := range []string{initBody, call} {
		r := mcpPost(t, srv.URL, map[string]string{"Origin": "http://localhost.evil.com",
			"Host": "localhost.evil.com:47392", "Content-Type": "text/plain"}, body)
		if r.StatusCode == http.StatusOK || r.StatusCode == http.StatusAccepted {
			t.Fatalf("the drive-by request got %d", r.StatusCode)
		}
	}
}

// Each check on its own, starting from a request that passes them all:
//   - Host must be loopback or one configured with hosts; a rebound page sends its own
//     hostname, so this is what stops it even when the Origin check is bypassed;
//   - the body must be JSON, because text/plain and form bodies are the ones a browser
//     sends cross-origin without a preflight;
//   - the token must arrive as "Bearer <token>", not bare.
func TestTheHTTPTransportChecksHostContentTypeAndBearer(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.httpHandler(httpOpts{addr: "127.0.0.1:0", token: "sekrit",
		hosts: "brain.example.com"}))
	defer srv.Close()
	ok := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer sekrit"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range ok {
			m[a] = b
		}
		m[k] = v
		return m
	}
	cases := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"foreign host", with("Host", "localhost.evil.com:7777"), http.StatusForbidden},
		{"rebinding host", with("Host", "attacker.example"), http.StatusForbidden},
		{"text/plain body", with("Content-Type", "text/plain"), http.StatusUnsupportedMediaType},
		{"form body", with("Content-Type", "application/x-www-form-urlencoded"), http.StatusUnsupportedMediaType},
		{"bare token", with("Authorization", "sekrit"), http.StatusUnauthorized},
		{"configured host", with("Host", "brain.example.com"), http.StatusOK},
		{"loopback host", with("Host", "localhost:7801"), http.StatusOK},
		{"json with charset", with("Content-Type", "application/json; charset=utf-8"), http.StatusOK},
	}
	for _, c := range cases {
		if r := mcpPost(t, srv.URL, c.hdr, initBody); r.StatusCode != c.want {
			t.Errorf("%s: got %d, want %d", c.name, r.StatusCode, c.want)
		}
	}
}

// The order a real client meets the checks: a wrong token is 401, a foreign Origin 403, a
// valid initialize returns a session id and every later request must carry it. A
// notification is acknowledged with 202 and no body.
func TestHTTPHandlerEnforcesTokenOriginAndSession(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.httpHandler(httpOpts{addr: "127.0.0.1:0", token: "sekrit"}))
	defer srv.Close()

	post := func(body, token, origin, session string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if r := post(initBody, "wrong", "", ""); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token got %d", r.StatusCode)
	}
	if r := post(initBody, "sekrit", "https://evil.example", ""); r.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin got %d", r.StatusCode)
	}
	r := post(initBody, "sekrit", "http://localhost:3000", "")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("valid initialize got %d", r.StatusCode)
	}
	sid := r.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("no Mcp-Session-Id on the InitializeResult")
	}
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	if r := post(list, "sekrit", "", ""); r.StatusCode != http.StatusBadRequest {
		t.Errorf("a request without the session id got %d", r.StatusCode)
	}
	if r := post(list, "sekrit", "", sid); r.StatusCode != http.StatusOK {
		t.Errorf("a request with the session id got %d", r.StatusCode)
	}
	// A notification carries no response.
	notif := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	if r := post(notif, "sekrit", "", sid); r.StatusCode != http.StatusAccepted {
		t.Errorf("notification got %d, want 202", r.StatusCode)
	}
}

// A restarted server has forgotten every session. Answering a stale id with 400 left a
// hosted connector stuck until the user reconnected by hand; 404 is the MCP spec's signal
// to initialize again and the JSON-RPC error code -32001 is what clients check for
// alongside it. A new session from another client must not make the stale one valid.
func TestHTTPHandlerTellsAStaleSessionToReinitialize(t *testing.T) {
	mcpEnv(t)
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	post := func(url, body, session string) *http.Response {
		req, _ := http.NewRequest("POST", url+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sekrit")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	before := httptest.NewServer(s.httpHandler(httpOpts{addr: "127.0.0.1:0", token: "sekrit"}))
	stale := post(before.URL, initBody, "").Header.Get("Mcp-Session-Id")
	before.Close()

	after := httptest.NewServer(s.httpHandler(httpOpts{addr: "127.0.0.1:0", token: "sekrit"}))
	defer after.Close()

	expect404 := func(when string) {
		t.Helper()
		r := post(after.URL, list, stale)
		defer r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: stale session got %d, want 404", when, r.StatusCode)
		}
		var body rpcResponse
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Error == nil ||
			body.Error.Code != codeSessionNotFound {
			t.Fatalf("%s: stale session body %+v (%v), want error %d", when, body, err, codeSessionNotFound)
		}
	}
	expect404("nothing initialized since the restart")
	fresh := post(after.URL, initBody, "").Header.Get("Mcp-Session-Id")
	expect404("another client initialized since the restart")
	if r := post(after.URL, list, fresh); r.StatusCode != http.StatusOK {
		t.Fatalf("re-initialized session got %d", r.StatusCode)
	}
}

// wakeup runs the shell commands (probes) a project declares and `wakeup trust` approves
// them, so trusting is approving code to run on this machine. It is a person's act:
// refused over MCP and refused on the CLI without a terminal. Adding or removing a probe
// is refused over HTTP as well: probes are commands this machine runs and a remote caller
// does not get to edit them.
func TestWakeupTrustIsAPersonsDecision(t *testing.T) {
	mcpEnv(t)
	useTerminal(t, nil)
	run := func(tr transport, args ...string) error {
		var buf strings.Builder
		return runWithVia(callVia{transport: tr}, "wakeup", args, &buf,
			flag.NewFlagSet("wakeup", flag.ContinueOnError), false, false, "", "")
	}
	if err := run(transportCLI, "add", "--name", "stack", "--run", "echo up"); err != nil {
		t.Fatal(err)
	}
	for _, tr := range []transport{transportMCPStdio, transportMCPHTTP} {
		if err := run(tr, "trust"); err == nil || !strings.Contains(err.Error(), "refused over MCP") {
			t.Errorf("trust over %s: %v", tr, err)
		}
	}
	if err := run(transportCLI, "trust"); err == nil || !strings.Contains(err.Error(), "controlling terminal") {
		t.Errorf("trust on the CLI with no terminal: %v", err)
	}
	for _, sub := range [][]string{{"add", "--name", "x", "--run", "id"}, {"rm", "--name", "stack"}} {
		if err := run(transportMCPHTTP, sub...); err == nil || !strings.Contains(err.Error(), "refused over HTTP") {
			t.Errorf("wakeup %s over MCP HTTP: %v", sub[0], err)
		}
	}

	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	s.transport = transportMCPHTTP
	if out, isErr := callText(t, s, "brain_wakeup", map[string]any{"args": "trust"}); !isErr {
		t.Errorf("brain_wakeup trust succeeded over MCP: %s", out)
	}
	if out, isErr := callText(t, s, "brain_wakeup", map[string]any{"args": "add", "name": "pwn",
		"run": "id"}); !isErr {
		t.Errorf("brain_wakeup add succeeded over MCP HTTP: %s", out)
	}
}
