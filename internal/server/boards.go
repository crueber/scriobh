package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The corkboard (/p/{slug}/cards), the status board (/p/{slug}/board) and reading mode (/p/{slug}/read).
// All three are read through the same ops as the outline (outline.get, doc.list, doc.get); changes are made by
// js/boards.js through doc.move and doc.update, which carry the revision a card was drawn with. The pages read
// without script; the script adds moving and editing.

func (s *Server) registerBoards(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/cards", s.cardsPage)
	m.HandleFunc("GET /p/{slug}/board", s.boardPage)
	m.HandleFunc("GET /p/{slug}/read", s.readLanding)
	m.HandleFunc("GET /p/{slug}/read/{docid}", s.readPage)
	m.HandleFunc("GET /ui/projects/{id}/cards", s.uiCards)
	m.HandleFunc("GET /ui/projects/{id}/board", s.uiBoard)
	m.HandleFunc("GET /ui/projects/{id}/read/{docid}", s.uiRead)
}

// boardProject finds the project a /p/{slug}/... address names for the caller; a stranger gets a 404.
func (s *Server) boardProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return "", false
	}
	pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, r.PathValue("slug"))
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return "", false
	}
	return pid, true
}

// ---- the corkboard and the status board ---------------------------------------------------------

type moveTarget struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// cardRow is a card with its neighbours among the documents inside the same folder, parts and cards a filter hides
// included, so "earlier" and "later" always swap with the document that really is next to it.
type cardRow struct {
	outlineRow
	Prev, Prev2, Next string
}

type cardLane struct {
	ID    string
	Title string // empty for cards outside any part when the book has no parts at all
	Part  bool
	Words int
	Cards []cardRow
	// A part moves as a whole: its neighbours among the top-level documents, as for a card.
	Prev, Prev2, Next string
}

type statusOpt struct{ Value, Label string }

type cardsView struct {
	outlineView
	Lanes       []cardLane
	Filter      string // a status, or empty for all
	Shown       int
	Cards       int
	Statuses    []statusOpt
	FilterLabel string
	TargetsJSON string
}

type boardCol struct {
	Status string
	Label  string
	Words  int
	Cards  []outlineRow
}

type boardView struct {
	outlineView
	Cols      []boardCol
	Cards     int
	StatusMap string // "idea:Idea,outline:Outline,..." for the script
}

var statusOrder3 = []statusOpt{{"idea", "Idea"}, {"outline", "Outline"}, {"draft", "Draft"}, {"revise", "Revise"}, {"done", "Done"}}

func (s *Server) cardsFor(r *http.Request, projectID string) (cardsView, error) {
	ov, err := s.outlineFor(r, projectID)
	if err != nil {
		return cardsView{}, err
	}
	return buildCards(ov, r.URL.Query().Get("status")), nil
}

// buildCards groups the outline into lanes of cards; a filter that is not a status is ignored.
func buildCards(ov outlineView, filter string) cardsView {
	v := cardsView{outlineView: ov, Statuses: statusOrder3}
	if statusLabels[filter] != "" {
		v.Filter, v.FilterLabel = filter, statusLabels[filter]
	}
	hasPart := false
	prev, prev2 := map[string]string{}, map[string]string{}
	next := map[string]string{}
	last := map[string]string{}
	for _, row := range ov.Rows {
		if row.Kind == "part" {
			hasPart = true
		}
		if l := last[row.ParentID]; l != "" {
			prev[row.ID], next[l] = l, row.ID
			prev2[row.ID] = prev[l]
		}
		last[row.ParentID] = row.ID
	}
	var targets []moveTarget
	var lane *cardLane
	flush := func() {
		if lane != nil && (len(lane.Cards) > 0 || (lane.Part && v.Filter == "")) {
			v.Lanes = append(v.Lanes, *lane)
		}
		lane = nil
	}
	for _, row := range ov.Rows {
		switch row.Kind {
		case "part":
			flush()
			lane = &cardLane{ID: row.ID, Title: row.Title, Part: true, Words: row.Rollup.Words, Prev: prev[row.ID], Prev2: prev2[row.ID], Next: next[row.ID]}
			targets = append(targets, moveTarget{ID: row.ID, Label: row.Title, Kind: "part"})
			continue
		case "chapter":
			targets = append(targets, moveTarget{ID: row.ID, Label: "Chapter " + row.Number + ": " + row.Title, Kind: "chapter"})
		}
		// Cards outside any part (before the first one, or after a part's last card) form a lane of their own.
		if lane == nil || (lane.Part && row.Level == 0) {
			flush()
			lane = &cardLane{ID: "top-" + row.ID, Title: "Outside the parts"}
			if !hasPart {
				lane.Title = ""
			}
		}
		v.Cards++
		lane.Words += row.WordCount
		if v.Filter != "" && row.Status != v.Filter {
			continue
		}
		v.Shown++
		lane.Cards = append(lane.Cards, cardRow{outlineRow: row, Prev: prev[row.ID], Prev2: prev2[row.ID], Next: next[row.ID]})
	}
	flush()
	if b, err := json.Marshal(targets); err == nil {
		v.TargetsJSON = string(b)
	}
	return v
}

func (s *Server) boardFor(r *http.Request, projectID string) (boardView, error) {
	ov, err := s.outlineFor(r, projectID)
	if err != nil {
		return boardView{}, err
	}
	return buildBoard(ov), nil
}

func buildBoard(ov outlineView) boardView {
	v := boardView{outlineView: ov}
	cols := map[string]*boardCol{}
	var names []string
	for _, o := range statusOrder3 {
		v.Cols = append(v.Cols, boardCol{Status: o.Value, Label: o.Label})
		names = append(names, o.Value+":"+o.Label)
	}
	for i := range v.Cols {
		cols[v.Cols[i].Status] = &v.Cols[i]
	}
	v.StatusMap = strings.Join(names, ",")
	for _, row := range ov.Rows {
		if row.Kind == "part" {
			continue
		}
		c := cols[row.Status]
		if c == nil {
			c = cols["idea"]
		}
		c.Cards = append(c.Cards, row)
		c.Words += row.WordCount
		v.Cards++
	}
	return v
}

func (s *Server) cardsPage(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.boardProject(w, r)
	if !ok {
		return
	}
	v, err := s.cardsFor(r, pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "cards", s.page(r, "Corkboard of "+v.Project.Title, v))
}

func (s *Server) boardPage(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.boardProject(w, r)
	if !ok {
		return
	}
	v, err := s.boardFor(r, pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "board", s.page(r, "Status board of "+v.Project.Title, v))
}

func (s *Server) uiCards(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	v, err := s.cardsFor(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "boards", "cards-body", v)
}

func (s *Server) uiBoard(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	v, err := s.boardFor(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "boards", "board-body", v)
}

// ---- reading mode -----------------------------------------------------------------------------------------

// readUnit is one page of reading mode: a document that is not inside a chapter, with the scenes and notes
// inside it. Parts with no text of their own are not pages; their titles are shown above the first page inside.
type readUnit struct {
	ID      string
	Title   string
	Kind    string
	Number  string
	Kicker  string // "Part One"
	Words   int
	Members []docs.Meta // the document, then what is inside it, in reading order
}

type readTocItem struct {
	ID      string
	Label   string
	Part    string // a part heading before this entry, when one starts here
	Current bool
	Words   int
	Docs    string // the ids of the documents on this page, space separated, so a count of comments can be put on it
}

type readData struct {
	Project   projects.Project
	Slug      string
	ProjectID string
	CanEdit   bool
	Prefs     editorPrefs
	Empty     bool
	Unit      *readUnit
	Article   readArticle
	Prev      *readTocItem
	Next      *readTocItem
	Toc       []readTocItem
	Position  int
	Count     int
	WriteURL  string
	// Review: the member may see and write comments (reviewer and up); Decide: and may resolve any thread and accept
	// suggestions (editor and owner). A reader reads and nothing else.
	Role           string
	Review, Decide bool
	Dir            string // "rtl", "ltr" or "" (auto) for the manuscript's text
}

type readArticle struct {
	ID     string
	Doc    string
	Title  string
	Label  string
	Kicker string
	Next   string // the next page's id, for the scrolling script
	Body   []readSection
}

type readSection struct {
	ID    string
	Doc   string // the document this stretch of the page is, for a comment or a suggestion
	Rev   int    // its revision as rendered, which a suggestion is made against
	Title string
	Scene bool
	HTML  template.HTML
}

func foldTitle(t string) string { return strings.ToLower(strings.Join(strings.Fields(t), " ")) }

// readUnits lays the project's manuscript out as pages, in reading order.
func readUnits(items []docs.Meta, order []string) (units []readUnit, byMember map[string]int) {
	byID := make(map[string]docs.Meta, len(items))
	for _, m := range items {
		byID[m.ID] = m
	}
	skip := map[string]bool{}
	byMember = map[string]int{}
	var num chapterCounter
	var parts []string // titles of parts met since the last page
	var partKicker string
	for _, id := range order {
		m, ok := byID[id]
		if !ok {
			continue
		}
		if m.Kind == "note" || skip[m.ParentID] {
			skip[id] = true
			continue
		}
		label := num.label(m.Kind)
		if pu, in := byMember[m.ParentID]; in && units[pu].Kind == "chapter" {
			units[pu].Members = append(units[pu].Members, m)
			units[pu].Words += m.WordCount
			byMember[id] = pu
			continue
		}
		if m.Kind == "part" {
			partKicker = displayTitle(m.Title)
			if m.WordCount == 0 {
				parts = append(parts, partKicker)
				continue
			}
		}
		u := readUnit{ID: id, Title: displayTitle(m.Title), Kind: m.Kind, Number: label, Words: m.WordCount, Members: []docs.Meta{m}}
		if m.Kind != "part" && m.ParentID != "" {
			if pm, ok := byID[m.ParentID]; ok && pm.Kind == "part" {
				u.Kicker = displayTitle(pm.Title)
			}
		}
		if len(parts) > 0 {
			u.Kicker = strings.Join(parts, " · ")
			parts = nil
		}
		units = append(units, u)
		byMember[id] = len(units) - 1
	}
	return units, byMember
}

func (s *Server) readData(r *http.Request, projectID, docID string) (readData, int, error) {
	p := s.principal(r)
	ctx := r.Context()
	po, err := s.Reg.Call(ctx, p, "project.get", projects.GetIn{ID: projectID})
	if err != nil {
		return readData{}, 0, err
	}
	proj := po.(projects.Item).Item.Addressed()
	if proj.Trashed {
		return readData{}, 0, ops.NotFound("project")
	}
	lo, err := s.Reg.Call(ctx, p, "doc.list", docs.ListIn{ProjectID: projectID})
	if err != nil {
		return readData{}, 0, err
	}
	list := lo.(docs.ListOut)
	units, byMember := readUnits(list.Items, list.Order)
	d := readData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CanEdit: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor, Count: len(units)}
	d.Role = proj.Role
	d.Decide = d.CanEdit
	d.Review = d.CanEdit || proj.Role == ops.ProjectReviewer
	d.Dir = textDir(proj.Language)
	d.Prefs = defaultPrefs()
	if len(units) == 0 {
		d.Empty = true
		return d, -1, nil
	}
	idx := 0
	if docID != "" {
		i, ok := byMember[docID]
		if !ok {
			return d, 0, ops.NotFound("document")
		}
		idx = i
		if units[i].ID != docID { // a scene inside a chapter: its page, at its place
			return d, i, nil
		}
	}
	d.Position = idx + 1
	u := units[idx]
	d.Unit = &u
	d.WriteURL = "/p/" + proj.Slug + "/d/" + u.ID
	for i, un := range units {
		item := readTocItem{ID: un.ID, Label: chapterLabel(proj.Language, un.Number, un.Title), Part: "", Current: i == idx, Words: un.Words, Docs: memberIDs(un.Members)}
		if un.Kicker != "" && (i == 0 || units[i-1].Kicker != un.Kicker) {
			item.Part = un.Kicker
		}
		d.Toc = append(d.Toc, item)
	}
	if idx > 0 {
		d.Prev = &d.Toc[idx-1]
	}
	if idx+1 < len(units) {
		d.Next = &d.Toc[idx+1]
	}
	art, err := s.readArticle(r, proj, units, idx, list.Items)
	if err != nil {
		return d, 0, err
	}
	d.Article = art
	return d, idx, nil
}

// chapterLabel is how a chapter is named in a list: "Chapter 3: The Quay", or just "Chapter 3" when its title only says that.
func chapterLabel(lang, number, title string) string {
	title = strings.TrimSpace(title)
	if number == "" {
		return title
	}
	name := i18n.Default.T(lang, "rd.chapter", "n", number) // in the book's own language where the catalogue has it
	if strings.EqualFold(title, "Chapter "+number) || strings.EqualFold(title, name) || title == "" {
		return name
	}
	return name + ": " + title
}

func (s *Server) readArticle(r *http.Request, proj projects.Project, units []readUnit, idx int, items []docs.Meta) (readArticle, error) {
	u := units[idx]
	// A [[wiki link]] in the text goes to the page the target is on, in this project's manuscript.
	pageOf := map[string]string{}
	for _, un := range units {
		for _, m := range un.Members {
			if k := foldTitle(m.Title); k != "" && pageOf[k] == "" {
				ref := un.ID
				if m.ID != un.ID {
					ref += "#d-" + m.ID
				}
				pageOf[k] = ref
			}
		}
	}
	wiki := func(t string) (string, bool) {
		h, ok := pageOf[foldTitle(t)]
		return h, ok
	}
	label := ""
	if u.Number != "" {
		label = i18n.Default.T(proj.Language, "rd.chapter", "n", u.Number)
	} else {
		label = docKindLabel(u.Kind)
	}
	a := readArticle{ID: "u-" + u.ID, Doc: u.ID, Title: u.Title, Label: label, Kicker: u.Kicker}
	if idx+1 < len(units) {
		a.Next = units[idx+1].ID
	}
	for i, m := range u.Members {
		out, err := s.Reg.Call(r.Context(), s.principal(r), "doc.get", docs.DocIn{ID: m.ID})
		if err != nil {
			return a, err
		}
		d := out.(docs.Item).Item
		shift := 2 // the chapter's title is an h2, so a heading inside its text starts at h3
		if i > 0 {
			shift = 3
		}
		// Ghosted text is what the writer cut: the page shows the book as it stands, as a reader of a link would see it.
		html := text.RenderHTML(text.StripGhosts(comanchor.Body(d.Content), true), text.HTMLOptions{IDPrefix: "d" + shortID(m.ID) + "-", HeadingShift: shift, WikiHref: wiki, LocalFiles: true})
		sec := readSection{ID: "d-" + m.ID, Doc: m.ID, Rev: d.Revision, HTML: template.HTML(html), Scene: i > 0}
		if i > 0 && strings.TrimSpace(m.Title) != "" {
			sec.Title = m.Title
		}
		a.Body = append(a.Body, sec)
	}
	return a, nil
}

func memberIDs(ms []docs.Meta) string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return strings.Join(ids, " ")
}

func shortID(id string) string {
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

func (s *Server) readLanding(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.boardProject(w, r)
	if !ok {
		return
	}
	d, _, err := s.readData(r, pid, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Empty {
		pg := s.page(r, "Reading "+d.Project.Title, nil)
		d.Prefs = s.withTypography(r.Context(), s.principal(r).UserID, pid, pg.Prefs, editorPrefsFrom(pg.Prefs))
		pg.Data = d
		s.render(w, r, http.StatusOK, "read", pg)
		return
	}
	http.Redirect(w, r, "/p/"+d.Slug+"/read/"+d.Unit.ID, http.StatusSeeOther)
}

func (s *Server) readPage(w http.ResponseWriter, r *http.Request) {
	pid, ok := s.boardProject(w, r)
	if !ok {
		return
	}
	start := time.Now()
	d, idx, err := s.readData(r, pid, r.PathValue("docid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Empty {
		s.fail(w, r, ops.NotFound("document"))
		return
	}
	if d.Unit == nil { // a scene inside a chapter: the chapter's page, at the scene
		out, _ := s.Reg.Call(r.Context(), s.principal(r), "doc.list", docs.ListIn{ProjectID: pid})
		units, _ := readUnits(out.(docs.ListOut).Items, out.(docs.ListOut).Order)
		http.Redirect(w, r, "/p/"+d.Slug+"/read/"+units[idx].ID+"#d-"+r.PathValue("docid"), http.StatusSeeOther)
		return
	}
	s.Log.Debug("reading page", "page", idx+1, "build", time.Since(start))
	pg := s.page(r, d.Unit.Title+" · "+d.Project.Title, nil)
	d.Prefs = s.withTypography(r.Context(), s.principal(r).UserID, pid, pg.Prefs, editorPrefsFrom(pg.Prefs))
	pg.Data = d
	s.render(w, r, http.StatusOK, "read", pg)
}

// uiRead is one page's article as a fragment, for the script's continuous scrolling.
func (s *Server) uiRead(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	d, _, err := s.readData(r, r.PathValue("id"), r.PathValue("docid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Unit == nil {
		s.fail(w, r, ops.NotFound("document"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "read", "read-article", d.Article)
}
