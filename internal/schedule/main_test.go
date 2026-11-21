package schedule

import (
	"testing"

	"github.com/chka/os-brain/internal/testguard"
)

// Every package with tests runs under the same floor: paths pinned to a sandbox, and a
// check afterwards that no real spine gained a project.
func TestMain(m *testing.M) { testguard.Run(m) }
