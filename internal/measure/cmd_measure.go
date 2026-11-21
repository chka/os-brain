package measure

import (
	"database/sql"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/chka/os-brain/internal/lexicon"
	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// Measures are numeric series: a reading with a unit, the day it was taken and where it
// came from. A key can carry an optional band (a floor, a ceiling and which way is
// better), and only then does the brain judge a reading. Measures live in their own table,
// outside the memory index, so a number never outranks a fact in recall.

var spark = []rune("▁▂▃▄▅▆▇█")

// sparkPoints is how many of the newest readings the sparkline draws: enough to show a
// trend on one line, few enough to fit beside the label.
const sparkPoints = 24

// Measure is one reading: a value, a unit, the day it was taken and where it came from.
type Measure struct {
	ID         int64   `json:"id"`
	Project    string  `json:"project"`
	Scope      string  `json:"scope"`
	Key        string  `json:"key"`
	Label      string  `json:"label"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	TakenOn    string  `json:"taken_on"`
	TakenAt    int64   `json:"taken_at"`
	Source     string  `json:"source"`
	Confidence string  `json:"confidence"`
	Ref        string  `json:"ref"`
	Note       string  `json:"note"`
	CreatedAt  int64   `json:"created_at"`
}

const measureCols = `id, project, scope, key, label, value, unit, taken_on, taken_at,
	source, confidence, ref, note, created_at`

func scanMeasure(s interface{ Scan(...interface{}) error }) (*Measure, error) {
	var m Measure
	err := s.Scan(&m.ID, &m.Project, &m.Scope, &m.Key, &m.Label, &m.Value, &m.Unit,
		&m.TakenOn, &m.TakenAt, &m.Source, &m.Confidence, &m.Ref, &m.Note, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// MeasureLimit is the optional per-key band. Absent means a series is recorded and never
// judged: most keys have no right answer, and a tool that invents one starts shouting
// about readings nobody asked it to police.
type MeasureLimit struct {
	Key     string   `json:"key"`
	Low     *float64 `json:"low"`
	High    *float64 `json:"high"`
	Better  string   `json:"better"`
	Unit    string   `json:"unit"`
	Why     string   `json:"why"`
	Updated int64    `json:"updated_at"`
}

// MeasureSummary reduces one key to what a brief line needs: latest, direction, and
// whether it is outside its band.
type MeasureSummary struct {
	Key        string   `json:"key"`
	Label      string   `json:"label"`
	Unit       string   `json:"unit"`
	N          int      `json:"n"`
	Latest     float64  `json:"latest"`
	LatestOn   string   `json:"latest_on"`
	Previous   *float64 `json:"previous"`
	PreviousOn string   `json:"previous_on"`
	Delta      *float64 `json:"delta"`
	Arrow      string   `json:"arrow"`
	Direction  string   `json:"direction"`
	Min        float64  `json:"min"`
	Max        float64  `json:"max"`
	Mean       float64  `json:"mean"`
	First      float64  `json:"first"`
	FirstOn    string   `json:"first_on"`
	Spark      string   `json:"spark"`
	Breach     string   `json:"breach"`
	Low        *float64 `json:"low"`
	High       *float64 `json:"high"`
	Better     string   `json:"better"`
	Why        string   `json:"why"`
}

// NormMeasureKey is the identity of a series: folded, lowercased, punctuation to
// underscores. So 'Kt/V', 'kt_v' and 'kt v' are one series rather than three.
func NormMeasureKey(text string) string {
	lowered := lexicon.StripAccents(strings.ToLower(strings.TrimSpace(text)))
	var b strings.Builder
	for _, r := range lowered {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	parts := strings.FieldsFunc(strings.ReplaceAll(b.String(), "-", "_"),
		func(r rune) bool { return r == '_' })
	return strings.Join(parts, "_")
}

// FmtMeasure prints an optional number the way the rest of the output prints numbers,
// and an absent one as nothing.
func FmtMeasure(v *float64) string {
	if v == nil {
		return ""
	}
	return pyfmt.PyG(*v)
}

func Fm(f float64) string { return FmtMeasure(&f) }

// CheckMeasureValue refuses Inf and NaN. strconv reads "Inf", SQLite stores it, and one
// such reading made every summary of its series index the sparkline at MinInt64: brief,
// wakeup and doctor all panicked on every session until somebody found the id.
func CheckMeasureValue(what string, v *float64) error {
	if v != nil && (math.IsInf(*v, 0) || math.IsNaN(*v)) {
		return textguard.Judged("%s must be a finite number, not %s", what, pyfmt.PyG(*v))
	}
	return nil
}

// finiteMeasures drops readings no renderer can place. A store written before the write
// path refused them can still hold one; doctor names it, and nothing else should die on it.
func finiteMeasures(rows []*Measure) []*Measure {
	out := rows[:0:0]
	for _, r := range rows {
		if !math.IsInf(r.Value, 0) && !math.IsNaN(r.Value) {
			out = append(out, r)
		}
	}
	return out
}

// sparkline draws values scaled between their own minimum and maximum. A flat series
// (or one whose range overflows) is drawn as a flat line on the lowest bar.
func sparkline(values []float64) string {
	if len(values) < 2 {
		return ""
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi == lo || math.IsInf(hi-lo, 0) || math.IsNaN(hi-lo) {
		return strings.Repeat(string(spark[0]), len(values))
	}
	var b strings.Builder
	for _, v := range values {
		i := int((v - lo) / (hi - lo) * float64(len(spark)-1))
		if i < 0 {
			i = 0
		}
		if i > len(spark)-1 {
			i = len(spark) - 1
		}
		b.WriteRune(spark[i])
	}
	return b.String()
}

// measureLimit is the band declared for a key, or nil when there is none.
func (s *Store) measureLimit(key string) *MeasureLimit {
	row := s.DB.QueryRow("SELECT key, low, high, better, unit, why, updated_at FROM "+
		"measure_limits WHERE project=? AND key=?", s.Env.Project, key)
	var l MeasureLimit
	if err := row.Scan(&l.Key, &l.Low, &l.High, &l.Better, &l.Unit, &l.Why, &l.Updated); err != nil {
		return nil
	}
	return &l
}

// measureBreach returns "below" or "above", or "" when there is no band or the value sits
// inside it. The bounds themselves are inside the band.
func measureBreach(l *MeasureLimit, value float64) string {
	if l == nil {
		return ""
	}
	if l.Low != nil && value < *l.Low {
		return "below"
	}
	if l.High != nil && value > *l.High {
		return "above"
	}
	return ""
}

// measureSeries reads one key oldest first, because a series reads forward and every
// caller wants it that way. --from and --to are inclusive days; limit keeps the newest.
func (s *Store) measureSeries(key, scope, start, end string, limit int) ([]*Measure, error) {
	where := []string{"project=?", "key=?"}
	params := []interface{}{s.Env.Project, key}
	if scope != "" {
		clause, sp := store.ScopeFilter(scope, "measures")
		where = append(where, clause)
		params = append(params, sp...)
	}
	if start != "" {
		where = append(where, "taken_on >= ?")
		params = append(params, start)
	}
	if end != "" {
		where = append(where, "taken_on <= ?")
		params = append(params, end)
	}
	rows, err := s.DB.Query("SELECT "+measureCols+" FROM measures WHERE "+
		strings.Join(where, " AND ")+" ORDER BY taken_at ASC, id ASC", params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Measure
	for rows.Next() {
		m, err := scanMeasure(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, rows.Err()
}

func (s *Store) measureSummary(key string, rows []*Measure) *MeasureSummary {
	return summariseMeasures(key, rows, s.measureLimit(key))
}

// summariseMeasures is the band verdict, the trend arrow and the sparkline for one key.
//
// rows are OLDEST FIRST. The last element is the latest reading and the arrow is read
// against the one before it. Direction is "better" or "worse" only when the band says
// which way is better and the value actually moved.
func summariseMeasures(key string, rows []*Measure, lim *MeasureLimit) *MeasureSummary {
	rows = finiteMeasures(rows)
	if len(rows) == 0 {
		return nil
	}
	vals := make([]float64, len(rows))
	sum := 0.0
	lo, hi := rows[0].Value, rows[0].Value
	for i, r := range rows {
		vals[i] = r.Value
		sum += r.Value
		lo, hi = math.Min(lo, r.Value), math.Max(hi, r.Value)
	}
	latest := rows[len(rows)-1]
	var delta, previous *float64
	prevOn := ""
	arrow := "→"
	if len(rows) > 1 {
		prev := rows[len(rows)-2]
		d := latest.Value - prev.Value
		delta, previous, prevOn = &d, &prev.Value, prev.TakenOn
		arrow = trendArrow(d)
	}
	label := latest.Label
	if label == "" {
		label = key
	}
	tail := vals
	if len(tail) > sparkPoints {
		tail = tail[len(tail)-sparkPoints:]
	}
	sm := &MeasureSummary{
		Key: key, Label: label, Unit: latest.Unit, N: len(rows), Latest: latest.Value,
		LatestOn: latest.TakenOn, Previous: previous, PreviousOn: prevOn, Delta: delta,
		Arrow: arrow, Direction: trendVerdict(delta, lim), Min: lo, Max: hi,
		Mean: sum / float64(len(rows)), First: vals[0], FirstOn: rows[0].TakenOn,
		Spark: sparkline(tail), Breach: measureBreach(lim, latest.Value),
	}
	if lim != nil {
		sm.Low, sm.High, sm.Better, sm.Why = lim.Low, lim.High, lim.Better, lim.Why
	}
	return sm
}

func trendArrow(delta float64) string {
	switch {
	case delta > 0:
		return "↑"
	case delta < 0:
		return "↓"
	}
	return "→"
}

func trendVerdict(delta *float64, lim *MeasureLimit) string {
	better := ""
	if lim != nil {
		better = lim.Better
	}
	if delta == nil || *delta == 0 || better == "" {
		return ""
	}
	if (*delta > 0) == (better == "up") {
		return "better"
	}
	return "worse"
}

// measureKeys is every key with readings in scope, most recently read first.
func (s *Store) measureKeys(scope string) []string {
	where := []string{"project=?"}
	params := []interface{}{s.Env.Project}
	if scope != "" {
		clause, sp := store.ScopeFilter(scope, "measures")
		where = append(where, clause)
		params = append(params, sp...)
	}
	rows, err := s.DB.Query("SELECT key, MAX(taken_at) t FROM measures WHERE "+
		strings.Join(where, " AND ")+" GROUP BY key ORDER BY t DESC", params...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		var t int64
		if err := rows.Scan(&k, &t); err != nil {
			return keys
		}
		keys = append(keys, k)
	}
	return keys
}

// MeasureOverview lists every key with a summary, breaches first: a number outside its
// band is the only part of a series that needs reading today. After that, the longest
// series first.
func (s *Store) MeasureOverview(scope string) []*MeasureSummary {
	out := []*MeasureSummary{}
	for _, key := range s.measureKeys(scope) {
		rows, err := s.measureSeries(key, scope, "", "", 0)
		if err != nil {
			continue
		}
		if sm := s.measureSummary(key, rows); sm != nil {
			out = append(out, sm)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		bi, bj := out[i].Breach != "", out[j].Breach != ""
		if bi != bj {
			return bi
		}
		return out[i].N > out[j].N
	})
	return out
}

// MeasuresMatching is a folded substring test over key, label and note. Measures are
// deliberately outside the FTS index: putting them there would let a reading outrank a
// fact in memory ranking.
func (s *Store) MeasuresMatching(query, scope string) []*MeasureSummary {
	tokens := measureQueryTokens(query)
	if len(tokens) == 0 {
		return nil
	}
	var hits []*MeasureSummary
	for _, sm := range s.MeasureOverview(scope) {
		hay := lexicon.StripAccents(strings.ToLower(strings.Join(s.measureSearchText(sm), " ")))
		for _, t := range tokens {
			if strings.Contains(hay, t) {
				hits = append(hits, sm)
				break
			}
		}
	}
	return hits
}

// measureQueryTokens folds a query into words of two characters or more; a single letter
// would match nearly every series.
func measureQueryTokens(query string) []string {
	q := lexicon.StripAccents(strings.ToLower(query))
	var tokens []string
	for _, t := range strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if len(t) >= 2 {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

// measureSearchText is everything a search may match for one series: its key, and every
// label and note the series has ever carried, not just the latest point's. Keys are ASCII
// by necessity (phosphate, k_pre, tmp) while the project that owns them may work entirely
// in another language: a search for 'φώσφορος' once missed a 58-point series whose
// labels were Greek, which is the "reads as never-recorded" failure this exists to prevent.
func (s *Store) measureSearchText(sm *MeasureSummary) []string {
	parts := []string{sm.Key, sm.Label}
	rows, err := s.DB.Query("SELECT DISTINCT label, note FROM measures WHERE project=? "+
		"AND key=? AND (label != '' OR note != '') LIMIT 40", s.Env.Project, sm.Key)
	if err != nil {
		return parts
	}
	defer rows.Close()
	for rows.Next() {
		var l, n string
		if err := rows.Scan(&l, &n); err == nil {
			parts = append(parts, l, n)
		}
	}
	return parts
}

// MeasureLine is one series on one line: latest value, the move since the reading before,
// the count, and the bound it crossed when it is outside its band.
func MeasureLine(s *MeasureSummary, prefix string) string {
	mark := ""
	if s.Breach != "" {
		mark = "! "
	}
	line := fmt.Sprintf("%s%s%-22s %s%s", prefix, mark, s.Label, Fm(s.Latest), UnitSuffix(s.Unit))
	if s.Delta != nil {
		line += fmt.Sprintf("  %s %s", s.Arrow, signed(s.Delta))
		if s.PreviousOn != "" {
			line += " since " + s.PreviousOn
		}
		if s.Direction != "" {
			line += " (" + s.Direction + ")"
		}
	}
	line += fmt.Sprintf("   n=%d", s.N)
	if s.Breach != "" {
		bound, _ := breachedBound(s)
		line += fmt.Sprintf("   %s %s", strings.ToUpper(s.Breach), FmtMeasure(bound))
	}
	return line
}

func UnitSuffix(unit string) string {
	if unit == "" {
		return ""
	}
	return " " + unit
}

// signed prints a delta with an explicit + when it rose.
func signed(d *float64) string {
	if *d > 0 {
		return "+" + FmtMeasure(d)
	}
	return FmtMeasure(d)
}

// breachedBound is the bound a breaching summary crossed, and what that bound is called.
func breachedBound(s *MeasureSummary) (*float64, string) {
	if s.Breach == "above" {
		return s.High, "ceiling"
	}
	return s.Low, "floor"
}

type MeasureArgs struct {
	AllowSecret bool
	Sub         string // add | series | list | limit | relabel | forget
	Key         string
	Value       float64
	Label       string
	Unit        string
	Scope       string
	Date        string
	Source      string
	Confidence  string
	Ref         string
	Note        string
	FromDate    string
	ToDate      string
	N           int
	Low         *float64
	High        *float64
	Better      string
	Why         string
	ID          int64
	JSON        bool
}

func (s *Store) Measure(w io.Writer, a MeasureArgs) error {
	switch a.Sub {
	case "add":
		return s.measureAdd(w, a)
	case "series":
		return s.measureShowSeries(w, a)
	case "list":
		return s.measureList(w, a)
	case "limit":
		return s.measureBand(w, a)
	case "forget":
		return s.measureForget(w, a)
	case "relabel":
		return s.measureRelabel(w, a)
	}
	return fmt.Errorf("measure needs one of: add, series, list, limit, relabel, forget")
}

func (s *Store) measureAdd(w io.Writer, a MeasureArgs) error {
	key := NormMeasureKey(a.Key)
	if key == "" {
		return fmt.Errorf("could not read a measure name from %s", pyfmt.Repr(a.Key))
	}
	if a.Scope == "" {
		a.Scope = "global"
	}
	if err := checkNewMeasure(a); err != nil {
		return err
	}
	conf := store.NormConfidence(a.Confidence)
	takenOn, takenAt, err := measureTakenTime(a.Date)
	if err != nil {
		return err
	}
	label := strings.TrimSpace(a.Label)
	if label == "" {
		label = strings.TrimSpace(a.Key)
	}
	source := a.Source
	if source == "" {
		source = "session"
	}
	res, err := s.DB.Exec("INSERT INTO measures(project,scope,key,label,value,unit,"+
		"taken_on,taken_at,source,confidence,ref,note,created_at) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
		s.Env.Project, a.Scope, key, label, a.Value, a.Unit, takenOn, takenAt,
		source, conf, a.Ref, a.Note, timefmt.Now())
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	rows, err := s.measureSeries(key, "", "", "", 0)
	if err != nil {
		return err
	}
	sm := s.measureSummary(key, rows)
	return store.Out(w, a.JSON, measureAddedMessage(id, key, a.Value, a.Unit, takenOn, sm),
		map[string]interface{}{
			"id": id, "key": key, "value": a.Value, "taken_on": takenOn, "summary": sm})
}

// checkNewMeasure is every refusal a new reading can meet, in the order they are checked.
func checkNewMeasure(a MeasureArgs) error {
	if err := textguard.CheckToken(a.Scope, "scope"); err != nil {
		return err
	}
	if err := textguard.CheckLines("--label", a.Label, "the name", a.Key, "--unit", a.Unit,
		"--source", a.Source); err != nil {
		return err
	}
	if err := textguard.GuardText(a.AllowSecret, a.Note, a.Ref, a.Unit, a.Label, a.Source); err != nil {
		return err
	}
	return CheckMeasureValue("the value", &a.Value)
}

// measureTakenTime turns --date into the day a reading was taken and the timestamp that
// orders it. The day is what was asked for; the clock only orders readings within it, so
// a backdated point sorts at the start of its day and still comes before today's, while
// a reading for today takes the current time.
func measureTakenTime(date string) (string, int64, error) {
	takenOn, err := timefmt.ParseDate(date, "--date")
	if err != nil {
		return "", 0, err
	}
	if takenOn == "" {
		takenOn = timefmt.Day(timefmt.Now())
	}
	takenAt, err := timefmt.DayToTS(takenOn)
	if err != nil {
		return "", 0, err
	}
	if takenOn == timefmt.Day(timefmt.Now()) {
		takenAt = timefmt.Now()
	}
	return takenOn, takenAt, nil
}

// measureAddedMessage confirms a new reading, with the series' latest move and a warning
// when the latest reading is outside its band.
func measureAddedMessage(id int64, key string, value float64, unit, takenOn string,
	sm *MeasureSummary) string {
	human := fmt.Sprintf("#%d %s = %s%s on %s", id, key, Fm(value), UnitSuffix(unit), takenOn)
	// The delta belongs to the series' two most recent points, which is the right thing to
	// say when the reading just added IS the most recent one. Backdate a reading behind
	// existing points and it once described a pair the caller never mentioned: adding a
	// 2020 value printed "up 1 since 2026-08-06", which is two other readings' story told
	// against this one's date.
	if sm != nil && sm.Delta != nil && takenOn >= sm.LatestOn {
		human += fmt.Sprintf("  %s %s since %s", sm.Arrow, signed(sm.Delta), sm.PreviousOn)
	} else if sm != nil && sm.Delta != nil {
		human += fmt.Sprintf("  (backdated: the series still runs to %s)", sm.LatestOn)
	}
	if sm != nil && sm.Breach != "" {
		bound, word := breachedBound(sm)
		human += fmt.Sprintf("\n[brain] %s is %s its declared %s of %s", key, sm.Breach,
			word, FmtMeasure(bound))
		if sm.Why != "" {
			human += ": " + sm.Why
		}
	}
	return human
}

func (s *Store) measureShowSeries(w io.Writer, a MeasureArgs) error {
	key := NormMeasureKey(a.Key)
	start, err := timefmt.ParseDate(a.FromDate, "--from")
	if err != nil {
		return err
	}
	end, err := timefmt.ParseDate(a.ToDate, "--to")
	if err != nil {
		return err
	}
	rows, err := s.measureSeries(key, a.Scope, start, end, a.N)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return s.noSuchSeries(w, a, key, start != "" || end != "")
	}
	sm := s.measureSummary(key, rows)
	lines := []string{MeasureLine(sm, "")}
	if sm.Spark != "" {
		lines = append(lines, fmt.Sprintf("  %s   min %s · mean %s · max %s",
			sm.Spark, Fm(sm.Min), Fm(round3(sm.Mean)), Fm(sm.Max)))
	}
	lines = append(lines, "")
	lim := s.measureLimit(key)
	for _, r := range rows {
		lines = append(lines, measurePointLine(r, lim))
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"matched": true, "key": key, "count": len(rows), "summary": sm, "points": rows})
}

// noSuchSeries answers a series with no readings. Fail closed: an unknown key returns
// nothing rather than every series, and names the keys that do exist.
func (s *Store) noSuchSeries(w io.Writer, a MeasureArgs, key string, windowed bool) error {
	known := s.measureKeys(a.Scope)
	hint := "  no measures recorded yet: os-brain measure add <key> <value> --unit ..."
	if len(known) > 0 {
		hint = "  known keys: " + strings.Join(known, ", ")
	}
	window := ""
	if windowed {
		window = " in that window"
	}
	if known == nil {
		known = []string{}
	}
	return store.Out(w, a.JSON, fmt.Sprintf("(no measure %s%s)\n%s", pyfmt.Repr(key), window, hint),
		map[string]interface{}{"matched": false, "key": key, "count": 0,
			"points": []*Measure{}, "known_keys": known})
}

// measurePointLine is one reading of a series listing, flagged when it is outside the band.
func measurePointLine(r *Measure, lim *MeasureLimit) string {
	flag := ""
	if b := measureBreach(lim, r.Value); b != "" {
		flag = "  !" + b
	}
	var extras []string
	for _, x := range []string{r.Ref, r.Note} {
		if x != "" {
			extras = append(extras, x)
		}
	}
	line := fmt.Sprintf("  %s  %10s %-8s%s", r.TakenOn, Fm(r.Value), r.Unit, flag)
	if len(extras) > 0 {
		line += "   " + strings.Join(extras, " · ")
	}
	return line
}

func (s *Store) measureList(w io.Writer, a MeasureArgs) error {
	rows := s.MeasureOverview(a.Scope)
	if len(rows) == 0 {
		return store.Out(w, a.JSON, "(no measures recorded)",
			map[string]interface{}{"count": 0, "measures": []*MeasureSummary{}})
	}
	var lines []string
	breaches := 0
	for _, sm := range rows {
		lines = append(lines, MeasureLine(sm, "  "))
		if sm.Breach != "" {
			breaches++
		}
	}
	if breaches > 0 {
		lines = append(lines, fmt.Sprintf("\n  %d outside its band. os-brain measure "+
			"series <key> for the history.", breaches))
	}
	return store.Out(w, a.JSON, strings.Join(lines, "\n"), map[string]interface{}{
		"count": len(rows), "measures": rows, "breaches": breaches})
}

// measureBand is `measure limit`: with no band flags it shows the band, otherwise it sets
// it. Flags left out keep their stored values, so a band can be adjusted one bound at a
// time.
func (s *Store) measureBand(w io.Writer, a MeasureArgs) error {
	key := NormMeasureKey(a.Key)
	if a.Low == nil && a.High == nil && a.Why == "" && a.Better == "" {
		return s.measureShowBand(w, a, key)
	}
	if err := checkBand(a); err != nil {
		return err
	}
	low, high, better, why := a.Low, a.High, a.Better, a.Why
	if existing := s.measureLimit(key); existing != nil {
		if low == nil {
			low = existing.Low
		}
		if high == nil {
			high = existing.High
		}
		if better == "" {
			better = existing.Better
		}
		if why == "" {
			why = existing.Why
		}
	}
	if _, err := s.DB.Exec("INSERT INTO measure_limits(project,key,low,high,better,unit,"+
		"why,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project,key) DO UPDATE SET "+
		"low=excluded.low, high=excluded.high, better=excluded.better, why=excluded.why, "+
		"updated_at=excluded.updated_at",
		s.Env.Project, key, low, high, better, a.Unit, why, timefmt.Now()); err != nil {
		return err
	}
	rows, err := s.measureSeries(key, "", "", "", 0)
	if err != nil {
		return err
	}
	sm := s.measureSummary(key, rows)
	human := fmt.Sprintf("band for %s: low=%s high=%s", key, FmtMeasure(low), FmtMeasure(high))
	if better != "" {
		human += " better=" + better
	}
	if sm != nil && sm.Breach != "" {
		human += fmt.Sprintf("\n[brain] the latest reading (%s) is %s it",
			Fm(sm.Latest), sm.Breach)
	}
	return store.Out(w, a.JSON, human, map[string]interface{}{
		"key": key, "low": low, "high": high, "better": better})
}

// checkBand is every refusal a new band can meet, in the order they are checked.
func checkBand(a MeasureArgs) error {
	if a.Better != "" && a.Better != "up" && a.Better != "down" {
		return fmt.Errorf("--better must be 'up' or 'down'")
	}
	if err := CheckMeasureValue("--low", a.Low); err != nil {
		return err
	}
	if err := CheckMeasureValue("--high", a.High); err != nil {
		return err
	}
	if a.Low != nil && a.High != nil && *a.Low > *a.High {
		return fmt.Errorf("--low %s is above --high %s", Fm(*a.Low), Fm(*a.High))
	}
	// --why is printed on the brief's breach line, so it is text like any other.
	if err := textguard.CheckLines("--why", a.Why, "--unit", a.Unit); err != nil {
		return err
	}
	return textguard.GuardText(a.AllowSecret, a.Why, a.Unit)
}

func (s *Store) measureShowBand(w io.Writer, a MeasureArgs, key string) error {
	lim := s.measureLimit(key)
	if lim == nil {
		return store.Out(w, a.JSON, fmt.Sprintf("(no band declared for %s)", pyfmt.Repr(key)),
			map[string]interface{}{"matched": false, "key": key})
	}
	better := lim.Better
	if better == "" {
		better = "-"
	}
	human := fmt.Sprintf("%s: low=%s high=%s better=%s", key,
		FmtMeasure(lim.Low), FmtMeasure(lim.High), better)
	if lim.Why != "" {
		human += "  (" + lim.Why + ")"
	}
	return store.Out(w, a.JSON, human, map[string]interface{}{"matched": true,
		"key": key, "low": lim.Low, "high": lim.High, "better": lim.Better,
		"unit": lim.Unit, "why": lim.Why})
}

// ownMeasure loads one reading of this project. Another project's id answers as a missing
// one: naming its owner once enumerated every label in the store, private ones included.
func (s *Store) ownMeasure(id int64) (*Measure, error) {
	row := s.DB.QueryRow("SELECT "+measureCols+" FROM measures WHERE id=? AND project=?",
		id, s.Env.Project)
	m, err := scanMeasure(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no measure #%d", id)
	}
	return m, err
}

func (s *Store) measureForget(w io.Writer, a MeasureArgs) error {
	m, err := s.ownMeasure(a.ID)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec("DELETE FROM measures WHERE id=? AND project=?",
		a.ID, s.Env.Project); err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("forgot measure #%d (%s = %s on %s)",
		a.ID, m.Key, Fm(m.Value), m.TakenOn),
		map[string]interface{}{"id": a.ID, "key": m.Key})
}

// measureRelabel changes the unit of one reading. doctor reports a series that mixes
// units and says "--fix repairs the repairable ones", and there was once no command that
// could change a unit, so the only routes were forget-and-re-add (which discards the
// note, source and provenance the reading was worth keeping for) or raw SQL. A real
// session took the SQL route. A tool that reports a fault and offers no fix teaches
// people to reach past it.
//
// Deliberately relabel, not convert: 3.8 kg and 2.3 L are the same reading written two
// ways only when the caller knows the substance. Guessing a conversion factor would
// silently change data, so this renames the unit and never touches the value.
func (s *Store) measureRelabel(w io.Writer, a MeasureArgs) error {
	m, err := s.ownMeasure(a.ID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(a.Unit) == "" {
		return fmt.Errorf("measure relabel %d needs --unit\n"+
			"  #%d is %s = %s %s on %s\n"+
			"It renames the unit and never converts the value, because only you know "+
			"whether the two units measure the same thing.", a.ID, a.ID, m.Key,
			Fm(m.Value), pyfmt.Repr(m.Unit), m.TakenOn)
	}
	unit := strings.TrimSpace(a.Unit)
	if err := textguard.CheckLine("--unit", unit); err != nil {
		return err
	}
	if err := textguard.GuardText(a.AllowSecret, unit); err != nil {
		return err
	}
	if unit == m.Unit {
		return fmt.Errorf("measure #%d already reads in %s", a.ID, pyfmt.Repr(unit))
	}
	if _, err := s.DB.Exec("UPDATE measures SET unit=? WHERE id=? AND project=?",
		unit, a.ID, s.Env.Project); err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("measure #%d now reads %s %s (was %s); the value is unchanged",
		a.ID, Fm(m.Value), unit, pyfmt.Repr(m.Unit)),
		map[string]interface{}{"id": a.ID, "key": m.Key, "unit": unit,
			"was": m.Unit, "value": m.Value})
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
