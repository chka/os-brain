package main

import (
	"github.com/chka/os-brain/internal/brain"
)

func cmdBrief(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.BriefArgs{}
	fs.IntVar(&a.MaxPinned, "max-pinned", brain.BriefMaxPinned, "")
	fs.IntVar(&a.MaxPrefs, "max-prefs", brain.BriefMaxPrefs, "")
	fs.IntVar(&a.NewFor, "new-for", 1, "days of 'new since' to always show")
	fs.IntVar(&a.NewProjectBelow, "new-project-below", 15,
		"show house standards in full below this many memories")
	fs.BoolVar(&a.NoAxioms, "no-axioms", false, "")
	fs.BoolVar(&a.NoCursor, "no-cursor", false, "do not move the last-brief marker")
	fs.IntVar(&a.MaxBytes, "max-bytes", 0,
		"trim from the least important end to fit; 0 for all of it (the default here: "+
			"brief is requested, wakeup is injected)")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.MaxBytesSet = flagSet(fs, "max-bytes")
	a.NewForSet = flagSet(fs, "new-for")
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Brief(stdout, a) })
}

func cmdOpen(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Three names for one command, and the aliases are not indulgence: the failure
	// this answers is a session reaching for grep because it did not think the brain
	// had an answer, and a session that guesses the wrong verb learns exactly that.
	a := brain.OpenArgs{}
	fs.IntVar(&a.N, "n", 6, "how many in-flight memories and events")
	fs.BoolVar(&a.Full, "full", false, "task bodies too, and longer memory extracts")
	fs.BoolVar(&a.Compact, "compact", false, "the hook's form: shortest extracts")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Open(stdout, a) })
}

func cmdMap(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	mapTop := fs.Int("top", 3, "top memories to show per scope")
	if _, err := c.parse(); err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error {
		return s.Map(stdout, brain.MapArgs{Top: *mapTop, JSON: c.json()})
	})
}

func cmdStats(c *invocation) error {
	stdout := c.stdout
	_, err := c.parse()
	if err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error { return s.Stats(stdout, c.json()) })
}

func cmdReview(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.ReviewArgs{}
	fs.IntVar(&a.N, "n", 8, "")
	fs.IntVar(&a.StaleDays, "stale-days", 180, "tier A/B unverified for this long is flagged")
	fs.BoolVar(&a.Duplicates, "duplicates", false, "run the pairwise scan anyway")
	fs.IntVar(&a.MaxPairwise, "max-pairwise", 600, "")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Review(stdout, a) })
}

func cmdBootstrap(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.BootstrapArgs{}
	fs.BoolVar(&a.Done, "done", false, "record that the pass is finished")
	fs.BoolVar(&a.Again, "again", false, "go through the transcripts once more")
	fs.BoolVar(&a.IncludeMoved, "include-moved", false,
		"also read directories matching this project's name but not its path")
	bootRead := multiFlag{}
	fs.Var(&bootRead, "read", "repeatable: mark one transcript read")
	fs.IntVar(&a.N, "n", 12, "")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.Read = bootRead
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Bootstrap(stdout, a) })
}

func cmdEnv(c *invocation) error {
	stdout := c.stdout
	_, err := c.parse()
	if err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error { return s.EnvBlock(stdout, c.json()) })
}
