package project

import (
	"io"
	"testing"
)

// Declaring '*' would match every scope and silently switch the undeclared-scope check off.
func TestRegistryRefusesAPatternThatMatchesEverything(t *testing.T) {
	s := newTestStore(t)
	if err := s.Registry(io.Discard, RegistryArgs{Sub: "add", Name: "*"}); err == nil {
		t.Error("declared '*', which disables the undeclared-scope check entirely")
	}
}
