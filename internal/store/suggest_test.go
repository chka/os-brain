package store

import (
	"strings"
	"testing"
)

// A mistyped project or scope name is answered with "did you mean", so a typo is caught at the
// prompt instead of silently creating or querying a name nobody uses. The cases cover a
// doubled letter, a dropped hyphen, a dropped letter and a prefix, and the opposite failure:
// an exact name must not be "corrected" into a different one, and a name close to nothing
// must not produce a suggestion at all.
func TestNearNamesSuggestsTheIntendedNameAndNothingElse(t *testing.T) {
	known := []string{"gateway", "registry-ui", "server-manager", "isup", "cloud-manager"}
	cases := map[string]string{
		"gatewway":       "gateway",
		"registryui":     "registry-ui",
		"server-manger":  "server-manager",
		"registry":       "registry-ui",
		"isup":           "",
		"something-else": "",
		"":               "",
	}
	for in, want := range cases {
		got := strings.Join(NearNames(in, known), ",")
		if want == "" && got != "" || want != "" && !strings.HasPrefix(got, want) {
			t.Errorf("NearNames(%q) = %q, want first %q", in, got, want)
		}
	}
	if DidYouMean("zzzzzz", known) != "" {
		t.Error("a name close to nothing produced a suggestion")
	}
}
