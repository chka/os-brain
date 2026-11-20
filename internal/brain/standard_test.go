package brain

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Standards are the machine-wide engineering defaults (stack, security, style), kept in the
// shared spine beside the axioms.

// A decision already recorded as a memory can become a standard without retyping it; its
// body becomes the rationale and the project it came from is recorded as the origin.
func TestStandardCanBeLiftedFromAMemory(t *testing.T) {
	s := newTestStore(t)
	remember(t, s, RememberArgs{Title: "Go for new infrastructure tools",
		Body: "one static binary, no runtime to install", Type: "decision"})
	if err := s.Standard(io.Discard, StandardArgs{Confirm: yes, Sub: "add", FromID: 1,
		Category: "stack"}); err != nil {
		t.Fatal(err)
	}
	std := s.Standards()
	if len(std) != 1 || std[0].Text != "Go for new infrastructure tools" {
		t.Fatalf("standards: %+v", std)
	}
	if std[0].Rationale == "" {
		t.Error("the memory body should become the rationale")
	}
	if std[0].Origin != s.Env.Project {
		t.Errorf("origin is %q", std[0].Origin)
	}
}

// The CLI hands the verb over in Text as well as Sub. `list` once arrived as the positional
// text, so the category filter matched nothing.
func TestStandardListFiltersByCategory(t *testing.T) {
	s := newTestStore(t)
	var buf bytes.Buffer
	if err := s.Standard(&buf, StandardArgs{Confirm: yes, Sub: "add", Text: "Go for infra tools",
		Category: "stack", Why: "one binary"}); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := s.Standard(&buf, StandardArgs{Sub: "list", Text: "list", Category: "stack"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Go for infra tools") {
		t.Errorf("--category stack must return the stack standards:\n%s", buf.String())
	}
	buf.Reset()
	if err := s.Standard(&buf, StandardArgs{Sub: "list", Text: "list", Category: "security"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Go for infra tools") {
		t.Error("--category security must not return a stack standard")
	}
}
