package cli

import (
	"fmt"
	"io"

	"github.com/chka/os-brain/internal/store"
)

// The servers (MCP, the web UI and the direct-message channel) are commands of this CLI, and
// every call they serve runs this CLI's dispatch. Go imports go one way only, so the servers'
// packages import cli and cli reaches them through this registration point, which the
// binary's main fills in before the first command runs.

// MCPOptions are the mcp command's flags. They are registered and parsed here, like every
// other command's, because the MCP tool list and `help mcp` are derived from the CLI's own
// flag registration.
type MCPOptions struct {
	HTTPAddr      string   // --http: serve Streamable HTTP here instead of stdio
	Token         string   // --token, or OS_BRAIN_MCP_TOKEN
	TLSCert       string   // --tls-cert
	TLSKey        string   // --tls-key
	Origin        string   // --origin
	AllowHost     string   // --allow-host
	PublicURL     string   // --public-url
	RedirectHosts []string // --oauth-redirect-host, repeatable
}

// Servers are the entry points of the commands that serve until they are killed.
type Servers struct {
	// MCP serves every command as an MCP tool, over stdin and stdout or over HTTP.
	MCP func(opts MCPOptions, stdin io.Reader, stdout io.Writer) error
	// UI serves the web UI on host:port, which the ui command has already checked is
	// loopback.
	UI func(host string, port int, openBrowser bool, stdout io.Writer) error
	// Channel pushes direct messages for env's session over stdin and stdout.
	Channel func(env store.Env, stdin io.Reader, stdout io.Writer) error
}

var servers Servers

// RegisterServers wires the servers in. The os-brain binary calls it once, from main.
func RegisterServers(s Servers) { servers = s }

// notWiredIn is the error for a server command in a program that registered no server.
func notWiredIn(cmd string) error {
	return fmt.Errorf("%s is not built into this program", cmd)
}
