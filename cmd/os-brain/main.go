// Command os-brain keeps an AI coding agent's project memories in a local SQLite store and
// wires the agent's hooks to it.
//
// The command line is internal/cli. The servers it starts (internal/mcp, internal/webui and
// internal/channel) run the CLI's own dispatch for every call, so they import cli and are
// wired into it here rather than imported by it.
package main

import (
	"fmt"
	"os"

	"github.com/chka/os-brain/internal/brain"
	"github.com/chka/os-brain/internal/channel"
	"github.com/chka/os-brain/internal/cli"
	"github.com/chka/os-brain/internal/mcp"
	"github.com/chka/os-brain/internal/webui"
)

func main() {
	cli.RegisterServers(cli.Servers{MCP: mcp.Serve, UI: webui.Serve, Channel: channel.Serve})
	if err := cli.Run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(brain.SafeOutput(os.Stderr), err)
		os.Exit(1)
	}
}
