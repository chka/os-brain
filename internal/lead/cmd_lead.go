package lead

import (
	"database/sql"
	"fmt"
	"io"
	"strings"

	"github.com/chka/os-brain/internal/pyfmt"
	"github.com/chka/os-brain/internal/store"
	"github.com/chka/os-brain/internal/textguard"
	"github.com/chka/os-brain/internal/timefmt"
)

// Leads are a small sales pipeline kept beside a project's memories: who, at which
// company, where they are in the funnel, what they might be worth and what to do next.
// The brief shows the hot ones and the next actions, and `lead` manages the rows.

// LeadStatuses is the funnel in order, and the only statuses a lead may have.
var LeadStatuses = []string{
	"new", "researching", "contacted", "replied", "engaged",
	"demo", "trial", "negotiating", "won", "lost", "skip",
}

// LeadHotStatuses is the middle of the funnel: somebody who answered and has not yet won
// or gone away. LeadClosedStatuses is the other end, where a next action is whatever
// closed the row rather than work still owed.
//
// Both are named here rather than written into the brief's SQL because they decide what
// a lead MEANS to a brief, and the funnel above is where a new status would be added.
var LeadHotStatuses = []string{"replied", "engaged", "demo", "trial", "negotiating"}

var LeadClosedStatuses = []string{"won", "lost", "skip"}

// AnyOf widens a string list into query arguments, for the IN clauses these two feed.
func AnyOf(v []string) []interface{} {
	out := make([]interface{}, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

type Lead struct {
	ID         int64  `json:"id"`
	Project    string `json:"project"`
	Name       string `json:"name"`
	Company    string `json:"company"`
	Role       string `json:"role"`
	Channel    string `json:"channel"`
	URL        string `json:"url"`
	Email      string `json:"email"`
	Segment    string `json:"segment"`
	Status     string `json:"status"`
	Tier       string `json:"tier"`
	ValueEst   int64  `json:"value_est"`
	NextAction string `json:"next_action"`
	NextDate   string `json:"next_date"`
	Notes      string `json:"notes"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

const leadCols = `id, project, name, company, role, channel, url, email, segment, status,
	tier, value_est, next_action, next_date, notes, created_at, updated_at`

func scanLead(s interface{ Scan(...interface{}) error }) (*Lead, error) {
	var l Lead
	err := s.Scan(&l.ID, &l.Project, &l.Name, &l.Company, &l.Role, &l.Channel, &l.URL,
		&l.Email, &l.Segment, &l.Status, &l.Tier, &l.ValueEst, &l.NextAction, &l.NextDate,
		&l.Notes, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// LeadArgs uses pointers for the fields `lead update` may change, so an absent flag
// leaves the column alone while a flag given as "" clears it.
type LeadArgs struct {
	Sub        string // add | list | show | update | pipeline | hot | next
	ID         int64
	Name       string
	Company    string
	Role       *string
	Channel    string
	URL        *string
	Email      *string
	Segment    *string
	Status     *string
	Tier       *string
	ValueEst   *int64
	NextAction *string
	NextDate   *string
	Notes      *string
	AppendNote string
	N          int
	JSON       bool
}

// Comma groups thousands with commas (1234567 -> "1,234,567") for money totals.
func Comma(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}

func (s *Store) Lead(w io.Writer, a LeadArgs) error {
	if a.N <= 0 {
		a.N = 50
	}
	switch a.Sub {
	case "add":
		return s.leadAdd(w, a)
	case "list":
		return s.leadList(w, a)
	case "show":
		return s.leadShow(w, a)
	case "update":
		return s.leadUpdate(w, a)
	case "pipeline":
		return s.leadPipeline(w, a)
	case "hot":
		ls, err := s.HotLeads()
		if err != nil {
			return err
		}
		return store.Out(w, a.JSON, renderLeadRows("hot leads", ls, RenderHotLead), ls)
	case "next":
		ls, err := s.LeadsWithNextAction()
		if err != nil {
			return err
		}
		return store.Out(w, a.JSON, renderLeadRows("next actions", ls, RenderNextAction), ls)
	}
	return fmt.Errorf("lead needs one of: add, list, show, update, pipeline, hot, next")
}

func checkLeadStatus(status string) error {
	if store.Contains(LeadStatuses, status) {
		return nil
	}
	return fmt.Errorf("unknown status %s. valid: %s", pyfmt.Repr(status),
		strings.Join(LeadStatuses, ", "))
}

func (s *Store) leadAdd(w io.Writer, a LeadArgs) error {
	// Leads hold contact details, so a secret in them is refused outright: there is no
	// --allow-secret for this command.
	if err := textguard.GuardTextNoOverride(store.Str(a.Notes), store.Str(a.NextAction), store.Str(a.Email), store.Str(a.URL)); err != nil {
		return err
	}
	status := store.Str(a.Status)
	if status == "" {
		status = "new"
	}
	if err := checkLeadStatus(status); err != nil {
		return err
	}
	ts := timefmt.Now()
	var val int64
	if a.ValueEst != nil {
		val = *a.ValueEst
	}
	res, err := s.DB.Exec("INSERT INTO leads(project,name,company,role,channel,url,email,"+
		"segment,status,tier,value_est,next_action,next_date,notes,created_at,updated_at) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		s.Env.Project, a.Name, a.Company, store.Str(a.Role), a.Channel, store.Str(a.URL), store.Str(a.Email),
		store.Str(a.Segment), status, store.Str(a.Tier), val, store.Str(a.NextAction), store.Str(a.NextDate),
		store.Str(a.Notes), ts, ts)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	return store.Out(w, a.JSON, fmt.Sprintf("lead #%d added: %s @ %s [%s]", id, a.Name,
		a.Company, status), map[string]interface{}{"id": id})
}

// leadList lists the most valuable leads first, optionally narrowed by status and
// segment, with the pipeline total of the rows shown.
func (s *Store) leadList(w io.Writer, a LeadArgs) error {
	where, params := "project=?", []interface{}{s.Env.Project}
	if a.Status != nil && *a.Status != "" {
		where += " AND status=?"
		params = append(params, *a.Status)
	}
	if a.Segment != nil && *a.Segment != "" {
		where += " AND segment=?"
		params = append(params, *a.Segment)
	}
	rows, err := s.DB.Query("SELECT "+leadCols+" FROM leads WHERE "+where+
		" ORDER BY value_est DESC, updated_at DESC LIMIT ?", append(params, a.N)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	ls := []*Lead{}
	var lines []string
	var total int64
	for rows.Next() {
		l, err := scanLead(rows)
		if err != nil {
			return err
		}
		ls = append(ls, l)
		total += l.ValueEst
		lines = append(lines, renderLeadLine(l))
	}
	body := "(no leads)"
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
	}
	return store.Out(w, a.JSON, fmt.Sprintf("%s\n%d leads, pipeline value $%s", body,
		len(ls), Comma(total)), ls)
}

// ownLead loads one lead of this project; another project's id answers as missing.
func (s *Store) ownLead(id int64) (*Lead, error) {
	row := s.DB.QueryRow("SELECT "+leadCols+" FROM leads WHERE id=? AND project=?", id,
		s.Env.Project)
	l, err := scanLead(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no lead #%d", id)
	}
	return l, err
}

func (s *Store) leadShow(w io.Writer, a LeadArgs) error {
	l, err := s.ownLead(a.ID)
	if err != nil {
		return err
	}
	return store.Out(w, a.JSON, renderLeadFields(l), l)
}

// renderLeadFields is every column of a lead, one per line, name then value.
func renderLeadFields(l *Lead) string {
	return strings.Join([]string{
		fmt.Sprintf("%-12s %d", "id", l.ID),
		fmt.Sprintf("%-12s %s", "project", l.Project),
		fmt.Sprintf("%-12s %s", "name", l.Name),
		fmt.Sprintf("%-12s %s", "company", l.Company),
		fmt.Sprintf("%-12s %s", "role", l.Role),
		fmt.Sprintf("%-12s %s", "channel", l.Channel),
		fmt.Sprintf("%-12s %s", "url", l.URL),
		fmt.Sprintf("%-12s %s", "email", l.Email),
		fmt.Sprintf("%-12s %s", "segment", l.Segment),
		fmt.Sprintf("%-12s %s", "status", l.Status),
		fmt.Sprintf("%-12s %s", "tier", l.Tier),
		fmt.Sprintf("%-12s %d", "value_est", l.ValueEst),
		fmt.Sprintf("%-12s %s", "next_action", l.NextAction),
		fmt.Sprintf("%-12s %s", "next_date", l.NextDate),
		fmt.Sprintf("%-12s %s", "notes", l.Notes),
		fmt.Sprintf("%-12s %d", "created_at", l.CreatedAt),
		fmt.Sprintf("%-12s %d", "updated_at", l.UpdatedAt),
	}, "\n")
}

func (s *Store) leadUpdate(w io.Writer, a LeadArgs) error {
	l, err := s.ownLead(a.ID)
	if err != nil {
		return err
	}
	if err := textguard.GuardTextNoOverride(store.Str(a.Notes), a.AppendNote, store.Str(a.NextAction),
		store.Str(a.Email), store.Str(a.URL)); err != nil {
		return err
	}
	if a.Status != nil {
		if err := checkLeadStatus(*a.Status); err != nil {
			return err
		}
	}
	fields, vals := leadChanges(a, l.Notes)
	if len(fields) == 0 {
		return fmt.Errorf("nothing to update")
	}
	fields, vals = append(fields, "updated_at=?"), append(vals, timefmt.Now())
	vals = append(vals, a.ID, s.Env.Project)
	if _, err := s.DB.Exec("UPDATE leads SET "+strings.Join(fields, ", ")+
		" WHERE id=? AND project=?", vals...); err != nil {
		return err
	}
	return store.Out(w, a.JSON, fmt.Sprintf("lead #%d updated", a.ID),
		map[string]interface{}{"id": a.ID, "updated": true})
}

// leadChanges is the SET list for `lead update`: one "col=?" and its value for every flag
// given. --append-note adds a timestamped line under the existing notes; given with
// --notes as well, both assignments are sent and the appended one, listed last, is what
// SQLite keeps.
func leadChanges(a LeadArgs, notes string) ([]string, []interface{}) {
	var fields []string
	var vals []interface{}
	for _, f := range []struct {
		col string
		val *string
	}{
		{"status", a.Status}, {"tier", a.Tier}, {"next_action", a.NextAction},
		{"next_date", a.NextDate}, {"notes", a.Notes}, {"email", a.Email},
		{"url", a.URL}, {"segment", a.Segment}, {"role", a.Role},
	} {
		if f.val != nil {
			fields, vals = append(fields, f.col+"=?"), append(vals, *f.val)
		}
	}
	if a.ValueEst != nil {
		fields, vals = append(fields, "value_est=?"), append(vals, *a.ValueEst)
	}
	if a.AppendNote != "" {
		prefix := ""
		if notes != "" {
			prefix = notes + "\n"
		}
		appended := prefix + fmt.Sprintf("[%s] %s", timefmt.ISO(timefmt.Now()), a.AppendNote)
		fields, vals = append(fields, "notes=?"), append(vals, appended)
	}
	return fields, vals
}

func (s *Store) leadPipeline(w io.Writer, a LeadArgs) error {
	rows, err := s.PipelineByStatus()
	if err != nil {
		return err
	}
	var count, value int64
	for _, r := range rows {
		count, value = count+r.Count, value+r.Value
	}
	return store.Out(w, a.JSON, renderPipeline(rows, count, value),
		map[string]interface{}{"rows": rows, "count": count, "value": value})
}

// renderPipeline is the count and value of leads per status, with the total.
func renderPipeline(rows []PipelineRow, count, value int64) string {
	if len(rows) == 0 {
		return "(no leads)"
	}
	lines := make([]string, 0, len(rows)+1)
	for _, p := range rows {
		lines = append(lines, fmt.Sprintf("  %-12s %3d   $%s", p.Status, p.Count, Comma(p.Value)))
	}
	lines = append(lines, fmt.Sprintf("%d leads, pipeline value $%s", count, Comma(value)))
	return strings.Join(lines, "\n")
}

// RenderHotLead and RenderNextAction are the brief's lead lines, and the same lines
// `lead hot` and `lead next` print, so the command and the brief never disagree.
func RenderHotLead(l *Lead) string {
	return fmt.Sprintf("  #%d [%s] %s @ %s $%s | %s", l.ID, l.Status, l.Name, l.Company,
		Comma(l.ValueEst), l.NextAction)
}

func RenderNextAction(l *Lead) string {
	line := fmt.Sprintf("  #%d %s @ %s: %s", l.ID, l.Name, l.Company, l.NextAction)
	if l.NextDate != "" {
		line += fmt.Sprintf(" (%s)", l.NextDate)
	}
	return line
}

func renderLeadRows(what string, ls []*Lead, one func(*Lead) string) string {
	if len(ls) == 0 {
		return "(no " + what + ")"
	}
	lines := make([]string, 0, len(ls))
	for _, l := range ls {
		lines = append(lines, one(l))
	}
	return strings.Join(lines, "\n")
}

// renderLeadLine is one lead as a `lead list` row. The segment says what kind of lead it
// is; without one, the channel it came through stands in.
func renderLeadLine(l *Lead) string {
	v := "-"
	if l.ValueEst != 0 {
		v = "$" + Comma(l.ValueEst)
	}
	na := ""
	if l.NextAction != "" {
		na = " | next: " + l.NextAction
		if l.NextDate != "" {
			na += " (" + l.NextDate + ")"
		}
	}
	seg := l.Segment
	if seg == "" {
		seg = l.Channel
	}
	return fmt.Sprintf("#%d [%s] %s @ %s (%s) %s%s",
		l.ID, l.Status, l.Name, l.Company, seg, v, na)
}

// PipelineRow is one status bucket of the sales pipeline. The JSON keys are the column
// aliases of the query (`c` and `v`), which is what ns-brain has always emitted.
type PipelineRow struct {
	Status string `json:"status"`
	Count  int64  `json:"c"`
	Value  int64  `json:"v"`
}

func (s *Store) PipelineByStatus() ([]PipelineRow, error) {
	rows, err := s.DB.Query("SELECT status, COUNT(*) c, COALESCE(SUM(value_est),0) v "+
		"FROM leads WHERE project=? GROUP BY status ORDER BY status", s.Env.Project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PipelineRow{}
	for rows.Next() {
		var p PipelineRow
		if err := rows.Scan(&p.Status, &p.Count, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) leadsQuery(where string, args ...interface{}) ([]*Lead, error) {
	rows, err := s.DB.Query("SELECT "+leadCols+" FROM leads WHERE project=? "+where,
		append([]interface{}{s.Env.Project}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Lead{}
	for rows.Next() {
		l, err := scanLead(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// How many hot leads and next actions the brief shows. They are the brief's limits, kept
// here beside the queries they bound, and they match ns-brain's local edition for the same
// reason as the brief's other limits: a brain moved between the two must brief the same.
const (
	briefHotLeads    = 10
	briefNextActions = 12
)

func (s *Store) HotLeads() ([]*Lead, error) {
	return s.leadsQuery("AND status IN ("+store.Placeholders(len(LeadHotStatuses))+") "+
		"ORDER BY value_est DESC LIMIT ?",
		append(AnyOf(LeadHotStatuses), briefHotLeads)...)
}

func (s *Store) LeadsWithNextAction() ([]*Lead, error) {
	return s.leadsQuery("AND next_action != '' AND status NOT IN ("+
		store.Placeholders(len(LeadClosedStatuses))+") ORDER BY next_date LIMIT ?",
		append(AnyOf(LeadClosedStatuses), briefNextActions)...)
}
