package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// Probe is one command a project wants run at session open, edited with `wakeup add`/`rm`.
type Probe struct {
	Name    string `json:"name"`
	Run     string `json:"run"`
	Timeout int    `json:"timeout,omitempty"` // seconds; probeTimeout if unset
	Chars   int    `json:"chars,omitempty"`   // output clip; probeChars if unset
}

// UnmarshalJSON accepts numeric strings for timeout and chars, so brain.json round-trips a
// probe list edited by hand, which is how a config gets edited in practice.
func (p *Probe) UnmarshalJSON(data []byte) error {
	type raw struct {
		Name    string      `json:"name"`
		Run     string      `json:"run"`
		Timeout interface{} `json:"timeout"`
		Chars   interface{} `json:"chars"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	p.Name, p.Run = r.Name, r.Run
	p.Timeout, p.Chars = asInt(r.Timeout), asInt(r.Chars)
	return nil
}

func asInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}

// ProbeHash identifies a probe by exactly what would be executed. Editing either half
// revokes trust for that probe alone.
func ProbeHash(p Probe) string {
	sum := sha256.Sum256([]byte(p.Name + "\x00" + p.Run))
	return hex.EncodeToString(sum[:])
}
