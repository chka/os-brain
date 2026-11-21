package hook

import (
	"testing"

	"github.com/chka/os-brain/internal/memory"
	"github.com/chka/os-brain/internal/timefmt"
)

// Captured prompts are what the user typed, kept so a memory can cite the words it was
// distilled from. Every prompt used to be kept verbatim forever. They now expire after
// UtteranceDaysDefault days (or utterance_days in brain.json), except the prompt a memory
// cites, and the sweep touches only this project's rows.
func TestCapturedPromptsExpire(t *testing.T) {
	s := newTestStore(t)
	old := timefmt.Now() - int64(UtteranceDaysDefault+1)*86400
	for _, txt := range []string{"stale prompt", "cited prompt", "other project prompt"} {
		project := s.Env.Project
		if txt == "other project prompt" {
			project = "elsewhere"
		}
		if _, err := s.DB.Exec("INSERT INTO utterances(project, ts, text, class, class_why, lang, session) "+
			"VALUES(?,?,?,'other','','en','')", project, old, txt); err != nil {
			t.Fatal(err)
		}
	}
	remember(t, s, memory.RememberArgs{Type: "decision", Title: "a memory distilled from a prompt", Body: "x"})
	if _, err := s.DB.Exec("UPDATE memories SET from_utterance=(SELECT id FROM utterances WHERE text='cited prompt')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture("a fresh prompt today", ""); err != nil {
		t.Fatal(err)
	}
	left := map[string]bool{}
	rows, _ := s.DB.Query("SELECT text FROM utterances")
	for rows.Next() {
		var x string
		_ = rows.Scan(&x)
		left[x] = true
	}
	rows.Close()
	if left["stale prompt"] || !left["cited prompt"] || !left["a fresh prompt today"] || !left["other project prompt"] {
		t.Errorf("retention kept the wrong rows: %v", left)
	}

	s.Env.Config.UtteranceDays = 90
	if _, err := s.DB.Exec("INSERT INTO utterances(project, ts, text, class, class_why, lang, session) "+
		"VALUES(?,?,'kept by config','other','','en','')", s.Env.Project, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Capture("another fresh prompt", ""); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.DB.QueryRow("SELECT COUNT(*) FROM utterances WHERE text='kept by config'").Scan(&n)
	if n != 1 {
		t.Error("utterance_days in brain.json was not honoured")
	}
}
