package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/chka/os-brain/internal/brain"
)

func cmdLog(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.LogArgs{}
	fs.StringVar(&a.Scope, "scope", "global", "")
	fs.StringVar(&a.Kind, "kind", "note", "")
	fs.StringVar(&a.Actor, "actor", "claude", "")
	fs.StringVar(&a.Summary, "summary", "", "")
	fs.StringVar(&a.Ref, "ref", "", "")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "")
	date := fs.String("date", "", "the day it happened, when that was not today")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	day, err := brain.ParseDate(*date, "--date")
	if err != nil {
		return err
	}
	if a.TS, err = brain.DayToTS(day); err != nil {
		return err
	}
	if a.Summary == "" {
		a.Summary = strings.Join(pos, " ")
	}
	if a.Summary == "" {
		return fmt.Errorf("--summary is required")
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Log(stdout, a) })
}

func cmdEvents(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.EventsArgs{}
	fs.StringVar(&a.Scope, "scope", "", "")
	fs.StringVar(&a.Kind, "kind", "", "")
	fs.IntVar(&a.Since, "since", 0, "days")
	fs.IntVar(&a.N, "n", 20, "")
	fs.BoolVar(&a.AllProjects, "all-projects", false, "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Query, a.JSON = strings.Join(pos, " "), c.json()
	return c.store(func(s *brain.Store) error { return s.Events(stdout, a) })
}

func cmdCapture(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// The thesis: what the user said is stored BEFORE the turn is answered. Normally
	// run by the hook rather than by hand, and it never fails the caller's turn.
	a := brain.CaptureArgs{}
	fs.StringVar(&a.Session, "session", "", "the session that heard it")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Text = strings.Join(pos, " ")
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.CaptureCmd(stdout, a) })
}

func cmdUtterances(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.UtterancesArgs{}
	fs.IntVar(&a.N, "n", 20, "")
	fs.StringVar(&a.Class, "class", "", "command | taste | directive | exhibit")
	fs.BoolVar(&a.Unclassified, "unclassified", false, "only what could not be classified")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Utterances(stdout, a) })
}

func cmdWhoami(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.WhoamiArgs{}
	fs.StringVar(&a.Observe, "observe", "", "record a trait: key=value")
	fs.StringVar(&a.Evidence, "evidence", "", "what shows it; required with --observe")
	fs.StringVar(&a.Forget, "forget", "", "drop a trait by key")
	if _, err := c.parse(); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Whoami(stdout, a) })
}

func cmdCoverage(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// What this project knows ABOUT, rather than what it knows. Retrieval assumes a
	// query, and a query assumes the searcher already suspects the answer exists.
	near := fs.String("near", "", "what is held near this, for a search that found nothing")
	if _, err := c.parse(); err != nil {
		return err
	}
	return c.store(func(s *brain.Store) error {
		return s.CoverageCmd(stdout, *near, c.json())
	})
}

func cmdTask(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	// Delivery is pull, on hooks that already run. Nothing here schedules anything:
	// due tasks are shown by hook-recall, wakeup and hook-stop.
	a := brain.TaskArgs{}
	fs.StringVar(&a.Title, "title", "", "with set: the new title; with add: instead of the words")
	fs.StringVar(&a.Body, "body", "", "")
	fs.StringVar(&a.Owner, "owner", "", "user | agent")
	fs.StringVar(&a.Scope, "scope", "", "")
	fs.StringVar(&a.When, "when", "", `"in 3 hours", "tomorrow 9am", "every monday 9am", "daily 17:00"; with set, "none" clears it`)
	fs.StringVar(&a.Until, "until", "", "snooze this occurrence until")
	fs.StringVar(&a.State, "state", "", "with list: open | waiting | done | dropped")
	fs.BoolVar(&a.Notify, "notify", false, "")
	// GuardText's refusal names this flag, so it has to exist here. Three commands
	// called the guard without registering it, which made a false positive on any of
	// them permanently unstoreable: the error told you to pass a flag the command
	// would then reject.
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "store it anyway; it is a false positive")
	fs.BoolVar(&a.All, "all", false, "closed ones too")
	fs.StringVar(&a.Out, "out", "", "with todo: where to write it")
	fs.BoolVar(&a.AllowOutside, "allow-outside", false, "with todo: write outside the project")
	fs.BoolVar(&a.AllowTracked, "allow-tracked", false, "with todo: write to a git-tracked path")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	a.Sub = "list"
	if len(pos) > 0 {
		a.Sub, pos = pos[0], pos[1:]
	}
	// Refused before anything runs: an unknown subcommand or a flag the subcommand would
	// ignore used to print the list and exit 0, so a typo looked like success.
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "json" {
			a.Fields = append(a.Fields, f.Name)
		}
	})
	if err := brain.CheckTaskArgs(a.Sub, a.Fields); err != nil {
		if _, numeric := strconv.ParseInt(strings.TrimPrefix(a.Sub, "#"), 10, 64); numeric == nil {
			return fmt.Errorf("%v\n  a task id goes after the subcommand: task set %s --body ...",
				err, a.Sub)
		}
		return err
	}
	if err := taskPositionals(&a, pos); err != nil {
		return err
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Task(stdout, a) })
}

// taskPositionals reads what follows the subcommand: the title for add, one task id for the
// subcommands that act on a task, and nothing for the rest.
func taskPositionals(a *brain.TaskArgs, pos []string) error {
	switch {
	case a.Sub == "add":
		if a.Title != "" && len(pos) > 0 {
			return fmt.Errorf("give the title as words or as --title, not both")
		}
		if a.Title == "" {
			title, err := titleFromWords(pos, "task add")
			if err != nil {
				return err
			}
			a.Title = title
		}
	case brain.TaskTakesID(a.Sub):
		if len(pos) != 1 {
			return fmt.Errorf("task %s takes exactly one task id, got %d arguments", a.Sub, len(pos))
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(pos[0], "#"), 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("%q is not a task id", pos[0])
		}
		a.ID = id
	case len(pos) > 0:
		return fmt.Errorf("task %s takes no arguments, got %q", a.Sub, strings.Join(pos, " "))
	}
	return nil
}

func cmdMeasure(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.MeasureArgs{}
	fs.StringVar(&a.Label, "label", "", "")
	fs.StringVar(&a.Unit, "unit", "", "")
	fs.StringVar(&a.Scope, "scope", "", "")
	fs.StringVar(&a.Date, "date", "", "when it was taken")
	fs.StringVar(&a.Source, "source", "session", "")
	fs.StringVar(&a.Confidence, "confidence", "", "")
	fs.StringVar(&a.Ref, "ref", "", "")
	fs.StringVar(&a.Note, "note", "", "")
	fs.StringVar(&a.FromDate, "from", "", "")
	fs.StringVar(&a.ToDate, "to", "", "")
	fs.IntVar(&a.N, "n", 0, "")
	low := fs.Float64("low", 0, "band floor")
	high := fs.Float64("high", 0, "band ceiling")
	fs.StringVar(&a.Better, "better", "", "up | down")
	fs.StringVar(&a.Why, "why", "", "")
	fs.BoolVar(&a.AllowSecret, "allow-secret", false, "store it anyway; it is a false positive")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	seen := flagsSeen(fs)
	if seen["low"] {
		a.Low = low
	}
	if seen["high"] {
		a.High = high
	}
	a.Sub = "list"
	if len(pos) > 0 {
		a.Sub, pos = pos[0], pos[1:]
	}
	if len(pos) > 0 {
		a.Key = pos[0]
	}
	if a.Sub == "add" && len(pos) > 1 {
		v, err := strconv.ParseFloat(pos[1], 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", pos[1])
		}
		a.Value = v
	}
	if a.Sub == "forget" || a.Sub == "relabel" {
		if a.ID, err = argID(pos); err != nil {
			return err
		}
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Measure(stdout, a) })
}

func cmdLead(c *invocation) error {
	stdout := c.stdout
	fs := c.fs
	a := brain.LeadArgs{}
	fs.StringVar(&a.Name, "name", "", "")
	fs.StringVar(&a.Company, "company", "", "")
	fs.StringVar(&a.Channel, "channel", "", "")
	fs.IntVar(&a.N, "n", 50, "")
	fs.StringVar(&a.AppendNote, "append-note", "", "timestamped line onto the notes")
	role := fs.String("role", "", "")
	url := fs.String("url", "", "")
	email := fs.String("email", "", "")
	segment := fs.String("segment", "", "")
	status := fs.String("status", "", strings.Join(brain.LeadStatuses, " | "))
	tier := fs.String("tier", "", "")
	valueEst := fs.Int64("value-est", 0, "")
	nextAction := fs.String("next-action", "", "")
	nextDate := fs.String("next-date", "", "")
	notes := fs.String("notes", "", "")
	pos, err := c.parse()
	if err != nil {
		return err
	}
	seen := flagsSeen(fs)
	for _, f := range []struct {
		name string
		dst  **string
		src  *string
	}{
		{"role", &a.Role, role}, {"url", &a.URL, url}, {"email", &a.Email, email},
		{"segment", &a.Segment, segment}, {"status", &a.Status, status},
		{"tier", &a.Tier, tier}, {"next-action", &a.NextAction, nextAction},
		{"next-date", &a.NextDate, nextDate}, {"notes", &a.Notes, notes},
	} {
		if seen[f.name] {
			*f.dst = f.src
		}
	}
	if seen["value-est"] {
		a.ValueEst = valueEst
	}
	a.Sub = "list"
	if len(pos) > 0 {
		a.Sub, pos = pos[0], pos[1:]
	}
	switch a.Sub {
	case "add":
		if a.Name == "" {
			a.Name = strings.Join(pos, " ")
		}
	case "show", "update":
		if a.ID, err = argID(pos); err != nil {
			return err
		}
	}
	a.JSON = c.json()
	return c.store(func(s *brain.Store) error { return s.Lead(stdout, a) })
}
