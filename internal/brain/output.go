package brain

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Out prints either the human rendering or the JSON object, never both. A caller parsing
// any command gets the clock for free, so a session reading JSON still knows the hour.
func Out(w io.Writer, asJSON bool, human string, obj interface{}) error {
	if !asJSON {
		_, err := fmt.Fprintln(w, human)
		return err
	}
	if m, ok := obj.(map[string]interface{}); ok {
		if _, has := m["now"]; !has {
			m["now"] = time.Now().Format(time.RFC3339)
			m["now_epoch"] = Now()
		}
	}
	data, err := MarshalPy(obj, "  ")
	if err != nil {
		return err
	}
	// A struct payload (get, lead show) gets the clock too: every JSON answer carries it.
	// Splice it in rather than round-trip through a map, which would reorder the fields and
	// turn every integer into a float.
	if _, isMap := obj.(map[string]interface{}); !isMap {
		if body := string(data); strings.HasPrefix(body, "{") && strings.HasSuffix(body, "}") &&
			!strings.Contains(body, "\n  \"now\":") {
			data = []byte(strings.TrimSuffix(body, "\n}") +
				fmt.Sprintf(",\n  \"now\": %q,\n  \"now_epoch\": %d\n}",
					time.Now().Format(time.RFC3339), Now()))
		}
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
