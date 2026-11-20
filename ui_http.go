package main

// The UI's HTTP surface and its guards. The UI is plain HTTP on loopback (the `ui` command
// refuses any other bind), so what keeps a brain private here is: the Host check against
// DNS rebinding, the per-process key on every data route, the Origin check, and the
// single-use bootstrap link that hands the key to exactly one browser.

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

var allowedUIHosts = map[string]bool{"127.0.0.1": true, "localhost": true,
	"::1": true, "[::1]": true}

// tokenOK compares in constant time; an empty token never matches.
func tokenOK(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(uiToken)) == 1
}

// A bootstrap link carries a single-use nonce rather than the key. The link `ui` opens is on
// the launcher's argv, and on a cold start the browser's, where any local user reads it in
// ps, and a query string lands in history and session restore. A nonce is spent on the
// first visit, which trades it for an HttpOnly cookie holding the key, so what those places
// keep opens nothing. The opened link also expires quickly; the printed one lasts the
// process, because somebody may read the banner a while later, and it is not on any argv.
var uiBoot = struct {
	sync.Mutex
	nonces map[string]time.Time // zero time: no expiry
}{nonces: map[string]time.Time{}}

const uiLaunchTTL = time.Minute

// uiURL mints a bootstrap link. ttl 0 lasts until it is used.
func uiURL(addr string, ttl time.Duration) string {
	n := randomToken()
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	uiBoot.Lock()
	uiBoot.nonces[n] = exp
	uiBoot.Unlock()
	return addr + "/?k=" + n
}

// takeBoot spends a nonce: valid or not, it never works twice.
func takeBoot(n string) bool {
	if n == "" {
		return false
	}
	uiBoot.Lock()
	defer uiBoot.Unlock()
	exp, ok := uiBoot.nonces[n]
	delete(uiBoot.nonces, n)
	return ok && (exp.IsZero() || time.Now().Before(exp))
}

// uiCookie is per port: cookies ignore the port, so two `ui` processes on one host would
// otherwise overwrite each other's key.
func uiCookie(host string) string {
	_, port, _ := net.SplitHostPort(host)
	if _, err := strconv.Atoi(port); err != nil {
		port = "0"
	}
	return "brain_ui_" + port
}

func uiCookieOK(r *http.Request) bool {
	c, err := r.Cookie(uiCookie(r.Host))
	return err == nil && tokenOK(c.Value)
}

// uiServer holds what the page's handler serves: the data routes and the embedded assets.
type uiServer struct {
	routes map[string]uiRoute
	assets fs.FS
}

// uiHandler is the page's whole HTTP surface, split out so its guards are tested without a
// port.
func uiHandler() (http.Handler, error) {
	assets, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, err
	}
	u := &uiServer{routes: uiRoutes(), assets: assets}
	mux := http.NewServeMux()
	mux.HandleFunc("/", u.serve)
	return mux, nil
}

func (u *uiServer) serve(w http.ResponseWriter, r *http.Request) {
	if !uiHostAllowed(r.Host) {
		http.Error(w, "bad host", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodPost {
		u.serveForget(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if route, ok := u.routes[r.URL.Path]; ok {
		u.serveRoute(w, r, route)
		return
	}
	u.serveAsset(w, r)
}

// uiHostAllowed rejects DNS rebinding, and the malformed Host values that would otherwise
// throw out of URL parsing and take the process down. The port is cut off by hand rather
// than with net.SplitHostPort so a bare bracketed IPv6 host is still recognised.
func uiHostAllowed(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	return allowedUIHosts[h]
}

// uiOriginOK is false when the request carries an Origin that is not this page's own. An
// absent Origin passes: some clients send none, and the key is checked as well.
func uiOriginOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && allowedUIHosts[u.Hostname()]
}

func uiFail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func uiWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// serveForget is the one POST: forgetting a memory.
func (u *uiServer) serveForget(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/forget" {
		uiFail(w, http.StatusNotFound, "not found")
		return
	}
	// Two independent checks, because either alone has a hole: a token can leak into a
	// log, and Origin is absent on some non-browser clients.
	if !uiOriginOK(r) {
		uiFail(w, http.StatusForbidden, "bad origin")
		return
	}
	if !tokenOK(r.Header.Get("X-Brain-Token")) {
		uiFail(w, http.StatusForbidden, "stale page: reload it")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		uiFail(w, http.StatusBadRequest, "body is not JSON")
		return
	}
	res, err := uiForget(body)
	if err != nil {
		// The brain's refusals are the useful half of this response; pass the text
		// through whole rather than reducing it to a status code.
		uiFail(w, http.StatusConflict, err.Error())
		return
	}
	uiWriteJSON(w, res)
}

// serveRoute answers a data route. Reads are the brain too, so they get the same Origin
// check and key as the write.
func (u *uiServer) serveRoute(w http.ResponseWriter, r *http.Request, route uiRoute) {
	if !uiOriginOK(r) {
		uiFail(w, http.StatusForbidden, "bad origin")
		return
	}
	if !tokenOK(r.Header.Get("X-Brain-Token")) {
		uiFail(w, http.StatusForbidden, "open the link `os-brain ui` printed: this page "+
			"needs that process's key")
		return
	}
	data, err := route(r.URL)
	if err != nil {
		uiFail(w, http.StatusInternalServerError, err.Error())
		return
	}
	uiWriteJSON(w, data)
}

// serveAsset serves an embedded file. index.html is special: it is the one carrier of the
// key, so it goes only to a browser that came through a bootstrap link.
func (u *uiServer) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	body, err := fs.ReadFile(u.assets, path.Clean(name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if name == "index.html" {
		var done bool
		if body, done = serveIndexAuth(w, r, body); done {
			return
		}
	}
	w.Header().Set("content-type", assetContentType(name))
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("x-content-type-options", "nosniff")
	w.Header().Set("content-security-policy",
		"default-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'")
	_, _ = w.Write(body)
}

// serveIndexAuth applies the bootstrap rules to a request for the page. A visit with a
// nonce spends it, sets the cookie and redirects to the bare URL, so the nonce leaves the
// address bar; a visit without one needs the cookie. It reports done when it has already
// answered; otherwise it returns the page with the key injected.
func serveIndexAuth(w http.ResponseWriter, r *http.Request, page []byte) ([]byte, bool) {
	if k := r.URL.Query().Get("k"); k != "" {
		if takeBoot(k) {
			http.SetCookie(w, &http.Cookie{Name: uiCookie(r.Host), Value: uiToken, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode})
		} else if !uiCookieOK(r) {
			uiPlainForbidden(w, "this link has been used or has expired: run "+
				"`os-brain ui` again for a fresh one\n")
			return nil, true
		}
		w.Header().Set("referrer-policy", "no-referrer")
		w.Header().Set("cache-control", "no-store")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return nil, true
	}
	if !uiCookieOK(r) {
		uiPlainForbidden(w, "open the link `os-brain ui` printed: this page "+
			"needs that process's key\n")
		return nil, true
	}
	page = bytes.Replace(page, []byte("<head>"), []byte(`<head>
<meta name="brain-token" content="`+uiToken+`">`), 1)
	w.Header().Set("referrer-policy", "no-referrer")
	return page, false
}

func uiPlainForbidden(w http.ResponseWriter, msg string) {
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, msg)
}

func assetContentType(name string) string {
	ct := map[string]string{
		".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
		".css": "text/css; charset=utf-8",
	}[path.Ext(name)]
	if ct == "" {
		ct = "application/octet-stream"
	}
	return ct
}
