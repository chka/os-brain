package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/chka/os-brain/internal/install"
	"github.com/chka/os-brain/internal/netguard"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
)

func cmdInit(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Creates the brain and wires the hooks. The binary ships alone, with no template
	// to copy, so it writes everything itself.
	a := install.InitArgs{}
	fs.StringVar(&a.Dir, "dir", "brain", "where the brain goes, relative to the project")
	fs.StringVar(&a.Project, "project", "", "name it calls itself (default: the directory)")
	fs.BoolVar(&a.NoHook, "no-hook", false, "do not touch .claude/settings.json")
	fs.BoolVar(&a.NoRecall, "no-auto-recall", false, "skip the UserPromptSubmit hook")
	fs.BoolVar(&a.NoGitgnore, "no-gitignore", false, "do not add brain.db to .gitignore")
	fs.StringVar(&a.Agents, "agent", "", "wire these agents (claude,codex,cursor,gemini,opencode,kilo,copilot,devin,qwen,hermes,continue) instead of detecting")
	fs.BoolVar(&a.Channel, "channel", false, "register the direct-message channel in .mcp.json")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	root := "."
	if len(pos) > 0 {
		root = pos[0]
	}
	a.JSON = c.json()
	if !flagSet(fs, "agent") && !a.NoHook && !a.JSON {
		if in := agentPromptIn(); in != nil {
			key, manual, err := askAgent(in, stdout, root)
			if err != nil {
				return err
			}
			a.Agents, a.Manual = key, manual
			fmt.Fprintln(stdout)
		}
	}
	// init over a team-edition brain would rewire its hooks to a binary that refuses
	// every command there, so it refuses first and changes nothing.
	if cfg, err := store.LoadConfig(filepath.Join(root, a.Dir, "brain.json")); err == nil {
		if err := refuseTeamBrain(store.Env{Config: cfg}); err != nil {
			return err
		}
	}
	return install.Init(stdout, root, a)
}

func cmdUI(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Served by the binary that owns the store, so there is nothing else to install.
	port := fs.Int("port", 7788, "")
	host := fs.String("host", "127.0.0.1", "")
	noOpen := fs.Bool("no-open", false, "do not open a browser")
	if _, err := c.parse(); err != nil {
		return err
	}
	// The UI is plain HTTP, so it is loopback or nothing: the per-process key keeps
	// other local users out, and nothing keeps a network sniffer out of plain HTTP.
	// --host 0.0.0.0 was accepted and served the brain to the LAN; the Host allowlist
	// stops a browser, not curl with a forged Host header.
	if !netguard.IsLoopback(net.JoinHostPort(*host, "0")) {
		return fmt.Errorf("refusing --host %s: the UI is plain HTTP, so it binds "+
			"loopback only. Reach it from another machine over SSH: "+
			"ssh -L %d:127.0.0.1:%d <this host>", *host, *port, *port)
	}
	if servers.UI == nil {
		return notWiredIn("ui")
	}
	return servers.UI(*host, *port, !*noOpen, stdout)
}

func cmdUninstall(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Removes exactly what init added. A dry run unless --apply, and it never deletes
	// the database unless --purge: everything else here is one command away from
	// being recreated, and the database is not.
	a := install.UninstallArgs{}
	fs.StringVar(&a.Dir, "dir", "brain", "the brain directory, relative to the project")
	fs.BoolVar(&a.Apply, "apply", false, "actually do it (default: report the plan)")
	fs.BoolVar(&a.Purge, "purge", false, "delete brain.db too — this is the only copy")
	fs.StringVar(&a.Backup, "backup", "", "where to write the dump taken first")
	fs.BoolVar(&a.Force, "force", false, "overwrite a --backup file this tool did not generate")
	fs.BoolVar(&a.AllowOutside, "allow-outside", false, "write the dump outside the project")
	fs.BoolVar(&a.AllowTracked, "allow-tracked", false, "write the dump to a git-tracked path")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	root, err := uninstallRoot(c.env, pos, a.Dir)
	if err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error {
		return install.Uninstall(stdout, s, root, a)
	})
}

func cmdMCP(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Serves the whole command surface, derived from the same flag registration the
	// CLI uses (see ProbeFlags).
	httpAddr := fs.String("http", "", "serve Streamable HTTP on this address "+
		"instead of stdio, e.g. 127.0.0.1:7777")
	token := fs.String("token", "", "bearer token; required off loopback, and minted per "+
		"process into a mode-600 file when omitted on loopback. With --public-url it is "+
		"the login page's password and is not accepted as a bearer")
	tlsCert := fs.String("tls-cert", "", "serve HTTPS with this certificate (PEM); "+
		"required, with --tls-key, for any bind that is not loopback")
	tlsKey := fs.String("tls-key", "", "the certificate's private key (PEM)")
	origin := fs.String("origin", "", "comma-separated allowed Origins, or * ")
	allowHost := fs.String("allow-host", "", "comma-separated Host names answered besides "+
		"loopback ones, for a reverse proxy that passes its public name through")
	publicURL := fs.String("public-url", "", "the https URL hosted clients reach this "+
		"server at (e.g. a Tailscale Funnel name); mounts OAuth so the Claude app can "+
		"connect, with --token as the password on the login page")
	var redirectHosts []string
	fs.Func("oauth-redirect-host", "with --public-url: the only host an OAuth client may "+
		"register an https redirect to (repeatable; e.g. claude.ai). Unset, any https "+
		"redirect or http on loopback registers", func(v string) error {
		redirectHosts = append(redirectHosts, v)
		return nil
	})
	if _, err := c.parse(); err != nil {
		return err
	}
	if *token == "" {
		// A supervisor (launchd, systemd) hands the secret over the environment so
		// it is not on the command line for every user on the box to read in ps.
		*token = os.Getenv("OS_BRAIN_MCP_TOKEN")
	}
	if servers.MCP == nil {
		return notWiredIn("mcp")
	}
	return servers.MCP(MCPOptions{HTTPAddr: *httpAddr, Token: *token, TLSCert: *tlsCert,
		TLSKey: *tlsKey, Origin: *origin, AllowHost: *allowHost, PublicURL: *publicURL,
		RedirectHosts: redirectHosts}, os.Stdin, stdout)
}

// uninstallRoot is the project root uninstall acts on, and it must be the root of the brain
// this command opened. The store came from discovery (or OS_BRAIN_HOME) and the files from
// the argument, so `uninstall ../other --purge` once backed up this project and deleted the
// other one's only database. Two arguments naming two brains is ambiguous, and ambiguity is
// refused. With no argument the root is derived from the opened brain, so a shell in a
// subdirectory uninstalls the brain it found rather than <subdir>/brain.
func uninstallRoot(env store.Env, pos []string, dir string) (string, error) {
	if dir == "" {
		dir = "brain"
	}
	clean := filepath.Clean(dir)
	if filepath.IsAbs(dir) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("--dir is the brain directory relative to the project, and %s "+
			"is not one. Nothing was changed.", pyfmt.Repr(dir))
	}
	here, err := filepath.EvalSymlinks(env.Here)
	if err != nil {
		return "", err
	}
	if len(pos) == 0 {
		return rootOfOpenedBrain(env, dir, clean)
	}
	abs, err := filepath.Abs(pos[0])
	if err != nil {
		return "", err
	}
	target, terr := filepath.EvalSymlinks(filepath.Join(abs, clean))
	if terr != nil || target != here {
		return "", fmt.Errorf("%s is not the brain this command opened (%s), which is the store "+
			"it would back up and delete. Nothing was changed. Run uninstall from inside the "+
			"project being removed, or with OS_BRAIN_HOME set to its brain directory.",
			filepath.Join(abs, clean), env.Here)
	}
	return abs, nil
}

// rootOfOpenedBrain walks up from the opened brain directory by as many levels as --dir has,
// and refuses when --dir does not name that brain from the root it reaches.
func rootOfOpenedBrain(env store.Env, dir, clean string) (string, error) {
	root := env.Here
	for range strings.Split(clean, string(filepath.Separator)) {
		root = filepath.Dir(root)
	}
	if filepath.Join(root, clean) != filepath.Clean(env.Here) {
		return "", fmt.Errorf("--dir %s is not the brain this command opened (%s). "+
			"Nothing was changed. Run uninstall from that project with its own --dir.",
			dir, env.Here)
	}
	return root, nil
}
