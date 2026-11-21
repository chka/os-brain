package dump

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/chka/os-brain/internal/fsguard"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/timefmt"
)

// The JSON dump format, both ways: writing it (export, backup, prune's and wipe's safety
// dumps, doctor's pre-repair dump) and reading it back (import).
//
// The layout is fixed byte for byte, not just as JSON: dumps are read by people and diffed
// against each other, and a dump written by ns-brain must read the same here. A reshuffled
// file reads as a changed file.

// BackupMarker identifies this tool's own JSON output, so a re-run can replace it while a
// file somebody else wrote is refused. Every JSON dump carries it near the top: a bare
// `"schema_version"` used to be the marker, and any JSON config with that key matched it.
const BackupMarker = `"generated_by": "os-brain`

// dumpHeaderOrder is the order the header keys are written in, ahead of the tables.
var dumpHeaderOrder = []string{"project", "schema_version", "exported_at", "generated_by"}

// newDump starts a dump with its header. generated_by carries BackupMarker, which is what
// lets the next run overwrite this file and nothing else.
func (s *Store) newDump() map[string]interface{} {
	return map[string]interface{}{
		"project": s.Env.Project, "schema_version": store.SchemaVersion, "exported_at": timefmt.Now(),
		"generated_by": "os-brain " + store.Version,
	}
}

// writeDumpFile renders dump with the header first and then the tables in the given order,
// and writes it through out's refusals.
func writeDumpFile(out *fsguard.Dump, dump map[string]interface{}, tables []string) error {
	order := append(append([]string{}, dumpHeaderOrder...), tables...)
	data, err := marshalOrdered(dump, order)
	if err != nil {
		return err
	}
	return out.Write(string(data) + "\n")
}

// Stamp is the timestamp every dump in this codebase is named with. One definition, so a
// backup written by one command sorts beside one written by another.
func Stamp() string { return time.Now().Format("20060102-150405") }

// Row keeps its columns in SELECT order. A plain map marshals alphabetically, which would
// reshuffle every exported row relative to the table it came from.
type Row struct {
	cols []string
	vals map[string]interface{}
}

func (r Row) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteString("{")
	for i, c := range r.cols {
		kb, err := pyfmt.MarshalPy(c, "")
		if err != nil {
			return nil, err
		}
		vb, err := pyfmt.MarshalPy(r.vals[c], "")
		if err != nil {
			return nil, err
		}
		if i > 0 {
			b.WriteString(",")
		}
		b.Write(kb)
		b.WriteString(":")
		b.Write(vb)
	}
	b.WriteString("}")
	return []byte(b.String()), nil
}

func (r Row) Get(k string) interface{} { return r.vals[k] }

// rowsOrdered runs a query and keeps each row's columns in SELECT order.
func (s *Store) rowsOrdered(q string, args ...interface{}) ([]Row, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	err = scanDumpRows(rows, func(names []string, vals map[string]interface{}) {
		out = append(out, Row{cols: names, vals: vals})
	})
	return out, err
}

// tableRows reads a table, optionally filtered, as plain maps (whose keys marshal
// alphabetically: that is the backup's row layout).
func (s *Store) tableRows(table, where string, args []interface{}) ([]map[string]interface{}, error) {
	q := "SELECT * FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]interface{}{}
	err = scanDumpRows(rows, func(_ []string, vals map[string]interface{}) {
		out = append(out, vals)
	})
	return out, err
}

// scanDumpRows reads every row into a map of dump-ready values and hands each to add.
// Text arrives from the driver as bytes and is written as a string. A whole-numbered real
// is written as 80.0 rather than 80, so a reading stays a real when it is imported again.
func scanDumpRows(rows *sql.Rows, add func(names []string, vals map[string]interface{})) error {
	names, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]interface{}, len(names))
		ptrs := make([]interface{}, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		rec := map[string]interface{}{}
		for i, n := range names {
			switch v := vals[i].(type) {
			case []byte:
				rec[n] = string(v)
			case float64:
				rec[n] = pyfmt.PyFloat(v)
			default:
				rec[n] = vals[i]
			}
		}
		add(names, rec)
	}
	return rows.Err()
}

// marshalOrdered renders a JSON object with its keys in the given order, then anything
// left over alphabetically. encoding/json sorts map keys, which would reorder every dump.
// It also matters for safety: the marker must sit within the first bytes of the file for
// GuardOutfile to recognise this tool's own output, and sorted keys pushed it past them.
func marshalOrdered(obj map[string]interface{}, order []string) ([]byte, error) {
	seen := map[string]bool{}
	keys := make([]string, 0, len(obj))
	for _, k := range order {
		if _, ok := obj[k]; ok {
			keys, seen[k] = append(keys, k), true
		}
	}
	var rest []string
	for k := range obj {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		kb, err := pyfmt.MarshalPy(k, "")
		if err != nil {
			return nil, err
		}
		vb, err := pyfmt.MarshalPy(obj[k], "  ")
		if err != nil {
			return nil, err
		}
		b.WriteString("  ")
		b.Write(kb)
		b.WriteString(": ")
		b.WriteString(strings.ReplaceAll(string(vb), "\n", "\n  "))
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}")
	return []byte(b.String()), nil
}

// Reading a dump. encoding/json decodes every number as float64 and every absent key as
// nil, and a dump is a file somebody handed over, so every value is read defensively
// rather than type-asserted.

func jsonList(v interface{}) []map[string]interface{} {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, e := range raw {
		if m, ok := e.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func JSONStr(v interface{}, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func JSONInt(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case bool:
		if n {
			return 1
		}
	case string:
		var i int64
		fmt.Sscanf(n, "%d", &i)
		return i
	}
	return 0
}

func JSONIntOr(v interface{}, def int64) int64 {
	if v == nil {
		return def
	}
	return JSONInt(v)
}

func jsonNullInt(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	return JSONInt(v)
}

func jsonFloat(v interface{}) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func jsonFloatOr(v interface{}, def float64) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return def
}

func jsonNullFloat(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	return jsonFloat(v)
}
