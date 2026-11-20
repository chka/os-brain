package main

import "testing"

// Every command that reads an id acts on exactly one. `forget 30 31` once deleted #30 and
// said nothing about #31, so the person believed both were gone. A second id must stop the
// command before anything runs and a single id, with or without its "#", still parses.
func TestASecondIDIsRefusedBeforeAnythingRuns(t *testing.T) {
	if _, err := argID([]string{"30", "31"}); err == nil {
		t.Fatal("a second id was ignored rather than refused")
	}
	if id, err := argID([]string{"#30"}); err != nil || id != 30 {
		t.Fatalf("one id: %d %v", id, err)
	}
}
