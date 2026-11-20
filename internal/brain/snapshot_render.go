package brain

import (
	"fmt"
	"strings"
)

// SnapshotRow is one memory as the snapshot prints it. The renderer takes plain data
// rather than a database so that every command producing a snapshot shares one copy of it:
// a second copy of a renderer is how one view drifts from another without anybody noticing.
type SnapshotRow struct {
	ID         int64
	Title      string
	Scope      string
	MemType    string
	Confidence string
	Body       string
}

type SnapshotScope struct {
	Name string
	Rows []SnapshotRow
}

type SnapshotDoc struct {
	Project    string
	Generated  int64
	Guardrails []SnapshotRow
	Pinned     []SnapshotRow
	Scopes     []SnapshotScope
}

// RenderSnapshot writes the generated markdown mirror: a do-not-edit header, then the
// guardrails, the pinned memories and every scope, each as a section of entries.
func RenderSnapshot(d SnapshotDoc) string {
	lines := []string{
		fmt.Sprintf("<!-- %s ON %s. DO NOT EDIT.", SnapshotMarker, ISO(d.Generated)),
		"     Edit the brain (os-brain remember / update / forget) and regenerate. -->",
		fmt.Sprintf("# %s — os-brain snapshot", OneLine(d.Project)), "",
	}
	block := func(title string, rows []SnapshotRow) {
		if len(rows) == 0 {
			return
		}
		lines = append(lines, "## "+OneLine(title), "")
		for _, r := range rows {
			conf := ""
			if r.Confidence != DefaultConfidence && r.Confidence != "" {
				conf = " `" + r.Confidence + "`"
			}
			lines = append(lines, fmt.Sprintf("- **%s** `#%d` `%s/%s`%s",
				OneLine(r.Title), r.ID, OneLine(r.Scope), r.MemType, conf))
			if r.Body != "" {
				for _, ln := range strings.Split(BodyText(r.Body), "\n") {
					lines = append(lines, "  "+ln)
				}
			}
		}
		lines = append(lines, "")
	}
	block("Guardrails", d.Guardrails)
	block("Pinned", d.Pinned)
	for _, sc := range d.Scopes {
		block(sc.Name, sc.Rows)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), " \n\t") + "\n"
}

// SnapshotRowsOf converts memories into the rows RenderSnapshot takes.
func SnapshotRowsOf(rows []*Memory) []SnapshotRow {
	out := make([]SnapshotRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, SnapshotRow{ID: r.ID, Title: r.Title, Scope: r.Scope,
			MemType: r.MemType, Confidence: r.Confidence, Body: r.Body})
	}
	return out
}
