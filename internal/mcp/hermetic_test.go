package mcp

import (
	"testing"

	"github.com/chka/os-brain/internal/clitest"
)

// TestMain pins every path to a sandbox before any test runs and reports a run that wrote
// into the real store anyway (see clitest.Main).
func TestMain(m *testing.M) { clitest.Main(m) }
