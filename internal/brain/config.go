package brain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// DefaultScope is where a memory goes when nobody says otherwise, and the root every other
// scope hangs off. It is declared by construction, never by a project.
const DefaultScope = "global"

// Version is this build of the brain: yymmddNN, NN counting releases within that day. A date
// is legible at a glance and sorts as a plain integer, which a dotted version does not.
// SchemaVersion is a different thing: the database shape, which only moves forward.
const (
	Version       = "26110501"
	SchemaVersion = 16
)

// Config is brain.json, the file that makes a directory a brain. It is committed with the
// project, so everything in it is the repository's word, and the fields that could run a
// command or reach outside the project are gated elsewhere.
type Config struct {
	Project string `json:"project,omitempty"`
	Root    string `json:"root,omitempty"`
	Shared  bool   `json:"shared,omitempty"`
	// Private keeps a project's memories out of every OTHER project's --all-projects while
	// they still live in the shared file. Sharing a database and being readable by everything
	// in it are two different questions, and without this flag one project could read another
	// through --all-projects with no way to opt out.
	Private bool  `json:"private,omitempty"`
	Global  *bool `json:"global,omitempty"`
	// Index keeps this project out of the shared project index entirely. Default on: an index
	// nobody is in answers no questions. The row carries a hash of the directory rather than
	// the directory itself, so the reason to opt out is narrow.
	Index   *bool             `json:"index,omitempty"`
	Scopes  map[string]string `json:"scopes,omitempty"`
	Kinds   map[string]string `json:"kinds,omitempty"`
	Context map[string]string `json:"context,omitempty"`
	Spec    map[string]string `json:"spec,omitempty"`
	// HookExclude names scopes the auto-recall hook never volunteers into the model's context.
	HookExclude []string `json:"hook_exclude,omitempty"`
	// UtteranceDays is how long a captured prompt is kept; 0 means UtteranceDaysDefault.
	UtteranceDays int `json:"utterance_days,omitempty"`
	// Wakeup lists commands this project wants run at session open. Declared here, gated on a
	// per-machine approval, because this file is committed. See wakeup.go.
	Wakeup []Probe `json:"wakeup,omitempty"`
	// Backups is how a verified safety copy leaves this machine. See safety.go.
	Backups BackupRef `json:"backups,omitempty"`

	// undecoded holds known keys whose value had the wrong type, verbatim, so SaveConfig
	// writes them back instead of dropping them.
	undecoded map[string]json.RawMessage
	// teamEdition is set when this file belongs to a team-edition brain; see TeamEdition.
	teamEdition string
}

// BackupRef declares what to run after a safety copy verifies. {file} is substituted with
// its path. Gated on a per-machine approval like a wakeup probe, because this file is
// committed and a cloned repo must not run a stranger's command at session open.
type BackupRef struct {
	Push string `json:"push,omitempty"`
}

const unreadableConfigHint = "Fix it, or restore it from git; nothing runs against a brain " +
	"whose config cannot be read"

// LoadConfig reads brain.json. A missing file is the zero Config; a file that does not parse
// is an error. Ignoring a broken file would make it read as a brain with no name and no
// settings, so a typo could quietly change which project and which store a command uses.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	doc, err := configObject(path, data)
	if err != nil {
		return Config{}, err
	}
	c := lenientConfig(doc)
	c.teamEdition = teamEditionOf(doc)
	if err := checkPrintedFields(path, c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// configObject parses brain.json into its top-level keys. Only a file that is not a JSON
// object refuses; a field of the wrong type is lenientConfig's to skip.
func configObject(path string, data []byte) (map[string]json.RawMessage, error) {
	if !json.Valid(data) {
		var probe interface{}
		return nil, fmt.Errorf("%s does not parse: %w.\n"+unreadableConfigHint, path,
			json.Unmarshal(data, &probe))
	}
	// Valid JSON that is not an object (42, "x", [], null) carries no keys at all, so it would
	// read as an empty brain exactly like a file that does not parse.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil || doc == nil {
		return nil, fmt.Errorf("%s is not a JSON object.\n"+unreadableConfigHint, path)
	}
	return doc, nil
}

// checkPrintedFields refuses a newline in the fields printed into briefs. The name and the
// context lines are printed into this project's brief and copied into the shared index,
// where every other project's brief prints them: a newline in a committed brain.json could
// forge a whole section, such as a fake GUARDRAILS block, in somebody else's brief.
func checkPrintedFields(path string, c Config) error {
	if err := CheckLine(path+": project", c.Project); err != nil {
		return err
	}
	for _, m := range []map[string]string{c.Context, c.Spec} {
		for k, v := range m {
			if err := CheckLines(path+": a key", k, path+": "+k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// TeamEdition names the team-edition brain this brain.json belongs to, or returns "" for a
// local brain. ns-brain's team edition writes a "hive" block naming the server a project's
// memories live on, or a "blind" marker for a zero-knowledge brain. os-brain cannot read
// either, so it refuses to run there rather than answer from a local file that does not
// hold this project's memories. Both keys are kept verbatim by SaveConfig, like any key
// this binary does not own.
func (c Config) TeamEdition() string { return c.teamEdition }

func teamEditionOf(doc map[string]json.RawMessage) string {
	if raw, ok := doc["blind"]; ok && string(raw) != "null" {
		return "a zero-knowledge brain"
	}
	var hive struct {
		Server string `json:"server"`
	}
	if raw, ok := doc["hive"]; ok && json.Unmarshal(raw, &hive) == nil && strings.TrimSpace(hive.Server) != "" {
		return "a Hive server (" + strings.TrimSpace(hive.Server) + ")"
	}
	return ""
}

// lenientConfig decodes each known key on its own, so one of the wrong type (a number where
// a string was meant) is skipped with a warning and the rest still read. Refusing the whole
// file instead took down every command in a project whose file had always been that way,
// init and the session-open hook included.
func lenientConfig(doc map[string]json.RawMessage) Config {
	var c Config
	v := reflect.ValueOf(&c).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name := jsonKey(t.Field(i))
		raw, ok := doc[name]
		if name == "" || name == "-" || !ok {
			continue
		}
		f := reflect.New(t.Field(i).Type)
		if err := json.Unmarshal(raw, f.Interface()); err != nil {
			if c.undecoded == nil {
				c.undecoded = map[string]json.RawMessage{}
			}
			c.undecoded[name] = raw
			Warn("%s in brain.json is not the type this version expects (%v); ignoring it", name, err)
			continue
		}
		v.Field(i).Set(f.Elem())
	}
	return c
}

func jsonKey(f reflect.StructField) string {
	return strings.Split(f.Tag.Get("json"), ",")[0]
}

// configKeys is every top-level key Config owns, from its json tags.
var configKeys = func() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		if name := jsonKey(t.Field(i)); name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}()

// SaveConfig rewrites brain.json atomically. The file is the project's identity and its
// declared scopes; a truncated write during a crash would leave the brain nameless.
func SaveConfig(here string, c Config) error {
	path := filepath.Join(here, "brain.json")
	if err := CheckNoLinks(filepath.Dir(here), path); err != nil {
		return err
	}
	known, err := json.Marshal(c)
	if err != nil {
		return err
	}
	known = dropEmptyBlocks(known)
	if prev, rerr := os.ReadFile(path); rerr == nil {
		known = keepForeignKeys(known, prev, c)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, known, "", "  "); err != nil {
		return err
	}
	// Rooted at the brain directory's parent, so a brain directory that is itself a link
	// is refused along with a linked brain.json.
	return WriteFileSafe(path, append(buf.Bytes(), '\n'), WriteOpts{Root: filepath.Dir(here),
		Mode: 0o600})
}

// dropEmptyBlocks removes "backups":{} from the marshalled config. omitempty does not drop
// an empty struct, and an empty block in a committed file is noise in every diff.
func dropEmptyBlocks(known []byte) []byte {
	for _, k := range []string{`"backups":{}`} {
		known = bytes.Replace(known, []byte(","+k), nil, 1)
		known = bytes.Replace(known, []byte(k+","), nil, 1)
		known = bytes.Replace(known, []byte(k), nil, 1)
	}
	return known
}

// keepForeignKeys appends to known every key of the previous file this binary does not
// write: keys it does not know (the team edition's "hive" and "blind", or whatever a later
// version adds) and known keys it skipped for having the wrong type. A rewrite by a binary
// whose Config lacked a field is how such a key would otherwise vanish. The extra keys go
// last, in sorted order, so the output is stable.
func keepForeignKeys(known, prev []byte, c Config) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(prev, &doc) != nil {
		return known
	}
	var mine map[string]json.RawMessage
	_ = json.Unmarshal(known, &mine)
	var extra []string
	for k := range doc {
		_, set := mine[k]
		if !configKeys[k] || (c.undecoded[k] != nil && !set) {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	out := append([]byte(nil), known[:len(known)-1]...)
	for _, k := range extra {
		if len(out) > 1 {
			out = append(out, ',')
		}
		name, _ := json.Marshal(k)
		out = append(append(append(out, name...), ':'), doc[k]...)
	}
	return append(out, '}')
}
