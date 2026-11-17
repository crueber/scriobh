package studio

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
)

// The analytics export (F-510): the numbers a person can see on the dashboard, as CSV or Markdown.
//
// It is the person's own data only (the same visibility as every other read: a locked, trashed or foreign project
// contributes nothing), it is deterministic (the same state and the same date give the same bytes: nothing in the
// file is the time it was made), its size is bounded by the range (at most ten years of days, a few hundred weeks),
// and a CSV cell that is text is made safe for a spreadsheet: a cell that begins with =, +, - or @ (or a tab or a
// carriage return) gets a leading apostrophe, so it is read as text and never as a formula. Numbers are written as
// numbers and are not touched, which keeps a negative net-words figure a number: it cannot be a formula, and
// neutralising it would turn the column into text.

// maxExportBytes bounds the file; the ranges below keep it far under.
const maxExportBytes = 2 << 20

// ExportIn asks for an export.
type ExportIn struct {
	Format string `query:"format" enum:"csv,md" desc:"csv (default) or md."`
	Table  string `query:"table" enum:"daily,weekly,goals,sprints,projects" desc:"CSV only: which table. Default daily. A Markdown export holds all of them."`
	Range  string `query:"range" enum:"30d,90d,365d,all" desc:"The days covered. Default 90d."`
	From   string `query:"from" desc:"A custom range: YYYY-MM-DD, with to. At most ten years."`
	To     string `query:"to"`
}

// ExportOut is the file. Over REST it is the file itself with a safe Content-Disposition; elsewhere it is this JSON.
type ExportOut struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	Body        []byte `json:"body" desc:"The file. The REST route returns it as the response body instead of JSON."`
}

// Download makes ExportOut an ops.Download.
func (o ExportOut) Download() (string, string, []byte) { return o.Filename, o.ContentType, o.Body }

// csvText is a text cell: neutralised against formulas, then quoted if it needs it.
func csvText(s string) string {
	if s != "" {
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			s = "'" + s
		}
	}
	return csvQuote(s)
}

func csvQuote(s string) string {
	if strings.ContainsAny(s, ",\"\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func csvNum(n int) string { return strconv.Itoa(n) }

type csvOut struct{ b bytes.Buffer }

func (w *csvOut) row(cells ...string) {
	w.b.WriteString(strings.Join(cells, ","))
	w.b.WriteString("\r\n")
}

func export(c *ops.Ctx, in ExportIn) (ExportOut, error) {
	format := in.Format
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "md" {
		return ExportOut{}, ops.Invalid("format", "The format is csv or md.")
	}
	table := in.Table
	if table == "" {
		table = "daily"
	}
	if !oneOf([]string{"daily", "weekly", "goals", "sprints", "projects"}, table) {
		return ExportOut{}, ops.Invalid("table", "The table is daily, weekly, goals, sprints or projects.")
	}
	rng := in.Range
	if rng == "" && in.From == "" {
		rng = "90d"
	}
	v, err := newViewer(c)
	if err != nil {
		return ExportOut{}, err
	}
	s, err := v.snapshot()
	if err != nil {
		return ExportOut{}, err
	}
	r, from, to, err := s.resolveRange(rng, in.From, in.To)
	if err != nil {
		return ExportOut{}, err
	}
	_ = r
	var body []byte
	name := fmt.Sprintf("scriobh-writing-%s-%s-%s", table, from, to)
	if format == "md" {
		name = fmt.Sprintf("scriobh-writing-%s-%s", from, to)
		body, err = s.markdown(from, to)
	} else {
		body, err = s.csvTable(table, from, to)
	}
	if err != nil {
		return ExportOut{}, err
	}
	if len(body) > maxExportBytes {
		return ExportOut{}, ops.Invalid("range", "That is too much for one file. Ask for a shorter range.")
	}
	out := ExportOut{Filename: name + "." + format, Bytes: len(body), Body: body, ContentType: "text/csv; charset=utf-8"}
	if format == "md" {
		out.ContentType = "text/markdown; charset=utf-8"
	}
	return out, nil
}

func (s *snapshot) csvTable(table, from, to string) ([]byte, error) {
	var w csvOut
	switch table {
	case "daily":
		w.row("day", "words_added", "net_words", "minutes", "sessions", "goal_met")
		for d := from; d <= to; d = addDays(d, 1) {
			x := s.all.Days[d]
			met := ""
			if s.hits[d] {
				met = "1"
			}
			w.row(d, csvNum(x.Words), csvNum(x.Net), csvNum(minutesOf(x.Seconds)), csvNum(x.Sessions), met)
		}
	case "weekly":
		w.row("week_start", "words_added", "net_words", "minutes", "sessions", "writing_days")
		for ws := weekStart(from); ws <= to; ws = addDays(ws, 7) {
			f := figuresOf(s.all.Days, max(ws, from), min(addDays(ws, 6), to))
			w.row(ws, csvNum(f.Words), csvNum(f.Net), csvNum(f.Minutes), csvNum(f.Sessions), csvNum(f.WritingDays))
		}
	case "goals":
		gs, err := s.goalsOut(true)
		if err != nil {
			return nil, err
		}
		w.row("period", "measure", "target", "value", "percent", "status", "start_date", "deadline", "project")
		for _, g := range gs.Items {
			w.row(csvText(g.Period), csvText(g.Measure), csvNum(g.Target), csvNum(g.Value), csvNum(g.Percent), csvText(g.Status),
				g.Start, g.Deadline, csvText(g.ProjectTitle))
		}
	case "sprints":
		all, err := loadSprints(s.v, from, to)
		if err != nil {
			return nil, err
		}
		infos, err := s.v.projectInfos(s.v.IDs)
		if err != nil {
			return nil, err
		}
		w.row("day", "started_at", "planned_minutes", "minutes", "words_added", "net_words", "project")
		for _, o := range all {
			w.row(o.Day, o.StartedAt, csvNum(o.PlannedMinutes), csvNum(minutesOf(o.ElapsedSecs)), csvNum(o.WordsAdded), csvNum(o.WordsNet), csvText(infos[o.ProjectID].Title))
		}
	case "projects":
		rows, err := s.projectRows()
		if err != nil {
			return nil, err
		}
		w.row("project", "today", "this_week", "previous_week", "this_week_net")
		for _, p := range rows {
			w.row(csvText(p.Title), csvNum(p.Today), csvNum(p.Week), csvNum(p.PreviousWeek), csvNum(p.WeekNet))
		}
	}
	return w.b.Bytes(), nil
}

// mdText makes a name safe to put in a Markdown table cell or line: no line breaks, and nothing that could be read as
// markup or a link.
func mdText(s string) string {
	r := strings.NewReplacer("\r", " ", "\n", " ", "|", `\|`, "\\", `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "<", "&lt;", ">", "&gt;", "#", `\#`)
	return strings.TrimSpace(r.Replace(s))
}

func (s *snapshot) markdown(from, to string) ([]byte, error) {
	var b bytes.Buffer
	days := s.all.Days
	fmt.Fprintf(&b, "# Writing, %s to %s\n\n", from, to)
	f := figuresOf(days, from, to)
	fmt.Fprintf(&b, "Words added are what goals count; net words are the growth of the text and are never held against a day. Days are cut in the zone %s.\n\n", s.v.Zone)
	fmt.Fprintf(&b, "- Words added: %d\n- Net words: %d\n- Minutes: %d\n- Sessions: %d\n- Writing days: %d\n- Current streak: %d days (longest %d)\n\n", f.Words, f.Net, f.Minutes, f.Sessions, f.WritingDays, s.streak, s.longest)

	b.WriteString("## Periods\n\n| Period | Words added | Previous | Change | Net words | Minutes |\n| --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, c := range cards(days, s.v.Today) {
		// as on the screens: a percentage is said of a gain only, and today, which is not over, is not set against a whole day
		change, pc := fmt.Sprint(c.Delta.Change), ""
		if c.Delta.Percent != nil && *c.Delta.Percent > 0 && c.Key != "today" {
			pc = fmt.Sprintf(" (%d%%)", *c.Delta.Percent)
		}
		if c.Key == "today" && c.Delta.Change < 0 {
			change = ""
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s%s | %d | %d |\n", map[string]string{"today": "Today", "7d": "7 days", "30d": "30 days", "365d": "365 days"}[c.Key],
			c.Words, c.Previous.Words, change, pc, c.Net, c.Minutes)
	}

	gs, err := s.goalsOut(true)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "\n## Goals\n\n%d active, %d completed.\n\n", gs.Active, gs.Completed)
	if len(gs.Items) > 0 {
		b.WriteString("| Goal | Target | Now | Percent | Status |\n| --- | ---: | ---: | ---: | --- |\n")
		for _, g := range gs.Items {
			scope := "every project"
			if g.ProjectTitle != "" {
				scope = mdText(g.ProjectTitle)
			}
			fmt.Fprintf(&b, "| %s %s, %s | %d | %d | %d | %s |\n", g.Period, g.Measure, scope, g.Target, g.Value, g.Percent, g.Status)
		}
	}

	wd, _ := weekdays(days, from, to)
	b.WriteString("\n## Days of the week\n\n| Day | Words added | Writing days |\n| --- | ---: | ---: |\n")
	for i, n := range []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"} {
		fmt.Fprintf(&b, "| %s | %d | %d |\n", n, wd[i].Words, wd[i].WritingDays)
	}

	b.WriteString("\n## Days\n\n| Day | Words added | Net words | Minutes |\n| --- | ---: | ---: | ---: |\n")
	for d := from; d <= to; d = addDays(d, 1) {
		x := days[d]
		if x.Words == 0 && x.Net == 0 && x.Seconds == 0 {
			continue // an empty day is simply not listed
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", d, x.Words, x.Net, minutesOf(x.Seconds))
	}
	return b.Bytes(), nil
}
