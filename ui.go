package main

// `os-brain ui`: a web page over the brain, served by the binary that owns the store.
//
// The assets (ui/) are embedded, so one static binary is still the whole install: there is
// no runtime to install and no viewer on disk to fall out of step with it.
//
// The rule this is built on, and the reason the Go side is thin: **it holds no SQL.** Every
// route builds an argv and runs the ordinary dispatch (ui_routes.go), so ranking, scoping,
// the project filter and every refusal are the CLI's, not a second implementation that has
// to be kept in step. A second implementation existed once and drifted three times.
//
// The HTTP surface and its guards are in ui_http.go.

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

//go:embed ui/index.html ui/app.js ui/style.css
var uiFiles embed.FS

// uiToken is minted per process and is the key to every route that touches the brain,
// reads included. It reaches the page one way only: the page's HTML carries it in a meta
// tag, and the page is served only to a browser holding the cookie a bootstrap link sets.
// An earlier /api/meta returned it to anybody, and the reads needed none, so any local user
// could read the brain on loopback. A page left open across a restart holds a dead key and
// is told to reload rather than silently failing.
var uiToken = func() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

// call runs one CLI command in-process and returns its JSON. Identical to what the MCP
// server does, and for the same reason: the answer a page sees has to be the answer the
// command gives.
func call(args ...string) (json.RawMessage, error) {
	var out bytes.Buffer
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// The UI transport marks the caller as a page, not a person at a terminal, so every
	// guard that refuses such a caller over MCP refuses the page too.
	if err := runWithVia(callVia{transport: transportUI}, args[0], args[1:], &out, fs, false, true, "", ""); err != nil {
		return nil, err
	}
	body := bytes.TrimSpace(out.Bytes())
	if len(body) == 0 {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("%s did not return JSON", args[0])
	}
	return json.RawMessage(body), nil
}

func callMap(args ...string) (map[string]any, error) {
	raw, err := call(args...)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func callList(args ...string) ([]any, error) {
	raw, err := call(args...)
	if err != nil {
		return nil, err
	}
	var l []any
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	return l, nil
}

// serveUI binds, prints the link that opens the page, optionally opens a browser on a
// second link, and serves until killed. The caller has already refused a non-loopback host.
func serveUI(host string, port int, openBrowser bool, out io.Writer) error {
	mux, err := uiHandler()
	if err != nil {
		return err
	}
	// Bind before announcing: a port already in use should say so instead of printing a
	// URL that answers with somebody else's page.
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("cannot listen on %s:%d: %w", host, port, err)
	}
	stats, err := callMap("stats")
	if err != nil {
		ln.Close()
		return err
	}
	addr := fmt.Sprintf("http://%s:%d", host, port)
	fmt.Fprintf(out, "%v brain  %s\n", stats["project"], uiURL(addr, 0))
	fmt.Fprintf(out, "brain   LOCAL FILE %v\n", stats["db"])
	fmt.Fprintln(out, "every read runs the brain's own commands, so this page cannot "+
		"disagree with it about scoping or ranking")
	fmt.Fprintln(out, "the link opens the page once, in one browser: the first visit spends it "+
		"and leaves this process's key in that browser's cookie")
	fmt.Fprintln(out, "ctrl-c to stop")
	if openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			launch(uiURL(addr, uiLaunchTTL))
		}()
	}
	return http.Serve(ln, mux)
}

// launch opens addr in the desktop's default browser, best effort.
func launch(addr string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, addr)...).Start()
}
