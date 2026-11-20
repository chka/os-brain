package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

func cmdRemember(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.RememberArgs{}
	fs.StringVar(&a.Scope, "scope", "global", "")
	fs.StringVar(&a.Type, "type", "note", "")
	fs.StringVar(&a.Title, "title", "", "")
	body := fs.String("body", "", `text, or - to read stdin`)
	bodyFile := fs.String("body-file", "", "")
	fs.StringVar(&a.Source, "source", "session", "")
	fs.StringVar(&a.SourceDate, "source-date", "", "")
	fs.StringVar(&a.Confidence, "confidence", "", "confirmed | reported | assumed")
	fs.IntVar(&a.Importance, "importance", 3, "1..5, 3=neutral")
	fs.BoolVar(&a.Pin, "pin", false, "")
	fs.BoolVar(&a.Update, "update", false, "allow replacing an existing body")
	fs.Int64Var(&a.Supersedes, "supersedes", 0, "id this memory replaces")
	fs.BoolVar(&a.ForkOK, "fork-ok", false,
		"supersede a memory another correction already superseded")
	fs.BoolVar(&a.Everywhere, "everywhere", false,
		"store it at the org, so EVERY project reads it, not just this one")
	fs.StringVar(&a.Happened, "date", "", "when it happened")
	fs.StringVar(&a.SnoozeUntil, "snooze-until", "", "")
	fs.StringVar(&a.ReviewOn, "review", "", "")
	fs.BoolVar(&a.NoReview, "no-review", false,
		"store no review date, even for a type that defaults to one")
	fs.StringVar(&a.Trust, "trust", brain.DefaultTrust, "stated | imported | external")
	fs.StringVar(&a.ValidFrom, "valid-from", "", "the date this became true")
	fs.StringVar(&a.ValidTo, "valid-to", "", "the date it stops being true")
	fs.BoolVar(&a.AllowDuplicate, "allow-duplicate", false,
		"store it even though another memory in this scope is about the same thing")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "")
	fs.BoolVar(&a.ClaimOK, "claim-ok", false,
		"the text says \"supersedes #N\" as history, not as something this write does")
	refs := multiFlag{}
	fs.Var(&refs, "ref", "repeatable")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	b, err := readBody(*body, *bodyFile)
	if err != nil {
		return err
	}
	a.Body, a.Refs, a.JSON = b, refs, c.json()
	if a.Title == "" {
		if a.Title, err = titleFromWords(pos, "remember"); err != nil {
			return err
		}
	} else if len(pos) > 0 {
		return fmt.Errorf("give the title as words or as --title, not both: %q was not stored",
			strings.Join(pos, " "))
	}
	return c.store(func(s *brain.Store) error { return s.Remember(stdout, a) })
}

func cmdRecall(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.RecallArgs{}
	fs.StringVar(&a.Scope, "scope", "", "")
	fs.IntVar(&a.N, "n", 8, "")
	fs.StringVar(&a.Type, "type", "", "")
	fs.StringVar(&a.Tier, "tier", "", "A B C D N")
	fs.IntVar(&a.Since, "since", 0, "days")
	fs.BoolVar(&a.All, "all", false, "include superseded")
	fs.BoolVar(&a.AllProjects, "all-projects", false, "")
	fs.BoolVar(&a.Strict, "strict", false, "never browse on a miss")
	fs.BoolVar(&a.Browse, "browse", false, "show the scope on a miss")
	fs.BoolVar(&a.NoTouch, "no-touch", false, "do not count this as a use")
	fs.StringVar(&a.OnDate, "on", "", "")
	fs.StringVar(&a.FromDate, "from", "", "")
	fs.StringVar(&a.ToDate, "to", "", "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Query, a.JSON = strings.Join(pos, " "), c.json()
	return c.store(func(s *brain.Store) error { return s.Recall(stdout, a) })
}

func cmdGet(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	all := fs.Bool("all-projects", false, "read another project's memory (never a private one)")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	id, err := argID(pos)
	if err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error { return s.Get(stdout, id, *all, c.json()) })
}

func cmdUpdate(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.UpdateArgs{}
	title := fs.String("title", "", "")
	body := fs.String("body", "", "")
	bodyFile := fs.String("body-file", "", "")
	memType := fs.String("type", "", "")
	scope := fs.String("scope", "", "refile into another scope")
	source := fs.String("source", "", "")
	sourceDate := fs.String("source-date", "", "")
	confidence := fs.String("confidence", "", "")
	importance := fs.Int("importance", 0, "")
	fs.StringVar(&a.Happened, "date", "", "")
	fs.StringVar(&a.SnoozeUntil, "snooze-until", "", "")
	fs.StringVar(&a.ReviewOn, "review", "", "")
	fs.BoolVar(&a.NoReview, "no-review", false, "clear this memory's review date")
	fs.BoolVar(&a.Pin, "pin", false, "")
	fs.BoolVar(&a.Unpin, "unpin", false, "")
	fs.BoolVar(&a.Unsupersede, "unsupersede", false,
		"clear this memory's superseded_by link, leaving both standing")
	fs.BoolVar(&a.Force, "force", false, "allow --body \"\" to empty a body that is not empty")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "")
	updRefs := multiFlag{}
	fs.Var(&updRefs, "ref", "repeatable")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	id, err := argID(pos)
	if err != nil {
		return err
	}
	a.ID, a.JSON, a.Refs = id, c.json(), updRefs
	// Only flags the caller actually passed may change anything: a zero value here
	// is "not given", not "set it to empty".
	seen := flagsSeen(fs)
	if seen["title"] {
		a.Title = title
	}
	if seen["body"] || seen["body-file"] {
		b, err := readBody(*body, *bodyFile)
		if err != nil {
			return err
		}
		a.Body = &b
	}
	if seen["type"] {
		a.Type = memType
	}
	if seen["scope"] {
		a.Scope = scope
	}
	if seen["source"] {
		a.Source = source
	}
	if seen["source-date"] {
		a.SourceDate = sourceDate
	}
	if seen["confidence"] {
		a.Confidence = confidence
	}
	if seen["importance"] {
		a.Importance = importance
	}
	return c.store(func(s *brain.Store) error { return s.Update(stdout, a) })
}

func cmdVerify(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.VerifyArgs{}
	fs.StringVar(&a.Confidence, "confidence", "", "")
	fs.StringVar(&a.Source, "source", "", "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	id, err := argID(pos)
	if err != nil {
		return err
	}
	a.ID, a.JSON = id, c.json()
	return c.store(func(s *brain.Store) error { return s.Verify(stdout, a) })
}

func cmdForget(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.ForgetArgs{}
	fs.StringVar(&a.Reason, "reason", "", "")
	fs.BoolVar(&a.Force, "force", false, "")
	fs.BoolVar(&a.Purge, "purge", false, "destroy it: no archived version kept, every one already held deleted, nothing can restore it")
	fs.BoolVar(&a.All, "all", false, "every memory in this project, pinned included")
	fs.BoolVar(&a.Everything, "everything", false, "--all plus activity, measures, leads and history")
	fs.BoolVar(&a.Apply, "apply", false, "with --all: actually do it (default: report the plan)")
	fs.BoolVar(&a.IMeanIt, "i-mean-it", false, "with --all --apply: confirm the wipe")
	fs.StringVar(&a.Backup, "backup", "", "with --all: where to write the dump taken first")
	// The dump guard refuses a git-tracked or outside path and names these two as the
	// way through. They have to exist here, or the refusal is an instruction with no
	// command behind it.
	fs.BoolVar(&a.AllowOutside, "allow-outside", false, "write the dump outside the project")
	fs.BoolVar(&a.AllowTracked, "allow-tracked", false, "write the dump to a git-tracked path")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if !a.All && !a.Everything {
		id, err := argID(pos)
		if err != nil {
			return err
		}
		a.ID = id
	} else if len(pos) > 0 {
		return fmt.Errorf("forget --all takes no id: it deletes every memory in this " +
			"project. Drop --all to delete one")
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Forget(stdout, a) })
}

func cmdPrefer(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.PreferArgs{}
	fs.StringVar(&a.Why, "why", "", "what prompted it")
	fs.StringVar(&a.About, "about", "", "a person, making this a relationship memory")
	fs.StringVar(&a.Scope, "scope", "global", "")
	fs.BoolVar(&a.Pin, "pin", false, "")
	fs.IntVar(&a.Importance, "importance", 0, "")
	fs.BoolVar(&a.Confirmed, "confirmed", false, "they stated it outright")
	fs.Int64Var(&a.Supersedes, "supersedes", 0, "")
	fs.BoolVar(&a.Update, "update", false, "")
	fs.StringVar(&a.Source, "source", "", "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Text, a.JSON = strings.Join(pos, " "), c.json()
	if a.Text == "" {
		return fmt.Errorf("the rule is required: os-brain prefer \"<the rule in their words>\"")
	}
	return c.store(func(s *brain.Store) error { return s.Prefer(stdout, a) })
}

func cmdHistory(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.HistoryArgs{}
	fs.Int64Var(&a.ID, "id", 0, "only this memory's archived versions")
	fs.IntVar(&a.N, "n", 20, "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	// `history 748`, like `get 748`. Only --id used to be read, so the positional form every
	// sibling takes was dropped without a word and the whole project's history was listed
	// as though asked for.
	if len(pos) > 0 && a.ID == 0 {
		id, perr := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
		if perr != nil || id <= 0 {
			return fmt.Errorf("history takes a memory id, and %s is not one", brain.Repr(pos[0]))
		}
		a.ID = id
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.History(stdout, a) })
}

func cmdRestore(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.RestoreArgs{}
	fs.BoolVar(&a.Force, "force", false, "roll a live memory back to the archived version")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	// history prints every version as h<id> and says to restore it; the prefix is how
	// the id was shown, so it is how it gets pasted back.
	if len(pos) > 0 {
		pos[0] = strings.TrimPrefix(strings.ToLower(pos[0]), "h")
	}
	hid, err := argID(pos)
	if err != nil {
		return err
	}
	a.HID, a.JSON = hid, c.json()
	return c.store(func(s *brain.Store) error { return s.Restore(stdout, a) })
}

func cmdPromote(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.PromoteArgs{}
	fs.StringVar(&a.To, "to", "global", "destination scope")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if a.ID, err = argID(pos); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Promote(stdout, a) })
}

func cmdObject(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Recording an objection is the price of arguing, and it is what stops the third
	// session reconstructing an argument that has already been won twice.
	why := fs.String("why", "", "the reasoning, which is the half worth keeping")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	id, err := argID(pos)
	if err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error {
		return s.ObjectCmd(stdout, id, *why, c.json())
	})
}

func cmdLadder(c *invocation) error {
	stdout := c.stdout
	pos, err := c.parse()
	if err != nil {
		return err
	}
	id, err := argID(pos)
	if err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error {
		return s.LadderCmd(stdout, id, c.json())
	})
}
