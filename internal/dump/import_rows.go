package dump

import (
	"fmt"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
)

// A dump is untrusted input: somebody handed over a file. Everything here exists because
// import once took a file's word for what to write and where (which project, which
// columns, which pins) and absorb did the same with a database. The checks are shared by
// both.

// importTables are the tables import writes, and the only ones. Everything else a dump can
// carry is reported and left alone.
var importTables = []string{"memories", "events", "tasks", "measures", "measure_limits"}

// dumpHeaderKeys are the dump's own metadata rather than tables. node_id and count come
// from the team edition's server backup, which has the same document shape on purpose, so
// a dump from there can be restored here.
var dumpHeaderKeys = map[string]bool{"project": true, "schema_version": true, "exported_at": true,
	"generated_by": true, "node_id": true, "count": true}

// teamRowKeys are the keys the team edition's server backup adds to a memory row that a
// local table does not have: it writes the type under a second name and says which node
// the row came from. Accepted on memory rows only, and ignored.
var teamRowKeys = map[string]bool{"type": true, "node_id": true}

// spineTables are shared by every project in a store. A dump that writes an axiom writes a
// binding rule into every brief on the machine, so import never does, whatever the flags.
var spineTables = map[string]bool{"axioms": true, "standards": true, "lessons": true,
	"notes": true, "releases": true}

// checkDumpShape refuses a dump with a key this store does not know, at the top or on any
// row of a table import writes, and returns what it will leave alone: tables the store has
// but import does not restore, the spine first among them.
//
// Column names were once the dump's JSON keys, spliced into the INSERT; now every key is
// compared against the store's own columns before anything in the dump is read for meaning.
func (s *Store) checkDumpShape(db store.Querier, dump map[string]interface{}) (map[string]int, error) {
	writes := map[string]bool{}
	for _, t := range importTables {
		writes[t] = true
	}
	left := map[string]int{}
	for _, k := range store.SortedIfaceKeys(dump) {
		if dumpHeaderKeys[k] {
			continue
		}
		if writes[k] {
			if err := checkImportedTable(db, k, dump[k]); err != nil {
				return nil, err
			}
			continue
		}
		table := k
		if k == "history" {
			table = "mem_history"
		}
		if !spineTables[k] && !s.TableExists(table) && !s.spineHas(table) {
			return nil, fmt.Errorf("this dump carries %s, which is not a table of this brain. "+
				"Refusing the whole import", pyfmt.Repr(k))
		}
		left[k] = len(jsonList(dump[k]))
	}
	return left, nil
}

// checkImportedTable refuses a table import writes unless it is a list of rows whose every
// key is a column of that table in this store.
func checkImportedTable(db store.Querier, table string, value interface{}) error {
	cols, err := store.TableColumns(db, table)
	if err != nil {
		return err
	}
	if value != nil {
		if _, ok := value.([]interface{}); !ok {
			return fmt.Errorf("%s in this dump is not a list of rows", pyfmt.Repr(table))
		}
	}
	for i, r := range jsonList(value) {
		for _, c := range store.SortedIfaceKeys(r) {
			if !cols[c] && !(table == "memories" && teamRowKeys[c]) {
				return fmt.Errorf("row %d of %s carries %s, which is not a column "+
					"of %s in this brain. Refusing the whole import: a dump from "+
					"something else is not restored by guesswork", i+1, table, pyfmt.Repr(c), table)
			}
		}
	}
	return nil
}

// GuardRow runs the secret scan over every text value of a row, whichever column it is in.
// Import once scanned a memory's title and body and nothing else, so a credential could go
// in as a memory's source, an event, a task or a measure note.
func GuardRow(allow bool, table string, i int, r map[string]interface{}) error {
	var text []string
	for _, k := range store.SortedIfaceKeys(r) {
		if v, ok := r[k].(string); ok {
			text = append(text, v)
		}
	}
	if err := textguard.GuardText(allow, text...); err != nil {
		return fmt.Errorf("row %d of %s: %v. Refusing the whole import; clean the dump", i+1,
			table, err)
	}
	return nil
}

// ImportedMemory is a memory row as it may be written: everything a dump cannot vouch for
// already set to what an ordinary write would give it.
type ImportedMemory struct {
	owner, scope, typ, title, body, source, sourceDate, sourceCommit, confidence, session string
	importance, useCount                                                                  int
	lastUsed, verifiedAt                                                                  interface{}
	createdAt, updatedAt                                                                  int64
	// dropped records what the row claimed and did not get, for the report.
	pinDropped, downgraded bool
}

// Hash is the identity the memory takes in its owner's brain: the content hash remember
// gives a memory with the same scope and title, so a row already there is recognised.
func (im *ImportedMemory) Hash() string { return store.ContentHash(im.owner, im.scope, im.title) }

// SanitiseMemory is the remember path's rules applied to a row somebody else wrote. A dump
// cannot pin (a pin puts a row first in every brief), cannot claim `confirmed` without the
// citation remember requires, carries trust `imported` whatever it says, and cannot date a
// row in the future, where it would never decay and top "new since" forever.
func SanitiseMemory(m map[string]interface{}, owner, defSource string, now int64) (*ImportedMemory, error) {
	title := strings.TrimSpace(JSONStr(m["title"], ""))
	if title == "" {
		return nil, fmt.Errorf("a row has no title, so it has no identity and nothing to be " +
			"recalled by")
	}
	if err := textguard.CheckLine("the title", title); err != nil {
		return nil, fmt.Errorf("%v (memory %s)", err, pyfmt.Repr(textguard.Clip(title, 60)))
	}
	typ := importedType(m)
	if !store.AllTypes()[typ] {
		return nil, fmt.Errorf("unknown type %s on %s. valid: %s", pyfmt.Repr(typ), pyfmt.Repr(title),
			store.JoinSorted(store.AllTypes()))
	}
	scope := JSONStr(m["scope"], "global")
	if err := textguard.CheckToken(scope, "scope"); err != nil {
		return nil, fmt.Errorf("%v (memory %s)", err, pyfmt.Repr(title))
	}
	conf := JSONStr(m["confidence"], "")
	if conf != "" && !store.KnownConfidence(conf) {
		return nil, fmt.Errorf("unknown confidence %s on %s", pyfmt.Repr(conf), pyfmt.Repr(title))
	}
	body := JSONStr(m["body"], "")
	if err := textguard.CheckMemorySize(title, body); err != nil {
		return nil, fmt.Errorf("%v (memory %s)", err, pyfmt.Repr(textguard.Clip(title, 60)))
	}
	source := JSONStr(m["source"], defSource)
	if err := textguard.CheckLines("the source", source, "the source date", JSONStr(m["source_date"], "")); err != nil {
		return nil, fmt.Errorf("%v (memory %s)", err, pyfmt.Repr(title))
	}
	im := &ImportedMemory{owner: owner, scope: scope, typ: typ, title: title, body: body,
		source: source, sourceDate: JSONStr(m["source_date"], ""),
		sourceCommit: JSONStr(m["source_commit"], ""), session: JSONStr(m["session"], ""),
		importance: store.ClampImportance(int(JSONIntOr(m["importance"], 3))),
		useCount:   int(JSONInt(m["use_count"])),
		lastUsed:   clampNullTS(m["last_used"], now), verifiedAt: clampNullTS(m["verified_at"], now),
		createdAt: ClampTS(JSONIntOr(m["created_at"], now), now),
		updatedAt: ClampTS(JSONIntOr(m["updated_at"], now), now),
	}
	if im.useCount < 0 {
		im.useCount = 0
	}
	im.pinDropped = JSONInt(m["pinned"]) != 0
	im.confidence = store.NormConfidence(conf)
	if earned, err := store.EarnedConfidence(im.confidence, source, nil, body); err != nil {
		im.confidence, im.downgraded = "reported", true
	} else {
		im.confidence = earned
	}
	return im, nil
}

// importedType reads the memory type from either key a dump may carry: a local export
// writes "mem_type", the team edition's server backup writes "type". Reading only the first
// once defaulted every memory of a server dump to "note", which is not a cosmetic loss:
// "note" never decays, so the whole imported corpus outranked properly aged facts forever.
func importedType(m map[string]interface{}) string {
	if t := JSONStr(m["mem_type"], ""); t != "" {
		return t
	}
	if t := JSONStr(m["type"], ""); t != "" {
		return t
	}
	return "note"
}

// ClampTS pulls a timestamp from the future back to now. A dump cannot date a row ahead of
// time, where it would never decay and would top "new since" forever.
func ClampTS(ts, now int64) int64 {
	if ts > now {
		return now
	}
	return ts
}

func clampNullTS(v interface{}, now int64) interface{} {
	n := jsonNullInt(v)
	if ts, ok := n.(int64); ok && ts > now {
		return now
	}
	return n
}

// foreignLabels counts, per project label other than this one, the rows a dump would write
// there across every table import writes. The gate once counted memories alone, so a dump
// of this project's memories carrying another project's events, tasks and measures passed
// it, and the forged tasks surfaced in that project's brief as work it owed.
func foreignLabels(dump map[string]interface{}, self string, events bool) map[string]int {
	out := map[string]int{}
	for _, t := range importTables {
		if (t == "events" || t == "tasks") && !events {
			continue
		}
		for _, r := range jsonList(dump[t]) {
			if owner := JSONStr(r["project"], self); owner != self {
				out[owner]++
			}
		}
	}
	return out
}

func labelList(m map[string]int) string {
	var parts []string
	for _, k := range store.SortedStrings(m) {
		parts = append(parts, fmt.Sprintf("%s (%d)", pyfmt.Repr(k), m[k]))
	}
	return strings.Join(parts, ", ")
}

// ImportPrivate is whether a dump may not write into this project. A private declaration
// lives in the shared index, so a store with no index has none to honour; an index that
// cannot be read counts as declaring it (NamePrivate).
func (s *Store) ImportPrivate(name string) bool {
	g := s.GlobalConnect(false)
	if g == nil {
		return false
	}
	defer s.CloseGlobal(g)
	return s.NamePrivate(g, name)
}

// spineHas is whether the machine's shared store has the table: a backup of a shared store
// carries the index and the spine, and those are known, just never imported.
func (s *Store) spineHas(table string) bool {
	g := s.GlobalConnect(false)
	if g == nil {
		return false
	}
	defer s.CloseGlobal(g)
	return s.TableExistsIn(g, table)
}
