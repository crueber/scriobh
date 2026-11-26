package server

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The other two views of the Compendium tab (F-1262, F-1263): the events the open scene tells, and a quick note. Both are fragments of
// the same panel, under /ui/projects/{id}/lookup, and every change goes through an existing op (timeline.scene.link and unlink,
// timeline.event.create, project.research.create and update). Nothing here names an event or a note the ops would not show the caller:
// the events come from the Compendiums the caller is on, the notes from the book, and the scene is a live document of this book or
// nothing (a document id of another book answers as an unknown one, so the book's gate is never walked around through this one).

const (
	lkEvPage  = 500 // timeline.event.list's own page
	lkEvPages = 20  // the pages of matches the events view and a search of events to link read at most: 10,000 events, twice the budget's 5,000
	lkEvHits  = 10  // the events a search offers
	lkNotesN  = 10  // the notes listed with no search
	lkNotesQ  = 30  // and with one
	lkNoteMax = 200 // the longest title the form takes
)

// lkEvent is an event as the panel lists it.
type lkEvent struct {
	ID, Title, When, Href string
}

// lkScene is the events view.
type lkScene struct {
	DocID, DocTitle string
	Events, Hits    []lkEvent
	Q               string
	CanLink, CanNew bool
	Why             string // why the caller may only read
	NewTitle        string
	NewDate         string
	CompName        string // where a new event goes
}

// lkNoteRow is a note in the list, and lkNote the one that is open.
type lkNoteRow struct {
	ID, Title, Updated string
	Words              int
}

type lkNote struct {
	ID, Title, Body string
	Revision        int
	HTML            template.HTML
	Href            string
}

// lkNotes is the notes view.
type lkNotes struct {
	Rows        []lkNoteRow
	Q           string
	Total       int
	Note        *lkNote
	CanEdit     bool
	Why         string
	NewTitle    string
	NewBody     string
	All         string // the page of all the notes
	Error       string
	ConflictRev int // the revision a refused save found: what Keep mine sends
}

// lkGate says what the book's Compendiums allow: "closed" when it has one the caller is not on and none it is, "none" when it has none at
// all. Both are the states of the entries view, and the events are not asked for.
func lkGate(c lkCtx) string {
	w := c.wiring
	if w.Primary != nil || len(w.References) > 0 {
		return ""
	}
	if w.HasPrimary || w.Hidden > 0 {
		return "closed"
	}
	return "none"
}

// comps are the Compendiums of the book the caller is on, the primary first.
func (c lkCtx) comps() []string {
	var out []string
	if c.wiring.Primary != nil {
		out = append(out, c.wiring.Primary.CompendiumID)
	}
	for _, r := range c.wiring.References {
		out = append(out, r.CompendiumID)
	}
	return out
}

// lkDoc is the scene the panel is open on: a live document of this book. Anything else is an unknown document.
func (s *Server) lkDoc(w http.ResponseWriter, r *http.Request, c lkCtx, id string) (docs.Doc, bool) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "doc.get", docs.DocIn{ID: id})
	if err != nil {
		s.fail(w, r, ops.NotFound("document"))
		return docs.Doc{}, false
	}
	d := out.(docs.Item).Item
	if d.ProjectID != c.proj.ID || d.Trashed {
		s.fail(w, r, ops.NotFound("document"))
		return docs.Doc{}, false
	}
	return d, true
}

func lkWhen(pg Page, e timeline.Event) string {
	switch {
	case e.StoryLabel != "":
		return e.StoryLabel
	case e.StoryDate == "":
		return pg.T("lk.ev_undated")
	case e.StoryDateEnd != "":
		return e.StoryDate + " – " + e.StoryDateEnd
	}
	return e.StoryDate
}

func (s *Server) lkEventRow(pg Page, addr map[string]string, e timeline.Event) lkEvent {
	return lkEvent{ID: e.ID, Title: e.Title, When: lkWhen(pg, e), Href: "/c/" + addr[e.CompendiumID] + "/timeline/events/" + e.ID}
}

// lkEventPages reads the events of a Compendium that filter selects (a scene's, or a search's), a page at a time, for as long as keep
// says to go on. The database does the selecting; a page is read in full only when it is asked for.
func (s *Server) lkEventPages(r *http.Request, cid string, filter timeline.ListIn, pages int, keep func(timeline.Event) bool) error {
	off := 0
	for i := 0; i < pages; i++ {
		in := filter
		in.ID, in.Limit, in.Offset = cid, lkEvPage, off
		out, err := s.Reg.Call(r.Context(), s.principal(r), "timeline.event.list", in)
		if err != nil {
			return err
		}
		res := out.(timeline.ListOut)
		for _, e := range res.Items {
			if !keep(e) {
				return nil
			}
		}
		if res.NextOffset == 0 {
			return nil
		}
		off = res.NextOffset
	}
	return nil
}

func tellsScene(e timeline.Event, doc string) bool {
	for _, sc := range e.Scenes {
		if sc.ID == doc {
			return true
		}
	}
	return false
}

// lkFillScene reads the events view: the events this document tells (the book's scenes tell events of the Compendiums it uses), the
// search for one to link, and what the caller may do.
func (s *Server) lkFillScene(w http.ResponseWriter, r *http.Request, c lkCtx, v *lookupView, doc docs.Doc, q url.Values) bool {
	v.View, v.Mode = "events", "events"
	if g := lkGate(c); g != "" {
		v.Mode = g
		return true
	}
	sc := &lkScene{DocID: doc.ID, DocTitle: doc.Title, Q: strings.TrimSpace(q.Get("eq"))}
	if sc.DocTitle == "" {
		sc.DocTitle = v.T("nav.untitled")
	}
	v.Scene = sc
	addr := s.compAddresses(r)
	for _, cid := range c.comps() {
		err := s.lkEventPages(r, cid, timeline.ListIn{Project: c.proj.ID, Scene: doc.ID}, lkEvPages, func(e timeline.Event) bool {
			if tellsScene(e, doc.ID) {
				sc.Events = append(sc.Events, s.lkEventRow(v.Page, addr, e))
			}
			return true
		})
		if err != nil {
			s.fail(w, r, err)
			return false
		}
	}
	sc.CanLink = c.projEditor()
	if pr := c.wiring.Primary; pr != nil {
		sc.CompName = pr.Name
		if co, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.get", compendiums.GetIn{ID: pr.CompendiumID}); err == nil {
			sc.CanNew = sc.CanLink && compEditable(co.(compendiums.DetailItem).Item.Compendium)
		}
	}
	switch {
	case !sc.CanLink:
		sc.Why = v.T("lk.ev_ro")
	case !sc.CanNew:
		sc.Why = v.T("lk.ev_ro_comp")
	}
	if sc.Q != "" && sc.CanLink {
		if !s.lkEventHits(w, r, c, v.Page, sc, addr) {
			return false
		}
	}
	return true
}

// lkEventHits searches the events of the book's Compendiums, by the words of a title, a place, a label or a person, for one this scene
// does not tell yet.
func (s *Server) lkEventHits(w http.ResponseWriter, r *http.Request, c lkCtx, pg Page, sc *lkScene, addr map[string]string) bool {
	for _, cid := range c.comps() {
		// The words are matched by timeline.event.list (every word, in the title, label, date, place or a person's name); the events this
		// scene tells are passed over here, so a match the scene already has never takes one of the ten places.
		err := s.lkEventPages(r, cid, timeline.ListIn{Q: sc.Q}, lkEvPages, func(e timeline.Event) bool {
			if tellsScene(e, sc.DocID) {
				return true
			}
			sc.Hits = append(sc.Hits, s.lkEventRow(pg, addr, e))
			return len(sc.Hits) < lkEvHits
		})
		if err != nil {
			s.fail(w, r, err)
			return false
		}
		if len(sc.Hits) >= lkEvHits {
			break
		}
	}
	return true
}

// ---- the events view's changes ----------------------------------------------------------------------------------------

// lkScenePost handles event-link, event-unlink and event-new.
func (s *Server) lkScenePost(w http.ResponseWriter, r *http.Request, c lkCtx, v lookupView, action string) {
	doc, ok := s.lkDoc(w, r, c, r.PostFormValue("doc"))
	if !ok {
		return
	}
	if g := lkGate(c); g != "" {
		s.fail(w, r, ops.NotFound("event"))
		return
	}
	p := s.principal(r)
	done := ""
	switch action {
	case "event-link":
		if _, err := s.Reg.Call(r.Context(), p, "timeline.scene.link", timeline.LinkIn{ID: doc.ID, EventID: r.PostFormValue("event_id")}); err != nil {
			s.lkStatusErr(w, r, v, err)
			return
		}
		done = v.T("lk.ev_linked")
	case "event-unlink":
		if _, err := s.Reg.Call(r.Context(), p, "timeline.scene.unlink", timeline.UnlinkIn{ID: doc.ID, EventID: r.PostFormValue("event_id")}); err != nil {
			s.lkStatusErr(w, r, v, err)
			return
		}
		done = v.T("lk.ev_unlinked")
	case "event-new":
		title, date := strings.TrimSpace(r.PostFormValue("title")), strings.TrimSpace(r.PostFormValue("date"))
		if c.wiring.Primary == nil {
			s.fail(w, r, ops.NotFound("event"))
			return
		}
		out, err := s.Reg.Call(r.Context(), p, "timeline.event.create", timeline.CreateIn{ID: c.primaryID(), Title: title, StoryDate: date})
		if err != nil {
			if e := ops.AsError(err); e.Code == ops.CodeInvalid && e.Field == "story_date" {
				// A date the timeline cannot read is said beside its box, which is in the closed New event section: the form stays as it is.
				v.Target, v.Status, v.StatusBad = "#lkev-date-err", e.Message, true
				s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-evdate-status")
				return
			}
			s.lkStatusErr(w, r, v, err)
			return
		}
		if _, err := s.Reg.Call(r.Context(), p, "timeline.scene.link", timeline.LinkIn{ID: doc.ID, EventID: out.(timeline.EventItem).Item.ID}); err != nil {
			// The event exists and is the Compendium's; only the tie to this scene was refused.
			v.Status, v.StatusBad = v.T("lk.ev_added_unlinked", "name", c.wiring.Primary.Name), true
			s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
			return
		}
		done = v.T("lk.ev_added", "name", c.wiring.Primary.Name)
	default:
		s.fail(w, r, ops.NotFound("page"))
		return
	}
	if !s.lkFillScene(w, r, c, &v, doc, url.Values{}) {
		return
	}
	v.Status = done
	s.lkWrite(w, r, http.StatusOK, v, "lk-main")
}

// ---- the notes view -----------------------------------------------------------------------------------------------------

// lkFillNotes reads the notes view: the book's most recently changed notes, or the one asked for, or the form for a new one.
func (s *Server) lkFillNotes(w http.ResponseWriter, r *http.Request, c lkCtx, v *lookupView, q url.Values) bool {
	v.View, v.Mode = "notes", "notes"
	n := &lkNotes{Q: strings.TrimSpace(q.Get("nq")), CanEdit: c.projEditor(), All: "/p/" + c.proj.Slug + "/research"}
	v.Notes = n
	if !n.CanEdit {
		n.Why = v.T("lk.nt_ro")
	}
	if id := q.Get("note"); id != "" {
		return s.lkFillNote(w, r, c, v, n, id)
	}
	if q.Has("new") {
		v.Mode = "newnote"
		if !n.CanEdit {
			v.Mode = "notes"
		}
		return true
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.research.list", research.ListIn{ID: c.proj.ID, Kind: "note"})
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	items := out.(research.ListOut).Items
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		return items[i].ID > items[j].ID // the same millisecond: the later made first (ids are made in time order)
	})
	limit := lkNotesN
	if n.Q != "" {
		limit = lkNotesQ
	}
	for _, e := range items {
		if n.Q != "" && !strings.Contains(text.FoldTitle(e.Title), text.FoldTitle(n.Q)) {
			continue
		}
		n.Total++
		if len(n.Rows) < limit {
			t := e.Title
			if strings.TrimSpace(t) == "" {
				t = v.T("lk.nt_untitled")
			}
			n.Rows = append(n.Rows, lkNoteRow{ID: e.ID, Title: t, Updated: lkDay(e.UpdatedAt), Words: e.WordCount})
		}
	}
	return true
}

func lkDay(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func (s *Server) lkFillNote(w http.ResponseWriter, r *http.Request, c lkCtx, v *lookupView, n *lkNotes, id string) bool {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.research.get", research.NoteIn{ID: c.proj.ID, NoteID: id})
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	nt := out.(research.Item).Item
	if nt.Kind != "note" || nt.Trashed {
		s.fail(w, r, ops.NotFound("note"))
		return false
	}
	v.Mode = "note"
	n.Note = &lkNote{ID: nt.ID, Title: nt.Title, Body: nt.Body, Revision: nt.Revision, Href: "/p/" + c.proj.Slug + "/research/" + nt.ID,
		HTML: template.HTML(text.RenderHTML(nt.Body, text.HTMLOptions{HeadingShift: 2}))}
	return true
}

// lkNotesPost handles note-new and note-save.
func (s *Server) lkNotesPost(w http.ResponseWriter, r *http.Request, c lkCtx, v lookupView, action string) {
	p := s.principal(r)
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")
	if utf8.RuneCountInString(title) > lkNoteMax {
		title = string([]rune(title)[:lkNoteMax])
	}
	switch action {
	case "note-new":
		out, err := s.Reg.Call(r.Context(), p, "project.research.create", research.CreateIn{ID: c.proj.ID, Kind: "note", Title: title, Body: body})
		if err != nil {
			e := ops.AsError(err)
			if e.Code == ops.CodeInvalid {
				// The words stay in the form: only the line under it changes.
				v.Status, v.StatusBad = e.Message, true
				s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
				return
			}
			s.fail(w, r, err)
			return
		}
		q := url.Values{"note": {out.(research.Item).Item.ID}}
		if !s.lkFillNotes(w, r, c, &v, q) {
			return
		}
		v.Status = v.T("lk.nt_added")
		s.lkWrite(w, r, http.StatusOK, v, "lk-main")
	case "note-save":
		id := r.PostFormValue("note_id")
		rev, _ := strconv.Atoi(r.PostFormValue("base_revision"))
		// Only a live note of this book is saved through the panel: a folder, a clipping or a note in the trash answers as an unknown one.
		if cur, err := s.Reg.Call(r.Context(), p, "project.research.get", research.NoteIn{ID: c.proj.ID, NoteID: id}); err != nil {
			s.fail(w, r, err)
			return
		} else if n := cur.(research.Item).Item; n.Kind != "note" || n.Trashed {
			s.fail(w, r, ops.NotFound("note"))
			return
		}
		in := research.UpdateIn{ID: c.proj.ID, NoteID: id, BaseRevision: rev, Title: &title, Body: &body}
		out, err := s.Reg.Call(r.Context(), p, "project.research.update", in)
		if err != nil {
			e := ops.AsError(err)
			switch e.Code {
			case ops.CodeConflict:
				cur := 0
				switch n := e.Data["revision"].(type) {
				case int:
					cur = n
				case float64:
					cur = int(n)
				}
				if cur == 0 { // the note is in the trash, or some other refusal: its words, as they are
					v.Status, v.StatusBad = e.Message, true
					s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
					return
				}
				v.Notes = &lkNotes{ConflictRev: cur, Note: &lkNote{ID: id}}
				v.Status, v.StatusBad = v.T("lk.nt_conflict"), true
				s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-note-status")
			case ops.CodeInvalid:
				v.Status, v.StatusBad = e.Message, true
				s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
			default:
				s.fail(w, r, err)
			}
			return
		}
		v.Notes = &lkNotes{Note: &lkNote{ID: id, Revision: out.(research.Item).Item.Revision}}
		v.Status = v.T("lk.nt_saved")
		w.Header().Set("Cache-Control", "no-store")
		s.fragment(w, r, http.StatusOK, "lookup", "lk-note-saved", v)
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}
