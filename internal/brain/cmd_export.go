package brain

import (
	"fmt"
	"io"
)

type ExportArgs struct {
	Out          string
	Force        bool
	AllowOutside bool
	AllowTracked bool
	JSON         bool
}

// exportTables are what `export` writes, in this order: this project's rows and nothing of
// the spine, which every project shares. Tasks travel too: without them an export/import
// round trip lost everything the project still owed.
var exportTables = []string{"memories", "events", "leads", "measures", "measure_limits", "tasks"}

// Export writes this project's rows as a JSON dump, to stdout or to --out.
func (s *Store) Export(w io.Writer, a ExportArgs) error {
	dump := s.newDump()
	for _, t := range exportTables {
		order := " ORDER BY id"
		if t == "measure_limits" {
			order = "" // keyed by (project, key), it has no id column
		}
		rows, err := s.rowsOrdered("SELECT * FROM "+t+" WHERE project=?"+order, s.Env.Project)
		if err != nil {
			return err
		}
		dump[t] = rows
	}
	data, err := marshalOrdered(dump, append(append([]string{}, dumpHeaderOrder...), exportTables...))
	if err != nil {
		return err
	}
	if a.Out == "" {
		return Out(w, a.JSON, string(data), map[string]interface{}{"dump": dump})
	}
	if err := (&Dump{Path: a.Out, Root: s.Env.ProjectRoot(), Marker: BackupMarker, Force: a.Force,
		AllowOutside: a.AllowOutside, AllowTracked: a.AllowTracked}).Write(string(data) + "\n"); err != nil {
		return err
	}
	nm := len(dump["memories"].([]Row))
	ne := len(dump["events"].([]Row))
	nl := len(dump["leads"].([]Row))
	return Out(w, a.JSON, fmt.Sprintf("exported %d memories, %d events, %d leads to %s",
		nm, ne, nl, a.Out),
		map[string]interface{}{"out": a.Out, "memories": nm, "events": ne, "leads": nl})
}
