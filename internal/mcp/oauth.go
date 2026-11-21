package mcp

// OAuth 2.1 in front of the Streamable HTTP endpoint, so a hosted MCP client (the Claude
// app's custom connectors, first of all) can reach a brain. Those clients do not send a
// static bearer token: they discover an authorization server, register themselves, send the
// person to a login page, and exchange the code for a token of their own. This file is that
// authorization server, and it is deliberately the smallest one that satisfies the MCP
// authorization spec (2025-06-18): RFC 9728 protected-resource metadata, RFC 8414 server
// metadata, RFC 7591 dynamic registration, PKCE S256, RFC 8707 resource binding, rotated
// refresh tokens, and a 401 that says where the metadata is.
//
// The "login" is the shared secret the operator already set with --token, typed once into
// a page. There is one person and one secret; an identity provider would be a second thing
// to run for no second user. Issued tokens are random, short-lived, bound to this server's
// public URL, and persisted (as hashes) in the config directory so a restart does not
// disconnect the phone.
//
// Opt-in: nothing here is mounted unless --public-url is given. Without it the endpoint
// keeps the static-token behaviour that local clients use. With it, /mcp takes only issued
// tokens: --token is a password a person types, and a password that is also a bearer can be
// guessed at /mcp without ever meeting the login page's lockout.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chka/os-brain/internal/brain"
	"github.com/chka/os-brain/internal/netguard"
)

const (
	oauthAccessTTL  = 24 * time.Hour
	oauthRefreshTTL = 90 * 24 * time.Hour
	oauthCodeTTL    = 10 * time.Minute
	// A registration nobody has logged in through within this long is dropped, and when the
	// table is full the stalest unused one makes room: anybody can register, so a table
	// that only grew was a table anybody could fill and so shut the owner out.
	oauthUnusedTTL  = 24 * time.Hour
	oauthMaxClients = 200
	// A registration younger than this is never evicted: the owner's own, made a moment
	// before a flood, would otherwise be gone before they reached the login page. A full
	// table of fresh ones refuses with 429 instead.
	oauthFreshTTL = 15 * time.Minute
	// Registrations admitted per source and in all per regWindow. The global ceiling over
	// oauthFreshTTL stays well under oauthMaxClients, so a spread-out flood cannot make the
	// whole table fresh, and one source cannot reach the ceiling alone.
	regWindow    = 15 * time.Minute
	regPerSource = 10
	regGlobal    = 60
)

type oauthClient struct {
	RedirectURIs []string `json:"redirect_uris"`
	Name         string   `json:"client_name,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	// UsedAt is the last successful login through this client; 0 means never.
	UsedAt int64 `json:"used_at,omitempty"`
}

type oauthToken struct {
	ClientID  string `json:"client_id"`
	ExpiresAt int64  `json:"expires_at"`
	// Refresh tokens point at (the hash of) the access token they can replace, so a used
	// refresh token retires both halves at once (OAuth 2.1 §4.3.1 rotation).
	Access string `json:"access,omitempty"`
}

type oauthCode struct {
	clientID    string
	redirectURI string
	challenge   string
	resource    string
	expiresAt   time.Time
}

// oauthState keys Access and Refresh by tokenHash, never by the token: a backup, a dotfile
// sync or a support bundle that captures the file then holds nothing that works as a bearer.
// Hashed marks a file written that way; one without it is from before and its keys are
// hashed on load.
type oauthState struct {
	Hashed  bool                   `json:"tokens_hashed"`
	Clients map[string]oauthClient `json:"clients"`
	Access  map[string]oauthToken  `json:"access"`
	Refresh map[string]oauthToken  `json:"refresh"`
}

type oauthServer struct {
	issuer   string // public https URL, no trailing slash
	resource string // issuer + "/mcp": what tokens are bound to
	password string
	path     string // where state is persisted
	// gate counts wrong passwords and wrong bearers; the /mcp endpoint shares this one
	// with the login page.
	gate *failGate
	reg  *regLimiter
	// redirectHosts is the --oauth-redirect-host allow-list; empty admits any redirect
	// redirectAllowed admits.
	redirectHosts map[string]bool

	mu    sync.Mutex
	state oauthState
	codes map[string]oauthCode
}

func tokenHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func oauthStatePath() string {
	return filepath.Join(brain.ConfigDir(), "mcp-oauth.json")
}

func newOAuthServer(publicURL, password, statePath string) (*oauthServer, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("--public-url must be an https URL (the authorization "+
			"endpoints are only allowed over TLS): got %q", publicURL)
	}
	if password == "" {
		return nil, errors.New("--public-url needs --token: it is the password on the login page")
	}
	issuer := strings.TrimRight(publicURL, "/")
	if statePath == "" {
		statePath = oauthStatePath()
	}
	o := &oauthServer{issuer: issuer, resource: issuer + "/mcp", password: password,
		path: statePath, codes: map[string]oauthCode{}, gate: newFailGate(), reg: newRegLimiter()}
	o.state = oauthState{Hashed: true, Clients: map[string]oauthClient{},
		Access: map[string]oauthToken{}, Refresh: map[string]oauthToken{}}
	if err := o.loadState(); err != nil {
		return nil, err
	}
	return o, nil
}

// loadState reads the persisted state over the empty one, if there is a file, and rewrites
// a file from before tokens were hashed so its bearers stop being usable as they sit.
func (o *oauthServer) loadState() error {
	b, err := os.ReadFile(o.path)
	if err != nil {
		return nil // no state yet
	}
	var st oauthState
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("%s is not the state file this server writes: %w", o.path, err)
	}
	if st.Clients != nil {
		o.state.Clients = st.Clients
	}
	if st.Access != nil {
		o.state.Access = st.Access
	}
	if st.Refresh != nil {
		o.state.Refresh = st.Refresh
	}
	if st.Hashed {
		return nil
	}
	o.hashTokenKeys()
	if err := o.persist(); err != nil {
		return fmt.Errorf("rewriting %s with hashed tokens: %w", o.path, err)
	}
	return nil
}

// hashTokenKeys replaces every plaintext token key, and every refresh token's pointer to
// its access token, with its hash.
func (o *oauthServer) hashTokenKeys() {
	access := map[string]oauthToken{}
	for k, t := range o.state.Access {
		access[tokenHash(k)] = t
	}
	refresh := map[string]oauthToken{}
	for k, t := range o.state.Refresh {
		if t.Access != "" {
			t.Access = tokenHash(t.Access)
		}
		refresh[tokenHash(k)] = t
	}
	o.state.Access, o.state.Refresh = access, refresh
}

// persist writes the state with mode 600. Called with o.mu held.
func (o *oauthServer) persist() error {
	b, err := json.MarshalIndent(o.state, "", " ")
	if err != nil {
		return err
	}
	return brain.WriteFileSafe(o.path, b, brain.WriteOpts{MkdirMode: 0o700})
}

// mount adds the discovery and authorization routes. The /mcp route stays with the
// transport; it calls authorized for the bearer check.
func (o *oauthServer) mount(mux *http.ServeMux) {
	// RFC 9728 §3: the metadata for a resource with a path lives under the path-suffixed
	// well-known URL; clients that only know the host ask the bare one. Serve both.
	mux.HandleFunc("/.well-known/oauth-protected-resource", o.resourceMetadata)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", o.resourceMetadata)
	// Some clients look for the OpenID name even for plain OAuth; the answer is the same.
	mux.HandleFunc("/.well-known/oauth-authorization-server", o.serverMetadata)
	mux.HandleFunc("/.well-known/openid-configuration", o.serverMetadata)
	mux.HandleFunc("/register", o.register)
	mux.HandleFunc("/authorize", o.authorizePage)
	mux.HandleFunc("/token", o.token)
}

func (o *oauthServer) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSONNoStore(w, http.StatusOK, map[string]interface{}{
		"resource":                 o.resource,
		"authorization_servers":    []string{o.issuer},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "os-brain",
	})
}

func (o *oauthServer) serverMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSONNoStore(w, http.StatusOK, map[string]interface{}{
		"issuer":                                o.issuer,
		"authorization_endpoint":                o.issuer + "/authorize",
		"token_endpoint":                        o.issuer + "/token",
		"registration_endpoint":                 o.issuer + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"},
		"scopes_supported":                      []string{"brain"},
	})
}

func writeJSONNoStore(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	if status >= 400 {
		log.Printf("[oauth] %d %s: %s", status, code, desc)
	}
	writeJSONNoStore(w, status, map[string]string{"error": code, "error_description": desc})
}

// challenge is the 401 the MCP spec requires: the client reads resource_metadata from it.
func (o *oauthServer) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="brain"`, o.issuer))
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// authorized reports whether a bearer value is a live token this server issued. An expired
// one is dropped on the way past.
func (o *oauthServer) authorized(bearer string) bool {
	if bearer == "" {
		return false
	}
	key := tokenHash(bearer)
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.state.Access[key]
	if !ok {
		return false
	}
	if time.Now().Unix() >= t.ExpiresAt {
		delete(o.state.Access, key)
		_ = o.persist()
		return false
	}
	return true
}

// --- registration ----------------------------------------------------------------------

// register: RFC 7591. Anyone may register a client; a client is only a redirect allow-list
// and a name, and gets nothing until the person types the password.
func (o *oauthServer) register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		Name         string   `json:"client_name"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "body is not JSON")
		return
	}
	if !o.redirectURIsAcceptable(w, req.RedirectURIs) {
		return
	}
	src := sourceOf(r)
	if wait := o.reg.allow(src); wait > 0 {
		log.Printf("[oauth] register refused: too many registrations (from %s)", src)
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		oauthError(w, http.StatusTooManyRequests, "slow_down",
			"too many registrations; try again later")
		return
	}
	id := "c_" + netguard.RandomToken()[:24]
	o.mu.Lock()
	if !o.makeRoom(time.Now()) {
		o.mu.Unlock()
		log.Printf("[oauth] register refused: the client table is full of fresh or in-use registrations")
		w.Header().Set("Retry-After", fmt.Sprint(int(oauthFreshTTL.Seconds())))
		oauthError(w, http.StatusTooManyRequests, "slow_down",
			"no room for another client right now; try again later")
		return
	}
	log.Printf("[oauth] register client=%q redirect_uris=%v from %s -> %s", req.Name, req.RedirectURIs, src, id)
	o.state.Clients[id] = oauthClient{RedirectURIs: req.RedirectURIs, Name: req.Name,
		CreatedAt: time.Now().Unix()}
	err := o.persist()
	o.mu.Unlock()
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	writeJSONNoStore(w, http.StatusCreated, map[string]interface{}{
		"client_id":                  id,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                req.Name,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// redirectURIsAcceptable checks every redirect a registration asks for, and writes the
// refusal when one fails.
func (o *oauthServer) redirectURIsAcceptable(w http.ResponseWriter, uris []string) bool {
	if len(uris) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return false
	}
	for _, u := range uris {
		if !redirectAllowed(u) {
			log.Printf("[oauth] register refused: redirect_uri %q is not https or loopback", u)
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"redirect_uris must be https, or http on localhost: "+u)
			return false
		}
		if !o.redirectListed(u) {
			log.Printf("[oauth] register refused: redirect_uri %q is not on --oauth-redirect-host", u)
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"this server registers clients only for redirects to its listed hosts: "+u)
			return false
		}
	}
	return true
}

// makeRoom drops registrations nobody has logged in through for oauthUnusedTTL and clients
// with no live token unused for longer than a refresh token lives, then, if the table is still
// full, the stalest unused registration older than oauthFreshTTL. Nothing a person logged in
// through and nothing fresh is evicted to admit an unauthenticated registration: when only
// those remain it reports false and the caller refuses. Called with o.mu held.
func (o *oauthServer) makeRoom(now time.Time) bool {
	o.dropStaleClients(now)
	for len(o.state.Clients) >= oauthMaxClients {
		victim := o.stalestUnusedClient(now)
		if victim == "" {
			return false
		}
		delete(o.state.Clients, victim)
	}
	return true
}

func (o *oauthServer) dropStaleClients(now time.Time) {
	live := o.clientsWithLiveTokens(now)
	for id, c := range o.state.Clients {
		age := now.Unix() - c.CreatedAt
		if c.UsedAt == 0 && age >= int64(oauthUnusedTTL.Seconds()) {
			delete(o.state.Clients, id)
		}
		if c.UsedAt != 0 && !live[id] && now.Unix()-c.UsedAt >= int64(oauthRefreshTTL.Seconds()) {
			delete(o.state.Clients, id)
		}
	}
}

func (o *oauthServer) clientsWithLiveTokens(now time.Time) map[string]bool {
	live := map[string]bool{}
	for _, t := range o.state.Access {
		if now.Unix() < t.ExpiresAt {
			live[t.ClientID] = true
		}
	}
	for _, t := range o.state.Refresh {
		if now.Unix() < t.ExpiresAt {
			live[t.ClientID] = true
		}
	}
	return live
}

// stalestUnusedClient is the oldest registration nobody logged in through that is past
// oauthFreshTTL, or "" when there is none.
func (o *oauthServer) stalestUnusedClient(now time.Time) string {
	victim, best := "", int64(0)
	for id, c := range o.state.Clients {
		if c.UsedAt != 0 || now.Unix()-c.CreatedAt < int64(oauthFreshTTL.Seconds()) {
			continue
		}
		if victim == "" || c.CreatedAt < best {
			victim, best = id, c.CreatedAt
		}
	}
	return victim
}

func redirectAllowed(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Fragment != "" {
		return false
	}
	if p.Scheme == "https" && p.Host != "" {
		return true
	}
	// OAuth 2.1 §8.4.2: loopback redirects may be http.
	if p.Scheme == "http" {
		h := p.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// parseRedirectHosts reads --oauth-redirect-host values. Each is a host as it appears in a
// URL's authority, compared exactly: no scheme, path, wildcard or userinfo, because a value
// that only looks like a host would admit something the person did not name.
func parseRedirectHosts(vals []string) (map[string]bool, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	out := map[string]bool{}
	for _, v := range vals {
		h := strings.ToLower(strings.TrimSpace(v))
		p, err := url.Parse("https://" + h)
		if h == "" || err != nil || p.Host != h || p.Hostname() == "" || strings.ContainsAny(h, "*@/?#") {
			return nil, fmt.Errorf("--oauth-redirect-host %q is not a host name (give it as it "+
				"appears in the redirect URL, e.g. claude.ai, with no scheme or path)", v)
		}
		out[h] = true
	}
	return out, nil
}

// redirectListed is the allow-list check: with --oauth-redirect-host set, only https to a
// listed host, loopback included, because a consent page is phished through a redirect the
// person never chose.
func (o *oauthServer) redirectListed(u string) bool {
	if len(o.redirectHosts) == 0 {
		return true
	}
	p, err := url.Parse(u)
	return err == nil && p.Scheme == "https" && o.redirectHosts[strings.ToLower(p.Host)]
}

// --- authorization ---------------------------------------------------------------------

type authzRequest struct {
	clientID, redirectURI, state, challenge, method, resource, scope string
}

func parseAuthz(v url.Values) authzRequest {
	return authzRequest{
		clientID: v.Get("client_id"), redirectURI: v.Get("redirect_uri"), state: v.Get("state"),
		challenge: v.Get("code_challenge"), method: v.Get("code_challenge_method"),
		resource: v.Get("resource"), scope: v.Get("scope"),
	}
}

// validateAuthz checks everything that must be right BEFORE the page is shown, and
// returns a message safe to show. A bad client or redirect is never redirected to.
func (o *oauthServer) validateAuthz(a authzRequest) string {
	o.mu.Lock()
	c, ok := o.state.Clients[a.clientID]
	o.mu.Unlock()
	if !ok {
		return "unknown client_id"
	}
	found := false
	for _, u := range c.RedirectURIs {
		if u == a.redirectURI {
			found = true
		}
	}
	if !found {
		return "redirect_uri is not one this client registered"
	}
	// A client registered before the list was set is held to it as well.
	if !o.redirectListed(a.redirectURI) {
		return "redirect_uri is not on this server's --oauth-redirect-host list"
	}
	if a.challenge == "" || a.method != "S256" {
		return "PKCE with S256 is required"
	}
	// RFC 8707: a token is bound to this resource, so a request for any other is refused
	// rather than issued a token that would be accepted somewhere it was not meant for.
	if a.resource != "" && a.resource != o.resource && a.resource != o.issuer {
		return "resource must be " + o.resource
	}
	return ""
}

// authorizePage shows the login page on GET and checks the password on POST. The request
// is validated in full both times, because the POST is built by whoever submits it.
func (o *oauthServer) authorizePage(w http.ResponseWriter, r *http.Request) {
	var a authzRequest
	var password string
	switch r.Method {
	case http.MethodGet:
		a = parseAuthz(r.URL.Query())
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		a = parseAuthz(r.PostForm)
		password = r.PostForm.Get("password")
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rt := r.FormValue("response_type"); rt != "" && rt != "code" {
		http.Error(w, "only response_type=code", http.StatusBadRequest)
		return
	}
	if msg := o.validateAuthz(a); msg != "" {
		log.Printf("[oauth] authorize refused (%s): client=%s redirect=%s method=%s resource=%s",
			msg, a.clientID, a.redirectURI, a.method, a.resource)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		o.renderLogin(w, a, "")
		return
	}
	o.login(w, r, a, password)
}

// login checks the typed password and, when it is right, redirects to the client with a
// one-use authorization code.
func (o *oauthServer) login(w http.ResponseWriter, r *http.Request, a authzRequest, password string) {
	src := sourceOf(r)
	if wait := o.gate.wait(src); wait > 0 {
		o.renderLogin(w, a, fmt.Sprintf("too many wrong passwords; try again in %s",
			wait.Round(time.Second)))
		return
	}
	if subtle.ConstantTimeCompare([]byte(password), []byte(o.password)) != 1 {
		o.gate.fail(src)
		time.Sleep(300 * time.Millisecond)
		log.Printf("[oauth] wrong password for client=%s from %s", a.clientID, src)
		o.renderLogin(w, a, "wrong password")
		return
	}
	o.gate.ok(src)
	log.Printf("[oauth] login ok for client=%s, redirecting to %s", a.clientID, a.redirectURI)
	code := o.issueCode(a)

	dest, _ := url.Parse(a.redirectURI)
	q := dest.Query()
	q.Set("code", code)
	if a.state != "" {
		q.Set("state", a.state)
	}
	dest.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// issueCode records the login against the client (so its registration is never evicted)
// and mints an authorization code bound to the client, redirect, PKCE challenge and
// resource. Expired codes are dropped on the way past.
func (o *oauthServer) issueCode(a authzRequest) string {
	code := netguard.RandomToken()
	o.mu.Lock()
	defer o.mu.Unlock()
	if c, ok := o.state.Clients[a.clientID]; ok {
		c.UsedAt = time.Now().Unix()
		o.state.Clients[a.clientID] = c
		if err := o.persist(); err != nil {
			log.Printf("[oauth] recording the login for client=%s: %v", a.clientID, err)
		}
	}
	for k, c := range o.codes {
		if time.Now().After(c.expiresAt) {
			delete(o.codes, k)
		}
	}
	o.codes[code] = oauthCode{clientID: a.clientID, redirectURI: a.redirectURI,
		challenge: a.challenge, resource: a.resource, expiresAt: time.Now().Add(oauthCodeTTL)}
	return code
}

// loginPage is the format of the login page; renderLogin fills it. %% is a literal %.
const loginPage = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>os-brain</title>
<style>body{font:16px system-ui,sans-serif;margin:0;background:#111;color:#eee;display:flex;min-height:100vh;align-items:center;justify-content:center}
form{background:#1c1c1c;padding:28px;border-radius:12px;width:min(92vw,380px)}h1{font-size:18px;margin:0 0 6px}p{margin:0 0 14px;color:#aaa}
.dest{color:#fff;font-size:17px;margin:0 0 6px;word-break:break-all}.dest b{color:#ffd166}.small{font-size:13px}
input[type=password]{width:100%%;box-sizing:border-box;font-size:18px;padding:12px;border-radius:8px;border:1px solid #444;background:#000;color:#fff}
button{margin-top:14px;width:100%%;font-size:17px;padding:12px;border:0;border-radius:8px;background:#e5e5e5;color:#000}
.err{color:#f66;margin-bottom:12px}</style>
<form method="post" action="/authorize"><h1>os-brain</h1>
<p class="dest">Allowing sends access to this brain to <b>%s</b></p>
<p class="small">Only continue if that is the app you are connecting. The client calls itself &ldquo;%s&rdquo;; that name is its own claim and nothing here checked it.</p>%s
<input type="password" name="password" autofocus autocomplete="current-password" placeholder="password">
%s%s%s%s%s%s%s<button>Allow %s</button></form>`

// renderLogin names where the code goes, because that is the only thing on this page an
// attacker cannot choose: registration is open, so the client's name is whatever it said it
// was, and a page that showed only "Claude wants to use this brain" asked for the password on
// behalf of any https host at all. The page cannot be framed, so it cannot be dressed up.
func (o *oauthServer) renderLogin(w http.ResponseWriter, a authzRequest, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; "+
		"form-action 'self'; frame-ancestors 'none'")
	hidden := func(k, v string) string {
		return fmt.Sprintf(`<input type="hidden" name="%s" value="%s">`, k, html.EscapeString(v))
	}
	errBox := ""
	if msg != "" {
		errBox = `<div class="err">` + html.EscapeString(msg) + `</div>`
	}
	host := redirectHost(a.redirectURI)
	fmt.Fprintf(w, loginPage,
		html.EscapeString(host), html.EscapeString(o.clientName(a.clientID)), errBox,
		hidden("response_type", "code"), hidden("client_id", a.clientID),
		hidden("redirect_uri", a.redirectURI), hidden("state", a.state),
		hidden("code_challenge", a.challenge), hidden("code_challenge_method", "S256"),
		hidden("resource", a.resource), html.EscapeString(host))
}

// clientName is the name a client registered with, or its id when it gave none.
func (o *oauthServer) clientName(clientID string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if c, ok := o.state.Clients[clientID]; ok && c.Name != "" {
		return c.Name
	}
	return clientID
}

// redirectHost is the host a redirect URI points at, or the whole URI when it has none.
func redirectHost(redirectURI string) string {
	if u, err := url.Parse(redirectURI); err == nil && u.Host != "" {
		return u.Host
	}
	return redirectURI
}

// --- tokens ----------------------------------------------------------------------------

func pkceOK(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// token is the token endpoint: it trades an authorization code, or a refresh token, for a
// fresh access and refresh pair.
func (o *oauthServer) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "form expected")
		return
	}
	f := r.PostForm
	clientID := f.Get("client_id")
	if clientID == "" {
		// client_secret_basic clients put the id in the Authorization header.
		if u, _, ok := r.BasicAuth(); ok {
			clientID = u
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.state.Clients[clientID]; !ok {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
		return
	}
	var ok bool
	switch f.Get("grant_type") {
	case "authorization_code":
		ok = o.redeemCode(w, f, clientID)
	case "refresh_token":
		ok = o.redeemRefresh(w, f, clientID)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "authorization_code or refresh_token")
		return
	}
	if ok {
		o.issueTokens(w, clientID, f.Get("grant_type"))
	}
}

// redeemCode spends an authorization code and reports whether it was good, writing the
// refusal when it was not. A code is spent on any attempt, right or wrong, so one that
// leaked cannot be retried. Called with o.mu held.
func (o *oauthServer) redeemCode(w http.ResponseWriter, f url.Values, clientID string) bool {
	c, ok := o.codes[f.Get("code")]
	delete(o.codes, f.Get("code"))
	if !ok || time.Now().After(c.expiresAt) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code")
		return false
	}
	if c.clientID != clientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code was issued to another client")
		return false
	}
	if ru := f.Get("redirect_uri"); ru != "" && ru != c.redirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
		return false
	}
	if !pkceOK(f.Get("code_verifier"), c.challenge) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return false
	}
	if res := f.Get("resource"); res != "" && res != o.resource && res != o.issuer {
		oauthError(w, http.StatusBadRequest, "invalid_target", "resource must be "+o.resource)
		return false
	}
	return true
}

// redeemRefresh retires a refresh token and the access token it was issued with, and
// reports whether it was good, writing the refusal when it was not. Rotation means a
// stolen refresh token works once at most, and its use cuts off the holder it was stolen
// from, which is how the theft shows. Called with o.mu held.
func (o *oauthServer) redeemRefresh(w http.ResponseWriter, f url.Values, clientID string) bool {
	key := tokenHash(f.Get("refresh_token"))
	rt, ok := o.state.Refresh[key]
	if !ok || time.Now().Unix() >= rt.ExpiresAt || rt.ClientID != clientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown, expired or foreign refresh token")
		return false
	}
	delete(o.state.Refresh, key)
	delete(o.state.Access, rt.Access)
	return true
}

// issueTokens mints and persists a new access and refresh pair for clientID and writes the
// token response. Called with o.mu held.
func (o *oauthServer) issueTokens(w http.ResponseWriter, clientID, grantType string) {
	access, refresh := netguard.RandomToken(), netguard.RandomToken()
	now := time.Now()
	o.state.Access[tokenHash(access)] = oauthToken{ClientID: clientID,
		ExpiresAt: now.Add(oauthAccessTTL).Unix()}
	o.state.Refresh[tokenHash(refresh)] = oauthToken{ClientID: clientID,
		ExpiresAt: now.Add(oauthRefreshTTL).Unix(), Access: tokenHash(access)}
	// Expired tokens are dropped on the way past, so the file does not grow with every day
	// the phone renews.
	dropExpired(o.state.Access, now)
	dropExpired(o.state.Refresh, now)
	if err := o.persist(); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	log.Printf("[oauth] token issued to client=%s (%s)", clientID, grantType)
	writeJSONNoStore(w, http.StatusOK, map[string]interface{}{
		"access_token": access, "token_type": "Bearer",
		"expires_in": int(oauthAccessTTL.Seconds()), "refresh_token": refresh, "scope": "brain",
	})
}

func dropExpired(tokens map[string]oauthToken, now time.Time) {
	for k, t := range tokens {
		if now.Unix() >= t.ExpiresAt {
			delete(tokens, k)
		}
	}
}
