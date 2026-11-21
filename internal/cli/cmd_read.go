package cli

import (
	"github.com/chka/os-brain/internal/bootstrap"
	"github.com/chka/os-brain/internal/brief"
	"github.com/chka/os-brain/internal/curate"
	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/overview"
	"github.com/chka/os-brain/internal/store"
)

func cmdBrief(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brief.BriefArgs{}
	fs.IntVar(&a.MaxPinned, "max-pinned", brief.BriefMaxPinned, "")
	fs.IntVar(&a.MaxPrefs, "max-prefs", brief.BriefMaxPrefs, "")
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
	return c.store(func(s *store.Store) error { return brief.New(s).Brief(stdout, a) })
}

func cmdOpen(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Three names for one command, and the aliases are not indulgence: the failure
	// this answers is a session reaching for grep because it did not think the brain
	// had an answer, and a session that guesses the wrong verb learns exactly that.
	a := brief.OpenArgs{}
	fs.IntVar(&a.N, "n", 6, "how many in-flight memories and events")
	fs.BoolVar(&a.Full, "full", false, "task bodies too, and longer memory extracts")
	fs.BoolVar(&a.Compact, "compact", false, "the hook's form: shortest extracts")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error { return brief.New(s).Open(stdout, a) })
}

func cmdMap(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	mapTop := fs.Int("top", 3, "top memories to show per scope")
	if _, err := c.parse(); err != nil {
		return err
	}
	return c.store(func(s *store.Store) error {
		return overview.New(s).Map(stdout, overview.MapArgs{Top: *mapTop, JSON: c.json()})
	})
}

func cmdStats(c *invocation) error {
	stdout := c.stdout
	_, err := c.parse()
	if err != nil {
		return err
	}
	return c.store(func(s *store.Store) error { return memory.New(s).Stats(stdout, c.json()) })
}

func cmdReview(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := curate.ReviewArgs{}
	fs.IntVar(&a.N, "n", 8, "")
	fs.IntVar(&a.StaleDays, "stale-days", 180, "tier A/B unverified for this long is flagged")
	fs.BoolVar(&a.Duplicates, "duplicates", false, "run the pairwise scan anyway")
	fs.IntVar(&a.MaxPairwise, "max-pairwise", 600, "")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error { return curate.New(s).Review(stdout, a) })
}

func cmdBootstrap(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := bootstrap.BootstrapArgs{}
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
	return c.store(func(s *store.Store) error { return bootstrap.New(s).Bootstrap(stdout, a) })
}

func cmdEnv(c *invocation) error {
	stdout := c.stdout
	_, err := c.parse()
	if err != nil {
		return err
	}
	return c.store(func(s *store.Store) error { return brief.New(s).EnvBlock(stdout, c.json()) })
}
