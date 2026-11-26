package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
)

// The imports a project has had, and taking one back (import.source.list, import.undo). The history is a section of the project's import
// page; the result of an import into a project offers the same undo. Nothing here writes but through the ops.

// impAlready is the notice on a review of a file the project has had before.
type impAlready struct {
	Text string
	Href string
	Link string
}

// impSource is one earlier import on the project's import page.
type impSource struct {
	ID, RID, Name, Label, When string
	Docs, State, Action, Help  string
	Summary                    string
	CanUndo                    bool
	Count                      int // the documents an undo would move
	Form                       string
	CSRF                       string
	Kept                       []impKept // what an undo would keep, and why
	More                       string    // "and 3 more", when the list is cut
	KeptHead                   string
}

// impKept is a document an undo keeps, with the reason in words. The title is the writer's own text: it is set as text.
type impKept struct{ Title, Reason string }

// impUndone is what an undo just did, said once on the page it comes back to.
type impUndone struct {
	Trashed, KeptN int
	Text           string
	Kept           []impKept // the documents of that import that stayed, with each one's reason
	More, Head     string
}

// importSources reads the project's imports and says each in words.
func (s *Server) importSources(r *http.Request, pr *projects.Project) []impSource {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "import.source.list", imports.SourceListIn{ProjectID: pr.ID})
	if err != nil {
		return nil
	}
	loc := s.userZone(r)
	v := &impView{Lang: s.page(r, "", nil).Lang}
	csrf := authFrom(r.Context()).CSRF
	var items []impSource
	for _, it := range out.(imports.SourceListOut).Items {
		if it.Recorded == 0 {
			continue // nothing of it is a document: an import of research notes only
		}
		layout := it.Layout
		if it.Kind == "git" {
			layout = ""
		}
		is := impSource{ID: it.ID, RID: rid(it.ID), Name: it.Name, Label: v.layoutLabel(layout), When: whenIn(it.ImportedAt, loc), CanUndo: it.CanUndo, Count: it.WouldTrash, CSRF: csrf,
			Form: "/ui/projects/" + pr.ID + "/imports/" + it.ID + "/undo"}
		if it.Kind == "git" {
			is.Label = v.T("import.name_git")
		}
		if is.Name == "" {
			is.Name = is.Label
		}
		// The noun agrees with the total the import made (one or many), not with how many are left: trN would take the count for both.
		form := "other"
		if it.Recorded == 1 {
			form = "one"
		}
		is.Docs = s.tr(r, "import.src_docs."+form, "n", commas(it.Live), "total", commas(it.Recorded))
		switch {
		case it.Live == 0:
			is.State = s.tr(r, "import.src_none_left")
		case it.CanUndo && it.WouldKeep == 0:
			is.State = s.trN(r, "import.src_would_all", it.WouldTrash, "n", commas(it.WouldTrash))
		case it.CanUndo:
			is.State = s.trN(r, "import.src_would_some", it.WouldTrash, "n", commas(it.WouldTrash), "k", commas(it.WouldKeep))
		default:
			is.State = s.tr(r, "import.src_would_none")
		}
		is.Action = s.trN(r, "import.src_move", it.WouldTrash, "n", commas(it.WouldTrash))
		is.Kept, is.More, is.KeptHead = s.keptList(r, it.KeptDocs, it.WouldKeep)
		is.Summary, is.Help = s.tr(r, "import.src_undo"), s.tr(r, "import.src_undo_help")
		items = append(items, is)
	}
	return items
}

// keptList says in words why each document is kept; a list cut at 20 says how many more.
func (s *Server) keptList(r *http.Request, docs []imports.KeptDoc, total int) (list []impKept, more, head string) {
	for _, d := range docs {
		list = append(list, impKept{Title: d.Title, Reason: s.tr(r, "import.kept_"+d.Why)})
	}
	if total > len(docs) {
		more = s.tr(r, "import.kept_more", "n", commas(total-len(docs)))
	}
	if len(list) > 0 {
		head = s.trN(r, "import.kept_h", total, "n", commas(total))
	}
	return
}

// importUndone reads what an undo did from the address it redirected to: two counts and nothing else.
func (s *Server) importUndone(r *http.Request, sources []impSource) *impUndone {
	q := r.URL.Query()
	if !q.Has("undone") {
		return nil
	}
	n, err1 := strconv.Atoi(q.Get("undone"))
	k, err2 := strconv.Atoi(q.Get("kept"))
	if err1 != nil || err2 != nil || n < 0 || k < 0 || n > 1_000_000 || k > 1_000_000 {
		return nil
	}
	u := &impUndone{Trashed: n, KeptN: k}
	switch {
	case n == 0 && k == 0:
		u.Text = s.tr(r, "import.undone_nothing")
	case n == 0:
		u.Text = s.trN(r, "import.undone_none", k, "k", commas(k))
	case k == 0:
		u.Text = s.trN(r, "import.undone_all", n, "n", commas(n))
	default:
		u.Text = s.trN(r, "import.undone_some", n, "n", commas(n), "k", commas(k))
	}
	if id := q.Get("src"); reSrcID.MatchString(id) { // the import it was: only to find, among the project's own, the documents that stayed
		for _, src := range sources {
			if src.ID == id {
				u.Kept, u.More, u.Head = src.Kept, src.More, src.KeptHead
			}
		}
	}
	return u
}

var reSrcID = regexp.MustCompile(`^src_[0-9A-Za-z]{1,40}$`)

// uiImportUndo is POST /ui/projects/{id}/imports/{source}/undo: it takes one import back and returns to the project's import page, which says
// what was done.
func (s *Server) uiImportUndo(w http.ResponseWriter, r *http.Request) {
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "import.undo", imports.UndoIn{ProjectID: pr.ID, SourceID: r.PathValue("source")})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u := out.(imports.UndoOut)
	q := url.Values{"undone": {strconv.Itoa(u.Trashed)}, "kept": {strconv.Itoa(u.Kept)}, "src": {u.SourceID}}
	http.Redirect(w, r, "/p/"+pr.Slug+"/import?"+q.Encode()+"#imp-undone", http.StatusSeeOther)
}

// alreadyNotice says, on a review, that the project has had this file before.
func (s *Server) alreadyNotice(r *http.Request, pv *imports.PreviewOut, into *projects.Project) *impAlready {
	if into == nil { // a new project: an earlier import of the file into a project the caller can read, named only when they can read it
		e := pv.Elsewhere
		if e == nil {
			return nil
		}
		return &impAlready{Text: s.trN(r, "import.elsewhere", e.Documents, "title", e.ProjectTitle, "when", whenIn(e.ImportedAt, s.userZone(r)), "n", commas(e.Documents)),
			Href: "/p/" + e.Slug + "/import#imp-sources", Link: s.tr(r, "import.already_link")}
	}
	a := pv.Already
	if a == nil {
		return nil
	}
	when := whenIn(a.ImportedAt, s.userZone(r))
	text := s.trN(r, "import.already", a.Documents, "when", when, "n", commas(a.Documents))
	if a.Times > 1 {
		text += " " + s.tr(r, "import.already_times", "n", strconv.Itoa(a.Times))
	}
	return &impAlready{Text: text, Href: "/p/" + into.Slug + "/import#imp-sources", Link: s.tr(r, "import.already_link")}
}

// undoResult is the offer to take an import back, on the page of its result: only for an import added to a project, and only while
// something of it can be taken back.
func (s *Server) undoResult(r *http.Request, v *impView) {
	d := v.Done
	if d == nil || d.SourceID == "" || d.ProjectID == "" {
		return
	}
	pr := v.Into // an import into a project the caller already had; a new project is the one the import made, read as the caller may
	if pr == nil {
		pr = s.projectOfDraft(r, d.ProjectID)
	}
	if pr == nil {
		return
	}
	for _, src := range s.importSources(r, pr) {
		if src.ID != d.SourceID {
			continue
		}
		if src.CanUndo {
			v.UndoSrc = &src
		}
		return
	}
}
