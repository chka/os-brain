package mcp

// The Streamable HTTP transport for the MCP server: one endpoint, /mcp, taking POST.
//
// Every guard here comes from the transport spec's own security section, and each one is
// load-bearing for a brain specifically: this endpoint is every memory the developer has.

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/chka/os-brain/internal/cli"
	"github.com/chka/os-brain/internal/netguard"
	"github.com/chka/os-brain/internal/store"
)

type httpOpts struct {
	addr   string
	token  string
	origin string
	// hosts are Host names answered besides loopback ones: a reverse proxy that passes the
	// public name through. Anything else is a rebound DNS name and is refused.
	hosts string
	// publicURL, when set, mounts an OAuth 2.1 authorization server (oauth.go) so a
	// hosted MCP client can connect; token doubles as the password on its login page.
	publicURL string
	oauthPath string
	// redirectHosts, when any are given, are the only hosts a client may register an https
	// redirect to (--oauth-redirect-host).
	redirectHosts []string
	tlsCert       string
	tlsKey        string
}

// checkHTTPOpts refuses a bind this transport must not serve, before anything listens, and
// returns the TLS configuration when a certificate was given.
//
// Off loopback the bearer token, the OAuth password and every memory cross the network, so
// TLS is not optional there and there is no flag that makes it so: a firewall or an IP
// allow-list routes packets, it does not stop a neighbour on the same network segment
// reading them. A TLS-terminating proxy in front binds loopback and needs neither.
func checkHTTPOpts(o httpOpts) (*tls.Config, error) {
	cfg, err := tlsFloor(o.tlsCert, o.tlsKey)
	if err != nil {
		return nil, err
	}
	if netguard.IsLoopback(o.addr) {
		return cfg, nil
	}
	if cfg == nil {
		return nil, fmt.Errorf("refusing to listen on %s without TLS.\n"+
			"Off loopback this endpoint would send the token and every memory in the brain in "+
			"cleartext. Pass --tls-cert and --tls-key, or bind 127.0.0.1 behind a proxy that "+
			"terminates TLS", o.addr)
	}
	if o.token == "" {
		return nil, fmt.Errorf("refusing to listen on %s without --token.\n"+
			"This endpoint serves every memory in the brain. Bind 127.0.0.1 for local use, "+
			"or set a bearer token if another machine needs to reach it", o.addr)
	}
	return cfg, nil
}

// tlsFloor loads a certificate pair once, at startup, with the minimum version every
// listener in this binary uses. Neither file given is plain HTTP; one without the other is
// an error rather than a guess.
func tlsFloor(cert, key string) (*tls.Config, error) {
	if cert == "" && key == "" {
		return nil, nil
	}
	if cert == "" || key == "" {
		return nil, errors.New("--tls-cert and --tls-key go together: a certificate without its key serves nothing")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("loading --tls-cert/--tls-key: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}

// serveOn serves srv on ln, over TLS when cfg is set.
func serveOn(srv *http.Server, ln net.Listener, cfg *tls.Config) error {
	if cfg == nil {
		return srv.Serve(ln)
	}
	srv.TLSConfig = cfg
	return srv.ServeTLS(ln, "", "")
}

// serveHTTP checks the options, binds, announces itself on stderr and serves until killed.
func (s *mcpServer) serveHTTP(o httpOpts) error {
	cfg, err := checkHTTPOpts(o)
	if err != nil {
		return err
	}
	tokenFile := ""
	if o.token == "" && o.publicURL == "" {
		if tokenFile, err = mintHTTPToken(&o); err != nil {
			return err
		}
	}
	h, err := s.httpHandlerE(o)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", o.addr)
	if err != nil {
		return err
	}
	s.announceHTTP(o, cfg != nil, tokenFile)
	// Every command now runs in-process, and this daemon's stdin is nothing a command may
	// read its body from.
	cli.SetStdinIsProtocol(true)
	return serveOn(&http.Server{Handler: h}, ln, cfg)
}

// announceHTTP prints where the endpoint is and how a client authenticates to it.
func (s *mcpServer) announceHTTP(o httpOpts, overTLS bool, tokenFile string) {
	loopback := netguard.IsLoopback(o.addr)
	scheme, where := "http", o.addr
	if overTLS {
		scheme = "https"
	}
	if loopback {
		where += " (loopback only)"
	}
	fmt.Fprintf(os.Stderr, "[brain] MCP on %s://%s/mcp %s · %d tools · protocol %s\n",
		scheme, o.addr, where, len(s.tools), ProtocolVersion)
	if !loopback {
		fmt.Fprintln(os.Stderr, "[brain] listening off-loopback over TLS: every memory in this "+
			"brain is reachable from the network to whoever holds the token.")
	}
	if tokenFile != "" {
		fmt.Fprintf(os.Stderr, "[brain] no --token given, so this process minted one: it is in "+
			"%s (mode 600) and every request needs \"Authorization: Bearer <it>\"\n", tokenFile)
	}
	if o.publicURL != "" {
		fmt.Fprintf(os.Stderr, "[brain] OAuth for hosted clients at %s: add %s/mcp as a "+
			"connector and type the token on the login page. /mcp takes only the tokens that "+
			"login issues, not --token itself\n", o.publicURL, o.publicURL)
	}
}

// httpHandlerE is httpHandler plus the OAuth layer when publicURL is set; a bad public URL
// is an error at startup rather than a handler that serves nothing.
func (s *mcpServer) httpHandlerE(o httpOpts) (http.Handler, error) {
	oa, err := oauthFor(o)
	if err != nil {
		return nil, err
	}
	return s.httpHandlerWith(o, oa), nil
}

// oauthFor is the authorization server o asks for, nil without --public-url.
func oauthFor(o httpOpts) (*oauthServer, error) {
	if o.publicURL == "" && len(o.redirectHosts) > 0 {
		return nil, errors.New("--oauth-redirect-host restricts OAuth client registration, " +
			"which only --public-url mounts; refusing a flag that would do nothing")
	}
	if o.publicURL == "" {
		return nil, nil
	}
	oa, err := newOAuthServer(o.publicURL, o.token, o.oauthPath)
	if err != nil {
		return nil, err
	}
	if oa.redirectHosts, err = parseRedirectHosts(o.redirectHosts); err != nil {
		return nil, err
	}
	return oa, nil
}

// httpHandler is the handler without OAuth, so the transport's guards can be tested
// without binding a port.
func (s *mcpServer) httpHandler(o httpOpts) http.Handler {
	return s.httpHandlerWith(o, nil)
}

// mcpEndpoint is the state behind /mcp for one handler.
type mcpEndpoint struct {
	s    *mcpServer
	opts httpOpts
	// oa is the OAuth layer, nil when /mcp takes the static bearer token instead.
	oa   *oauthServer
	gate *failGate
	// hosts are the Host names answered besides loopback ones; checkHost is whether the
	// Host header is checked at all (see newMCPEndpoint).
	hosts     map[string]bool
	checkHost bool

	mu       sync.Mutex
	sessions map[string]bool
}

func (s *mcpServer) httpHandlerWith(o httpOpts, oa *oauthServer) http.Handler {
	s.transport = cli.TransportMCPHTTP
	e := newMCPEndpoint(s, o, oa)
	mux := http.NewServeMux()
	if oa != nil {
		oa.mount(mux)
	}
	mux.HandleFunc("/mcp", e.serve)
	return mux
}

func newMCPEndpoint(s *mcpServer, o httpOpts, oa *oauthServer) *mcpEndpoint {
	if o.token == "" {
		// Nothing upstream supplied one, so nobody can know this one: the handler refuses
		// every request rather than serving the store to anybody who asks.
		o.token = netguard.RandomToken()
	}
	hosts := map[string]bool{}
	for _, h := range strings.Split(o.hosts, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			hosts[h] = true
		}
	}
	if u, err := url.Parse(o.publicURL); err == nil && u.Hostname() != "" {
		hosts[strings.ToLower(u.Hostname())] = true
	}
	e := &mcpEndpoint{s: s, opts: o, oa: oa, gate: newFailGate(), hosts: hosts,
		// A bind to every interface with no names given cannot know which Host values
		// are its own, so it checks none; loopback always knows.
		checkHost: netguard.IsLoopback(o.addr) || len(hosts) > 0,
		sessions:  map[string]bool{}}
	if oa != nil {
		// One counter for wrong passwords on the login page and wrong bearers here, so a
		// guesser cannot split attempts between the two.
		e.gate = oa.gate
	}
	return e
}

func (e *mcpEndpoint) serve(w http.ResponseWriter, r *http.Request) {
	if !e.originChecksPass(w, r) || !e.authenticated(w, r) {
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" &&
		v != ProtocolVersion && v != FallbackProtocolVersion {
		http.Error(w, "unsupported MCP-Protocol-Version", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		// No server-initiated messages: nothing here is long-running or streamed, so the
		// spec's 405 is the honest answer rather than an idle stream.
		http.Error(w, "no server-initiated stream", http.StatusMethodNotAllowed)
	case http.MethodDelete:
		if sid := r.Header.Get("Mcp-Session-Id"); sid != "" {
			e.mu.Lock()
			delete(e.sessions, sid)
			e.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPost:
		e.servePost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// originChecksPass refuses a request a web page could have made on someone's behalf, and
// writes the refusal when it does.
func (e *mcpEndpoint) originChecksPass(w http.ResponseWriter, r *http.Request) bool {
	// DNS rebinding: a page whose name now resolves to 127.0.0.1 is same-origin with this
	// server, so only the Host it asked for gives it away.
	if e.checkHost && !hostAllowed(r.Host, e.hosts) {
		http.Error(w, "host not allowed", http.StatusForbidden)
		return false
	}
	// Without an Origin check a web page can drive a localhost server.
	if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, e.opts.origin) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return false
	}
	return true
}

// authenticated checks the bearer, counting wrong ones against their source, and writes the
// refusal when it fails.
func (e *mcpEndpoint) authenticated(w http.ResponseWriter, r *http.Request) bool {
	got, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !bearer {
		got = ""
	}
	src := sourceOf(r)
	// While a source waits nothing is compared, so a lockout cannot be used to test guesses.
	if wait := e.gate.wait(src); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return false
	}
	if e.bearerValid(got) {
		return true
	}
	// No credential at all is a client discovering where to log in, not a guess.
	if got != "" {
		e.gate.fail(src)
	}
	if e.oa != nil {
		e.oa.challenge(w) // the 401 carries where to find the metadata
	} else {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
	return false
}

func (e *mcpEndpoint) bearerValid(got string) bool {
	if e.oa != nil {
		// With OAuth mounted, --token is the login page's password and never a bearer: a
		// password that also worked here could be guessed without meeting the login
		// page's lockout.
		return e.oa.authorized(got)
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(e.opts.token)) == 1
}

func (e *mcpEndpoint) servePost(w http.ResponseWriter, r *http.Request) {
	// A browser sends text/plain and form bodies cross-origin with no preflight, so
	// accepting them is what lets a page fire a blind call.
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil ||
		mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var req RPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, http.StatusBadRequest, RPCResponse{JSONRPC: "2.0",
			Error: &RPCError{CodeParse, "parse error", nil}})
		return
	}
	isInit := req.Method == "initialize"
	if !isInit && !e.sessionKnown(w, r, req.ID) {
		return
	}
	caller := r.Header.Get(SessionHeader)
	if caller == "" {
		caller = r.Header.Get("Mcp-Session-Id")
	}
	resp := e.s.handle(&req, r.Header.Get(ProjectHeader), caller)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted) // a notification
		return
	}
	if isInit {
		sid := newSessionID()
		e.mu.Lock()
		e.sessions[sid] = true
		e.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", sid)
	}
	writeRPC(w, http.StatusOK, *resp)
}

// sessionKnown checks the Mcp-Session-Id of a request that is not an initialize, and writes
// the refusal when it fails. A request without one is let through only while no session
// exists at all.
func (e *mcpEndpoint) sessionKnown(w http.ResponseWriter, r *http.Request, id json.RawMessage) bool {
	sid := r.Header.Get("Mcp-Session-Id")
	e.mu.Lock()
	known, others := e.sessions[sid], len(e.sessions) > 0
	e.mu.Unlock()
	switch {
	case sid != "" && !known:
		// Sessions live in memory, so every restart forgets them all. A 400 here stranded
		// connected clients until someone reconnected by hand; 404 is the spec's
		// instruction to send a fresh initialize.
		writeRPC(w, http.StatusNotFound, RPCResponse{JSONRPC: "2.0", ID: id,
			Error: &RPCError{codeSessionNotFound, "session not found; re-initialize", nil}})
		return false
	case sid == "" && others:
		http.Error(w, "missing Mcp-Session-Id", http.StatusBadRequest)
		return false
	}
	return true
}

func writeRPC(w http.ResponseWriter, status int, resp RPCResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// originAllowed: by default only explicit localhost origins, which is what a local client
// sends. An operator who needs more says so with --origin.
func originAllowed(origin, allow string) bool {
	if allow == "*" {
		return true
	}
	if allow != "" {
		for _, a := range strings.Split(allow, ",") {
			if strings.TrimSpace(a) == origin {
				return true
			}
		}
		return false
	}
	// Parsed and compared whole: a prefix match let http://localhost.evil.com through.
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return false
	}
	return loopbackNames[strings.ToLower(u.Hostname())]
}

var loopbackNames = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

func hostAllowed(host string, extra map[string]bool) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	h = strings.ToLower(h)
	return h != "" && (loopbackNames[h] || extra[h])
}

// mintHTTPToken gives a loopback server started without --token a key of its own, as `ui`
// has: loopback keeps out other machines, not the other users and processes on this one. The
// file is how a client with no access to this process's stderr finds it. It is written to a
// temporary file and renamed, so a reader never sees half a token or a file not yet mode 600.
func mintHTTPToken(o *httpOpts) (string, error) {
	o.token = netguard.RandomToken()
	_, port, err := net.SplitHostPort(o.addr)
	if err != nil || port == "" {
		port = "default"
	}
	dir := store.ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "mcp-"+port+".token")
	f, err := os.CreateTemp(dir, ".mcp-token-*")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(o.token + "\n")
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(f.Name(), 0o600)
	}
	if werr == nil {
		werr = os.Rename(f.Name(), path)
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("writing the MCP token to %s: %w", path, werr)
	}
	return path, nil
}

func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "s" + strconv.FormatInt(int64(len(b)), 16)
	}
	return hex.EncodeToString(b)
}
