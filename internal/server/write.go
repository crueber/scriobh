package server

import (
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The writing view: a project's manuscript tree beside one document. The page
// is server-rendered, readable without JavaScript (the body is present as plain
// text), and enhanced by the editor and the save loop in web/static/js/doc.js.

// ---- view models -------------------------------------------------------------

// treeRow is one document in the manuscript tree, flattened in reading order.
type treeRow struct {
	ID, ParentID, Kind, Title string
	Level                     int
	Guides                    []int // one hairline per ancestor level
	Number                    string
	Words                     int // the document's own words
	Target                    int // the document's own word target, 0 when it has none
	Count                     int // what the row shows: own words, or the sum for a folder
	HasChildren               bool
	Current                   bool
	PosInSet, SetSize         int
}

// treeView is the whole tree fragment.
type treeView struct {
	ProjectID, Slug, CurrentID string
	Rows                       []treeRow
	Total                      int
	CanEdit                    bool
	CSRF                       string
	Lang                       string // the language of the row's words (English when empty)
}

// TargetText says a document's word target for a screen reader; the row shows it as "/2,000" after the count.
func (v treeView) TargetText(n int) string {
	lang := v.Lang
	if lang == "" {
		lang = "en"
	}
	return stT{lang}.T("studio.ui.tree.target", "n", n)
}

// jumpItem is one entry of the chapter jump list.
type jumpItem struct {
	ID, Title, Group, Number string
	Current                  bool
}

// editorPrefs are the editor preferences, rendered as data attributes so the
// stylesheet can apply them without inline styles. Stage 2 feeds them from the
// person's saved preferences; these are the defaults.
type editorPrefs struct {
	Size    int    // font size in px: 16 to 24
	Measure string // "66" (ch) or "full"
	LH      int    // line height x 100: 140 to 190
	Font    string // serif, sans, mono

	AutosaveMS int  // idle time before a save, in ms
	ShowCounts bool // the status bar shows the counts
	Typewriter bool
	FocusDim   bool

	// TypoAttrs is the project's typography with the person's override over it, as data attributes for the paper element
	// (F-085). Empty when neither sets anything, which leaves the markup byte for byte what it was. It is built only from
	// the fixed lists in internal/domain/typography.
	TypoAttrs template.HTMLAttr
}

func defaultPrefs() editorPrefs {
	return editorPrefs{Size: 19, Measure: "66", LH: 165, Font: "serif", AutosaveMS: 700, ShowCounts: true}
}

// writeData is the data of the write template.
type writeData struct {
	Project   projects.Project
	Slug      string
	CanEdit   bool
	Doc       *docs.Doc
	Tree      treeView
	Jump      []jumpItem
	Crumbs    []docs.Crumb
	Parent    *docs.Crumb // the nearest ancestor, for the line above the title
	Number    string
	Position  int // 1-based position of the document in reading order
	Count     int // documents in the project
	Prev      *jumpItem
	Next      *jumpItem
	Prefs     editorPrefs
	ReadOnly  string    // why the page opens read-only: "role" or "length"; empty when editable
	Undo      *undoView // set on an empty project right after its last document went to the trash
	ProjectID string
	AI        aiState // whether the writing partner is available here; nothing of it is drawn unless it is on
	Similar   bool    // the project's semantic index is running: the inspector lists scenes like this one
	Find      string  // a passage a search by meaning found: the page scrolls to it and selects it
	Inspector string  // "details" or "versions" when the person left the inspector open (a cookie); drawn open on a wide screen
	// Sprint is the status bar's sprint line, for a person who may write here: what is running on this project, if
	// anything, as the server saw it when the page was made (js/sprint.js takes over from its times).
	Sprint *writeSprint
}

func displayTitle(t string) string {
	if strings.TrimSpace(t) == "" {
		return "Untitled"
	}
	return t
}

// undoView is what an empty project offers after its last document was trashed: the
// way back, rendered by the server because the page has no script to do it.
type undoView struct{ ID, Title string }

var docIDPattern = regexp.MustCompile(`^doc_[0-9A-Za-z]+$`)

// chapterCounter numbers chapters in reading order. Only kind chapter is numbered: parts,
// scenes, notes, front matter and back matter are not, and the count runs on across parts.
// The tree and the outline both use it, so they can never disagree.
type chapterCounter struct{ n int }

func (c *chapterCounter) label(kind string) string {
	if kind != "chapter" {
		return ""
	}
	c.n++
	return strconv.Itoa(c.n)
}

// buildTree flattens the live documents in reading order with everything the
// fragment shows: level, hairline guides, chapter numbers, set sizes, and the
// sums a folder displays.
func buildTree(items []docs.Meta, order []string, p projects.Project, current string, canEdit bool, csrf string) treeView {
	byID := make(map[string]docs.Meta, len(items))
	for _, m := range items {
		byID[m.ID] = m
	}
	parentOf := func(m docs.Meta) string {
		if _, ok := byID[m.ParentID]; ok {
			return m.ParentID
		}
		return ""
	}
	total, project := map[string]int{}, 0
	kids, size := map[string]bool{}, map[string]int{}
	for _, m := range items {
		project += m.WordCount
		par := parentOf(m)
		size[par]++
		if par != "" {
			kids[par] = true
		}
		total[m.ID] += m.WordCount
		for a, hops := par, 0; a != "" && hops < 64; hops++ {
			total[a] += m.WordCount
			a = parentOf(byID[a])
		}
	}
	tv := treeView{ProjectID: p.ID, Slug: p.Slug, CurrentID: current, Total: project, CanEdit: canEdit, CSRF: csrf}
	pos, num := map[string]int{}, chapterCounter{}
	for _, id := range order {
		m, ok := byID[id]
		if !ok {
			continue
		}
		par := parentOf(m)
		pos[par]++
		level := 1
		for a, hops := par, 0; a != "" && hops < 64; hops++ {
			level++
			a = parentOf(byID[a])
		}
		row := treeRow{ID: m.ID, ParentID: par, Kind: m.Kind, Title: displayTitle(m.Title), Level: level, Words: m.WordCount, Target: m.TargetWords,
			Count: m.WordCount, HasChildren: kids[m.ID], Current: m.ID == current, PosInSet: pos[par], SetSize: size[par]}
		if row.HasChildren {
			row.Count = total[m.ID]
		}
		for i := 1; i < level; i++ {
			row.Guides = append(row.Guides, i)
		}
		row.Number = num.label(m.Kind)
		tv.Rows = append(tv.Rows, row)
	}
	return tv
}

func buildJump(tv treeView, byID map[string]docs.Meta) []jumpItem {
	out := make([]jumpItem, 0, len(tv.Rows))
	for _, r := range tv.Rows {
		var group []string
		for a, hops := byID[r.ID].ParentID, 0; a != "" && hops < 64; hops++ {
			m, ok := byID[a]
			if !ok {
				break
			}
			group = append([]string{displayTitle(m.Title)}, group...)
			a = m.ParentID
		}
		out = append(out, jumpItem{ID: r.ID, Title: r.Title, Group: strings.Join(group, " › "), Number: r.Number, Current: r.Current})
	}
	return out
}

// ---- the page ----------------------------------------------------------------

func (s *Server) writeProject(w http.ResponseWriter, r *http.Request) {
	s.writePage(w, r, "")
}

func (s *Server) writeDoc(w http.ResponseWriter, r *http.Request) {
	s.writePage(w, r, r.PathValue("docid"))
}

func (s *Server) writePage(w http.ResponseWriter, r *http.Request, docID string) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, r.PathValue("slug"))
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: pid})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	proj := out.(projects.Item).Item.Addressed()
	if proj.Trashed {
		// In the trash a project is out of reach until it is restored from the library.
		s.fail(w, r, ops.NotFound("project"))
		return
	}
	lo, err := s.Reg.Call(r.Context(), p, "doc.list", docs.ListIn{ProjectID: pid})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list := lo.(docs.ListOut)
	if docID == "" && len(list.Order) > 0 {
		docID = list.Order[0]
		// Resume where this person left off, when that document is still in the tree.
		for _, id := range list.Order {
			if id == proj.LastDocumentID {
				docID = id
			}
		}
	}
	insp := ""
	if c, err := r.Cookie("scriobh_insp"); err == nil && (c.Value == "details" || c.Value == "versions" || c.Value == "scene" || c.Value == "alts" || c.Value == "stash" || c.Value == "lab" || c.Value == "ai") {
		insp = c.Value
	}
	pv := account.Prefs(r.Context(), s.DB, p.UserID)
	d := writeData{Inspector: insp, Project: proj, Slug: proj.Slug, ProjectID: pid, Prefs: s.withTypography(r.Context(), p.UserID, pid, pv, editorPrefsFrom(pv)), Count: len(list.Order),
		CanEdit: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor}
	d.AI = s.aiStateOf(r.Context(), p, pid)
	if docID != "" {
		// "Scenes like this one" is offered where the project's index is running; it asks nothing of any endpoint.
		d.Similar = s.lookup(r.Context(), `SELECT 'y' FROM embed_projects WHERE project_id = ? AND enabled = 1 AND paused_reason = ''`, pid) == "y"
		// A passage to scroll to, from a search by meaning (js/jump.js).
		if f := strings.Join(strings.Fields(r.URL.Query().Get("find")), " "); f != "" && utf8.RuneCountInString(f) <= 80 {
			d.Find = f
		}
	}
	title := proj.Title
	if docID == "" {
		if t := r.URL.Query().Get("trashed"); docIDPattern.MatchString(t) {
			if do, err := s.Reg.Call(r.Context(), p, "doc.get", docs.DocIn{ID: t}); err == nil {
				if doc := do.(docs.Item).Item; doc.ProjectID == pid && doc.Trashed {
					d.Undo = &undoView{ID: doc.ID, Title: displayTitle(doc.Title)}
				}
			}
		}
	}
	if docID != "" {
		do, err := s.Reg.Call(r.Context(), p, "doc.get", docs.DocIn{ID: docID})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		doc := do.(docs.Item).Item
		// A document from another project, or one in the trash, is exactly as
		// missing as one that never existed.
		if doc.ProjectID != pid || doc.Trashed {
			s.fail(w, r, ops.NotFound("document"))
			return
		}
		d.Doc = &doc
		d.Crumbs = doc.Ancestors
		if n := len(doc.Ancestors); n > 0 {
			d.Parent = &doc.Ancestors[n-1]
		}
		title = displayTitle(doc.Title)
		switch {
		case !d.CanEdit:
			d.ReadOnly = "role"
		case doc.Guard == "readonly":
			d.ReadOnly = "length"
		}
		if d.CanEdit {
			d.Sprint = s.writeSprintOf(r, pid)
		}
	}
	cur := ""
	if d.Doc != nil {
		cur = d.Doc.ID
	}
	d.Tree = buildTree(list.Items, list.Order, proj, cur, d.CanEdit, authFrom(r.Context()).CSRF)
	d.Tree.Lang = s.page(r, "", nil).Lang
	byID := make(map[string]docs.Meta, len(list.Items))
	for _, m := range list.Items {
		byID[m.ID] = m
	}
	d.Jump = buildJump(d.Tree, byID)
	for i, it := range d.Jump {
		if it.ID != cur {
			continue
		}
		d.Position = i + 1
		d.Number = it.Number
		if i > 0 {
			d.Prev = &d.Jump[i-1]
		}
		if i+1 < len(d.Jump) {
			d.Next = &d.Jump[i+1]
		}
	}
	// The page carries the document's text and revision. A Back button must fetch it again, never show a copy
	// from before the last save, or the editor would open on old text.
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "write", s.page(r, title, d))
}

// ---- tree fragments and the routes that change the tree ---------------------

// fetchHeader marks a request made by the writing view's own script. Errors on
// such a request are JSON with the real status, never a redirect to sign-in,
// so a signed-out session cannot navigate away from unsaved text.
const fetchHeader = "X-Scriobh-Fetch"

func isFetch(r *http.Request) bool { return r.Header.Get(fetchHeader) != "" }

// treeFragment renders the tree for a project as an HTML fragment.
func (s *Server) treeFragment(w http.ResponseWriter, r *http.Request, status int, projectID, current string, header map[string]string) {
	p := s.principal(r)
	out, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: projectID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	proj := out.(projects.Item).Item.Addressed()
	lo, err := s.Reg.Call(r.Context(), p, "doc.list", docs.ListIn{ProjectID: projectID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list := lo.(docs.ListOut)
	canEdit := proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor
	tv := buildTree(list.Items, list.Order, proj, current, canEdit, authFrom(r.Context()).CSRF)
	tv.Lang = s.page(r, "", nil).Lang
	for k, v := range header {
		w.Header().Set(k, v)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, status, "tree", "tree", tv)
}

func (s *Server) uiTree(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	s.treeFragment(w, r, http.StatusOK, r.PathValue("id"), r.URL.Query().Get("current"), nil)
}

// uiCSRF gives the writing view a fresh token after the person has signed in
// again in another tab (the page's own copy belongs to the old session).
func (s *Server) uiCSRF(w http.ResponseWriter, r *http.Request) {
	a := authFrom(r.Context())
	if !a.P.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(`{"token":"` + a.CSRF + `"}`))
}

// formAfter reads the optional "after" sibling: absent means last, empty means first.
func formAfter(r *http.Request) *string {
	if _, ok := r.PostForm["after"]; !ok {
		return nil
	}
	v := r.PostForm.Get("after")
	return &v
}

func (s *Server) uiDocCreate(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	pid := r.PathValue("id")
	in := docs.CreateIn{
		ProjectID: pid, Title: r.PostFormValue("title"), Kind: r.PostFormValue("kind"),
		ParentID: r.PostFormValue("parent_id"), After: formAfter(r),
	}
	// Front matter opens the book and back matter closes it, whatever was selected when it was added.
	switch in.Kind {
	case "front_matter":
		first := ""
		in.ParentID, in.After = "", &first
	case "back_matter":
		in.ParentID, in.After = "", nil
	}
	out, err := s.Reg.Call(r.Context(), p, "doc.create", in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	doc := out.(docs.Item).Item
	if !isFetch(r) {
		po, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: pid})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		http.Redirect(w, r, "/p/"+po.(projects.Item).Item.Addressed().Slug+"/d/"+doc.ID, http.StatusSeeOther)
		return
	}
	s.treeFragment(w, r, http.StatusCreated, pid, r.PostFormValue("current"), map[string]string{"X-Doc-Id": doc.ID})
}

// docProject finds the project of a document the caller may edit, through doc.get
// (which authorizes). Used by the routes that name only a document.
func (s *Server) docProject(r *http.Request, id string) (docs.Doc, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "doc.get", docs.DocIn{ID: id})
	if err != nil {
		return docs.Doc{}, err
	}
	return out.(docs.Item).Item, nil
}

func (s *Server) uiDocMove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out, err := s.Reg.Call(r.Context(), s.principal(r), "doc.move", docs.MoveIn{ID: id, ParentID: r.PostFormValue("parent_id"), After: formAfter(r)})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.treeFragment(w, r, http.StatusOK, out.(docs.Item).Item.ProjectID, r.PostFormValue("current"), map[string]string{"X-Doc-Id": id})
}

func (s *Server) uiDocTrash(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out, err := s.Reg.Call(r.Context(), s.principal(r), "doc.trash", docs.DocIn{ID: id})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.treeFragment(w, r, http.StatusOK, out.(docs.Item).Item.ProjectID, r.PostFormValue("current"), map[string]string{"X-Doc-Id": id})
}

func (s *Server) uiDocRestore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	out, err := s.Reg.Call(r.Context(), s.principal(r), "doc.restore", docs.DocIn{ID: id})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !isFetch(r) { // the Undo form on an empty project's page
		po, err := s.Reg.Call(r.Context(), s.principal(r), "project.get", projects.GetIn{ID: out.(docs.Item).Item.ProjectID})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		http.Redirect(w, r, "/p/"+po.(projects.Item).Item.Addressed().Slug+"/d/"+id, http.StatusSeeOther)
		return
	}
	s.treeFragment(w, r, http.StatusOK, out.(docs.Item).Item.ProjectID, r.PostFormValue("current"), map[string]string{"X-Doc-Id": id})
}

// uiDocRename renames a document other than the one open for writing (that one
// is renamed through its own save loop, with its own revision). It reads the
// current revision and saves only the title, so it cannot overwrite text.
func (s *Server) uiDocRename(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	title := r.PostFormValue("title")
	var last error
	for try := 0; try < 2; try++ {
		doc, err := s.docProject(r, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		_, err = s.Reg.Call(r.Context(), s.principal(r), "doc.update", docs.UpdateIn{ID: id, BaseRevision: doc.Revision, Title: &title})
		if err == nil {
			s.treeFragment(w, r, http.StatusOK, doc.ProjectID, r.PostFormValue("current"), map[string]string{"X-Doc-Id": id})
			return
		}
		last = err
		if e := ops.AsError(err); e.Code != ops.CodeConflict {
			break
		}
	}
	s.fail(w, r, last)
}
