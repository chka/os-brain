package layers

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
)

const module = "github.com/chka/os-brain/"

// leaves are the small packages with no knowledge of the store. Every package above them may
// use them, so they are not repeated in each row below.
var leaves = []string{
	"internal/termtext", "internal/gitsafe",
	"internal/pyfmt", "internal/diag", "internal/lexicon", "internal/textguard",
	"internal/timefmt", "internal/schedule", "internal/fsguard",
}

// allowed is the layering: each package and the internal packages it may import directly.
// A package that is in no row and not unrestricted fails the test until it is given one.
var allowed = map[string][]string{
	"internal/termtext":  {},
	"internal/gitsafe":   {},
	"internal/netguard":  {},
	"internal/testguard": {},
	"internal/person":    {"internal/termtext"},
	"internal/layers":    {},

	"internal/pyfmt":     {"internal/termtext"},
	"internal/diag":      {},
	"internal/lexicon":   {},
	"internal/textguard": {"internal/pyfmt", "internal/termtext"},
	"internal/timefmt":   {"internal/textguard", "internal/pyfmt"},
	"internal/schedule":  {"internal/timefmt", "internal/pyfmt"},
	"internal/fsguard":   {"internal/diag", "internal/textguard", "internal/gitsafe"},

	"internal/store":    {"leaves"},
	"internal/testutil": {"leaves", "internal/store"},

	"internal/measure":   {"leaves", "internal/store"},
	"internal/lead":      {"leaves", "internal/store"},
	"internal/spine":     {"leaves", "internal/store"},
	"internal/bootstrap": {"leaves", "internal/store"},
	"internal/dm":        {"leaves", "internal/store"},
	"internal/safety":    {"leaves", "internal/store"},
	"internal/dump":      {"leaves", "internal/store", "internal/measure", "internal/safety"},
	"internal/memory":    {"leaves", "internal/store", "internal/dump", "internal/measure", "internal/lead"},
	"internal/tasks":     {"leaves", "internal/store", "internal/memory"},
	"internal/recall":    {"leaves", "internal/store", "internal/measure"},
	"internal/doctor":    {"leaves", "internal/store", "internal/memory", "internal/measure", "internal/dump"},
	"internal/install":   {"leaves", "internal/store", "internal/dump", "internal/bootstrap"},
	"internal/curate":    {"leaves", "internal/store"},
	"internal/project":   {"leaves", "internal/store", "internal/dump", "internal/install"},
	"internal/overview":  {"leaves", "internal/store", "internal/recall", "internal/measure"},
	"internal/brief": {"leaves", "internal/store", "internal/recall", "internal/spine",
		"internal/tasks", "internal/memory", "internal/lead", "internal/measure", "internal/safety",
		"internal/install", "internal/bootstrap", "internal/curate"},
	"internal/hook": {"leaves", "internal/store", "internal/brief", "internal/recall",
		"internal/memory", "internal/tasks"},
}

// unrestricted packages sit on top and may import anything: the commands and transports,
// the test floors, and internal/integration, whose tests drive several features at once.
var unrestricted = map[string]bool{
	"cmd/os-brain":               true,
	"internal/cli":               true,
	"internal/mcp":               true,
	"internal/webui":             true,
	"internal/channel":           true,
	"internal/clitest":           true,
	"internal/person/persontest": true,
	"internal/integration":       true,
}

func TestEveryPackageImportsOnlyWhatItsLayerAllows(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}}",
		module+"...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	var problems []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		pkg := strings.TrimPrefix(fields[0], module)
		if unrestricted[pkg] {
			continue
		}
		rule, ok := allowed[pkg]
		if !ok {
			problems = append(problems, pkg+": not in the layering table; give it a row")
			continue
		}
		may := map[string]bool{}
		for _, a := range rule {
			if a == "leaves" {
				for _, l := range leaves {
					may[l] = true
				}
				continue
			}
			may[a] = true
		}
		for _, imp := range fields[1:] {
			if !strings.HasPrefix(imp, module) {
				continue
			}
			dep := strings.TrimPrefix(imp, module)
			if dep != pkg && !may[dep] {
				problems = append(problems, pkg+" imports "+dep)
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}
