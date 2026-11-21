package brief

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// contextFacts is everything the session cannot work out for itself, or gets wrong when
// it guesses: the wall clock (not just the date), where it is, and what the project
// calls itself. The brief and `env` both render it, and --json returns it as it stands.
func (s *Store) contextFacts() map[string]interface{} {
	now := time.Now()
	host, _ := os.Hostname()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	facts := map[string]interface{}{
		"project":      s.Env.Project,
		"now":          now.Format("2006-01-02 15:04:05 MST"),
		"weekday":      now.Format("Monday"),
		"day_part":     timefmt.DayPart(now.Hour()),
		"timezone":     now.Format("MST"),
		"epoch":        now.Unix(),
		"project_root": s.Env.ProjectRoot(),
		"brain_dir":    s.Env.Here,
		"db":           s.Env.DBPath,
		"host":         host,
		"user":         username,
		"platform":     platformName(),
		"go":           runtime.Version(),
		"machine":      store.MachineName(),
		// Stated, not implied: a session reads "local" here rather than deducing it from
		// the absence of a server. ns-brain's team edition answers "hive" in this field.
		"edition": "local",
	}
	if s.Env.Remote {
		// Served to a session working somewhere else: this machine's git tree, host and
		// paths belong to the server and say nothing true about the caller.
		facts["remote"] = true
		delete(facts, "project_root")
		delete(facts, "host")
		delete(facts, "user")
		delete(facts, "machine")
	} else {
		for k, v := range s.gitFacts() {
			facts["git_"+k] = v
		}
	}
	for k, v := range s.Env.Config.Context {
		facts["project_"+k] = v
	}
	if last := s.SessionCursor("last_brief_at"); last > 0 {
		facts["last_session"] = timefmt.HumanGap(now.Unix() - last)
	}
	return facts
}

// contextLines renders contextFacts as the block under the brief's title.
func contextLines(facts map[string]interface{}) []string {
	lines := []string{nowLine(facts)}
	if remote, _ := facts["remote"].(bool); remote {
		lines = append(lines,
			fmt.Sprintf("  project   %s (served remotely; this brain is not on your machine)",
				factLine(facts["project"])),
			"  note      the clock above is the SERVER's, and the git state of your working "+
				"tree is yours to read, not the brain's")
	} else {
		lines = append(lines, fmt.Sprintf("  project   %s at %s", factLine(facts["project"]),
			factLine(facts["project_root"])))
	}
	lines = append(lines, gitLines(facts)...)
	lines = append(lines, store.OriginFrom(facts).Lines()...)
	// The project's own context keys from brain.json (about, stack, ...), in a stable order.
	for _, k := range store.SortedIfaceKeys(facts) {
		if strings.HasPrefix(k, "project_") && k != "project_root" {
			lines = append(lines, fmt.Sprintf("  %-9s %s",
				factLine(strings.TrimPrefix(k, "project_")), factLine(facts[k])))
		}
	}
	return lines
}

// factLine flattens a fact to one line, so a value with a newline in it cannot forge a
// line of its own in the brief.
func factLine(v interface{}) string { return textguard.OneLine(fmt.Sprint(v)) }

// nowLine leads the brief with the wall clock: a session that knows the date but not the
// hour greets people at midnight as if it were morning.
func nowLine(facts map[string]interface{}) string {
	gap := ""
	if v, ok := facts["last_session"]; ok {
		gap = " · last session " + v.(string)
	}
	return fmt.Sprintf("  now       %s (%s %s)%s", facts["now"], facts["weekday"],
		facts["day_part"], gap)
}

func gitLines(facts map[string]interface{}) []string {
	branch, ok := facts["git_branch"].(string)
	if !ok || branch == "" {
		return nil
	}
	state := " · clean"
	if d, _ := facts["git_dirty_files"].(int); d > 0 {
		state = fmt.Sprintf(" · %d uncommitted", d)
	}
	if ahead, _ := facts["git_ahead"].(int); ahead > 0 {
		state += fmt.Sprintf(" · %d unpushed", ahead)
	}
	lines := []string{fmt.Sprintf("  git       %s · %s (%s)%s", factLine(branch),
		factLine(facts["git_commit"]), factLine(facts["git_committed"]), state)}
	if behind, _ := facts["git_behind"].(int); behind > 0 {
		// Telling a session to pull is a wish; telling it that it is behind is a fact it
		// can act on.
		suffix := "s"
		if behind == 1 {
			suffix = ""
		}
		lines = append(lines, fmt.Sprintf("            %d commit%s behind the remote: "+
			"pull before you work, another session has been here", behind, suffix))
	}
	return lines
}

// gitFacts is cheap, best-effort and never fatal: the agent should know what tree it is
// in, and a brief that fails because git is absent is worse than one without a branch.
func (s *Store) gitFacts() map[string]interface{} {
	root := s.Env.ProjectRoot()
	run := func(args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, err := fsguard.GitRead(ctx, root, args...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	}
	if run("rev-parse", "--is-inside-work-tree") != "true" {
		return nil
	}
	dirty := 0
	for _, ln := range strings.Split(run("status", "--porcelain"), "\n") {
		if strings.TrimSpace(ln) != "" {
			dirty++
		}
	}
	facts := map[string]interface{}{
		"branch":      run("rev-parse", "--abbrev-ref", "HEAD"),
		"commit":      run("log", "-1", "--format=%h %s"),
		"committed":   run("log", "-1", "--format=%cr"),
		"dirty_files": dirty,
	}
	// Reads the already-fetched remote ref, so it costs nothing and never blocks.
	if counts := strings.Fields(run("rev-list", "--left-right", "--count", "HEAD...@{u}")); len(counts) == 2 {
		ahead, _ := strconv.Atoi(counts[0])
		behind, _ := strconv.Atoi(counts[1])
		facts["ahead"], facts["behind"] = ahead, behind
	}
	return facts
}

// platformName is `uname -sr`, "Darwin 24.6.0" rather than "darwin arm64": the kernel
// release is the half that matters when a session is deciding whether a box is the one a
// memory was written against. Falls back to the compile-time pair wherever uname is not
// there to ask.
func platformName() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "uname", "-sr").Output(); err == nil {
		if name := strings.TrimSpace(string(out)); name != "" {
			return name
		}
	}
	return runtime.GOOS + " " + runtime.GOARCH
}
