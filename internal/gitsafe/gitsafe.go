// Package gitsafe is the only way this binary runs git. Every call reads a repository the
// person did not necessarily write: an unpacked tarball, a shared checkout, a directory
// another tool produced, and the SessionStart hook runs them unattended. Git honours that
// repository's own .git/config, and several keys there name programs git will execute on a
// plain `status` or `log`. Configuration from the environment (GIT_CONFIG_COUNT) beats every
// config file and is inherited by any git process the call spawns, so each such key is pinned
// off there. The environment rather than -c, because a key arrives whole: a filter driver
// named with an "=" in it cannot reshape a -c string into a different setting.
package gitsafe

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// pinned are the config keys that make git run a program (or reach the network) on its
// own, each set to a value that turns the behaviour off.
var pinned = [][2]string{
	{"core.fsmonitor", "false"},
	{"core.hooksPath", "/dev/null"},
	{"core.pager", "cat"},
	{"core.sshCommand", ""},
	{"core.askPass", ""},
	{"credential.helper", ""},
	{"diff.external", ""},
	{"log.showSignature", "false"},
	{"gpg.program", ""},
	{"gpg.ssh.program", ""},
	{"gpg.x509.program", ""},
	{"protocol.allow", "never"},
}

// Command builds git -C dir <args> with config-driven execution disabled, optional locks
// off (a status at session open must not rewrite the index under the person's own git), no
// prompt, and every GIT_* variable stripped so GIT_DIR or GIT_WORK_TREE cannot make another
// repository answer.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	argv := []string{"-C", dir, "--no-pager", "--no-optional-locks"}
	config := append([][2]string(nil), pinned...)
	if len(args) > 0 && args[0] == "status" {
		// Submodules carry configs of their own and are not entered.
		config = append(config, emptiedFilters(ctx, dir)...)
		args = append([]string{"status", "--ignore-submodules=all"}, args[1:]...)
	}
	cmd := exec.CommandContext(ctx, "git", append(argv, args...)...)
	cmd.Env = env()
	cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for i, kv := range config {
		n := strconv.Itoa(i)
		cmd.Env = append(cmd.Env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}
	return cmd
}

// emptiedFilters switches off every filter driver the repository defines. status runs a
// clean filter on any file whose stat data changed, to compare its content with the index,
// and the filter is whatever .git/config names under .gitattributes' driver. There is no
// switch for all drivers, so each one is emptied, which git treats as no filter.
func emptiedFilters(ctx context.Context, dir string) [][2]string {
	var config [][2]string
	for _, name := range filterDrivers(ctx, dir) {
		for _, key := range []string{"clean", "smudge", "process"} {
			config = append(config, [2]string{"filter." + name + "." + key, ""})
		}
		config = append(config, [2]string{"filter." + name + ".required", "false"})
	}
	return config
}

// Output runs Command and returns stdout untrimmed.
func Output(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := Command(ctx, dir, args...).Output()
	return string(out), err
}

// env is this process's environment without any GIT_* variable, plus the two that keep git
// from prompting or taking optional locks.
func env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
}

// filterDrivers lists the filter driver names the repository's config defines, from keys of
// the form filter.<name>.<setting>. Reading config runs no driver, so this call is safe.
func filterDrivers(ctx context.Context, dir string) []string {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "--name-only", "--get-regexp", `^filter\.`)
	cmd.Env = env()
	out, _ := cmd.Output()
	seen := map[string]bool{}
	var names []string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimPrefix(strings.TrimSpace(ln), "filter.")
		i := strings.LastIndexByte(ln, '.')
		if i <= 0 || seen[ln[:i]] {
			continue
		}
		seen[ln[:i]] = true
		names = append(names, ln[:i])
	}
	return names
}
