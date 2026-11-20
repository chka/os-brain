//go:build sweep

package person

// This file exists only in a binary built with `-tags sweep`, which is what tools/sweep.py
// runs. The sweep drives person-only commands as real processes, and those read a controlling
// terminal no subprocess has. OS_BRAIN_SWEEP_TTY names a directory whose tty.in is typed, one
// line per prompt, and whose tty.out receives what a person would have seen.

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const sweepBuild = true

const sweepTTYEnv = "OS_BRAIN_SWEEP_TTY"

type sweepTTY struct {
	mu     sync.Mutex
	dir    string
	inputs []string
}

func (t *sweepTTY) appendTo(name, s string) error {
	f, err := os.OpenFile(filepath.Join(t.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(s)
	return err
}

func (t *sweepTTY) Show(s string) error { return t.appendTo("tty.out", s) }

func (t *sweepTTY) ReadLine(p string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.appendTo("tty.out", p)
	if len(t.inputs) == 0 {
		return "", errors.New("sweep tty: no input left for prompt " + p)
	}
	in := t.inputs[0]
	t.inputs = t.inputs[1:]
	_ = t.appendTo("tty.out", "\n")
	return in, nil
}

func init() {
	dir := os.Getenv(sweepTTYEnv)
	if dir == "" {
		return
	}
	t := &sweepTTY{dir: dir}
	if f, err := os.Open(filepath.Join(dir, "tty.in")); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			t.inputs = append(t.inputs, strings.TrimRight(sc.Text(), "\r"))
		}
		f.Close()
	}
	openDevTTY = func() (TTY, error) { return t, nil }
}
