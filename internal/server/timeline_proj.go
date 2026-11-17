package server

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/storydate"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
)

// A book's timeline: the Compendium's events and the book's own dated scenes, in the order things happened or in the
// order this book tells them. Only the order of events can be changed here (timeline.order.set); a scene stays where
// the manuscript puts it.

type tlTold struct{ Title, Href, Where string }

// tlPRow is one place on the book's timeline.
type tlPRow struct {
	Key, ID, Kind, Title, When, Label string
	Dated, IsEvent                    bool
	Pos                               int // place in the order shown, from 1, among every item and not only this page
	Href                              string
	Place                             *vocab.Brief
	People                            []timeline.PersonOut
	PeopleMore                        int
	Told                              []tlTold
	Pov                               *vocab.Brief
	CanUp, CanDown                    bool
}

type tlEntityOpt struct{ ID, Name string }

type tlProjData struct {
	Project    projects.Project
	Slug       string
	ID         string
	CSRF       string
	CanEdit    bool
	Order      string // story or narrative
	Rows       []tlPRow
	Total      int // after the filters
	All        int // before them
	First      int
	Last       int
	Offset     int
	Prev, Next string
	Filter     tlFilter
	Filtered   bool
	Entities   []tlEntityOpt
	Strip      tlStrip
	Check      *tlCheck
	Explicit   bool
	Hidden     bool
	Unplaced   int
	Movable    bool // the narrative order, for someone who can change it, with nothing filtered out
	Saved      string
	Error      string
	Field      string
	OrderQuery string // the query that keeps the order and page, for the forms
}

func (s *Server) tlProject(w http.ResponseWriter, r *http.Request, pid string) (projects.Project, bool) {
	po, err := s.Reg.Call(r.Context(), s.principal(r), "project.get", projects.GetIn{ID: pid})
	if err != nil {
		s.fail(w, r, err)
		return projects.Project{}, false
	}
	proj := po.(projects.Item).Item.Addressed()
	if proj.Trashed {
		s.fail(w, r, ops.NotFound("project"))
		return projects.Project{}, false
	}
	return proj, true
}

func (s *Server) tlProjectBySlug(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return projects.Project{}, false
	}
	pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, r.PathValue("slug"))
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return projects.Project{}, false
	}
	return s.tlProject(w, r, pid)
}

// tlKeep applies the page's filters to an item: the entity, and a range of dates it must overlap.
func tlKeep(it timeline.ViewItem, f tlFilter, lo, hi string) bool {
	if f.Entity != "" {
		hit := it.Location != nil && it.Location.ID == f.Entity
		for _, p := range it.Participants {
			hit = hit || p.Entity.ID == f.Entity
		}
		if !hit {
			return false
		}
	}
	if lo != "" || hi != "" {
		iv := storydate.Span(it.StoryDate, it.StoryDateEnd)
		if !iv.Dated() || (lo != "" && iv.End < lo) || (hi != "" && iv.Start > hi) {
			return false
		}
	}
	return true
}

func (s *Server) tlProjectLoad(r *http.Request, proj projects.Project, order string, f tlFilter, offset int, check bool) (tlProjData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := tlProjData{Project: proj, Slug: proj.Slug, ID: proj.ID, CSRF: authFrom(ctx).CSRF, Order: order, Filter: f, Filtered: f.active(), Offset: offset,
		CanEdit: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor}
	vo, err := s.Reg.Call(ctx, p, "timeline.project.view", timeline.ViewIn{ID: proj.ID})
	if err != nil {
		return d, err
	}
	out := vo.(timeline.ViewOut)
	d.Explicit, d.Hidden, d.Unplaced, d.All = out.Explicit, out.Hidden, out.Unplaced, len(out.Items)
	byKey := make(map[string]timeline.ViewItem, len(out.Items))
	seen := map[string]bool{}
	for _, it := range out.Items {
		byKey[it.Key] = it
		add := func(b *vocab.Brief) {
			if b != nil && !seen[b.ID] {
				seen[b.ID] = true
				d.Entities = append(d.Entities, tlEntityOpt{ID: b.ID, Name: b.Name})
			}
		}
		add(it.Location)
		for _, pp := range it.Participants {
			e := pp.Entity
			add(&e)
		}
	}
	sort.Slice(d.Entities, func(i, j int) bool { return d.Entities[i].Name < d.Entities[j].Name })
	var lo, hi string
	if t, ok := storydate.Parse(f.From); ok {
		lo = t.StartKey()
	} else if f.From != "" {
		d.Error, d.Field, d.Filter.From = "Use a date such as 1998, 1998-04 or 1998-04-12 for the start of the range.", "from", ""
	}
	if t, ok := storydate.Parse(f.To); ok {
		hi = t.EndKey()
	} else if f.To != "" {
		d.Error, d.Field, d.Filter.To = "Use a date such as 1998, 1998-04 or 1998-04-12 for the end of the range.", "to", ""
	}
	keys := out.Story
	if order == "narrative" {
		keys = out.Narrative
	}
	evs := map[string]int{} // event id -> place among the events of the narrative
	var evOrder []string
	for _, k := range out.Narrative {
		if it, ok := byKey[k]; ok && it.Kind == "event" {
			evs[it.ID] = len(evOrder)
			evOrder = append(evOrder, it.ID)
		}
	}
	d.Movable = d.CanEdit && order == "narrative" && f.Entity == "" && lo == "" && hi == ""
	var rows []tlPRow
	var pts []tlPoint
	for i, k := range keys {
		it, ok := byKey[k]
		if !ok {
			continue
		}
		if tlKeep(it, tlFilter{Entity: f.Entity}, "", "") { // the strip is how a range is chosen, so it ignores the range
			pts = append(pts, tlPoint{Date: it.StoryDate, Scene: it.Kind == "scene", Title: it.Title})
		}
		if !tlKeep(it, f, lo, hi) {
			continue
		}
		row := tlPRow{Key: it.Key, ID: it.ID, Kind: it.Kind, Title: displayTitle(it.Title), When: tlWhen(it.StoryDate, it.StoryDateEnd), Label: it.StoryLabel,
			Dated: it.Dated, IsEvent: it.Kind == "event", Pos: i + 1, Place: it.Location, People: it.Participants, Pov: it.Pov}
		if row.IsEvent {
			row.Href = "/timeline/events/" + url.PathEscape(it.ID)
			j := evs[it.ID]
			row.CanUp, row.CanDown = j > 0, j < len(evOrder)-1
		} else {
			row.Href = "/p/" + url.PathEscape(proj.Slug) + "/d/" + url.PathEscape(it.ID)
		}
		if len(row.People) > 5 {
			row.PeopleMore = len(row.People) - 5
			row.People = row.People[:5]
		}
		if row.IsEvent {
			for _, t := range it.Told {
				w := ""
				if n := len(t.Breadcrumb); n > 1 {
					w = displayTitle(t.Breadcrumb[n-2].Title)
				}
				row.Told = append(row.Told, tlTold{Title: displayTitle(t.Title), Href: "/p/" + url.PathEscape(proj.Slug) + "/d/" + url.PathEscape(t.ID), Where: w})
			}
		}
		rows = append(rows, row)
	}
	d.Total = len(rows)
	if offset >= len(rows) {
		offset = 0
		d.Offset = 0
	}
	end := min(offset+tlPageSize, len(rows))
	d.Rows = rows[offset:end]
	if len(d.Rows) > 0 {
		d.First, d.Last = offset+1, end
	}
	q := f.values()
	q.Set("order", order)
	d.Prev, d.Next = tlPageLinks(q, offset, len(rows), "offset", tlPageSize)
	sq := url.Values{}
	sq.Set("order", order)
	if offset > 0 {
		sq.Set("offset", strconv.Itoa(offset))
	}
	d.OrderQuery = sq.Encode()
	base := f.values()
	base.Set("order", order)
	d.Strip = buildStrip(pts, func(from, to string) string { return tlRange(base, from, to) })
	if check {
		co, err := s.Reg.Call(ctx, p, "timeline.check", timeline.CheckIn{ID: proj.ID})
		if err != nil {
			return d, err
		}
		ck := buildCheck(co.(timeline.CheckOut), tlLinker{ProjectSlug: proj.Slug, EntityBase: "/p/" + url.PathEscape(proj.Slug) + "/timeline"}, true)
		d.Check = &ck
	}
	return d, nil
}

func tlOrderParam(q url.Values) string {
	if q.Get("order") == "narrative" {
		return "narrative"
	}
	return "story"
}

func (s *Server) tlProjectPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.tlProjectBySlug(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	d, err := s.tlProjectLoad(r, proj, tlOrderParam(q), tlFilterFrom(q), max(offset, 0), q.Get("check") == "1")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = tlSaved(q.Get("saved"))
	status := http.StatusOK
	if d.Error != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, status, "project-timeline", s.page(r, "Timeline of "+proj.Title, d))
}

// tlProjectRows is the table and its pager alone, for the order script to draw again after a change fails.
func (s *Server) tlProjectRows(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.tlProject(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	d, err := s.tlProjectLoad(r, proj, tlOrderParam(q), tlFilterFrom(q), max(offset, 0), false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "timeline-parts", "tl-ptable", d)
}

func (s *Server) tlProjectCheck(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.tlProject(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	co, err := s.Reg.Call(r.Context(), s.principal(r), "timeline.check", timeline.CheckIn{ID: proj.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ck := buildCheck(co.(timeline.CheckOut), tlLinker{ProjectSlug: proj.Slug, EntityBase: "/p/" + url.PathEscape(proj.Slug) + "/timeline"}, true)
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "timeline-parts", "tl-check", ck)
}

// tlOrder moves one event a place earlier or later in this book's telling, or forgets the book's own order. The move
// is worked out here from the order the server holds now, never from the page, so two quick moves or two tabs cannot
// undo each other: each swaps the event with the event next to it, and scenes stay where they are.
func (s *Server) tlOrder(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.tlProject(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	saved, anchor := "moved", ""
	var err error
	if r.PostFormValue("reset") != "" {
		_, err = s.Reg.Call(ctx, p, "timeline.order.set", timeline.OrderSetIn{ID: proj.ID, Reset: true})
		saved = "reset"
	} else {
		id, dir := r.PostFormValue("event"), r.PostFormValue("dir")
		var oo any
		if oo, err = s.Reg.Call(ctx, p, "timeline.order.get", timeline.OrderIn{ID: proj.ID}); err == nil {
			evs := append([]string(nil), oo.(timeline.OrderOut).Events...)
			i := -1
			for k, e := range evs {
				if e == id {
					i = k
				}
			}
			j := i - 1
			if dir == "later" {
				j = i + 1
			}
			switch {
			case i < 0 || (dir != "earlier" && dir != "later"):
				err = ops.Invalid("event", "That event is not on this book's timeline.")
			case j < 0 || j >= len(evs):
				saved = "" // already first or last: nothing to save
			default:
				evs[i], evs[j] = evs[j], evs[i]
				_, err = s.Reg.Call(ctx, p, "timeline.order.set", timeline.OrderSetIn{ID: proj.ID, EventIDs: evs})
			}
			anchor = "#tl-row-" + url.PathEscape(id)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if isFetch(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	q := url.Values{}
	q.Set("order", "narrative")
	if o, _ := strconv.Atoi(r.PostFormValue("offset")); o > 0 {
		q.Set("offset", strconv.Itoa(o))
	}
	if saved != "" {
		q.Set("saved", saved)
	}
	http.Redirect(w, r, "/p/"+url.PathEscape(proj.Slug)+"/timeline?"+q.Encode()+anchor, http.StatusSeeOther)
}
