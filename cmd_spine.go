package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

func cmdContext(c *invocation) error {
	stdout := c.stdout
	a := brain.ContextArgs{}
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Sub, a.Key, a.Value = sub(pos, "list", "set", "rm")
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Context(stdout, a) })
}

func cmdRegistry(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.RegistryArgs{Kinds: c.cmd == "kind"}
	fs.StringVar(&a.Desc, "desc", "", "what it is for")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Sub, a.Name, _ = sub(pos, "list", "add", "rm")
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Registry(stdout, a) })
}

func cmdAxiom(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.AxiomArgs{}
	fs.StringVar(&a.Why, "why", "", "what it prevents")
	fs.Int64Var(&a.ID, "id", 0, "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if a.Sub, a.Text, err = spineRule("axiom", pos, &a.ID, "list", "add", "rm", "edit"); err != nil {
		return err
	}
	a.JSON = c.json()
	if a.Sub != "list" {
		if a.Confirm, err = spinePerson(c.via, "axiom", "an axiom"); err != nil {
			return err
		}
	}
	return c.store(func(s *brain.Store) error { return s.Axiom(stdout, a) })
}

func cmdStandard(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.StandardArgs{}
	fs.StringVar(&a.Why, "why", "", "")
	fs.StringVar(&a.Category, "category", "general", "stack | security | deploy | ...")
	fs.Int64Var(&a.FromID, "from", 0, "lift the text from a memory")
	fs.Int64Var(&a.ID, "id", 0, "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if a.Sub, a.Text, err = spineRule("standard", pos, &a.ID, "list", "add", "rm"); err != nil {
		return err
	}
	a.JSON = c.json()
	if a.Sub != "list" {
		if a.Confirm, err = spinePerson(c.via, "standard", "a house standard"); err != nil {
			return err
		}
	}
	return c.store(func(s *brain.Store) error { return s.Standard(stdout, a) })
}

func cmdDescribe(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.DescribeArgs{}
	fs.StringVar(&a.Does, "does", "", "")
	fs.StringVar(&a.Provides, "provides", "", "")
	fs.StringVar(&a.Keywords, "keywords", "", "")
	fs.StringVar(&a.Stack, "stack", "", "")
	fs.StringVar(&a.Status, "status", "", "")
	fs.StringVar(&a.Visibility, "visibility", "", "described | listed")
	fs.BoolVar(&a.Sync, "sync", false, "restamp and push to the index")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Text, a.JSON = strings.Join(pos, " "), c.json()
	return c.store(func(s *brain.Store) error { return s.Describe(stdout, a) })
}

func cmdPublish(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.PublishArgs{}
	fs.StringVar(&a.Why, "why", "", "why other projects should see it")
	fs.BoolVar(&a.Retract, "retract", false, "take a published lesson back out of the shared index")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if a.ID, err = argID(pos); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Publish(stdout, a) })
}

func cmdRelease(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.ReleaseArgs{}
	fs.StringVar(&a.Notes, "notes", "", "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("release needs a version, e.g. 26073001")
	}
	a.Version, a.JSON = pos[0], c.json()
	// A release is announced under the system's own banner in every brief, so it is a
	// person's act: never over MCP, and only with a person at the terminal.
	if c.via.transport != viaCLI.transport {
		return errors.New("release is refused over MCP: it is announced in every project's " +
			"brief, a person's decision made at a terminal. Ask the person to run " +
			"`os-brain release` themselves")
	}
	if a.Confirm, err = confirmAtTerminal("publishing a release", "Publish this? Type yes: "); err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error { return s.Release(stdout, a) })
}

func cmdGlobal(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.GlobalArgs{}
	fs.StringVar(&a.Find, "find", "", "search the project index")
	fs.StringVar(&a.Recall, "recall", "", "search published lessons")
	fs.IntVar(&a.N, "n", 10, "")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Global(stdout, a) })
}

// spineRule reads `<verb> <text>` for axiom and standard. The rule is one argument, and rm
// takes the rule's id there when --id was not given.
func spineRule(cmd string, pos []string, id *int64, verbs ...string) (verb, text string, err error) {
	verb, text, extra := sub(pos, verbs[0], verbs[1:]...)
	if err := oneText(cmd, verb, text, extra); err != nil {
		return "", "", err
	}
	if verb == "rm" && *id == 0 {
		if *id, err = argID([]string{text}); err != nil {
			return "", "", err
		}
	}
	return verb, text, nil
}
