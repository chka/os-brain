package person

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/chka/os-brain/internal/termtext"
)

// TTY is how a confirmation reaches a person. Agents run this binary through a shell tool
// whose stdin and stdout belong to the agent, so a "type yes" prompt on stdout would be
// answered by the agent itself. /dev/tty is an accident guard, not a security boundary: it
// keeps the question out of the pipes an agent reads, and refuses when there is no terminal.
type TTY interface {
	Show(s string) error
	ReadLine(prompt string) (string, error)
}

type devTTY struct {
	f *os.File
	r *bufio.Reader
}

var ErrNoTTY = errors.New("this needs a controlling terminal; run it yourself in a terminal, not through an agent or a pipe")

// openDevTTY is replaced by a scripted terminal in a sweep build (sweep_on.go) and by a
// test through ReplaceTerminal.
var openDevTTY = func() (TTY, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", ErrNoTTY, err)
	}
	return &devTTY{f: f, r: bufio.NewReader(f)}, nil
}

// Show cleans whatever it is given, even text its callers already cleaned: what a person is
// asked to approve reaches it through many paths, and one missed path is an escape sequence
// that repaints the question.
func (t *devTTY) Show(s string) error {
	_, err := t.f.WriteString(termtext.CleanText(s, true, 0))
	return err
}

func (t *devTTY) ReadLine(prompt string) (string, error) {
	if err := t.Show(prompt); err != nil {
		return "", err
	}
	line, err := t.r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
