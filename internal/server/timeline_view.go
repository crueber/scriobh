package server

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/storydate"
	"github.com/crueber/scriobh/internal/domain/timeline"
)

// The pure parts of the timeline screens: the story-time strip, the plain-words rendering of a check, and the little
// text helpers the templates use. Nothing here reads the database or the request, so each is tested on its own.

const (
	tlPageSize = 50 // rows of the table on one page
	tlBuckets  = 12 // blocks of the strip, at most: the strip aggregates, the table lists
	tlLevels   = 8  // heights a block can have (a CSS class each)
)

// tlWhen writes a story date and its end the way the pages show them.
func tlWhen(date, end string) string {
	switch {
	case date == "":
		return "Undated"
	case end != "":
		return date + " to " + end
	}
	return date
}

func tlCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return commas(n) + " " + many
}

// ---- the strip --------------------------------------------------------------------------------------------------------

// tlPoint is one thing on the strip: an event, or a dated scene no event covers.
type tlPoint struct {
	Date  string
	Scene bool
	Title string
}

// tlBucket is one block of the strip: a stretch of story time and what starts in it.
type tlBucket struct {
	Name           string // "1998 to 1999", for screen readers and the table
	Label          string // the start, shown under the block
	From, To       string // the dates that list exactly this stretch in the table
	Events, Scenes int
	LevE, LevS     int // 0 to tlLevels: how tall the block is drawn
	Titles         string
	Href           string // empty when nothing starts here
	Text           string // the sentence a screen reader gets for the block
}

// tlStrip is the strip and its table alternative: the same blocks, as rows.
type tlStrip struct {
	Buckets       []tlBucket
	Dated         int
	HasScenes     bool
	Undated       int
	UndatedScenes int
	Unit          string
	Summary       string
	First, Last   string
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// tlIdx places a date on a line whose unit is a year, a month or a day. Days use a 372-day year of 31-day months:
// the numbers only have to be in order and invertible, and tlStr clamps to a real day.
func tlIdx(unit byte, d storydate.Date) int {
	m, day := max(d.Month, 1), max(d.Day, 1)
	switch unit {
	case 'y':
		return d.Year
	case 'm':
		return d.Year*12 + m - 1
	}
	return d.Year*372 + (m-1)*31 + day - 1
}

func tlStr(unit byte, i int) string {
	switch unit {
	case 'y':
		return storydate.Date{Year: i, Prec: storydate.Year}.String()
	case 'm':
		y := floorDiv(i, 12)
		return storydate.Date{Year: y, Month: i - y*12 + 1, Prec: storydate.Month}.String()
	}
	y := floorDiv(i, 372)
	r := i - y*372
	m := r/31 + 1
	return storydate.Date{Year: y, Month: m, Day: min(r%31+1, storydate.DaysIn(y, m)), Prec: storydate.Day}.String()
}

// buildStrip lays the points out in at most tlBuckets blocks of equal length. href names the table filter for a
// stretch (from and to are partial dates the timeline list understands).
func buildStrip(points []tlPoint, href func(from, to string) string) tlStrip {
	st := tlStrip{}
	type dp struct {
		d storydate.Date
		p tlPoint
	}
	var dated []dp
	for _, p := range points {
		d, ok := storydate.Parse(p.Date)
		if !ok {
			st.Undated++
			if p.Scene {
				st.UndatedScenes++
			}
			continue
		}
		dated = append(dated, dp{d, p})
	}
	st.Dated = len(dated)
	if len(dated) == 0 {
		st.Summary = "Nothing on this timeline has a date yet."
		if st.Undated > 0 {
			st.Summary = tlCount(st.Undated, "item has", "items have") + " no date yet, so there is nothing to place on a line."
		}
		return st
	}
	minM, maxM := 1<<30, -(1 << 30)
	allYear, allMonth := true, true
	for _, x := range dated {
		mm := x.d.Year*12 + max(x.d.Month, 1) - 1
		minM, maxM = min(minM, mm), max(maxM, mm)
		allYear = allYear && x.d.Prec == storydate.Year
		allMonth = allMonth && x.d.Prec <= storydate.Month
	}
	unit := byte('d')
	switch span := maxM - minM; {
	case span >= 48 || allYear:
		unit = 'y'
	case span >= 1 || allMonth:
		unit = 'm'
	}
	st.Unit = map[byte]string{'y': "years", 'm': "months", 'd': "days"}[unit]
	lo, hi := 1<<30, -(1 << 30)
	for _, x := range dated {
		i := tlIdx(unit, x.d)
		lo, hi = min(lo, i), max(hi, i)
	}
	step := max(1, (hi-lo+tlBuckets)/tlBuckets)
	n := (hi-lo)/step + 1
	st.Buckets = make([]tlBucket, n)
	titles := make([][]string, n)
	for _, x := range dated {
		b := (tlIdx(unit, x.d) - lo) / step
		if x.p.Scene {
			st.Buckets[b].Scenes++
			st.HasScenes = true
		} else {
			st.Buckets[b].Events++
		}
		titles[b] = append(titles[b], x.p.Title)
	}
	peak := 0
	for _, b := range st.Buckets {
		peak = max(peak, b.Events+b.Scenes)
	}
	level := func(c int) int {
		if c == 0 {
			return 0
		}
		return min(tlLevels, (c*tlLevels+peak-1)/peak)
	}
	for i := range st.Buckets {
		b := &st.Buckets[i]
		s, e := lo+i*step, min(lo+(i+1)*step-1, hi)
		b.From, b.To, b.Label = tlStr(unit, s), tlStr(unit, e), tlStr(unit, s)
		b.Name = b.Label
		if e > s {
			b.Name += " to " + b.To
		}
		b.LevE, b.LevS = level(b.Events), level(b.Scenes)
		var parts []string
		if b.Events > 0 {
			parts = append(parts, tlCount(b.Events, "event starts", "events start"))
		}
		if b.Scenes > 0 {
			parts = append(parts, tlCount(b.Scenes, "scene starts", "scenes start"))
		}
		if len(parts) == 0 {
			b.Text = b.Name + ": nothing starts here."
			continue
		}
		show := titles[i]
		more := 0
		if len(show) > 3 {
			show, more = show[:3], len(show)-3
		}
		b.Titles = strings.Join(show, ", ")
		if more > 0 {
			b.Titles += " and " + strconv.Itoa(more) + " more"
		}
		b.Text = b.Name + ": " + strings.Join(parts, ", ") + ". " + b.Titles + "."
		b.Href = href(b.From, b.To)
	}
	st.First, st.Last = st.Buckets[0].From, st.Buckets[n-1].To
	st.Summary = tlCount(st.Dated, "dated item", "dated items") + " from " + st.First + " to " + st.Last + ", in blocks of " + tlStepName(step, unit)
	if st.Undated > 0 {
		st.Summary += "; " + strconv.Itoa(st.Undated) + " with no date are left off the line"
	}
	st.Summary += "."
	return st
}

func tlStepName(step int, unit byte) string {
	one := map[byte]string{'y': "year", 'm': "month", 'd': "day"}[unit]
	if step == 1 {
		return "one " + one
	}
	return strconv.Itoa(step) + " " + one + "s"
}

// tlRange is the table filter for a block, as a query string.
func tlRange(base url.Values, from, to string) string {
	q := url.Values{}
	for k, v := range base {
		if k != "from" && k != "to" && k != "offset" && k != "page" {
			q[k] = v
		}
	}
	q.Set("from", from)
	q.Set("to", to)
	return "?" + q.Encode() + "#tl-events"
}

// ---- the check, in plain words ----------------------------------------------------------------------------------------

// tlLink is something a finding points at: an event, a scene or an entity.
type tlLink struct {
	Kind string // event, scene or entity
	Text string
	Href string
}

// tlFinding is one finding as the page shows it.
type tlFinding struct {
	ID       string
	Kind     string
	Label    string // "Two places at once"
	Severity string // error or warning
	SevText  string // "Error" or "Warning": the words that carry what the colour also says
	Message  string // the op's own sentence
	Explain  string // what the rule looks at, and what to do about it
	Links    []tlLink
}

// tlCheck is the check panel.
type tlCheck struct {
	Findings []tlFinding
	Errs     []tlFinding // the errors, then
	Warns    []tlFinding // the warnings, each in the order the op gave
	Errors   int
	Warnings int
	Events   int
	Scenes   int
	Hidden   bool
	Indexing bool
	Project  bool // a book's check; otherwise a Compendium's
	Headline string
	Empty    string
}

var tlKindText = map[string][2]string{
	"location_conflict":        {"Two places at once", "The same person is placed at two different places during overlapping, definite times. If that cannot be so, change one event's date, place or people."},
	"before_birth":             {"Before a birth", "An event a person takes part in happens entirely before the birth date on their entity sheet. Check the event's date or the birth date."},
	"after_death":              {"After a death", "An event a person takes part in happens entirely after the death date on their entity sheet. Check the event's date or the death date."},
	"invalid_range":            {"A scene ends before it starts", "The story date a scene ends on is earlier than the one it starts on. Correct the dates on the scene."},
	"narrated_out_of_order":    {"Told out of order", "This book tells one thing after something that happens later. That may be a flashback you meant, so it is only a warning. If it was not meant, change the order the book tells its events in, or the order of its scenes."},
	"scene_before_first_event": {"Before a first appearance", "A scene is dated earlier than the first recorded event of a person it features. Check the scene's date, or record an earlier event for the person."},
	"unreadable_date":          {"Dates the timeline cannot read", "These scenes have a story date the timeline cannot read, so they are left off it. Write a year, a month or a day, such as 1998, 1998-04 or 1998-04-12."},
}

var reSceneCount = regexp.MustCompile(`^(\d+) scene\(s\) have `)

// tlMessage tidies the one sentence the op writes with a count in it.
func tlMessage(kind, msg string) string {
	if kind != "unreadable_date" {
		return msg
	}
	if m := reSceneCount.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n == 1 {
			return strings.Replace(reSceneCount.ReplaceAllString(msg, "1 scene has "), "so they are left off it", "so it is left off the timeline", 1)
		}
		return strings.Replace(reSceneCount.ReplaceAllString(msg, m[1]+" scenes have "), "so they are left off it", "so they are left off the timeline", 1)
	}
	return msg
}

// tlLinker says where the links of a finding go.
type tlLinker struct {
	ProjectSlug string // the book whose scenes open; empty on a Compendium's check
	EntityBase  string // the timeline page an entity filters, such as /c/the-salt-road/timeline
}

func (l tlLinker) event(id string) string { return "/timeline/events/" + url.PathEscape(id) }
func (l tlLinker) scene(id string) string {
	if l.ProjectSlug == "" {
		return ""
	}
	return "/p/" + url.PathEscape(l.ProjectSlug) + "/d/" + url.PathEscape(id)
}
func (l tlLinker) entity(id string) string {
	return l.EntityBase + "?entity=" + url.QueryEscape(id) + "#tl-events"
}

func tlHumanKind(k string) string {
	s := strings.ReplaceAll(k, "_", " ")
	if s == "" {
		return "Finding"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// buildCheck turns a check result into what the page shows. Errors come first, then warnings, as the op orders them.
func buildCheck(out timeline.CheckOut, l tlLinker, project bool) tlCheck {
	c := tlCheck{Errors: out.Errors, Warnings: out.Warnings, Events: out.Events, Scenes: out.Scenes, Hidden: out.Hidden, Indexing: out.Indexing, Project: project}
	for _, f := range out.Findings {
		tf := tlFinding{ID: f.ID, Kind: f.Kind, Severity: f.Severity, Message: tlMessage(f.Kind, f.Message)}
		if t, ok := tlKindText[f.Kind]; ok {
			tf.Label, tf.Explain = t[0], t[1]
		} else {
			tf.Label = tlHumanKind(f.Kind)
		}
		tf.SevText = "Warning"
		if f.Severity == "error" {
			tf.SevText = "Error"
		}
		for _, e := range f.Events {
			tf.Links = append(tf.Links, tlLink{Kind: "event", Text: e.Title + tlDateNote(e.StoryDate), Href: l.event(e.ID)})
		}
		for _, s := range f.Scenes {
			text := displayTitle(s.Title) + tlDateNote(s.StoryDate)
			if n := len(s.Breadcrumb); n > 1 {
				text += ", in " + displayTitle(s.Breadcrumb[n-2].Title)
			}
			tf.Links = append(tf.Links, tlLink{Kind: "scene", Text: text, Href: l.scene(s.ID)})
		}
		for _, e := range f.Entities {
			tf.Links = append(tf.Links, tlLink{Kind: "entity", Text: e.Name, Href: l.entity(e.ID)})
		}
		c.Findings = append(c.Findings, tf)
		if f.Severity == "error" {
			c.Errs = append(c.Errs, tf)
		} else {
			c.Warns = append(c.Warns, tf)
		}
	}
	switch {
	case project:
		c.Headline = "Read " + tlCount(out.Events, "event", "events") + " and " + tlCount(out.Scenes, "scene", "scenes") + "."
		c.Empty = "These checks found nothing to flag. They look for one person at two places at once, an event before a birth or after a death, a scene that ends before it starts, something told after what happens later, a scene dated before a person's first event, and dates that cannot be read. They cannot tell whether the story is consistent; they only compare what is written down."
	default:
		c.Headline = "Read " + tlCount(out.Events, "event", "events") + "."
		c.Empty = "These checks found nothing to flag. On a Compendium's own timeline they look for one person at two places at once, and an event before a birth or after a death. Scenes belong to books, so open a book's timeline to check its scenes. They cannot tell whether the story is consistent; they only compare what is written down."
	}
	return c
}

func tlDateNote(d string) string {
	if d == "" {
		return ""
	}
	return " (" + d + ")"
}

// ---- small helpers ----------------------------------------------------------------------------------------------------

// tlSnippet is the first line of a description, plain and short, for a row of the table.
func tlSnippet(md string) string {
	s := strings.TrimSpace(md)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimLeft(s, "#>*- ")
	r := []rune(s)
	if len(r) > 140 {
		s = string(r[:140]) + "…"
	}
	return s
}

// tlPageLinks returns the query strings for the previous and next page of a table.
func tlPageLinks(base url.Values, offset, total int, key string, size int) (prev, next string) {
	mk := func(off int) string {
		q := url.Values{}
		for k, v := range base {
			if k != key && k != "check" {
				q[k] = v
			}
		}
		if off > 0 {
			q.Set(key, strconv.Itoa(off))
		}
		enc := q.Encode()
		if enc == "" {
			return "?#tl-events"
		}
		return "?" + enc + "#tl-events"
	}
	if offset > 0 {
		prev = mk(max(0, offset-size))
	}
	if offset+size < total {
		next = mk(offset + size)
	}
	return prev, next
}

func tlSaved(code string) string {
	return map[string]string{
		"created": "Event added.", "updated": "Event saved.", "trashed": "Event moved to the trash.", "restored": "Event restored.",
		"linked": "Scene linked to the event.", "unlinked": "Scene unlinked from the event.",
		"moved": "Order saved.", "reset": "Order reset to the one the scenes give.",
	}[code]
}

func tlEventURL(slug, id string) string {
	return fmt.Sprintf("/c/%s/timeline/events/%s", url.PathEscape(slug), url.PathEscape(id))
}
