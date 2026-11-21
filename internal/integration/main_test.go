package integration_test

import (
	"testing"

	"github.com/chka/os-brain/internal/testguard"
)

// Hermeticity is a property of the RUN, not a habit of each test: testguard.Run pins every
// machine-level path to a sandbox for the whole package and proves afterwards that no real
// spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }
