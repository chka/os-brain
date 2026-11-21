package mcp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chka/os-brain/internal/clitest"
	"github.com/chka/os-brain/internal/netguard"
)

// OAuth for hosted MCP clients, which cannot be given a static bearer token: the client
// registers itself, sends the owner to a login page and trades the code for tokens. The
// login password is the one secret that guards the whole store, so most of these tests
// are about what an attacker on the network cannot do with the flow: guess the password
// quickly, use it as a bearer, lock the owner out, flood the client table, read tokens
// from the state file, or send a code to a redirect the owner did not mean to approve.

// oauthFixture is a handler with OAuth mounted, its login password "sekrit" and a PKCE
// verifier and challenge for a client that logs in.
type oauthFixture struct {
	t         *testing.T
	srv       *httptest.Server
	oa        *oauthServer
	state     string // path of the OAuth state file
	verifier  string
	challenge string
}

// newOAuthFixture mounts OAuth with its state at statePath, or in a fresh temporary file
// when statePath is empty.
func newOAuthFixture(t *testing.T, statePath string) *oauthFixture {
	return newOAuthFixtureWith(t, statePath, nil)
}

// newOAuthFixtureWith is newOAuthFixture with --oauth-redirect-host set to redirectHosts.
func newOAuthFixtureWith(t *testing.T, statePath string, redirectHosts []string) *oauthFixture {
	clitest.Brain(t, "mcptest")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	if statePath == "" {
		statePath = filepath.Join(t.TempDir(), "mcp-oauth.json")
	}
	opts := httpOpts{addr: "127.0.0.1:0", token: "sekrit",
		publicURL: "https://laptop.example.ts.net", oauthPath: statePath, redirectHosts: redirectHosts}
	oa, err := oauthFor(opts)
	if err != nil {
		t.Fatal(err)
	}
	f := &oauthFixture{t: t, srv: httptest.NewServer(s.httpHandlerWith(opts, oa)), oa: oa, state: statePath,
		verifier: strings.Repeat("v", 43)}
	t.Cleanup(f.srv.Close)
	sum := sha256.Sum256([]byte(f.verifier))
	f.challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return f
}

// register registers a client by dynamic registration and returns its id, failing the
// test if the registration is refused.
func (f *oauthFixture) register(name, redirect string) string {
	r, err := http.Post(f.srv.URL+"/register", "application/json",
		strings.NewReader(`{"client_name":"`+name+`","redirect_uris":["`+redirect+`"]}`))
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	var reg struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&reg)
	if r.StatusCode != 201 {
		f.t.Fatalf("register: %d", r.StatusCode)
	}
	return reg.ClientID
}

// tryRegister is register for a request that may be refused: the status and the client id
// when there is one.
func (f *oauthFixture) tryRegister(redirect string, hdr map[string]string) (int, string) {
	req, _ := http.NewRequest("POST", f.srv.URL+"/register",
		strings.NewReader(`{"client_name":"Claude","redirect_uris":["`+redirect+`"]}`))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	var reg struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&reg)
	return r.StatusCode, reg.ClientID
}

// authz is the authorization request's parameters, with the fixture's PKCE challenge.
func (f *oauthFixture) authz(client, redirect string) url.Values {
	return url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {redirect},
		"code_challenge": {f.challenge}, "code_challenge_method": {"S256"}}
}

// login submits the login page with password. On success it returns 302 and the code;
// otherwise the status and the page that came back.
func (f *oauthFixture) login(client, redirect, password string, hdr map[string]string) (int, string) {
	form := f.authz(client, redirect)
	form.Set("password", password)
	req, _ := http.NewRequest("POST", f.srv.URL+"/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	r, err := noRedirect.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode == 302 {
		loc, _ := url.Parse(r.Header.Get("Location"))
		return 302, loc.Query().Get("code")
	}
	return r.StatusCode, string(b)
}

// token trades a code for the access and refresh tokens, failing the test if none is issued.
func (f *oauthFixture) token(client, redirect, code string) (string, string) {
	r, err := http.PostForm(f.srv.URL+"/token", url.Values{"grant_type": {"authorization_code"},
		"client_id": {client}, "redirect_uri": {redirect}, "code": {code}, "code_verifier": {f.verifier}})
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	a, _ := m["access_token"].(string)
	rt, _ := m["refresh_token"].(string)
	if a == "" {
		f.t.Fatalf("token: %d %v", r.StatusCode, m)
	}
	return a, rt
}

// mcp sends initialize to /mcp with bearer and returns the status.
func (f *oauthFixture) mcp(bearer string, hdr map[string]string) int {
	req, _ := http.NewRequest("POST", f.srv.URL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	r.Body.Close()
	return r.StatusCode
}

// The whole flow a hosted MCP client runs, in the order the spec draws it: 401 with
// resource_metadata, discovery, dynamic registration, the login page, the code, the token,
// the endpoint, then refresh. Along the way the refusals: the login password as a bearer,
// a plain-http redirect, an unknown client, a wrong password, a wrong verifier, a reused
// code and a reused refresh token. Last, the issued token still works after a restart
// that reloads the state file, so a connected client does not have to log in again.
func TestOAuthRoundTripForAHostedClient(t *testing.T) {
	clitest.Brain(t, "mcptest")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "mcp-oauth.json")
	opts := httpOpts{addr: "127.0.0.1:0", token: "sekrit",
		publicURL: "https://laptop.example.ts.net", oauthPath: statePath}
	h, err := s.httpHandlerE(opts)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	post := func(path, body, bearer string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		r, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	getJSON := func(path string, into interface{}) {
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("GET %s: %d", path, r.StatusCode)
		}
		if err := json.NewDecoder(r.Body).Decode(into); err != nil {
			t.Fatal(err)
		}
	}

	// 1. no token: 401 that names the metadata
	r := post("/mcp", initBody, "")
	if r.StatusCode != 401 {
		t.Fatalf("no token got %d", r.StatusCode)
	}
	if wa := r.Header.Get("WWW-Authenticate"); !strings.Contains(wa, "resource_metadata=\"https://laptop.example.ts.net/.well-known/oauth-protected-resource\"") {
		t.Fatalf("WWW-Authenticate = %q", wa)
	}
	// Once OAuth is mounted the login password is not a bearer (see
	// TestOAuthPasswordIsNotABearerAndBearerGuessesAreCounted).
	if r := post("/mcp", initBody, "sekrit"); r.StatusCode != 401 {
		t.Fatalf("the login password worked as a bearer: %d", r.StatusCode)
	}

	// 2. discovery
	var prm struct {
		Resource string   `json:"resource"`
		AS       []string `json:"authorization_servers"`
	}
	getJSON("/.well-known/oauth-protected-resource", &prm)
	if prm.Resource != "https://laptop.example.ts.net/mcp" || len(prm.AS) != 1 {
		t.Fatalf("PRM = %+v", prm)
	}
	var as struct {
		Issuer, Authz, Token, Reg string   `json:"-"`
		Methods                   []string `json:"code_challenge_methods_supported"`
		Raw                       map[string]interface{}
	}
	getJSON("/.well-known/oauth-authorization-server", &as.Raw)
	for _, k := range []string{"issuer", "authorization_endpoint", "token_endpoint", "registration_endpoint"} {
		if as.Raw[k] == nil {
			t.Fatalf("AS metadata lacks %s", k)
		}
	}

	// 3. dynamic registration
	regBody := `{"client_name":"Claude","redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"token_endpoint_auth_method":"none"}`
	r = post("/register", regBody, "")
	if r.StatusCode != 201 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("register got %d: %s", r.StatusCode, b)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&reg)
	if !strings.HasPrefix(reg.ClientID, "c_") {
		t.Fatalf("client_id = %q", reg.ClientID)
	}
	if r := post("/register", `{"redirect_uris":["http://evil.example/cb"]}`, ""); r.StatusCode != 400 {
		t.Errorf("a plain-http non-loopback redirect registered: %d", r.StatusCode)
	}

	// 4. the login page, then the password
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{"response_type": {"code"}, "client_id": {reg.ClientID},
		"redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "state": {"xyz"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"resource": {"https://laptop.example.ts.net/mcp"}}
	page, err := http.Get(srv.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	if page.StatusCode != 200 || !strings.Contains(string(body), `name="password"`) {
		t.Fatalf("login page: %d %s", page.StatusCode, body)
	}
	if r, _ := http.Get(srv.URL + "/authorize?" + strings.Replace(q.Encode(), reg.ClientID, "c_nobody", 1)); r.StatusCode != 400 {
		t.Errorf("unknown client got a login page: %d", r.StatusCode)
	}
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("password", "nope")
	r, _ = noRedirect.PostForm(srv.URL+"/authorize", form)
	if r.StatusCode != 200 {
		t.Fatalf("wrong password should re-render, got %d", r.StatusCode)
	}
	if b, _ := io.ReadAll(r.Body); !strings.Contains(string(b), "wrong password") {
		t.Fatal("wrong password not reported")
	}
	form.Set("password", "sekrit")
	r, _ = noRedirect.PostForm(srv.URL+"/authorize", form)
	if r.StatusCode != 302 {
		t.Fatalf("right password got %d", r.StatusCode)
	}
	loc, _ := url.Parse(r.Header.Get("Location"))
	if loc.Host != "claude.ai" || loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("redirect = %s", loc)
	}
	code := loc.Query().Get("code")

	// 5. the token: wrong verifier first, then right, then the code is spent
	tok := func(v url.Values) (int, map[string]interface{}) {
		r, err := http.PostForm(srv.URL+"/token", v)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var m map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&m)
		return r.StatusCode, m
	}
	base := url.Values{"grant_type": {"authorization_code"}, "client_id": {reg.ClientID},
		"redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"resource":     {"https://laptop.example.ts.net/mcp"}}
	bad := url.Values{}
	for k, v := range base {
		bad[k] = v
	}
	bad.Set("code", code)
	bad.Set("code_verifier", strings.Repeat("w", 43))
	if st, m := tok(bad); st != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", st, m)
	}
	// a code is single-use even when the attempt failed: get a fresh one
	r, _ = noRedirect.PostForm(srv.URL+"/authorize", form)
	loc, _ = url.Parse(r.Header.Get("Location"))
	code = loc.Query().Get("code")
	good := url.Values{}
	for k, v := range base {
		good[k] = v
	}
	good.Set("code", code)
	good.Set("code_verifier", verifier)
	st, m := tok(good)
	if st != 200 || m["access_token"] == nil || m["refresh_token"] == nil {
		t.Fatalf("token: %d %v", st, m)
	}
	access := m["access_token"].(string)
	if st, _ := tok(good); st != 400 {
		t.Errorf("a spent code was accepted: %d", st)
	}

	// 6. the endpoint with the issued token
	if r := post("/mcp", initBody, access); r.StatusCode != 200 {
		t.Fatalf("issued token got %d", r.StatusCode)
	}
	if r := post("/mcp", initBody, "not-a-token"); r.StatusCode != 401 {
		t.Fatalf("garbage token got %d", r.StatusCode)
	}

	// 7. refresh rotates: old refresh and old access both die
	st, m2 := tok(url.Values{"grant_type": {"refresh_token"}, "client_id": {reg.ClientID},
		"refresh_token": {m["refresh_token"].(string)}})
	if st != 200 || m2["access_token"] == access {
		t.Fatalf("refresh: %d %v", st, m2)
	}
	if r := post("/mcp", initBody, access); r.StatusCode != 401 {
		t.Errorf("rotated-out access token still works: %d", r.StatusCode)
	}
	if st, _ := tok(url.Values{"grant_type": {"refresh_token"}, "client_id": {reg.ClientID},
		"refresh_token": {m["refresh_token"].(string)}}); st != 400 {
		t.Errorf("a used refresh token was accepted again: %d", st)
	}
	access2 := m2["access_token"].(string)

	// 8. a restart keeps the client connected: new handler, same state file
	h2, err := s.httpHandlerE(opts)
	if err != nil {
		t.Fatal(err)
	}
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	req, _ := http.NewRequest("POST", srv2.URL+"/mcp", strings.NewReader(initBody))
	req.Header.Set("Authorization", "Bearer "+access2)
	req.Header.Set("Content-Type", "application/json")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != 200 {
		t.Fatalf("token did not survive a restart: %d", r.StatusCode)
	}
}

// OAuth needs two things. An https public URL, because the login password and the tokens
// travel through it. And a token, because the token doubles as the login password.
func TestOAuthRefusesPlainHTTPPublicURLAndMissingToken(t *testing.T) {
	clitest.Brain(t, "mcptest")
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.httpHandlerE(httpOpts{addr: "0.0.0.0:1", token: "x", publicURL: "http://laptop.example.ts.net"}); err == nil {
		t.Error("plain http public URL accepted")
	}
	if _, err := s.httpHandlerE(httpOpts{addr: "0.0.0.0:1", publicURL: "https://laptop.example.ts.net"}); err == nil {
		t.Error("public URL without a token accepted")
	}
}

// With OAuth mounted the login password is not a bearer: if it were, /mcp would be a
// second login page with no rate limit and an attacker would guess there. Bearer guesses
// at /mcp are counted against the same per-source limit as wrong passwords, so a source
// held back at one is held back at the other, while another source can still log in.
func TestOAuthPasswordIsNotABearerAndBearerGuessesAreCounted(t *testing.T) {
	f := newOAuthFixture(t, "")
	if code := f.mcp("sekrit", nil); code != 401 {
		t.Fatalf("the login password worked as a bearer on /mcp: %d", code)
	}
	from := map[string]string{"X-Forwarded-For": "203.0.113.7"}
	for i := 0; i < gateFree; i++ {
		f.mcp("guess", from)
	}
	if code := f.mcp("guess", from); code != http.StatusTooManyRequests {
		t.Fatalf("bearer guesses were not limited: %d", code)
	}
	// the same source is held at the login page too and another source is not
	redirect := "https://claude.ai/api/mcp/auth_callback"
	id := f.register("Claude", redirect)
	if code, body := f.login(id, redirect, "sekrit", from); code == 302 || !strings.Contains(body, "try again") {
		t.Errorf("a limited source logged in: %d", code)
	}
	other := map[string]string{"X-Forwarded-For": "198.51.100.9"}
	code, c := f.login(id, redirect, "sekrit", other)
	if code != 302 {
		t.Fatalf("another source was locked out by the first: %d %s", code, c)
	}
	access, _ := f.token(id, redirect, c)
	if code := f.mcp(access, other); code != 200 {
		t.Errorf("an issued token: %d", code)
	}
}

// The failure gate backs off per source, so one source failing as fast as it is allowed,
// for a whole day, never locks the owner out and its own wait never exceeds the cap. Only
// failures spread across many sources reach the global ceiling and the ceiling lifts
// when its window passes.
func TestFailGateBacksOffPerSourceAndOneSourceCannotHoldTheCeiling(t *testing.T) {
	g := newFailGate()
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	for i := 0; i < gateFree; i++ {
		if g.wait("a") != 0 {
			t.Fatalf("source waited after %d failures", i)
		}
		g.fail("a")
	}
	if w := g.wait("a"); w != gateBase {
		t.Fatalf("first backoff = %v", w)
	}
	if g.wait("owner") != 0 {
		t.Fatal("one source's failures locked out another")
	}
	// The attacker fails the moment each wait ends, for a whole day.
	fails := gateFree
	for end := now.Add(24 * time.Hour); now.Before(end); {
		if w := g.wait("a"); w > 0 {
			now = now.Add(w)
			continue
		}
		g.fail("a")
		fails++
		if g.wait("owner") != 0 {
			t.Fatalf("after %d failures from one source the owner is locked out", fails)
		}
	}
	if w := g.wait("a"); w > gateMax {
		t.Errorf("backoff exceeded its cap: %v", w)
	}
	// Many sources together do reach the ceiling.
	for i := 0; i < gateGlobal; i++ {
		g.fail("spread-" + string(rune('A'+i%26)) + string(rune('a'+i/26)))
	}
	if g.wait("owner") == 0 {
		t.Error("a spread-out guess did not reach the global ceiling")
	}
	now = now.Add(gateWindow)
	if g.wait("owner") != 0 {
		t.Error("the global ceiling outlived its window")
	}
}

// Registration is open to anyone who can reach the server, so it is rate limited per
// source: one source is limited on its own, it cannot spend the limit of another and the
// limit lifts when its window passes.
func TestOAuthRegistrationIsRateLimitedPerSource(t *testing.T) {
	f := newOAuthFixture(t, "")
	now := time.Now()
	f.oa.reg.now = func() time.Time { return now }
	from := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	for i := 0; i < regPerSource; i++ {
		if code, _ := f.tryRegister("https://evil.example/cb", from); code != 201 {
			t.Fatalf("registration %d from one source: %d", i+1, code)
		}
	}
	if code, _ := f.tryRegister("https://evil.example/cb", from); code != http.StatusTooManyRequests {
		t.Fatalf("registration %d from one source: %d", regPerSource+1, code)
	}
	if code, _ := f.tryRegister("https://claude.ai/api/mcp/auth_callback",
		map[string]string{"X-Forwarded-For": "198.51.100.9"}); code != 201 {
		t.Fatalf("another source was limited by the first: %d", code)
	}
	now = now.Add(regWindow)
	if code, _ := f.tryRegister("https://evil.example/cb", from); code != 201 {
		t.Errorf("the limit outlived its window: %d", code)
	}
}

// The client table is bounded, so a flood of registrations from many sources over many
// windows could push the owner's client out of it. It must not, whether the owner has
// already logged in through that client or registered it a moment ago and is still on the
// way to the login page; what does not fit is refused with 429.
func TestOAuthRegistrationFloodCannotEvictTheOwner(t *testing.T) {
	f := newOAuthFixture(t, "")
	now := time.Now()
	f.oa.reg.now = func() time.Time { return now }
	redirect := "https://claude.ai/api/mcp/auth_callback"
	used := f.register("Claude", redirect)
	code, c := f.login(used, redirect, "sekrit", nil)
	if code != 302 {
		t.Fatal(code, c)
	}
	access, _ := f.token(used, redirect, c)
	code, pending := f.tryRegister(redirect, map[string]string{"X-Forwarded-For": "192.0.2.1"})
	if code != 201 {
		t.Fatalf("the owner's second registration: %d", code)
	}
	refused := 0
	for win := 0; win < 5; win++ {
		for i := 0; i < 100; i++ {
			code, _ := f.tryRegister("https://evil.example/cb",
				map[string]string{"X-Forwarded-For": fmt.Sprintf("10.%d.%d.1", win, i)})
			switch code {
			case 201:
			case http.StatusTooManyRequests:
				refused++
			default:
				t.Fatalf("a flood registration answered %d", code)
			}
		}
		now = now.Add(regWindow)
	}
	if refused == 0 {
		t.Fatal("500 registrations from 500 sources were all admitted")
	}
	var st oauthState
	b, _ := os.ReadFile(f.state)
	_ = json.Unmarshal(b, &st)
	if len(st.Clients) > oauthMaxClients {
		t.Errorf("%d clients held", len(st.Clients))
	}
	if _, ok := st.Clients[pending]; !ok {
		t.Fatal("the flood evicted the owner's fresh registration")
	}
	if code, c := f.login(pending, redirect, "sekrit", nil); code != 302 {
		t.Errorf("the owner's fresh registration could not log in after the flood: %d %s", code, c)
	}
	if code := f.mcp(access, nil); code != 200 {
		t.Errorf("the flood evicted a client somebody had logged in through: %d", code)
	}
}

// The rules behind the flood test, one at a time. makeRoom drops a registration never used
// within its time to live and a client unused for longer than a refresh token lives. It
// keeps a registration that was used and a client that still holds a live refresh token.
// To admit a new registration into a full table it evicts an unused registration past the
// fresh window. When every client is fresh or in use it refuses and evicts nothing.
func TestOAuthClientTableEviction(t *testing.T) {
	o, err := newOAuthServer("https://laptop.example.ts.net", "p", filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	o.state.Clients["stale"] = oauthClient{CreatedAt: ago(oauthUnusedTTL + time.Minute)}
	o.state.Clients["used"] = oauthClient{CreatedAt: ago(oauthUnusedTTL + time.Minute), UsedAt: ago(time.Hour)}
	o.state.Clients["dead"] = oauthClient{CreatedAt: ago(oauthRefreshTTL * 2), UsedAt: ago(oauthRefreshTTL + time.Hour)}
	o.state.Clients["old-live"] = oauthClient{CreatedAt: ago(oauthRefreshTTL * 2), UsedAt: ago(oauthRefreshTTL + time.Hour)}
	o.state.Refresh["h"] = oauthToken{ClientID: "old-live", ExpiresAt: now.Add(time.Hour).Unix()}
	if !o.makeRoom(now) {
		t.Fatal("refused with room to spare")
	}
	for id, want := range map[string]bool{"stale": false, "used": true, "dead": false, "old-live": true} {
		if _, ok := o.state.Clients[id]; ok != want {
			t.Errorf("%s present=%v, want %v", id, ok, want)
		}
	}

	for i := len(o.state.Clients); i < oauthMaxClients-1; i++ {
		o.state.Clients[fmt.Sprintf("fresh%d", i)] = oauthClient{CreatedAt: ago(time.Minute)}
	}
	o.state.Clients["hour-old"] = oauthClient{CreatedAt: ago(time.Hour)}
	if !o.makeRoom(now) {
		t.Fatal("refused although an unused registration an hour old could go")
	}
	if _, ok := o.state.Clients["hour-old"]; ok {
		t.Error("the unused hour-old registration was not the one evicted")
	}
	o.state.Clients["fresh-last"] = oauthClient{CreatedAt: ago(oauthFreshTTL - time.Minute)}
	before := len(o.state.Clients)
	if o.makeRoom(now) {
		t.Fatal("a table of fresh and in-use clients made room")
	}
	if len(o.state.Clients) != before {
		t.Error("a refused makeRoom still evicted something")
	}
}

// The state file outlives restarts, so anyone who can read it must not be able to use what
// is in it. It keys tokens by their hash, never by the token itself. Only the owner can
// read it. A state file written in the older plaintext form is converted when loaded; the
// tokens in it keep working until they are rotated as usual.
func TestOAuthStateHoldsTokenHashesAndMigratesPlaintext(t *testing.T) {
	f := newOAuthFixture(t, "")
	redirect := "https://claude.ai/api/mcp/auth_callback"
	id := f.register("Claude", redirect)
	_, c := f.login(id, redirect, "sekrit", nil)
	access, refresh := f.token(id, redirect, c)
	raw, _ := os.ReadFile(f.state)
	if strings.Contains(string(raw), access) || strings.Contains(string(raw), refresh) {
		t.Fatal("the state file holds a live token")
	}
	if !strings.Contains(string(raw), tokenHash(access)) {
		t.Fatal("the access token's hash is not the key")
	}
	if fi, _ := os.Stat(f.state); fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode %v", fi.Mode().Perm())
	}

	legacy := filepath.Join(t.TempDir(), "mcp-oauth.json")
	plainA, plainR := netguard.RandomToken(), netguard.RandomToken()
	exp := time.Now().Add(time.Hour).Unix()
	old, _ := json.Marshal(map[string]any{
		"clients": map[string]any{"c_old": map[string]any{"redirect_uris": []string{redirect}, "created_at": 1}},
		"access":  map[string]any{plainA: map[string]any{"client_id": "c_old", "expires_at": exp}},
		"refresh": map[string]any{plainR: map[string]any{"client_id": "c_old", "expires_at": exp, "access": plainA}},
	})
	if err := os.WriteFile(legacy, old, 0o600); err != nil {
		t.Fatal(err)
	}
	g := newOAuthFixture(t, legacy)
	raw, _ = os.ReadFile(legacy)
	if strings.Contains(string(raw), plainA) || strings.Contains(string(raw), plainR) {
		t.Fatal("loading a plaintext state file left the tokens in it")
	}
	if code := g.mcp(plainA, nil); code != 200 {
		t.Errorf("a token issued before the migration stopped working: %d", code)
	}
	r, _ := http.PostForm(g.srv.URL+"/token", url.Values{"grant_type": {"refresh_token"},
		"client_id": {"c_old"}, "refresh_token": {plainR}})
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Errorf("a refresh token from before the migration: %d", r.StatusCode)
	}
	if code := g.mcp(plainA, nil); code != 401 {
		t.Errorf("rotation after migration left the old access token alive: %d", code)
	}
}

// Any client can register under any name, so a client calling itself "Claude" may send the
// code anywhere. The login page names the host the access will go to and says the
// client's name is only its own claim, so the owner approves a destination, not a label.
func TestOAuthLoginPageNamesTheRedirectHost(t *testing.T) {
	f := newOAuthFixture(t, "")
	redirect := "https://attacker.example/cb"
	id := f.register("Claude", redirect)
	r, err := http.Get(f.srv.URL + "/authorize?" + f.authz(id, redirect).Encode())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	page := string(b)
	if !strings.Contains(page, "<b>attacker.example</b>") || !strings.Contains(page, "Allow attacker.example") {
		t.Errorf("the login page does not name the redirect host:\n%s", page)
	}
	if !strings.Contains(page, "its own claim") {
		t.Error("the page presents the client's self-chosen name as fact")
	}
}

// --oauth-redirect-host removes that judgement call: a client can register only an https
// redirect to a listed host, matched exactly (no lookalike suffixes or prefixes, no other
// port, no plain http, no loopback) and a client registered before the list was set
// cannot log in to an unlisted host either. A malformed host, or the list without
// --public-url, is refused at startup.
func TestOAuthRedirectHostAllowList(t *testing.T) {
	state := filepath.Join(t.TempDir(), "mcp-oauth.json")
	open := newOAuthFixture(t, state)
	_, before := open.tryRegister("https://attacker.example/cb", nil)
	if before == "" {
		t.Fatal("without the list an https redirect did not register")
	}
	open.srv.Close()

	f := newOAuthFixtureWith(t, state, []string{"Claude.AI"})
	for _, u := range []string{
		"https://attacker.example/cb",
		"https://claude.ai.attacker.example/cb",
		"https://evil.claude.ai/cb",
		"https://claude.ai:8443/api/mcp/auth_callback",
		"http://claude.ai/api/mcp/auth_callback",
		"http://127.0.0.1:9/cb",
	} {
		if code, id := f.tryRegister(u, nil); code != http.StatusBadRequest || id != "" {
			t.Errorf("%s registered under the allow-list: %d %s", u, code, id)
		}
	}
	redirect := "https://claude.ai/api/mcp/auth_callback"
	code, id := f.tryRegister(redirect, nil)
	if code != 201 {
		t.Fatalf("a listed host was refused: %d", code)
	}
	if code, c := f.login(id, redirect, "sekrit", nil); code != 302 {
		t.Fatalf("a listed client could not log in: %d %s", code, c)
	}
	if code, body := f.login(before, "https://attacker.example/cb", "sekrit", nil); code != 400 ||
		!strings.Contains(body, "oauth-redirect-host") {
		t.Errorf("a client registered before the list logged in to an unlisted host: %d %s", code, body)
	}

	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"https://claude.ai", "claude.ai/x", "*.claude.ai", "", "u@claude.ai"} {
		if _, err := s.httpHandlerE(httpOpts{addr: "127.0.0.1:0", token: "x",
			publicURL: "https://laptop.example.ts.net", redirectHosts: []string{bad},
			oauthPath: filepath.Join(t.TempDir(), "s.json")}); err == nil {
			t.Errorf("--oauth-redirect-host %q was accepted", bad)
		}
	}
	if _, err := s.httpHandlerE(httpOpts{addr: "127.0.0.1:0", token: "x",
		redirectHosts: []string{"claude.ai"}}); err == nil {
		t.Error("--oauth-redirect-host without --public-url was accepted")
	}
}
