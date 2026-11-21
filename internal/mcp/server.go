// Package mcp is os-brain's MCP server: the whole CLI, offered as tools over stdio (this
// file) and over Streamable HTTP (http.go, with OAuth for hosted clients in oauth.go).
//
// Two things to keep in mind when editing:
//
//  1. The tool list is DERIVED from the CLI's own flag registration (tools.go), never
//     authored, so it cannot drift from the commands it describes.
//  2. A tools/call builds an argv and runs the ordinary dispatch, so the MCP path IS the
//     CLI path. Guards, refusals and output are identical by construction.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/chka/os-brain/internal/brain"
	"github.com/chka/os-brain/internal/cli"
)

// ProtocolVersion is what this server implements. A client asking for something else gets
// this back and decides whether it can live with it, which is what the spec prescribes.
const ProtocolVersion = "2025-06-18"

// FallbackProtocolVersion is what the spec says to assume when an HTTP client sends no
// MCP-Protocol-Version header at all.
const FallbackProtocolVersion = "2025-03-26"

// RPCRequest is one JSON-RPC 2.0 request or notification.
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError is the error member of a JSON-RPC 2.0 response.
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// RPCResponse is one JSON-RPC 2.0 response.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// JSON-RPC error codes. CodeParse and CodeMethodNotFound are exported for the channel
// server, which answers the same two failures.
const (
	CodeParse          = -32700
	CodeMethodNotFound = -32601
	codeInvalidParams  = -32602
	// Not JSON-RPC's own: the MCP SDKs' "session not found", which clients pair with a
	// 404 to decide that re-initializing is the fix.
	codeSessionNotFound = -32001
)

// ProjectHeader lets a client declare, once in its config, which project a connection is
// for. That is better than making the model remember _project on every call: config cannot
// forget, and a model can. The argument still wins when it is given, so one connection can
// still reach across projects deliberately.
const ProjectHeader = "X-Brain-Project"

// SessionHeader lets a client declare its identity once in its config, the same way
// ProjectHeader does, rather than repeating _session on every call.
const SessionHeader = "X-Brain-Session"

const instructions = `This is os-brain: persistent, scoped, decay-tiered memory for one project.

Read it before you search files, and write to it before you finish. recall answers what
this project already worked out; brief is the session handoff and carries the wall clock,
the git state, the binding rules and how this person wants to be worked with.

Two things that are easy to get wrong. Recall FAILS CLOSED: with json=true a miss returns
nothing rather than the whole scope, so check "matched" before acting on "results", and
"loose": true means no memory held most of the query's words, only some of them.
And storing is deliberate, not automatic — one distilled fact per memory, never a paste,
and never something the repository or git history already says.`

type mcpServer struct {
	tools []toolDef

	// cwdBlind is set when the client is known to start this server somewhere other than
	// the project it is working in, and nothing else names the brain. Every call refuses.
	cwdBlind string

	// What this server fronts, resolved once at startup: a store with one project can
	// answer without being told which, a store with several cannot.
	defaultProject string
	known          []string
	multi          bool
	// where is what this server fronts, in words, appended to the MCP instructions. A
	// client sees these once at initialize and never sees the brief's header at all.
	where string

	// transport is how this server is reached: a person-only command is refused over MCP.
	transport cli.Transport
}

func newServer() (*mcpServer, error) {
	tools, err := buildTools()
	if err != nil {
		return nil, err
	}
	s := &mcpServer{tools: tools}
	var shared bool
	var env brain.Env
	s.defaultProject, s.known, shared, env = surveyStore()
	// OS_BRAIN_PROJECT names the project outright, which is how a stdio server launched by a
	// client for one repo is configured. That is not ambiguous, shared store or not.
	if os.Getenv("OS_BRAIN_PROJECT") != "" {
		shared = false
	}
	// A SHARED store is multi-project by definition, whether or not a second project has
	// written to it yet. Gating on "more than one project exists" was wrong: a fresh shared
	// store holds exactly one, so the first session from another project would still be
	// silently filed under the server's name, the very case this guards.
	s.multi = shared || len(s.known) > 1
	s.where = whereThisBrainIs(env, s.multi, s.known)
	return s, nil
}

// surveyStore reports the project this server was started in, every project already in the
// store it opened, whether that store is shared, and the env it resolved. The project list
// is how a session on a shared store discovers what it may name; the env lets initialize
// say WHERE this brain is, because a connector's label is whatever the person who added it
// typed, and a session holding two of them cannot otherwise tell one brain from another.
func surveyStore() (string, []string, bool, brain.Env) {
	env := brain.NewEnv(serverHome())
	st, err := brain.Open(env)
	if err != nil {
		return env.Project, []string{env.Project}, env.Shared, env
	}
	defer st.Close()
	return env.Project, visibleProjects(st, env.Project), env.Shared, env
}

// serverHome is the brain directory this server fronts: OS_BRAIN_HOME when it is set, else
// the brain found from the working directory, else the working directory itself.
func serverHome() string {
	if here := os.Getenv("OS_BRAIN_HOME"); here != "" {
		return here
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if found, ok := brain.DiscoverHome(cwd); ok {
		return found
	}
	return cwd
}

// visibleProjects lists the projects in st that may be named to a caller, sorted, always
// including this server's own. A private project is not offered by name, in the initialize
// text or in the refusal that lists who is here; naming it is refused anyway.
func visibleProjects(st *brain.Store, own string) []string {
	seen := map[string]bool{own: true}
	rows, err := st.DB.Query("SELECT DISTINCT project FROM memories WHERE project != ''")
	if err == nil {
		var names []string
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil {
				names = append(names, p)
			}
		}
		rows.Close()
		for _, p := range names {
			if p == own || !st.PrivateProject(p) {
				seen[p] = true
			}
		}
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// via is the transport this server's calls reach the dispatcher over.
func (s *mcpServer) via() cli.Transport {
	t := s.transport
	if t == "" {
		t = cli.TransportMCPStdio
	}
	return t
}

// cwdBlindReason refuses a client that runs MCP servers from one long-lived process for every
// project. Cline's CLI starts them from a hub daemon whose directory is whichever project
// started the hub, and reports no roots, so a server finding its brain from the working
// directory answered from ANOTHER project's brain without a word (seen with Cline CLI
// 3.0.72). With OS_BRAIN_HOME or OS_BRAIN_PROJECT set, the brain is named and the directory
// does not matter.
func cwdBlindReason(client string, roots bool) string {
	if !strings.HasPrefix(client, "@cline/") || roots ||
		os.Getenv("OS_BRAIN_HOME") != "" || os.Getenv("OS_BRAIN_PROJECT") != "" {
		return ""
	}
	return "this server cannot tell which brain you mean: Cline starts MCP servers from one " +
		"shared process whose directory is whichever project started it first, so the " +
		"working directory names the wrong project as often as the right one. Set OS_BRAIN_HOME " +
		"to this project's brain directory (and OS_BRAIN_PROJECT on a shared store) in the " +
		"server's env in ~/.cline/data/settings/cline_mcp_settings.json"
}

func (s *mcpServer) handle(req *RPCRequest, sessionProject, mcpSession string) *RPCResponse {
	resp := &RPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = s.initialize(req.Params)
	case "notifications/initialized", "notifications/cancelled", "notifications/roots/list_changed":
		return nil // notifications take no response
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.tools}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &RPCError{codeInvalidParams, "invalid params: " + err.Error(), nil}
			return resp
		}
		resp.Result = s.callTool(p.Name, p.Arguments, sessionProject, mcpSession)
	default:
		resp.Error = &RPCError{CodeMethodNotFound, "unknown method: " + req.Method, nil}
	}
	return resp
}

// initialize answers the handshake. The client's name and whether it reports roots decide
// whether this server can trust its own working directory (cwdBlindReason).
func (s *mcpServer) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Roots *json.RawMessage `json:"roots"`
		} `json:"capabilities"`
		ClientInfo struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	_ = json.Unmarshal(params, &p)
	s.cwdBlind = cwdBlindReason(p.ClientInfo.Name, p.Capabilities.Roots != nil)
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo": map[string]any{
			// The title is what a client puts in its UI beside the connector's own label,
			// and that label is whatever the person who added it typed. Naming the project
			// here is what lets a session tell two connected brains apart.
			"name": "os-brain", "title": s.title(), "version": brain.Version,
		},
		"instructions": instructions + s.where,
	}
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

func toolError(text string) callResult {
	return callResult{[]contentBlock{{"text", text}}, true}
}

// callTool runs a command and returns its output. A failing command is a TOOL error
// (isError), not a protocol error: the call was well-formed, the operation refused. That
// distinction matters here, because most refusals in this codebase are deliberate guards
// and the model needs to read them rather than see a transport fault.
func (s *mcpServer) callTool(name string, args map[string]any, sessionProject, mcpSession string) callResult {
	if s.cwdBlind != "" {
		return toolError(s.cwdBlind)
	}
	cmd := commandFor(name)
	if cmd == "" {
		return toolError("unknown tool: " + name)
	}
	project, err := s.resolveProject(args, sessionProject)
	if err != nil {
		return toolError(err.Error())
	}
	argv, err := argvFor(cmd, args)
	if err != nil {
		return toolError(err.Error())
	}
	var buf bytes.Buffer
	if err := cli.RunVia(s.via(), argv, &buf, project, resolveSession(args, mcpSession)); err != nil {
		// Whatever the command printed before it failed is part of the answer.
		text := strings.TrimRight(buf.String(), "\n")
		if text != "" {
			text += "\n"
		}
		return toolError(text + err.Error())
	}
	out := strings.TrimRight(buf.String(), "\n")
	if out == "" {
		out = "(no output)"
	}
	return callResult{[]contentBlock{{"text", out}}, false}
}

// resolveSession prefers what the caller said, falls back to the transport's own
// connection id, and finally to nothing, which SessionID() then reads from the
// environment, giving the CLI its existing behaviour unchanged.
func resolveSession(args map[string]any, mcpSession string) string {
	if raw, ok := args["_session"]; ok {
		if v, ok := raw.(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if strings.TrimSpace(mcpSession) != "" {
		return "mcp:" + strings.TrimSpace(mcpSession)
	}
	return ""
}

// resolveProject decides which project a call is for: the _project argument, then the
// ProjectHeader, then this server's own project when it fronts only one. It fails closed:
// when the store holds several projects and the caller named none, it says so and lists
// them rather than quietly using the server's own label. "When you cannot tell what was
// meant, do nothing and say so" is a rule of this codebase, and this is a write path.
func (s *mcpServer) resolveProject(args map[string]any, fromHeader string) (string, error) {
	if raw, ok := args["_project"]; ok {
		if p := strings.TrimSpace(scalar(raw)); p != "" {
			return p, nil
		}
	}
	if p := strings.TrimSpace(fromHeader); p != "" {
		return p, nil
	}
	if !s.multi {
		return s.defaultProject, nil
	}
	//lint:ignore ST1005 the refusal is user-facing text, and its wording is part of the contract
	return "", fmt.Errorf("this server fronts a shared brain, so it cannot tell which "+
		"project you are working in: pass _project.\n  already here: %s\n"+
		"Use the name this project calls itself. A project new to this brain simply names "+
		"itself, and its first write creates it.\nA client can also declare it once in its "+
		"own config, with an "+ProjectHeader+" header, instead of passing it every call.",
		strings.Join(s.known, ", "))
}

// Serve is the mcp command once its flags are parsed: it builds the server and serves
// Streamable HTTP when opts name an address, stdio otherwise.
func Serve(opts cli.MCPOptions, stdin io.Reader, stdout io.Writer) error {
	srv, err := newServer()
	if err != nil {
		return err
	}
	if opts.HTTPAddr != "" {
		return srv.serveHTTP(httpOpts{addr: opts.HTTPAddr, token: opts.Token,
			origin: opts.Origin, hosts: opts.AllowHost, publicURL: opts.PublicURL,
			tlsCert: opts.TLSCert, tlsKey: opts.TLSKey, redirectHosts: opts.RedirectHosts})
	}
	return srv.serveStdio(stdin, stdout)
}

// serveStdio reads newline-delimited JSON-RPC from stdin and writes to stdout. NOTHING
// else may reach stdout: the brain's own warnings already go to stderr and must stay
// there, or they corrupt the stream.
func (s *mcpServer) serveStdio(in io.Reader, out io.Writer) error {
	s.transport = cli.TransportMCPStdio
	cli.SetStdinIsProtocol(true)
	defer cli.SetStdinIsProtocol(false)
	rd := bufio.NewScanner(in)
	rd.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(out)
	for rd.Scan() {
		line := bytes.TrimSpace(rd.Bytes())
		if len(line) == 0 {
			continue
		}
		var req RPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(RPCResponse{JSONRPC: "2.0",
				Error: &RPCError{CodeParse, "parse error: " + err.Error(), nil}})
			continue
		}
		if resp := s.handle(&req, os.Getenv("OS_BRAIN_PROJECT"), ""); resp != nil {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
	return rd.Err()
}

// title names the project and the edition, because a client shows it beside a connector
// label that only says whatever the person who added it typed.
func (s *mcpServer) title() string {
	// A server that answers for any project must not be titled after the one it started
	// in: sessions in other projects read "os-brain — mtest" as a connector bound to mtest.
	if s.multi {
		return "os-brain — every project (local)"
	}
	if s.defaultProject == "" {
		return "os-brain (local)"
	}
	return "os-brain — " + s.defaultProject + " (local)"
}

// whereThisBrainIs is the paragraph appended to the MCP instructions. Over stdio or HTTP a
// session never sees a brief header until it calls one, so the store and the machine are
// stated at initialize, where every client reads them exactly once.
func whereThisBrainIs(env brain.Env, multi bool, known []string) string {
	var b strings.Builder
	b.WriteString("\n\nWHERE THIS BRAIN IS\n")
	fmt.Fprintf(&b, "A LOCAL FILE brain: %s, on this machine only, shared with "+
		"nobody.\n", env.DBPath)
	if !multi {
		fmt.Fprintf(&b, "Reached from %s by os-brain %s, for project %q.\n",
			brain.MachineName(), brain.Version, env.Project)
	} else {
		// Naming only the project the server started in read, to every session in another
		// project, as "this connector belongs to someone else", and sessions went looking
		// for a mismatch that did not exist.
		fmt.Fprintf(&b, "Reached from %s by os-brain %s. It answers for EVERY project in "+
			"this store, not only %q, the project it was started in: each call names its "+
			"project with _project (or an %s header) and is answered from that project's "+
			"own memories. A call that names none is refused.\n",
			brain.MachineName(), brain.Version, env.Project, ProjectHeader)
		b.WriteString(projectsAlreadyHere(known))
	}
	b.WriteString("Call env for the live version of all of this.")
	return b.String()
}

// projectsAlreadyHere lists the known projects, capped at twenty so a large shared store
// does not bury the instructions.
func projectsAlreadyHere(known []string) string {
	if len(known) == 0 {
		return ""
	}
	list, more := known, ""
	if len(list) > 20 {
		list, more = list[:20], fmt.Sprintf(", and %d more", len(known)-20)
	}
	return fmt.Sprintf("Projects already here: %s%s.\n", strings.Join(list, ", "), more)
}
