package pyfmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// MarshalPy encodes JSON the way Python's json.dumps does. The first implementation of this
// tool was Python, its JSON is what every consumer already parses, and ns-brain prints the
// same bytes:
//
//   - non-ASCII is escaped as \uXXXX (ensure_ascii=True), so a Greek memory title reads
//     identically from either edition;
//   - <, > and & are left alone, where Go's encoder escapes them to \u003c and friends.
//
// Without this two dumps of the same brain differ in bytes, which defeats diffing them.
func MarshalPy(v interface{}, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return escapeNonASCII(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// escapeNonASCII rewrites every rune above U+007F as \uXXXX, using surrogate pairs above
// the BMP exactly as Python does. It only ever runs over encoder output, so the only
// strings it can see are already valid JSON.
func escapeNonASCII(b []byte) []byte {
	if isASCII(b) {
		return b
	}
	var out bytes.Buffer
	out.Grow(len(b) * 2)
	for i := 0; i < len(b); {
		if b[i] < utf8.RuneSelf {
			out.WriteByte(b[i])
			i++
			continue
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			out.WriteByte(b[i])
			i++
			continue
		}
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&out, "\\u%04x\\u%04x", hi, lo)
		} else {
			fmt.Fprintf(&out, "\\u%04x", r)
		}
		i += size
	}
	return out.Bytes()
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// PyFloat marshals a float the way Python's json does: a whole number keeps its ".0", so
// a measure of 80 kg exports as 80.0 rather than 80, as every earlier dump of the same
// reading did.
type PyFloat float64

func (f PyFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return []byte(strconv.FormatFloat(v, 'f', 1, 64)), nil
	}
	return json.Marshal(v)
}
