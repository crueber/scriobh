package server

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The Compendium tab of the writing view's inspector (F-1253 to F-1261): look an entry up, read it, change it in place, give it
// another name, make a new one from the words selected in the text, and say who is in the scene. Every page is a fragment made from
// the entity ops the full pages use, under /ui/projects/{id}/ so the project gate covers it, and every change goes through the same
// ops. Nothing here names a Compendium the caller is not on: what the caller cannot read is "not shared with you" and counts as
// nothing. The shared entry component (partials/entry.html) draws the entry; the sidebar's shell is its autosaving form.

const (
	lkPage      = 30  // the rows of one page of results
	lkNameMax   = 200 // the longest another name
	lkSelWords  = 4   // the words of a selection that are looked up
	lkSelLetter = 3   // the shortest word of a selection that is looked up
)

// lkRow is one entry in the results.
type lkRow struct {
	ID, Kind, KindLabel, Name, Summary, Color, ViaLabel, Compendium string
	Overridden                                                      bool
	Aliases                                                         []string
	CanTarget                                                       bool   // as the target of "another name for": an entry of the primary Compendium the caller edits
	Why                                                             string // why not, as the catalogue's words
	Hits                                                            int
}

type lkAlias struct{ ID, Alias, Notes string }

// lkBookField is one field as this book's version holds it: what the book changes, and what the Compendium says.
type lkBookField struct {
	entities.FieldDef
	Value, Base string
	Blanked     bool
}

// lkEntry is the entry as the panel draws it.
type lkEntry struct {
	ID, Name, Kind, KindLabel, Summary, Body, Color string
	Via, ViaLabel, CompName, CompSlug               string
	Revision                                        int
	Aliases                                         []lkAlias
	Scope                                           string // record, book or ro
	CanRecord, CanBook, CanAlias                    bool
	Why                                             string // the sentence that says why the fields are text
	Grid, GridRO                                    entryGrid
	Book                                            []lkBookField
	BookSet                                         bool
	OvName, OvSummary, OvNotes                      string
	BaseName, BaseSummary                           string
	HTML                                            template.HTML
	Words                                           int
	Relations                                       []relRow
	CanPOV, CanLoc                                  bool
	Sheet, CompHref, WhereHref                      string
	Trashed                                         bool
	Hashes                                          map[string]string // the words of the record as the form began with them
	FirstName                                       string            // a person's first name, offered as another name when none of its names is that word (never matched by itself)
}

type lkExisting struct{ Row lkRow }

// lookupView is what every fragment of the panel is drawn from.
type lookupView struct {
	Page
	Mode          string     // list, entry, new, none, closed, events, notes, note, newnote
	Count         *entryGrid // after a save: the Fields count the save left (nil when there is none to show)
	View          string     // which of the panel's views the buttons at its top say is on: entries, events or notes
	Scene         *lkScene
	Notes         *lkNotes
	Base          string // the address of the panel's own routes
	ProjectID     string
	Owner         bool // the project's owner: may make a Compendium
	Kinds         []entityKind
	Q, Kind       string
	Rows          []lkRow
	Total         int
	NextOffset    int
	Hidden        bool
	Sel           string // the words selected in the text
	Alias         bool   // the rows are targets of "another name for"
	Exact         *lkRow // the selection is already a name of this entry
	E             *lkEntry
	CanCreate     bool
	CreateWhy     string
	CompName      string // the primary Compendium, where a new entry goes
	NewName       string
	NewKind       string
	NewSummary    string
	Error         string
	Existing      *lkRow
	Status        string // a line for the panel's status area
	AkaStatus     string // and one by the names
	StatusBad     bool
	Conflict      *lkConflict
	Collision     *lkRow
	CollisionWord string
	Flash         string
	Notice        string // a sentence above the list: the Compendium is in the trash
	Target        string // where a refusal is said: the status line by default, or the one by the names
	Hashes        map[string]string
	Fresh         bool // the entry was just made from the panel: its first empty field takes the focus
}

// lkConflict is a refused save: the fields the writer changed that someone else changed too, as they are saved now.
type lkConflict struct {
	Revision int
	Rows     []lkConflictRow
}

type lkConflictRow struct{ Label, Theirs string }

var lkViaLabels = map[string]string{"primary": "lk.via_primary", "reference": "lk.via_reference", "link": "lk.via_link"}

// lkCtx is what a request of the panel knows about the book.
type lkCtx struct {
	proj   projects.Project
	wiring compendiums.Wiring
}

func (c lkCtx) projEditor() bool {
	return c.proj.Role == ops.ProjectOwner || c.proj.Role == ops.ProjectEditor
}

func (s *Server) registerLookup(m *http.ServeMux) {
	m.HandleFunc("GET /ui/projects/{id}/lookup", s.uiLookup)
	s.uiRoute("POST /ui/projects/{id}/lookup/{action}", s.uiLookupPost)
}

func (s *Server) lkLoad(w http.ResponseWriter, r *http.Request) (lkCtx, bool) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return lkCtx{}, false
	}
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return lkCtx{}, false
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.compendium.get", compendiums.ProjectIn{ID: proj.ID})
	if err != nil {
		s.fail(w, r, err)
		return lkCtx{}, false
	}
	return lkCtx{proj: proj, wiring: out.(compendiums.WiringItem).Item}, true
}

func (s *Server) lkBase(r *http.Request, c lkCtx) lookupView {
	pg := s.page(r, "", nil)
	return lookupView{Page: pg, Base: "/ui/projects/" + c.proj.ID + "/lookup", ProjectID: c.proj.ID, Owner: c.proj.Role == ops.ProjectOwner, Kinds: entityKinds, View: "entries"}
}

// lkRetargetMain makes the answer replace the whole of the panel's view, where the page asked for a part of it.
func lkRetargetMain(w http.ResponseWriter) {
	w.Header().Set("HX-Retarget", "#lk-main")
	w.Header().Set("HX-Reswap", "innerHTML")
}

func (s *Server) lkWrite(w http.ResponseWriter, r *http.Request, status int, v lookupView, block string) {
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasSuffix(block, "-status") {
		// A refusal is said in a status line, whatever form it came from: nothing the writer typed is replaced.
		if v.Target == "" {
			v.Target = "#lk-status"
		}
		w.Header().Set("HX-Retarget", v.Target)
		w.Header().Set("HX-Reswap", "innerHTML")
	}
	s.fragment(w, r, status, "lookup", block, v)
}

// ---- reading --------------------------------------------------------------------------------------------------

func (s *Server) uiLookup(w http.ResponseWriter, r *http.Request) {
	c, ok := s.lkLoad(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	v := s.lkBase(r, c)
	switch {
	case q.Get("entity") != "":
		if !s.lkFillEntry(w, r, c, &v, q.Get("entity"), q.Get("scope")) {
			return
		}
		s.lkWrite(w, r, http.StatusOK, v, "lk-main")
	case q.Get("view") == "events":
		doc, ok := s.lkDoc(w, r, c, q.Get("doc"))
		if !ok || !s.lkFillScene(w, r, c, &v, doc, q) {
			return
		}
		block := "lk-main"
		if q.Get("part") == "hits" {
			if v.Mode == "events" {
				block = "lk-ev-hits"
			} else {
				lkRetargetMain(w) // the view is another now (no Compendium any more): the whole of it, not inside the list of hits
			}
		}
		s.lkWrite(w, r, http.StatusOK, v, block)
	case q.Get("view") == "notes":
		if !s.lkFillNotes(w, r, c, &v, q) {
			return
		}
		block := "lk-main"
		if q.Get("part") == "nrows" {
			if v.Mode == "notes" {
				block = "lk-nrows"
			} else {
				lkRetargetMain(w) // a note was asked for or a new one: the whole view, not inside the list of rows
			}
		}
		s.lkWrite(w, r, http.StatusOK, v, block)
	case q.Has("new"):
		s.lkFillNew(r, c, &v, q.Get("new"), q.Get("kind"), "")
		s.lkWrite(w, r, http.StatusOK, v, "lk-main")
	default:
		if !s.lkFillList(w, r, c, &v, q) {
			return
		}
		block := "lk-main"
		switch q.Get("part") {
		case "rows":
			block = "lk-rows"
		case "more":
			block = "lk-more"
		}
		if block == "lk-main" && c.wiring.Primary != nil {
			// Entries of a Compendium in the trash are not read through the book at all; the list says so rather than looking empty.
			if co, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.get", compendiums.GetIn{ID: c.wiring.Primary.CompendiumID}); err == nil && co.(compendiums.DetailItem).Item.Compendium.Trashed {
				v.Notice = v.T("lk.trashed_comp")
			}
		}
		s.lkWrite(w, r, http.StatusOK, v, block)
	}
}

func (c lkCtx) primaryID() string {
	if c.wiring.Primary != nil {
		return c.wiring.Primary.CompendiumID
	}
	return ""
}

// lkRowOf is an entry of the results.
func lkRowOf(pg Page, e entities.ProjectEntity) lkRow {
	via := "" // an entry of the primary Compendium is the ordinary case and carries no tag
	switch e.Via {
	case "primary":
	case "reference", "link":
		via = pg.T(lkViaLabels[e.Via], "name", e.CompendiumName)
	default:
		via = pg.T("lk.via_other")
	}
	return lkRow{ID: e.ID, Kind: e.Kind, KindLabel: kindSingular(e.Kind), Name: e.Name, Summary: e.Summary, Color: colorOK(e.Color), ViaLabel: via, Compendium: e.CompendiumName,
		Overridden: e.Overridden, Aliases: e.Aliases, CanTarget: e.Via == "primary" && e.Editable}
}

// lkFillList reads the results: the page of entries the caller may read, and when words are selected the entries those words
// are likely to mean. The empty states are decided here, from the wiring and not from what the list happens to hold.
func (s *Server) lkFillList(w http.ResponseWriter, r *http.Request, c lkCtx, v *lookupView, q url.Values) bool {
	p := s.principal(r)
	v.Mode = "list"
	v.Q, v.Kind = strings.TrimSpace(q.Get("q")), q.Get("kind")
	if !validKind(v.Kind) {
		v.Kind = ""
	}
	v.Sel = lkClean(q.Get("sel"))
	if a := lkClean(q.Get("alias")); a != "" {
		v.Sel, v.Alias = a, true
	}
	off := 0
	if n, err := strconv.Atoi(q.Get("offset")); err == nil && n > 0 {
		off = n
	}
	out, err := s.Reg.Call(r.Context(), p, "project.entity.list", entities.ProjectListIn{ID: c.proj.ID, Kind: v.Kind, Q: v.Q, Limit: lkPage, Offset: off})
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid {
			s.fail(w, r, err)
			return false
		}
		v.Error = e.Message
		return true
	}
	res := out.(entities.ProjectListOut)
	v.Total, v.NextOffset, v.Hidden = res.Total, res.NextOffset, res.Hidden
	for _, e := range res.Items {
		v.Rows = append(v.Rows, lkRowOf(v.Page, e))
	}
	w2 := c.wiring
	if res.Total == 0 {
		switch {
		case w2.Primary == nil && len(w2.References) == 0 && (w2.HasPrimary || w2.Hidden > 0 || res.Hidden):
			// Whatever was asked, a Compendium the caller is not on is "not shared with you": nothing of it is named, counted or hinted at.
			v.Mode = "closed"
			v.Rows, v.Hidden = nil, false
			return true
		case v.Q == "" && v.Kind == "" && off == 0 && !w2.HasPrimary && len(w2.References) == 0 && w2.Hidden == 0 && !res.Hidden:
			v.Mode = "none"
			return true
		}
	}
	if v.Sel != "" {
		s.lkSelection(r, c, v)
	}
	return true
}

// lkClean is a selection as one line, at most as long as a name may be.
// lkNotAFirstName are the words a name can open with that are a title or an article, never the name a person goes by.
var lkNotAFirstName = map[string]bool{"the": true, "a": true, "an": true, "mr": true, "mrs": true, "ms": true, "miss": true, "dr": true, "sir": true, "lady": true, "lord": true, "saint": true, "st": true}

// lkFirstNameOffer is the first name of a person with a longer name, offered as another name for them, or "" when there is
// nothing to offer: a place, an item, an organisation or a concept is never offered one; nor is a name of one word, or a
// person one of whose names already is that word. Nothing is matched by it: the writer adds it with one click.
func lkFirstNameOffer(kind, name string, aliases []entities.Alias) string {
	words := strings.Fields(name)
	if kind != "character" || len(words) < 2 || lkNotAFirstName[strings.ToLower(strings.TrimSuffix(words[0], "."))] {
		return ""
	}
	for _, a := range aliases {
		if strings.EqualFold(strings.TrimSpace(a.Alias), words[0]) {
			return ""
		}
	}
	return words[0]
}

func lkClean(sel string) string {
	sel = strings.Join(strings.Fields(sel), " ")
	if utf8.RuneCountInString(sel) > lkNameMax {
		sel = string([]rune(sel)[:lkNameMax])
	}
	return sel
}

// lkSelection finds the entries the selected words are likely to mean: one search for each word of three letters or more (up to
// four), merged and ranked by how many words matched, and the entry that already has the whole selection for a name.
func (s *Server) lkSelection(r *http.Request, c lkCtx, v *lookupView) {
	p := s.principal(r)
	if o, err := s.Reg.Call(r.Context(), p, "project.entity.get", entities.ProjectGetIn{ID: c.proj.ID, Entity: v.Sel}); err == nil {
		pe := o.(entities.ProjectItem).Item
		for _, a := range append([]string{pe.Name}, pe.Aliases...) {
			if text.FoldTitle(a) == text.FoldTitle(v.Sel) {
				row := lkRowOf(v.Page, pe)
				v.Exact = &row
				break
			}
		}
	}
	var words []string
	seen := map[string]bool{}
	for _, wd := range strings.FieldsFunc(v.Sel, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '\'' && r != '’' }) {
		k := text.FoldTitle(wd)
		if utf8.RuneCountInString(wd) < lkSelLetter || seen[k] || len(words) >= lkSelWords {
			continue
		}
		seen[k] = true
		words = append(words, wd)
	}
	hits := map[string]*lkRow{}
	var order []string
	for _, wd := range words {
		o, err := s.Reg.Call(r.Context(), p, "project.entity.list", entities.ProjectListIn{ID: c.proj.ID, Q: wd, Limit: 8})
		if err != nil {
			continue
		}
		for _, e := range o.(entities.ProjectListOut).Items {
			if row, ok := hits[e.ID]; ok {
				row.Hits++
				continue
			}
			row := lkRowOf(v.Page, e)
			row.Hits = 1
			hits[e.ID] = &row
			order = append(order, e.ID)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return hits[order[i]].Hits > hits[order[j]].Hits })
	var likely []lkRow
	for _, id := range order {
		likely = append(likely, *hits[id])
		if len(likely) >= 6 {
			break
		}
	}
	if v.Q == "" && (len(likely) > 0 || v.Exact != nil) { // a search the writer typed is answered as typed
		v.Rows, v.Total, v.NextOffset = likely, len(likely), 0
	}
	if v.Alias {
		for i := range v.Rows {
			rw := &v.Rows[i]
			if rw.CanTarget {
				continue
			}
			rw.Why = v.T("lk.sel_cant_ro")
			if rw.ViaLabel != "" {
				rw.Why = v.T("lk.sel_cant_ref", "name", rw.Compendium)
			}
		}
	}
}

// lkFillEntry reads one entry through the ops the full pages use: project.entity.get (what this book reads, and the proof that
// the book reaches it), entity.get (the revision, the names with their ids and the relationships) and compendium.get (the role
// and whether it is in the trash). An entry the caller cannot read is as missing as one that never was.
func (s *Server) lkFillEntry(w http.ResponseWriter, r *http.Request, c lkCtx, v *lookupView, id, scope string) bool {
	if !entityIDPattern.MatchString(id) {
		s.fail(w, r, ops.NotFound("entity"))
		return false
	}
	e, ok := s.lkEntryOf(w, r, c, v.Page, id, scope)
	if !ok {
		return false
	}
	v.Mode, v.E = "entry", e
	return true
}

func (s *Server) lkEntryOf(w http.ResponseWriter, r *http.Request, c lkCtx, pg Page, id, scope string) (*lkEntry, bool) {
	p := s.principal(r)
	ctx := r.Context()
	po, err := s.Reg.Call(ctx, p, "project.entity.get", entities.ProjectGetIn{ID: c.proj.ID, Entity: id})
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	pe := po.(entities.ProjectItem).Item
	eo, err := s.Reg.Call(ctx, p, "entity.get", entities.GetIn{ID: pe.ID})
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	det := eo.(entities.DetailItem).Item
	co, err := s.Reg.Call(ctx, p, "compendium.get", compendiums.GetIn{ID: pe.CompendiumID})
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	comp := s.addressed(r, co.(compendiums.DetailItem).Item.Compendium)
	e := &lkEntry{ID: pe.ID, Name: pe.Name, Kind: pe.Kind, KindLabel: kindSingular(pe.Kind), Summary: pe.Summary, Body: det.Body, Color: colorOK(pe.Color),
		Via: pe.Via, CompName: pe.CompendiumName, CompSlug: comp.Slug, Revision: det.Revision, Trashed: det.Trashed,
		Sheet: "/p/" + c.proj.Slug + "/e/" + pe.ID, CompHref: "/c/" + comp.Slug + "/e/" + pe.ID, WhereHref: "/p/" + c.proj.Slug + "/e/" + pe.ID + "#where"}
	if k, ok := lkViaLabels[pe.Via]; ok {
		e.ViaLabel = pg.T(k, "name", pe.CompendiumName)
	} else {
		e.ViaLabel = pg.T("lk.via_other")
	}
	for _, a := range det.Aliases {
		e.Aliases = append(e.Aliases, lkAlias{ID: a.ID, Alias: a.Alias, Notes: a.Notes})
	}
	e.FirstName = lkFirstNameOffer(pe.Kind, det.Name, det.Aliases)
	for _, rel := range det.Relations {
		e.Relations = append(e.Relations, relRow{Relation: rel, Phrase: rel.Label + " " + rel.OtherName})
	}
	// What the caller may do, from the same facts the ops check. The ops stay the authority.
	trashed := comp.Trashed
	e.CanRecord = pe.Via == "primary" && pe.Editable && !trashed
	e.CanBook = c.projEditor() && !trashed
	e.CanAlias = e.CanRecord
	recordWhy, bookWhy := "", ""
	switch {
	case trashed:
		recordWhy = pg.T("lk.ro_trash")
	case pe.Via != "primary":
		recordWhy = pg.T("lk.ro_ref", "name", pe.CompendiumName)
	case !pe.Editable:
		recordWhy = pg.T("lk.ro_compendium")
	}
	switch {
	case trashed:
		bookWhy = pg.T("lk.ro_trash")
	case !c.projEditor():
		bookWhy = pg.T("lk.ro_book")
	}
	switch {
	case scope == "book" && e.CanBook:
		e.Scope = "book"
	case e.CanRecord:
		e.Scope = "record"
	case e.CanBook:
		e.Scope = "book"
	default:
		e.Scope = "ro"
	}
	switch e.Scope {
	case "book":
		e.Why = recordWhy
	case "ro":
		e.Why = strings.TrimSpace(recordWhy + " " + bookWhy)
		if recordWhy == bookWhy {
			e.Why = recordWhy
		}
	}
	e.CanPOV = e.CanBook && !trashed && pe.Kind == "character"
	e.CanLoc = e.CanBook && !trashed && pe.Kind == "place"
	e.HTML = template.HTML(text.RenderHTML(det.Body, text.HTMLOptions{HeadingShift: 2}))
	e.Words = len(strings.Fields(det.Body))
	// The fields: the record's as the Compendium holds them (a person who may only read sees what this book reads), and this book's
	// version of each.
	fo, err := s.Reg.Call(ctx, p, "entity.field.list", entities.FieldListIn{ID: pe.CompendiumID, Kind: pe.Kind})
	var rows, eff []fieldRow
	var defs []entities.FieldDef
	if err == nil {
		defs = fo.(entities.FieldListOut).Items
		for _, f := range defs {
			rows = append(rows, fieldRow{FieldDef: f, Value: det.Fields[f.Key], Multi: multiline(f.Key, det.Fields[f.Key])})
			eff = append(eff, fieldRow{FieldDef: f, Value: pe.Fields[f.Key]})
		}
	}
	e.Grid, e.GridRO = gridOf(rows, false), gridOf(eff, true)
	e.Hashes = lkHashes(det.Entity, defs)
	for _, f := range projectSheetFieldsOf(defs, pe) {
		e.Book = append(e.Book, lkBookField{FieldDef: f.FieldDef, Value: f.Override, Base: f.Base, Blanked: f.Blanked})
	}
	e.BookSet = pe.Override != nil
	e.BaseName, e.BaseSummary = pe.Name, pe.Summary
	if pe.Base != nil {
		e.BaseName, e.BaseSummary = pe.Base.Name, pe.Base.Summary
	}
	if pe.Override != nil {
		e.OvNotes = pe.Override.Notes
		if pe.Override.Name != nil {
			e.OvName = *pe.Override.Name
		}
		if pe.Override.Summary != nil {
			e.OvSummary = *pe.Override.Summary
		}
	}
	if e.Scope == "record" {
		e.Name, e.Summary = det.Name, det.Summary
	}
	return e, true
}

// lkFillNew prepares the form for a new entry: the name the selection gave, a kind (none chosen unless the writer asked for one),
// and a plain sentence of why when the caller may not create one.
func (s *Server) lkFillNew(r *http.Request, c lkCtx, v *lookupView, name, kind, summary string) {
	v.Mode = "new"
	v.NewName, v.NewKind, v.NewSummary = lkClean(name), kind, summary
	if !validKind(v.NewKind) {
		v.NewKind = ""
	}
	pr := c.wiring.Primary
	switch {
	case pr == nil && c.wiring.HasPrimary:
		v.Mode = "closed"
	case pr == nil:
		v.CanCreate, v.CreateWhy = false, v.T("lk.new_nobible")
	default:
		v.CompName = pr.Name
		// The role and the trash are the Compendium's own; the op stays the authority.
		v.CanCreate = true
		if co, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.get", compendiums.GetIn{ID: pr.CompendiumID}); err == nil {
			switch cc := co.(compendiums.DetailItem).Item.Compendium; {
			case cc.Trashed:
				v.CanCreate, v.CreateWhy = false, v.T("lk.ro_trash")
			case !compEditable(cc):
				v.CanCreate, v.CreateWhy = false, v.T("lk.new_ro")
			}
		}
	}
}

// ---- writing --------------------------------------------------------------------------------------------------

func (s *Server) uiLookupPost(w http.ResponseWriter, r *http.Request) {
	c, ok := s.lkLoad(w, r)
	if !ok {
		return
	}
	v := s.lkBase(r, c)
	action := r.PathValue("action")
	if action == "make" {
		s.lkMake(w, r, c, v)
		return
	}
	if action == "new" {
		s.lkNew(w, r, c, v)
		return
	}
	switch action {
	case "event-link", "event-unlink", "event-new":
		s.lkScenePost(w, r, c, v, action)
		return
	case "note-new", "note-save":
		s.lkNotesPost(w, r, c, v, action)
		return
	}
	id := r.PostFormValue("entity_id")
	if !entityIDPattern.MatchString(id) {
		s.fail(w, r, ops.NotFound("entity"))
		return
	}
	// The book must reach the entry, and the caller must be on its Compendium: project.entity.get is both proofs, and a
	// stranger to the Compendium gets exactly what a missing id gets. The entity ops check a Compendium's membership only, so
	// this is what stops a writer who is on one Compendium from writing to another through a book that does not use it.
	po, err := s.Reg.Call(r.Context(), s.principal(r), "project.entity.get", entities.ProjectGetIn{ID: c.proj.ID, Entity: id})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pe := po.(entities.ProjectItem).Item
	id = pe.ID
	switch action {
	case "save":
		s.lkSave(w, r, c, v, pe)
	case "override":
		s.lkOverride(w, r, c, v, pe)
	case "override-clear":
		if _, err := s.Reg.Call(r.Context(), s.principal(r), "project.entity.override.clear", entities.OverrideClearIn{ID: c.proj.ID, EntityID: id}); err != nil {
			s.lkStatusErr(w, r, v, err)
			return
		}
		if !s.lkFillEntry(w, r, c, &v, id, "book") {
			return
		}
		v.Status = v.T("lk.cleared")
		s.lkWrite(w, r, http.StatusOK, v, "lk-main")
	case "alias", "unalias":
		s.lkAlias(w, r, c, v, pe, action)
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

// lkStatusErr says why a change was refused, in the status area, in plain words. A refusal that is the caller's own state (not
// allowed, not found) is the ordinary failure, answered as JSON to the panel's script.
func (s *Server) lkStatusErr(w http.ResponseWriter, r *http.Request, v lookupView, err error) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	v.Status, v.StatusBad = e.Message, true
	s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
}

// The hash of a field's words, as js/lookup.js makes it: what a save names as the words it started from. A field that is as it was
// when the writer opened it can be saved over a newer revision, because nothing of anyone else's is in it; one that is not is a conflict.
func lkHash(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	h1, h2 := uint32(2166136261), uint32(5381)
	for _, b := range []byte(v) {
		h1 = (h1 ^ uint32(b)) * 16777619
		h2 = (h2*33 ^ uint32(b))
	}
	return fmt.Sprintf("%08x%08x", h1, h2)
}

// lkNorm is a posted value as the ops keep it.
func lkNorm(key, v string) string {
	if key == "body" {
		return strings.ReplaceAll(v, "\r\n", "\n")
	}
	return strings.TrimSpace(v)
}

// entityValue is the value a form key names.
func entityValue(e entities.Entity, key string) string {
	switch key {
	case "name":
		return e.Name
	case "sort_name":
		return e.SortName
	case "summary":
		return e.Summary
	case "body":
		return e.Body
	case "color":
		return e.Color
	}
	return e.Fields[strings.TrimPrefix(key, "f.")]
}

// lkHashes are the hashes of the words of an entry's record, by form key, for the form to hold as the words it started from.
func lkHashes(e entities.Entity, defs []entities.FieldDef) map[string]string {
	m := map[string]string{"summary": lkHash(e.Summary), "body": lkHash(e.Body)}
	for _, d := range defs {
		m["f."+d.Key] = lkHash(e.Fields[d.Key])
	}
	return m
}

// lkSave saves the fields the writer changed, and only those (the form sends nothing else). It goes through when nobody has changed the
// entry since it was read (the revision) or when none of the fields it changes has been changed by anyone else (their words are
// still the ones the form started from, or the ones an earlier save of this form made). A field someone else changed is a
// conflict: nothing is saved, and the box says which fields differ. force (Keep mine) saves them over what is there.
func (s *Server) lkSave(w http.ResponseWriter, r *http.Request, c lkCtx, v lookupView, pe entities.ProjectEntity) {
	if pe.Via != "primary" {
		v.Status, v.StatusBad = v.T("lk.ro_ref", "name", pe.CompendiumName), true
		s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
		return
	}
	p := s.principal(r)
	eo, err := s.Reg.Call(r.Context(), p, "entity.get", entities.GetIn{ID: pe.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cur := eo.(entities.DetailItem).Item
	in := entityUpdateFromForm(r, pe.ID)
	in.BaseRevision = &cur.Revision                           // the check and the write are one step: a change in between is a conflict
	asked, _ := strconv.Atoi(r.PostForm.Get("base_revision")) // a request that names no revision is not checking one
	force := r.PostForm.Get("force") == "1"
	var rows []lkConflictRow
	if asked > 0 && asked != cur.Revision && !force {
		var defs []entities.FieldDef
		if fo, ferr := s.Reg.Call(r.Context(), p, "entity.field.list", entities.FieldListIn{ID: pe.CompendiumID, Kind: pe.Kind}); ferr == nil {
			defs = fo.(entities.FieldListOut).Items
		}
		label := func(key string) string {
			switch key {
			case "summary":
				return v.T("lk.summary")
			case "body":
				return v.T("lk.desc")
			}
			for _, d := range defs {
				if "f."+d.Key == key {
					return d.Label
				}
			}
			return key
		}
		var keys []string
		if in.Summary != nil {
			keys = append(keys, "summary")
		}
		if in.Body != nil {
			keys = append(keys, "body")
		}
		for k := range in.Fields {
			keys = append(keys, "f."+k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			now := entityValue(cur.Entity, k)
			if lkNorm(k, r.PostForm.Get(k)) == now {
				continue
			}
			ok := false
			for _, h := range strings.Split(r.PostForm.Get("o."+k), ",") {
				ok = ok || h != "" && h == lkHash(now)
			}
			if !ok {
				rows = append(rows, lkConflictRow{Label: label(k), Theirs: now})
			}
		}
	}
	refuse := func() {
		v.Conflict = &lkConflict{Revision: cur.Revision, Rows: rows}
		v.Status, v.StatusBad = v.T("lk.conflict"), true
		s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
	}
	if len(rows) > 0 {
		refuse()
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "entity.update", in)
	if err != nil {
		if e := ops.AsError(err); e.Code == ops.CodeConflict && e.Data != nil {
			if _, ok := e.Data["revision"]; ok {
				refuse()
				return
			}
		}
		s.lkStatusErr(w, r, v, err)
		return
	}
	saved := out.(entities.Item).Item
	v.E = &lkEntry{ID: pe.ID, Revision: saved.Revision}
	v.Hashes = map[string]string{}
	for k := range r.PostForm {
		if k == "summary" || k == "body" || strings.HasPrefix(k, "f.") {
			v.Hashes[k] = lkHash(entityValue(saved, k))
		}
	}
	v.Status = v.T("lk.saved")
	// The Fields heading is drawn again with the count this save left, out of band (a save swaps only the status line).
	if fo, ferr := s.Reg.Call(r.Context(), p, "entity.field.list", entities.FieldListIn{ID: pe.CompendiumID, Kind: pe.Kind}); ferr == nil {
		var rows []fieldRow
		for _, f := range fo.(entities.FieldListOut).Items {
			rows = append(rows, fieldRow{FieldDef: f, Value: saved.Fields[f.Key]})
		}
		if len(rows) > 0 {
			g := gridOf(rows, false)
			v.Count = &g
		}
	}
	s.lkWrite(w, r, http.StatusOK, v, "lk-saved")
}

func (s *Server) lkOverride(w http.ResponseWriter, r *http.Request, c lkCtx, v lookupView, pe entities.ProjectEntity) {
	in := overrideFromForm(r, c.proj.ID, pe.ID)
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "project.entity.override.set", in); err != nil {
		s.lkStatusErr(w, r, v, err)
		return
	}
	v.Status = v.T("lk.saved_book")
	if r.PostForm.Has("name") {
		w.Header().Set("HX-Trigger", `{"lk:names":""}`) // the book may call it something else now: the marks in the text follow
	}
	s.lkWrite(w, r, http.StatusOK, v, "lk-status")
}

// lkAlias adds or removes another name. A word another entry already holds is refused, naming that entry, with a button that
// opens it; nothing is added. After a change the marks in the text owe a rescan, which the page is told of (HX-Trigger).
func (s *Server) lkAlias(w http.ResponseWriter, r *http.Request, c lkCtx, v lookupView, pe entities.ProjectEntity, action string) {
	p := s.principal(r)
	var err error
	v.Target = "#lk-aka-status" // by the names; from a selection's list there is only the one status line
	if r.PostFormValue("then") == "entry" {
		v.Target = "#lk-status"
	}
	if pe.Via != "primary" {
		v.Status, v.StatusBad = v.T("lk.ro_ref", "name", pe.CompendiumName), true
		s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
		return
	}
	word := strings.TrimSpace(r.PostFormValue("alias"))
	if action == "alias" {
		if strings.ContainsAny(word, "\r\n") || utf8.RuneCountInString(word) > lkNameMax {
			v.Status, v.StatusBad = v.T("lk.name_long"), true
			s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
			return
		}
		_, err = s.Reg.Call(r.Context(), p, "entity.alias.add", entities.AliasAddIn{ID: pe.ID, Alias: word, Notes: r.PostFormValue("notes")})
	} else {
		_, err = s.Reg.Call(r.Context(), p, "entity.alias.remove", entities.AliasRemoveIn{ID: pe.ID, AliasID: r.PostFormValue("alias_id")})
	}
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeConflict && e.Data != nil {
			if oid, ok := e.Data["entity_id"].(string); ok {
				name, _ := e.Data["name"].(string)
				v.Collision = &lkRow{ID: oid, Name: name}
				v.CollisionWord = word
				v.Status, v.StatusBad = v.T("lk.aka_collision", "word", word, "name", name), true
				s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-status")
				return
			}
		}
		s.lkStatusErr(w, r, v, err)
		return
	}
	if !s.lkFillEntry(w, r, c, &v, pe.ID, r.PostFormValue("scope")) {
		return
	}
	msg := v.T("lk.aka_removed")
	if action == "alias" {
		msg = v.T("lk.aka_added")
	}
	if r.PostFormValue("then") == "entry" {
		v.Status = msg
	} else {
		v.AkaStatus = msg
	}
	w.Header().Set("HX-Trigger", `{"lk:names":""}`)
	block := "lk-main"
	if r.PostFormValue("then") != "entry" {
		block = "lk-aka-box"
	}
	s.lkWrite(w, r, http.StatusOK, v, block)
}

// lkNew makes an entry in the book's primary Compendium from a name and a kind, as an editor of that Compendium.
func (s *Server) lkNew(w http.ResponseWriter, r *http.Request, c lkCtx, v lookupView) {
	name, kind, summary := lkClean(r.PostFormValue("name")), r.PostFormValue("kind"), strings.TrimSpace(r.PostFormValue("summary"))
	s.lkFillNew(r, c, &v, name, kind, summary)
	if v.Mode == "closed" || !v.CanCreate {
		s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-main")
		return
	}
	fail := func(msg string) {
		v.Error = msg
		s.lkWrite(w, r, http.StatusUnprocessableEntity, v, "lk-main")
	}
	if !validKind(kind) {
		fail(v.T("lk.new_kind_req"))
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "entity.create", entities.CreateIn{ID: c.primaryID(), Kind: kind, Name: name, Summary: summary})
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeConflict && e.Data != nil {
			if oid, ok := e.Data["entity_id"].(string); ok {
				if o, gerr := s.Reg.Call(r.Context(), s.principal(r), "project.entity.get", entities.ProjectGetIn{ID: c.proj.ID, Entity: oid}); gerr == nil {
					row := lkRowOf(v.Page, o.(entities.ProjectItem).Item)
					v.Existing = &row
				}
			}
			fail(v.T("lk.new_collision", "name", name))
			return
		}
		if e.Code == ops.CodeInvalid {
			fail(e.Message)
			return
		}
		s.fail(w, r, err)
		return
	}
	id := out.(entities.Item).Item.ID
	v2 := s.lkBase(r, c)
	if !s.lkFillEntry(w, r, c, &v2, id, "record") {
		return
	}
	v2.Fresh = true
	v2.Status = v2.T("lk.new_added", "name", v.CompName)
	w.Header().Set("HX-Trigger", `{"lk:names":""}`)
	s.lkWrite(w, r, http.StatusOK, v2, "lk-main")
}

// lkMake gives a book without a Compendium one, as its owner (compendium.create with the project).
func (s *Server) lkMake(w http.ResponseWriter, r *http.Request, c lkCtx, v lookupView) {
	name := c.proj.Title
	if utf8.RuneCountInString(name) > 180 {
		name = string([]rune(name)[:180])
	}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.create", compendiums.CreateIn{Name: v.T("lk.make_name", "title", name), ProjectID: c.proj.ID}); err != nil {
		s.fail(w, r, err)
		return
	}
	c2, ok := s.lkLoad(w, r)
	if !ok {
		return
	}
	v = s.lkBase(r, c2)
	if !s.lkFillList(w, r, c2, &v, url.Values{}) {
		return
	}
	s.lkWrite(w, r, http.StatusOK, v, "lk-main")
}
