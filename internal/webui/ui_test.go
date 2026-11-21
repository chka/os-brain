package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chka/os-brain/internal/cli"
	"github.com/chka/os-brain/internal/clitest"
)

// The web UI (`os-brain ui`): an embedded page served on loopback, reading JSON routes that
// run the ordinary commands, opened through a one-time link.

// seedUI makes a fresh brain (clitest.Brain) and gives the routes something to read: two memories
// and an event. It writes through the CLI, because that is the only path that keeps the
// full-text index in step.
func seedUI(t *testing.T) {
	t.Helper()
	clitest.Brain(t, "mcptest")
	for _, argv := range [][]string{
		{"remember", "nginx holds the wildcard certificate", "--body", "renews by certbot",
			"--type", "infra", "--source", "stated by the user", "--confidence", "confirmed"},
		{"remember", "the deploy runs from the makefile", "--body", "make deploy tags and pushes",
			"--type", "process", "--scope", "channel:deploy"},
		{"log", "--summary", "shipped 26080701", "--kind", "note"},
	} {
		var out bytes.Buffer
		if err := cli.Run(argv, &out); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
}

// The assets have to be in the binary. This is the whole point of the command: nothing
// ever installed a viewer, so a path on disk is not a delivery mechanism.
func TestTheUIIsEmbedded(t *testing.T) {
	for _, f := range []string{"ui/index.html", "ui/app.js", "ui/style.css"} {
		body, err := uiFiles.ReadFile(f)
		if err != nil {
			t.Fatalf("%s is not embedded: %v", f, err)
		}
		if len(body) < 500 {
			t.Errorf("%s is suspiciously small: %d bytes", f, len(body))
		}
	}
	index, _ := uiFiles.ReadFile("ui/index.html")
	for _, want := range []string{`id="view-tree"`, `id="mem-list"`, "/app.js", "/style.css"} {
		if !strings.Contains(string(index), want) {
			t.Errorf("the embedded index.html has no %q", want)
		}
	}
	app, _ := uiFiles.ReadFile("ui/app.js")
	// Every route the page fetches must exist on the server side.
	routes := uiRoutes()
	for _, path := range []string{"/api/brief", "/api/memories", "/api/tree", "/api/events",
		"/api/measures", "/api/map", "/api/stats", "/api/meta", "/api/leads"} {
		if strings.Contains(string(app), path) {
			if _, ok := routes[path]; !ok {
				t.Errorf("the page fetches %s and the server does not serve it", path)
			}
		}
	}
}

// Every route the page calls exists and answers with JSON. The page cannot report a
// server-side failure, so a route that errors or returns nothing is a blank tab with no
// error anywhere.
func TestEveryUIRouteReturnsJSON(t *testing.T) {
	seedUI(t)
	routes := uiRoutes()
	for _, want := range []string{"/api/brief", "/api/memories", "/api/tree", "/api/events",
		"/api/measures", "/api/map", "/api/stats", "/api/meta", "/api/leads"} {
		if _, ok := routes[want]; !ok {
			t.Errorf("route %s is missing; the page calls it", want)
		}
	}
	for path, route := range routes {
		u, _ := url.Parse("http://127.0.0.1:7788" + path)
		data, err := route(u)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		body, err := json.Marshal(data)
		if err != nil {
			t.Errorf("%s does not marshal: %v", path, err)
		}
		if len(body) == 0 || string(body) == "null" {
			t.Errorf("%s returned nothing", path)
		}
	}
}

// The page reads these keys by name. A rename in the CLI's JSON is invisible until a tab
// renders empty, a drift an earlier viewer suffered three times. The key that grants
// access must not be among them (see the comment on /api/meta below).
func TestUIPayloadsCarryWhatThePageReads(t *testing.T) {
	seedUI(t)
	routes := uiRoutes()
	check := func(path string, keys ...string) {
		t.Helper()
		u, _ := url.Parse("http://127.0.0.1:7788" + path)
		data, err := routes[path](u)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		m, ok := data.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object", path)
		}
		for _, k := range keys {
			if _, present := m[k]; !present {
				t.Errorf("%s is missing %q", path, k)
			}
		}
	}
	check("/api/memories", "memories", "matched", "scopes", "types")
	check("/api/tree", "memories", "declared", "tombstones")
	check("/api/events", "events", "kinds", "scopes")
	check("/api/measures", "measures", "count", "series")
	check("/api/leads", "leads", "statuses", "segments")
	check("/api/brief", "stats", "now")
	// The token is NOT here any more: /api/meta answered anybody and returning the key from
	// it handed every local user the brain. The page takes it from its own HTML.
	check("/api/meta", "project", "db")
	if m, _ := routes["/api/meta"](nil); m != nil {
		if _, leaked := m.(map[string]any)["token"]; leaked {
			t.Error("/api/meta still returns the token")
		}
	}

	u, _ := url.Parse("http://127.0.0.1:7788/api/memories")
	data, _ := routes["/api/memories"](u)
	mems, _ := data.(map[string]any)["memories"].([]any)
	if len(mems) == 0 {
		t.Fatal("the seeded memories did not come back")
	}
	first, _ := mems[0].(map[string]any)
	for _, k := range []string{"id", "title", "body", "scope", "mem_type", "tier", "score",
		"importance", "use_count", "updated_at", "pinned"} {
		if _, ok := first[k]; !ok {
			t.Errorf("a memory in the UI payload has no %q; the card renders it", k)
		}
	}
}

// An empty project must render an empty page, not an error. A brand new brain is the
// first thing a new user opens.
func TestUIOnAnEmptyBrain(t *testing.T) {
	clitest.Brain(t, "mcptest")
	for path, route := range uiRoutes() {
		u, _ := url.Parse("http://127.0.0.1:7788" + path)
		if _, err := route(u); err != nil {
			t.Errorf("%s fails on an empty brain: %v", path, err)
		}
	}
}

// Forgetting from the UI needs an id and a reason, like the CLI and refuses without them
// before it reaches the store.
func TestUIForgetRefusesWithoutAReason(t *testing.T) {
	seedUI(t)
	if _, err := uiForget(map[string]any{"id": float64(1)}); err == nil ||
		!strings.Contains(err.Error(), "why") {
		t.Errorf("a blank reason was accepted: %v", err)
	}
	if _, err := uiForget(map[string]any{"reason": "x"}); err == nil {
		t.Error("forget without an id was accepted")
	}
}

// The refusals a memory can meet are the CLI's own text, not a weaker copy: the route
// builds an argv and runs the command.
func TestUIForgetSurfacesTheCLIRefusal(t *testing.T) {
	seedUI(t)
	var out bytes.Buffer
	if err := cli.RunJSON(cli.TransportCLI, []string{"remember", "a pinned fact", "--body", "x",
		"--type", "knowledge", "--pin"}, &out); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	id, _ := m["id"].(float64)
	if id == 0 {
		t.Fatal("no id came back from remember")
	}
	_, err := uiForget(map[string]any{"id": id, "reason": "testing the guard"})
	if err == nil {
		t.Fatal("the UI deleted a pinned memory")
	}
	if !strings.Contains(err.Error(), "pinned") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal lost the CLI's wording: %v", err)
	}
}

// The UI has no TLS and no login beyond its launch link, so it listens on loopback only. A
// wildcard, empty or LAN host would serve the whole brain to the network.
func TestUIRefusesANonLoopbackHost(t *testing.T) {
	// A brain of its own: run from a checkout with no brain.json, the command would stop at
	// "no brain here" before reaching what this test checks.
	clitest.Brain(t, "mcptest")
	for _, h := range []string{"0.0.0.0", "", "192.168.1.10", "::"} {
		err := cli.Run([]string{"ui", "--host", h, "--no-open"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "loopback only") {
			t.Errorf("--host %q: expected a loopback refusal, got %v", h, err)
		}
	}
}

// The launcher opens the page by passing a link to the browser on its command line, where
// any local user can read it in the process list. So the link carries a one-time launch
// code, never the key: opening it sets the key in an HttpOnly, SameSite=Strict cookie and
// redirects, after which the link is worthless. An expired link, a missing or wrong
// cookie, or the key itself put in the URL all get 403.
func TestUIBootstrapLinkIsSpentForACookie(t *testing.T) {
	seedUI(t)
	h, err := uiHandler()
	if err != nil {
		t.Fatal(err)
	}
	do := func(path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "http://127.0.0.1:7788"+path, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	link := uiURL("http://127.0.0.1:7788", uiLaunchTTL)
	if strings.Contains(link, uiToken) {
		t.Fatal("the opened link carries the key")
	}
	u, _ := url.Parse(link)
	w := do("/?"+u.RawQuery, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("bootstrap: %d %q", w.Code, w.Header().Get("Location"))
	}
	var jar *http.Cookie
	for _, c := range w.Result().Cookies() {
		jar = c
	}
	if jar == nil || jar.Value != uiToken || !jar.HttpOnly || jar.SameSite != http.SameSiteStrictMode ||
		jar.Path != "/" || jar.Name != "brain_ui_7788" {
		t.Fatalf("cookie = %+v", jar)
	}
	if w := do("/?"+u.RawQuery, nil); w.Code != http.StatusForbidden {
		t.Errorf("a spent link opened the page again: %d", w.Code)
	}
	w = do("/", jar)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="brain-token" content="`+uiToken+`"`) {
		t.Fatalf("the cookie did not open the page: %d", w.Code)
	}
	if w := do("/", nil); w.Code != http.StatusForbidden {
		t.Errorf("the page without the cookie: %d", w.Code)
	}
	if w := do("/", &http.Cookie{Name: "brain_ui_7788", Value: "wrong"}); w.Code != http.StatusForbidden {
		t.Errorf("a wrong cookie: %d", w.Code)
	}
	if w := do("/?k="+uiToken, nil); w.Code != http.StatusForbidden {
		t.Errorf("the key itself in the URL still opens the page: %d", w.Code)
	}
	expired := uiURL("http://127.0.0.1:7788", time.Nanosecond)
	time.Sleep(time.Millisecond)
	eu, _ := url.Parse(expired)
	if w := do("/?"+eu.RawQuery, nil); w.Code != http.StatusForbidden {
		t.Errorf("an expired launch link opened the page: %d", w.Code)
	}
}

// The UI's argv puts "--" in front of the text a page sends, and its routes answer a query
// that looks like a flag instead of failing on it. The parser half of the rule is
// TestUserTextNeverBecomesAFlag in internal/cli.
func TestUserTextNeverBecomesAFlagInTheUI(t *testing.T) {
	if got := positional([]string{"recall", "--browse"}, "--all-projects"); strings.Join(got, " ") !=
		"recall --browse -- --all-projects" {
		t.Errorf("UI argv: %q", got)
	}

	seedUI(t)
	h, err := uiHandler()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		req := httptest.NewRequest("GET", "http://127.0.0.1:7788"+path, nil)
		req.Header.Set("X-Brain-Token", uiToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, body := get("/api/memories?q=--help"); code != 200 {
		t.Errorf("?q=--help reached the flag parser: %d %s", code, body)
	}
	if code, body := get("/api/events?q=--kind"); code != 200 {
		t.Errorf("events ?q=--kind: %d %s", code, body)
	}
}
