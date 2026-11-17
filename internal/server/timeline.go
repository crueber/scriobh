package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// The timeline screens. A Compendium's timeline lives at /c/{slug}/timeline, one event at
// /c/{slug}/timeline/events/{id}, and a book's own view of it at /p/{slug}/timeline. Every read and every change goes
// through the timeline ops (timeline.event.*, timeline.project.view, timeline.order.set, timeline.check,
// timeline.scene.link); nothing here writes SQL. The pages read without JavaScript: filters are a GET form, the check
// is a link, an order change is a form, and js/timeline.js only makes them quicker and adds the entity picker and the
// live date hints.

func (s *Server) registerTimeline(m *http.ServeMux) {
	m.HandleFunc("GET /c/{slug}/timeline", s.tlCompPage)
	m.HandleFunc("GET /c/{slug}/timeline/events/new", s.tlEventNew)
	m.HandleFunc("GET /c/{slug}/timeline/events/{id}", s.tlEventPage)
	m.HandleFunc("GET /timeline/events/{id}", s.tlEventRedirect)
	m.HandleFunc("GET /ui/compendiums/{id}/timeline/check", s.tlCompCheck)
	s.uiRoute("POST /ui/compendiums/{id}/timeline/events", s.tlEventCreate)
	s.uiRoute("POST /ui/timeline/events/{id}", s.tlEventUpdate)
	s.uiRoute("POST /ui/timeline/events/{id}/trash", s.tlEventState("timeline.event.delete", "trashed"))
	s.uiRoute("POST /ui/timeline/events/{id}/restore", s.tlEventState("timeline.event.restore", "restored"))
	s.uiRoute("POST /ui/timeline/events/{id}/scenes", s.tlSceneLink)
	s.uiRoute("POST /ui/timeline/events/{id}/scenes/unlink", s.tlSceneUnlink)
	m.HandleFunc("GET /p/{slug}/timeline", s.tlProjectPage)
	m.HandleFunc("GET /ui/projects/{id}/timeline/check", s.tlProjectCheck)
	m.HandleFunc("GET /ui/projects/{id}/timeline/rows", s.tlProjectRows)
	s.uiRoute("POST /ui/projects/{id}/timeline/order", s.tlOrder)
}

var reTLID = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// tlErr turns an op's refusal into a sentence for the page. The ops already write plain words; these cover the codes
// whose default text is written for the API.
func tlErr(err error, what string) (msg, field string) {
	e := ops.AsError(err)
	switch e.Code {
	case ops.CodeInvalid:
		return e.Message, e.Field
	case ops.CodeConflict:
		if _, ok := e.Data["revision"]; ok {
			return "This event was changed somewhere else while you had it open. What you typed is kept below. Open the event again to see the other version, then make your change again.", ""
		}
		return e.Message, ""
	case ops.CodeForbidden:
		return "You can read this but not change it, so " + what + " was not done.", ""
	case ops.CodeNotFound:
		return "That is no longer there, or you do not have access to it.", ""
	}
	return "Something went wrong and " + what + " was not done. Nothing was lost; try again in a moment.", ""
}

// ---- the Compendium page ------------------------------------------------------------------------------------------

type tlFilter struct {
	Entity, From, To, Project string
	Trash                     bool
}

func tlFilterFrom(q url.Values) tlFilter {
	f := tlFilter{Entity: strings.TrimSpace(q.Get("entity")), From: strings.TrimSpace(q.Get("from")), To: strings.TrimSpace(q.Get("to")),
		Project: strings.TrimSpace(q.Get("project")), Trash: q.Get("trash") == "1"}
	return f
}

func (f tlFilter) values() url.Values {
	q := url.Values{}
	for k, v := range map[string]string{"entity": f.Entity, "from": f.From, "to": f.To, "project": f.Project} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if f.Trash {
		q.Set("trash", "1")
	}
	return q
}

func (f tlFilter) active() bool {
	return f.Entity != "" || f.From != "" || f.To != "" || f.Project != ""
}

// tlScene is a scene an event is told by, as a link to the chapter.
type tlScene struct {
	ID, Title, Kind, Href, Project string
	Editable                       bool // the person may change that book, so may unlink the scene
}

// tlRow is one event in the Compendium's table.
type tlRow struct {
	timeline.Event
	When       string
	Snippet    string
	EventHref  string
	SceneRows  []tlScene
	PeopleMore int
}

type tlBook struct {
	ID, Slug, Title string
	Editable        bool
}

type tlCompData struct {
	C         compendiums.Compendium
	Slug      string
	CSRF      string
	CanEdit   bool
	Why       string // why the page is read-only, when it is
	Rows      []tlRow
	Total     int
	First     int
	Last      int
	Offset    int
	Prev      string
	Next      string
	Filter    tlFilter
	Filtered  bool
	Entities  []entities.Entry
	Projects  []compendiums.ProjectUse
	Strip     tlStrip
	StripNote string
	Check     *tlCheck
	Saved     string
	UndoID    string
	Error     string
	Field     string
}

// tlSlugs maps project ids to what a link and a permission need, for every book the person can open.
func (s *Server) tlBooks(r *http.Request) (map[string]tlBook, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.list", projects.ListIn{State: projects.StateAll})
	if err != nil {
		return nil, err
	}
	m := map[string]tlBook{}
	for _, p := range addressedProjects(out.(projects.ListOut).Items) {
		m[p.ID] = tlBook{ID: p.ID, Slug: p.Slug, Title: p.Title, Editable: !p.Trashed && (p.Role == ops.ProjectOwner || p.Role == ops.ProjectEditor)}
	}
	return m, nil
}

func tlCanEdit(c compendiums.Compendium) (bool, string) {
	switch {
	case c.Trashed:
		return false, "This Compendium is in the trash, so its timeline can be read but not changed. Restore the Compendium to edit it."
	case c.Archived:
		return false, "This Compendium is archived, so its timeline can be read but not changed. Unarchive it to edit."
	case c.Role != "owner" && c.Role != "editor":
		return false, "You can read this timeline. Editors of the Compendium can change it."
	}
	return true, ""
}

func tlRows(evs []timeline.Event, slug string, books map[string]tlBook) []tlRow {
	rows := make([]tlRow, 0, len(evs))
	for _, e := range evs {
		r := tlRow{Event: e, When: tlWhen(e.StoryDate, e.StoryDateEnd), Snippet: tlSnippet(e.Description), EventHref: tlEventURL(slug, e.ID)}
		if len(r.Participants) > 6 {
			r.PeopleMore = len(r.Participants) - 6
			r.Participants = r.Participants[:6]
		}
		for _, sc := range e.Scenes {
			ts := tlScene{ID: sc.ID, Title: displayTitle(sc.Title), Kind: docKindLabel(sc.Kind), Project: sc.ProjectTitle}
			if b, ok := books[sc.ProjectID]; ok {
				ts.Href, ts.Editable = "/p/"+url.PathEscape(b.Slug)+"/d/"+url.PathEscape(sc.ID), b.Editable
			}
			r.SceneRows = append(r.SceneRows, ts)
		}
		rows = append(rows, r)
	}
	return rows
}

// tlCompLoad reads everything the Compendium page shows. A bad filter date is shown beside the filter with the table
// unfiltered by it, rather than as an error page.
func (s *Server) tlCompLoad(r *http.Request, c compendiums.Compendium, f tlFilter, offset int, check bool) (tlCompData, error) {
	ctx, p := r.Context(), s.principal(r)
	canEdit, why := tlCanEdit(c)
	d := tlCompData{C: c, Slug: c.Slug, CSRF: authFrom(ctx).CSRF, CanEdit: canEdit, Why: why, Filter: f, Filtered: f.active(), Offset: offset}
	eo, err := s.Reg.Call(ctx, p, "entity.list", entities.ListIn{ID: c.ID, Limit: 500})
	if err != nil {
		return d, err
	}
	d.Entities = eo.(entities.ListOut).Items
	dt, err := s.Reg.Call(ctx, p, "compendium.get", compendiums.GetIn{ID: c.ID})
	if err != nil {
		return d, err
	}
	d.Projects = dt.(compendiums.DetailItem).Item.Projects
	books, err := s.tlBooks(r)
	if err != nil {
		return d, err
	}
	in := timeline.ListIn{ID: c.ID, Entity: f.Entity, From: f.From, To: f.To, Project: f.Project, Trash: f.Trash, Limit: tlPageSize, Offset: offset}
	lo, err := s.Reg.Call(ctx, p, "timeline.event.list", in)
	if e := ops.AsError(err); err != nil && e.Code == ops.CodeInvalid && (e.Field == "from" || e.Field == "to") {
		d.Error, d.Field = e.Message, e.Field
		in.From, in.To = "", ""
		d.Filter.From, d.Filter.To = "", ""
		lo, err = s.Reg.Call(ctx, p, "timeline.event.list", in)
	}
	if err != nil {
		return d, err
	}
	out := lo.(timeline.ListOut)
	d.Total = out.Total
	d.Rows = tlRows(out.Items, c.Slug, books)
	if len(d.Rows) > 0 {
		d.First, d.Last = offset+1, offset+len(d.Rows)
	}
	d.Prev, d.Next = tlPageLinks(d.Filter.values(), offset, out.Total, "offset", tlPageSize)
	if !f.Trash {
		if err := s.tlCompStrip(r, &d); err != nil {
			return d, err
		}
	}
	if check {
		co, err := s.Reg.Call(ctx, p, "timeline.compendium.check", timeline.CompendiumCheckIn{ID: c.ID})
		if err != nil {
			return d, err
		}
		ck := buildCheck(co.(timeline.CheckOut), tlLinker{EntityBase: "/c/" + url.PathEscape(c.Slug) + "/timeline"}, false)
		d.Check = &ck
	}
	return d, nil
}

// tlCompStrip aggregates every event the entity and book filters keep (not the date range: the strip is how a range
// is chosen) into blocks, reading the list in pages of the op's maximum.
func (s *Server) tlCompStrip(r *http.Request, d *tlCompData) error {
	ctx, p := r.Context(), s.principal(r)
	var pts []tlPoint
	base := d.Filter.values()
	for off := 0; ; {
		lo, err := s.Reg.Call(ctx, p, "timeline.event.list", timeline.ListIn{ID: d.C.ID, Entity: d.Filter.Entity, Project: d.Filter.Project, Limit: timeline.ListMax, Offset: off})
		if err != nil {
			return err
		}
		out := lo.(timeline.ListOut)
		for _, e := range out.Items {
			pts = append(pts, tlPoint{Date: e.StoryDate, Title: e.Title})
		}
		if out.NextOffset == 0 || off > 20*timeline.ListMax {
			break
		}
		off = out.NextOffset
	}
	d.Strip = buildStrip(pts, func(from, to string) string { return tlRange(base, from, to) })
	return nil
}

func (s *Server) tlCompPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	d, err := s.tlCompLoad(r, c, tlFilterFrom(q), max(offset, 0), q.Get("check") == "1")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = tlSaved(q.Get("saved"))
	if reTLID.MatchString(q.Get("event")) {
		d.UndoID = q.Get("event")
	}
	status := http.StatusOK
	if d.Error != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, status, "timeline", s.page(r, "Timeline of "+c.Name, d))
}

// tlCompDetail reads a Compendium by id through compendium.get, which answers 404 to a stranger.
func (s *Server) tlCompDetail(w http.ResponseWriter, r *http.Request, id string) (compendiums.Compendium, bool) {
	return s.compendiumByID(w, r, id)
}

func (s *Server) tlCompCheck(w http.ResponseWriter, r *http.Request) {
	c, ok := s.tlCompDetail(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	co, err := s.Reg.Call(r.Context(), s.principal(r), "timeline.compendium.check", timeline.CompendiumCheckIn{ID: c.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ck := buildCheck(co.(timeline.CheckOut), tlLinker{EntityBase: "/c/" + url.PathEscape(c.Slug) + "/timeline"}, false)
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "timeline-parts", "tl-check", ck)
}

// ---- one event --------------------------------------------------------------------------------------------------------

type tlPerson struct{ Name, Role string }

type tlForm struct {
	Title, Date, End, Label, Place, Description string
	People                                      []tlPerson
	Revision                                    int
}

type tlSceneOpt struct{ ID, Title, Where string }

type tlEventData struct {
	C          compendiums.Compendium
	Slug       string
	CSRF       string
	CanEdit    bool
	Why        string
	New        bool
	E          timeline.Event
	V          tlForm
	Rows       []tlPerson // the participants shown: the saved ones, then blank rows to fill
	Scenes     []tlScene
	Books      []tlBook
	Book       string
	Options    []tlSceneOpt
	Saved      string
	Error      string
	Field      string
	LinkErr    string
	Linkable   bool
	CanRestore bool // the event is in the trash and the person may bring it back
}

const tlBlankRows = 3

func tlFormOf(e timeline.Event) tlForm {
	v := tlForm{Title: e.Title, Date: e.StoryDate, End: e.StoryDateEnd, Label: e.StoryLabel, Description: e.Description, Revision: e.Revision}
	if e.Location != nil {
		v.Place = e.Location.Name
	}
	for _, p := range e.Participants {
		v.People = append(v.People, tlPerson{Name: p.Entity.Name, Role: p.Role})
	}
	return v
}

func tlFormFrom(r *http.Request) tlForm {
	v := tlForm{Title: r.PostFormValue("title"), Date: strings.TrimSpace(r.PostFormValue("story_date")), End: strings.TrimSpace(r.PostFormValue("story_date_end")),
		Label: r.PostFormValue("story_label"), Place: strings.TrimSpace(r.PostFormValue("location")), Description: r.PostFormValue("description")}
	v.Revision, _ = strconv.Atoi(r.PostFormValue("base_revision"))
	names, roles := r.PostForm["person_name"], r.PostForm["person_role"]
	for i, n := range names {
		role := ""
		if i < len(roles) {
			role = strings.TrimSpace(roles[i])
		}
		if n = strings.TrimSpace(n); n != "" || role != "" {
			v.People = append(v.People, tlPerson{Name: n, Role: role})
		}
	}
	return v
}

func (v tlForm) participants() ([]timeline.Participant, error) {
	out := make([]timeline.Participant, 0, len(v.People))
	for _, p := range v.People {
		if p.Name == "" {
			return nil, ops.Invalid("participants", "Choose who took part for each role you typed.")
		}
		out = append(out, timeline.Participant{Entity: p.Name, Role: p.Role})
	}
	return out, nil
}

// samePeople reports whether the form's participants are what the event already has, so that saving a title does not
// rewrite the list (which would drop a person whose entity is in the trash and so is not shown).
func (v tlForm) samePeople(e timeline.Event) bool {
	if len(v.People) != len(e.Participants) {
		return false
	}
	for i, p := range e.Participants {
		if !strings.EqualFold(v.People[i].Name, p.Entity.Name) || v.People[i].Role != p.Role {
			return false
		}
	}
	return true
}

func (s *Server) tlEventBase(r *http.Request, c compendiums.Compendium) tlEventData {
	return tlEventBaseFor(c, authFrom(r.Context()).CSRF)
}

func tlEventBaseFor(c compendiums.Compendium, csrf string) tlEventData {
	canEdit, why := tlCanEdit(c)
	return tlEventData{C: c, Slug: c.Slug, CSRF: csrf, CanEdit: canEdit, Why: why}
}

// tlEventFill adds what the form needs besides the event: blank participant rows, the linked scenes with their links,
// and, when a book is chosen, its scenes to pick from.
func (s *Server) tlEventFill(r *http.Request, d *tlEventData, book string) error {
	ctx, p := r.Context(), s.principal(r)
	d.Rows = append(d.Rows[:0], d.V.People...)
	for i := 0; i < tlBlankRows && len(d.Rows) < 100; i++ {
		d.Rows = append(d.Rows, tlPerson{})
	}
	if d.New {
		return nil
	}
	books, err := s.tlBooks(r)
	if err != nil {
		return err
	}
	d.Scenes = nil
	for _, sc := range tlRows([]timeline.Event{d.E}, d.Slug, books)[0].SceneRows {
		d.Scenes = append(d.Scenes, sc)
	}
	if !d.CanEdit {
		return nil
	}
	co, err := s.Reg.Call(ctx, p, "compendium.get", compendiums.GetIn{ID: d.C.ID})
	if err != nil {
		return err
	}
	for _, u := range co.(compendiums.DetailItem).Item.Projects {
		if b, ok := books[u.ID]; ok && b.Editable {
			d.Books = append(d.Books, b)
		}
	}
	d.Linkable = len(d.Books) > 0
	for _, b := range d.Books {
		if b.ID != book {
			continue
		}
		d.Book = b.ID
		oo, err := s.Reg.Call(ctx, p, "outline.get", docs.OutlineIn{ProjectID: b.ID})
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, sc := range d.E.Scenes {
			have[sc.ID] = true
		}
		title := map[string]string{}
		for _, it := range oo.(docs.OutlineOut).Items {
			title[it.ID] = displayTitle(it.Title)
		}
		for _, it := range oo.(docs.OutlineOut).Items {
			if it.ChildCount == 0 && !have[it.ID] && (it.Kind == "scene" || it.Kind == "chapter") {
				o := tlSceneOpt{ID: it.ID, Title: displayTitle(it.Title)}
				if it.ParentID != "" {
					o.Where = title[it.ParentID]
				}
				d.Options = append(d.Options, o)
			}
		}
	}
	return nil
}

func (s *Server) tlEventNew(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	d := s.tlEventBase(r, c)
	d.New = true
	if !d.CanEdit {
		s.fail(w, r, ops.Forbidden("Only editors of this Compendium can add events. "+d.Why)) // a member, so nothing leaks
		return
	}
	if err := s.tlEventFill(r, &d, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "timeline-event", s.page(r, "New event, "+c.Name, d))
}

func (s *Server) tlEventGet(r *http.Request, id string) (timeline.Event, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "timeline.event.get", timeline.GetIn{ID: id})
	if err != nil {
		return timeline.Event{}, err
	}
	return out.(timeline.EventItem).Item, nil
}

func (s *Server) tlEventPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	e, err := s.tlEventGet(r, r.PathValue("id"))
	if err == nil && e.CompendiumID != c.ID {
		err = ops.NotFound("event")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := s.tlEventBase(r, c)
	d.E, d.V = e, tlFormOf(e)
	if e.Trashed {
		d.CanRestore = d.CanEdit
		d.CanEdit, d.Why = false, "This event is in the trash. Restore it to edit it."
		if !d.CanRestore {
			d.Why = "This event is in the trash."
		}
	}
	if err := s.tlEventFill(r, &d, r.URL.Query().Get("book")); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = tlSaved(r.URL.Query().Get("saved"))
	s.render(w, r, http.StatusOK, "timeline-event", s.page(r, e.Title+", "+c.Name, d))
}

// tlEventRedirect sends an event id to its page, for links from a book's view and from findings, which know the event
// and not its Compendium.
func (s *Server) tlEventRedirect(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	e, err := s.tlEventGet(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, ok := s.tlCompDetail(w, r, e.CompendiumID)
	if !ok {
		return
	}
	http.Redirect(w, r, tlEventURL(c.Slug, e.ID), http.StatusSeeOther)
}

// tlEventAgain shows the form again beside a refusal, with what the person typed.
func (s *Server) tlEventAgain(w http.ResponseWriter, r *http.Request, c compendiums.Compendium, e *timeline.Event, v tlForm, err error, what string) {
	ec := ops.AsError(err)
	if ec.Code == ops.CodeNotFound || ec.Code == ops.CodeUnauthenticated || ec.Code == ops.CodeForbidden {
		s.fail(w, r, err) // nothing to show beside a form the person could not have used
		return
	}
	d := s.tlEventBase(r, c)
	d.New = e == nil
	if e != nil {
		d.E = *e
	}
	d.V = v
	d.Error, d.Field = tlErr(err, what)
	if ec.Code == ops.CodeInternal {
		s.fail(w, r, err)
		return
	}
	if ferr := s.tlEventFill(r, &d, ""); ferr != nil {
		s.fail(w, r, ferr)
		return
	}
	title := "New event, " + c.Name
	if e != nil {
		title = e.Title + ", " + c.Name
	}
	s.render(w, r, http.StatusUnprocessableEntity, "timeline-event", s.page(r, title, d))
}

func (s *Server) tlEventCreate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.tlCompDetail(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	v := tlFormFrom(r)
	people, err := v.participants()
	if err == nil {
		var out any
		out, err = s.Reg.Call(r.Context(), s.principal(r), "timeline.event.create", timeline.CreateIn{ID: c.ID, Title: v.Title, Description: v.Description,
			StoryDate: v.Date, StoryDateEnd: v.End, StoryLabel: v.Label, Location: v.Place, Participants: people})
		if err == nil {
			id := out.(timeline.EventItem).Item.ID
			http.Redirect(w, r, tlEventURL(c.Slug, id)+"?saved=created", http.StatusSeeOther)
			return
		}
	}
	s.tlEventAgain(w, r, c, nil, v, err, "adding the event")
}

func (s *Server) tlEventUpdate(w http.ResponseWriter, r *http.Request) {
	e, err := s.tlEventGet(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, ok := s.tlCompDetail(w, r, e.CompendiumID)
	if !ok {
		return
	}
	v := tlFormFrom(r)
	in := timeline.UpdateIn{ID: e.ID, BaseRevision: v.Revision, Title: &v.Title, Description: &v.Description, StoryDate: &v.Date, StoryDateEnd: &v.End, StoryLabel: &v.Label}
	if shown := tlFormOf(e).Place; !strings.EqualFold(v.Place, shown) {
		in.Location = &v.Place // a place in the trash is not shown, so only a place the person changed is sent
	}
	if !v.samePeople(e) {
		var people []timeline.Participant
		if people, err = v.participants(); err == nil {
			in.Participants = &people
		}
	}
	if err == nil {
		if _, err = s.Reg.Call(r.Context(), s.principal(r), "timeline.event.update", in); err == nil {
			http.Redirect(w, r, tlEventURL(c.Slug, e.ID)+"?saved=updated", http.StatusSeeOther)
			return
		}
	}
	s.tlEventAgain(w, r, c, &e, v, err, "saving the event")
}

// tlEventState trashes or restores an event and goes back to the list, where the way back is offered.
func (s *Server) tlEventState(op, saved string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		e, err := s.tlEventGet(r, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		c, ok := s.tlCompDetail(w, r, e.CompendiumID)
		if !ok {
			return
		}
		if _, err := s.Reg.Call(r.Context(), s.principal(r), op, timeline.StateIn{ID: id}); err != nil {
			s.fail(w, r, err)
			return
		}
		q := "?saved=" + saved
		if saved == "trashed" {
			q += "&event=" + url.QueryEscape(id)
		}
		http.Redirect(w, r, "/c/"+url.PathEscape(c.Slug)+"/timeline"+q+"#tl-events", http.StatusSeeOther)
	}
}

func (s *Server) tlSceneLink(w http.ResponseWriter, r *http.Request)   { s.tlScene(w, r, true) }
func (s *Server) tlSceneUnlink(w http.ResponseWriter, r *http.Request) { s.tlScene(w, r, false) }

func (s *Server) tlScene(w http.ResponseWriter, r *http.Request, on bool) {
	e, err := s.tlEventGet(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, ok := s.tlCompDetail(w, r, e.CompendiumID)
	if !ok {
		return
	}
	if ok, why := tlCanEdit(c); !ok {
		s.fail(w, r, ops.Forbidden(why))
		return
	}
	doc := strings.TrimSpace(r.PostFormValue("doc"))
	op, saved := "timeline.scene.link", "linked"
	var in any = timeline.LinkIn{ID: doc, EventID: e.ID}
	if !on {
		op, saved, in = "timeline.scene.unlink", "unlinked", timeline.UnlinkIn{ID: doc, EventID: e.ID}
	}
	if doc == "" {
		err = ops.Invalid("doc", "Choose a scene first.")
	} else {
		_, err = s.Reg.Call(r.Context(), s.principal(r), op, in)
	}
	if err == nil {
		http.Redirect(w, r, tlEventURL(c.Slug, e.ID)+"?saved="+saved+"&book="+url.QueryEscape(r.PostFormValue("book"))+"#tl-scenes", http.StatusSeeOther)
		return
	}
	if ops.AsError(err).Code == ops.CodeUnauthenticated {
		s.fail(w, r, err)
		return
	}
	d := s.tlEventBase(r, c)
	d.E, d.V = e, tlFormOf(e)
	msg, _ := tlErr(err, "linking the scene")
	if !on {
		msg, _ = tlErr(err, "unlinking the scene")
	}
	if ops.AsError(err).Code == ops.CodeForbidden || ops.AsError(err).Code == ops.CodeNotFound {
		msg = "You can change this Compendium, but not the book that scene is in, so the link was not changed."
	}
	d.LinkErr = msg
	if ferr := s.tlEventFill(r, &d, r.PostFormValue("book")); ferr != nil {
		s.fail(w, r, ferr)
		return
	}
	s.render(w, r, http.StatusUnprocessableEntity, "timeline-event", s.page(r, e.Title+", "+c.Name, d))
}
