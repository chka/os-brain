package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/chka/os-brain/internal/pyfmt"
)

// NearNames returns the names in known that a mistyped want most likely meant, closest
// first: an edit distance within a quarter of the name's length (at least one), or one name
// containing the other once both are four characters or longer. It only ever suggests; a
// caller that acted on a suggestion would turn a typo into a delivery to the wrong place.
func NearNames(want string, known []string) []string {
	w := strings.ToLower(strings.TrimSpace(want))
	if w == "" {
		return nil
	}
	limit := max(1, len([]rune(w))/4)
	type hit struct {
		name string
		d    int
	}
	var hits []hit
	for _, k := range known {
		lk := strings.ToLower(k)
		if lk == w {
			continue
		}
		d := editDistance(w, lk)
		contains := len(w) >= 4 && len(lk) >= 4 && (strings.Contains(lk, w) || strings.Contains(w, lk))
		if d <= limit || contains {
			hits = append(hits, hit{k, d})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].d < hits[j].d })
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.name)
	}
	if len(out) > 3 {
		out = out[:3]
	}
	return out
}

// DidYouMean renders NearNames as a sentence, or "" when there is nothing close.
func DidYouMean(want string, known []string) string {
	near := NearNames(want, known)
	if len(near) == 0 {
		return ""
	}
	q := make([]string, len(near))
	for i, n := range near {
		q[i] = pyfmt.Repr(n)
	}
	return " Did you mean " + strings.Join(q, " or ") + "?"
}

// editDistance is the Levenshtein distance between a and b, counted in runes.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(min(prev[j]+1, cur[j-1]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// ToolNames are what this program is called, plus "nsb", the short name of the team
// edition ns-brain, which its users type out of habit. Typed as a note's recipient they
// name the tool every project runs, never a project, and a refusal that only says "no such
// project" sends the caller looking for a typo that is not there.
var ToolNames = []string{"os-brain", "nsb", "brain"}

// NameAlias is something a project is known by other than its registered name: the
// directory it lives in, or a word its own description uses for itself. Why completes the
// sentence "<alias> is ...", with {project} standing for the registered name.
type NameAlias struct {
	Alias   string
	Project string
	Why     string
}

// DescriptionAliases maps typed to every project whose own description uses it as a word.
// descriptions is registered name to whatever text that project says about itself.
func DescriptionAliases(typed string, descriptions map[string]string) []NameAlias {
	var out []NameAlias
	names := make([]string, 0, len(descriptions))
	for n := range descriptions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !strings.EqualFold(n, typed) && MentionsName(descriptions[n], typed) {
			out = append(out, NameAlias{Alias: typed, Project: n,
				Why: "named in the description of the project registered as {project}"})
		}
	}
	return out
}

// DirAliases are the directory names this project is reached from, each mapped to the name
// it is registered under: the project root, the directory holding brain.json and the
// working directory when it is inside the project. A session in ~/src/brain/os-brain thinks
// of the project as "os-brain" or "brain" whatever brain.json calls it.
func (e Env) DirAliases() []NameAlias {
	if strings.TrimSpace(e.Project) == "" {
		return nil
	}
	root := e.ProjectRoot()
	dirs := []string{root, e.Here}
	if wd, err := os.Getwd(); err == nil && root != "" && within(wd, root) {
		dirs = append(dirs, wd)
	}
	var out []NameAlias
	seen := map[string]bool{}
	for _, d := range dirs {
		b := filepath.Base(filepath.Clean(d))
		if d == "" || b == "." || b == string(filepath.Separator) || seen[strings.ToLower(b)] ||
			strings.EqualFold(b, e.Project) {
			continue
		}
		seen[strings.ToLower(b)] = true
		out = append(out, NameAlias{Alias: b, Project: e.Project,
			Why: "the directory this project lives in, and this project is registered as {project}"})
	}
	return out
}

// MentionsName is whether text uses name as a whole word, a hyphen or underscore counting as
// part of the word so that "brain" does not match inside "os-brain".
func MentionsName(text, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	word := func(r rune) bool {
		return r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	for _, f := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !word(r) }) {
		if strings.Trim(f, "-_") == name {
			return true
		}
	}
	return false
}

// UnknownProjectHint is what a refusal to deliver to typed adds, so that the caller learns
// the name to use rather than only the name not to. In order: a registered name the typed
// one is an alias of, the tool's own name explained, the nearest spellings, and when none of
// those found anything the names that do exist. known is what spellings are matched against,
// listed what may be printed. It never picks one: a hint acted on by the program would turn
// a guess into a delivery to the wrong place.
func UnknownProjectHint(typed string, known, listed []string, aliases []NameAlias) string {
	typed = strings.TrimSpace(typed)
	parts, hit := aliasHints(typed, aliases)
	for _, t := range ToolNames {
		if strings.EqualFold(t, typed) {
			parts = append(parts, fmt.Sprintf("%s is the name of the tool every project runs, "+
				"not of a project. Use the name the project is registered under.", pyfmt.Repr(typed)))
			break
		}
	}
	near := DidYouMean(typed, known)
	if near != "" {
		parts = append(parts, strings.TrimSpace(near))
	}
	if len(hit) == 0 && near == "" && len(listed) > 0 {
		k := append([]string(nil), listed...)
		sort.Strings(k)
		more := ""
		if len(k) > 40 {
			more = fmt.Sprintf(" and %d more", len(k)-40)
			k = k[:40]
		}
		parts = append(parts, "Projects here: "+strings.Join(k, ", ")+more+".")
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// aliasHints explains each registered project the typed name is an alias of. hit is the
// set of projects explained, so the caller knows whether the full list is still needed.
func aliasHints(typed string, aliases []NameAlias) ([]string, map[string]bool) {
	var parts []string
	hit := map[string]bool{}
	for _, a := range aliases {
		if strings.EqualFold(a.Alias, typed) && !hit[a.Project] {
			hit[a.Project] = true
			parts = append(parts, pyfmt.Repr(typed)+" is "+
				strings.ReplaceAll(a.Why, "{project}", pyfmt.Repr(a.Project))+".")
		}
	}
	return parts, hit
}
