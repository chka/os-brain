// Package persontest holds a scripted terminal for tests. It lives apart from package person
// so a production binary never carries a terminal that answers from a script.
package persontest

import (
	"errors"
	"strings"
	"sync"
)

// FakeTTY records what would have been shown and replays scripted answers.
type FakeTTY struct {
	mu      sync.Mutex
	Shown   []string
	Prompts []string
	Inputs  []string
}

func (f *FakeTTY) Show(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Shown = append(f.Shown, s)
	return nil
}

func (f *FakeTTY) ReadLine(prompt string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Prompts = append(f.Prompts, prompt)
	if len(f.Inputs) == 0 {
		return "", errors.New("FakeTTY: no scripted input left")
	}
	in := f.Inputs[0]
	f.Inputs = f.Inputs[1:]
	return in, nil
}

// Transcript is everything shown, in order.
func (f *FakeTTY) Transcript() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.Shown, "")
}
