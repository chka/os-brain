package cli

import (
	"errors"
	"fmt"

	"github.com/chka/os-brain/internal/doctor"
	"github.com/chka/os-brain/internal/dump"
	"github.com/chka/os-brain/internal/overview"
	"github.com/chka/os-brain/internal/project"
	"github.com/chka/os-brain/internal/store"
)

func cmdDoctor(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	doctorFix := fs.Bool("fix", false, "repair the repairable problems")
	if _, err := c.parse(); err != nil {
		return err
	}
	return c.store(func(s *store.Store) error {
		return doctor.New(s).Doctor(stdout, doctor.DoctorArgs{Fix: *doctorFix, JSON: c.json()})
	})
}

func cmdReindex(c *invocation) error {
	stdout := c.stdout
	if _, err := c.parse(); err != nil {
		return err
	}
	return c.store(func(s *store.Store) error {
		if err := s.Reindex(); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "rebuilt the FTS index")
		return nil
	})
}

func cmdBackup(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := dump.BackupArgs{}
	fs.StringVar(&a.Out, "out", "", "")
	fs.BoolVar(&a.AllowOutside, "allow-outside", false, "write outside the project")
	fs.BoolVar(&a.AllowTracked, "allow-tracked", false, "write to a git-tracked path")
	fs.BoolVar(&a.Force, "force", false, "overwrite a file this tool did not generate")
	fs.BoolVar(&a.Auto, "auto", false, "make a safety copy if one is due (what the hook runs)")
	fs.BoolVar(&a.List, "list", false, "the safety copies for this store, newest first")
	fs.BoolVar(&a.Verify, "verify", false, "open the safety copies and prove they read")
	fs.StringVar(&a.Restore, "restore", "", "restore the store from this safety copy")
	fs.BoolVar(&a.Apply, "apply", false, "with --restore: actually do it")
	fs.BoolVar(&a.WholeStore, "whole-store", false, "every project in a shared store, not just this one (a person at a terminal)")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	if a.WholeStore {
		// Every project's memories in cleartext, in this directory: a person's act.
		if c.via.transport != viaCLI.transport {
			return errors.New("backup --whole-store is refused over MCP: it writes every " +
				"project's memories into this directory, a person's decision made at a terminal")
		}
		confirm, err := confirmAtTerminal("backing up every project in the shared store",
			"Write it? Type yes: ")
		if err != nil {
			return err
		}
		a.Confirm = confirm
	}
	return c.store(func(s *store.Store) error { return dump.New(s).Backup(stdout, a) })
}

func cmdExport(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := dump.ExportArgs{}
	fs.StringVar(&a.Out, "out", "", "write here instead of stdout")
	fs.BoolVar(&a.Force, "force", false, "overwrite a file this command did not generate")
	fs.BoolVar(&a.AllowOutside, "allow-outside", false, "")
	fs.BoolVar(&a.AllowTracked, "allow-tracked", false, "")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error { return dump.New(s).Export(stdout, a) })
}

func cmdImport(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := dump.ImportArgs{}
	fs.BoolVar(&a.DryRun, "dry-run", false, "report what would change, write nothing")
	fs.BoolVar(&a.Events, "events", false, "also import the events")
	fs.BoolVar(&a.AllProjects, "all-projects", false, "restore a whole multi-project store")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "")
	fs.BoolVar(&a.IMeanIt, "i-mean-it", false, "with --all-projects: write rows under another project's label")
	fs.BoolVar(&a.Force, "force", false, "accept guardrails from the dump, and let it retract a pinned memory or a guardrail")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("import needs a file to read")
	}
	a.File, a.JSON = pos[0], c.json()
	return c.store(func(s *store.Store) error { return dump.New(s).Import(stdout, a) })
}

func cmdPrune(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := dump.PruneArgs{}
	fs.IntVar(&a.OlderThan, "older-than", 30, "days")
	fs.StringVar(&a.Scope, "scope", "", "")
	fs.StringVar(&a.Tier, "tier", "", "A B C D")
	fs.BoolVar(&a.Superseded, "superseded", false, "prune retracted memories")
	fs.BoolVar(&a.Apply, "apply", false, "actually delete (dry run without it)")
	fs.BoolVar(&a.Yes, "yes", false, "accept a large deletion")
	fs.BoolVar(&a.IMeanIt, "i-mean-it", false, "required for tier A/B and --all-projects")
	fs.IntVar(&a.MaxDelete, "max-delete", 25, "")
	fs.StringVar(&a.Backup, "backup", "", "where to dump before deleting")
	fs.BoolVar(&a.Force, "force", false, "overwrite a --backup file this tool did not generate")
	fs.BoolVar(&a.AllowOutside, "allow-outside", false, "write the dump outside the project")
	fs.BoolVar(&a.AllowTracked, "allow-tracked", false, "write the dump to a git-tracked path")
	fs.BoolVar(&a.AllProjects, "all-projects", false, "")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error { return dump.New(s).Prune(stdout, a) })
}

func cmdSnapshot(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := overview.SnapshotArgs{}
	fs.StringVar(&a.Out, "out", "", "write here instead of stdout")
	fs.IntVar(&a.PerScope, "per-scope", 25, "")
	fs.BoolVar(&a.Force, "force", false, "")
	fs.BoolVar(&a.AllowOutside, "allow-outside", false, "")
	fs.BoolVar(&a.AllowTracked, "allow-tracked", false, "")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error { return overview.New(s).Snapshot(stdout, a) })
}

func cmdVacuum(c *invocation) error {
	stdout := c.stdout
	if _, err := c.parse(); err != nil {
		return err
	}
	return c.store(func(s *store.Store) error { return doctor.New(s).Vacuum(stdout, c.json()) })
}

func cmdPrivate(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// A declaration that only takes effect the next time something unrelated runs is
	// not a declaration, so this writes brain.json AND the shared index together.
	a := project.PrivateArgs{}
	fs.BoolVar(&a.Off, "off", false, "make it readable by the other projects again")
	fs.BoolVar(&a.Yes, "yes", false, "with --off: expose every memory, including the private ones")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error { return project.New(s).Private(stdout, a) })
}

func cmdShare(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// The move `absorb` plus a hand edit only pretended to do: every table with a
	// project column, the ids other rows point at remapped, and the index told.
	a := project.ShareArgs{}
	fs.BoolVar(&a.Off, "off", false, "move back out to this project's own database")
	fs.BoolVar(&a.Apply, "apply", false, "do it (without this, it reports and stops)")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *store.Store) error { return project.New(s).Share(stdout, a) })
}

func cmdRelabel(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := project.RelabelArgs{}
	fs.IntVar(&a.Expect, "expect", 0, "how many memories you expect to move")
	fs.BoolVar(&a.IMeanIt, "i-mean-it", false, "the label is not in the index and they are yours")
	fs.BoolVar(&a.Apply, "apply", false, "")
	claim := fs.Bool("claim", false, "bind this project's name to this directory, after a move (a person at a terminal)")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if *claim {
		// The binding is what keeps a cloned repository out of this project's memories,
		// so moving it is a person's act: never over MCP, never without a terminal.
		if len(pos) > 0 {
			return fmt.Errorf("relabel --claim takes no label: it binds this project's own name here")
		}
		if c.via.transport != viaCLI.transport {
			return errors.New("relabel --claim is refused over MCP: it hands a project's " +
				"memories to a directory, a person's decision made at a terminal")
		}
		confirm, err := confirmAtTerminal("claiming a project's name for this directory",
			"Claim it? Type yes: ")
		if err != nil {
			return err
		}
		c.env.Claim = true
		return c.store(func(s *store.Store) error { return s.Claim(stdout, confirm, c.json()) })
	}
	if len(pos) == 0 {
		return fmt.Errorf("relabel needs the label to claim")
	}
	a.Old, a.JSON = pos[0], c.json()
	return c.store(func(s *store.Store) error { return project.New(s).Relabel(stdout, a) })
}

func cmdAbsorb(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := project.AbsorbArgs{}
	fs.StringVar(&a.Project, "project", "", "file the rows under this project")
	fs.BoolVar(&a.ForceLabel, "force-label", false, "accept a label mismatch")
	fs.BoolVar(&a.DryRun, "dry-run", false, "")
	fs.BoolVar(&a.NoEvents, "no-events", false, "")
	fs.BoolVar(&a.IMeanIt, "i-mean-it", false, "with --project naming another project: file the rows there")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "store it anyway; it is a false positive")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("absorb needs a brain.db to read")
	}
	a.File, a.JSON = pos[0], c.json()
	return c.store(func(s *store.Store) error { return project.New(s).Absorb(stdout, a) })
}
