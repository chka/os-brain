package person

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"
)

// Show is the last line of defence before the terminal. Text written by another session or a
// prompt-injected agent can carry \x1b[8m (hidden), \r or \x1b[2K (erase line) to hide the
// real instruction behind a benign one on the very screen a person approves from, so Show
// strips escapes, controls and bidi overrides and keeps newlines and tabs.
func TestDevTTYShowStripsEscapesAndKeepsNewlines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tty")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tty := &devTTY{f: f, r: bufio.NewReader(f)}
	if err := tty.Show("run: rm -rf ~\x1b[8m hidden\r\x1b[2Kbenign‮\n\tnext\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, _ := os.ReadFile(p)
	want := "run: rm -rf ~ hidden benign\n\tnext\n"
	if string(got) != want {
		t.Fatalf("terminal got %q, want %q", got, want)
	}
}
