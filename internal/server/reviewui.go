package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// The writer's review page, /p/{slug}/review: every open comment in reading order with the counts per chapter,
// the suggestions to decide, "resolve all" with its preview and the count it must still match, and "accept all"
// with its plan. All of it is read and written through the review ops (review.queue, review.resolve_all,
// suggestion.list/accept/reject/accept_all, comment.resolve/reopen); a form here is an op with a button. The page
// is for editors and the owner. A reviewer's review screen is the reading view, so they are sent there; a reader
// is told plainly that review is not theirs.

func (s *Server) registerReviewUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/review", s.reviewPage)
	s.uiRoute("POST /ui/projects/{id}/review/{action}", s.uiReview)
}

type reviewThread struct {
	ID, Author, Kind, Body, Quote, When, At string
	Orphan, Resolved                        bool
	Replies                                 int
	ReadURL                                 string
}

type reviewGroup struct {
	DocID, Title, Crumb string
	Open, Orphaned      int
	Threads             []reviewThread
}

type reviewChange struct {
	Old, New string
	Insert   bool
}

type reviewSug struct {
	ID, Author, Note, When, At string
	Changes                    []reviewChange
	Stale                      bool
}

type reviewSugGroup struct {
	DocID, Title string
	Sugs         []reviewSug
}

type reviewSkip struct{ Reason, Message string }

// reviewPlan is what a preview said, shown with the form that confirms it.
type reviewPlan struct {
	Kind         string // "resolve" or "accept"
	Count, Total int
	Skipped      []reviewSkip
	DocID, Title string
	IDs          []string
}

type reviewData struct {
	Project              projects.Project
	Slug, ProjectID      string
	CSRF                 string
	Filter               reviewFilter
	Chapters             []review.DocCount
	Total, Orphaned      int
	ProjectLevel         int
	Groups               []reviewGroup
	SugGroups            []reviewSugGroup
	SugTotal             int
	Plan                 *reviewPlan
	Flash, Error         string
	More                 bool
	AnyFilter            bool
	ResolvedWith         string
	DocTitles            map[string]string
	CommentsOff          bool
	ReadURL, ReviewerURL string
}

type reviewFilter struct{ Doc, Author, Kind, State string }

func (f reviewFilter) any() bool {
	return f.Doc != "" || f.Author != "" || f.Kind != "" || f.State != ""
}

func filterFrom(r *http.Request) reviewFilter {
	pick := func(k string, ok ...string) string {
		v := r.FormValue(k)
		for _, o := range ok {
			if v == o {
				return v
			}
		}
		return ""
	}
	f := reviewFilter{Doc: strings.TrimSpace(r.FormValue("doc")), Author: pick("author", "me"), Kind: pick("kind", "comment", "question", "suggestion-note"), State: pick("state", "active", "orphaned")}
	if len(f.Doc) > 64 {
		f.Doc = ""
	}
	return f
}

func plainSource(s string) string {
	return strings.NewReplacer("*", "", "_", "", "~", "", "`", "").Replace(s)
}

func clip(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// reviewProject is the page's project and the caller's standing: editor or owner, else a redirect or a refusal.
func (s *Server) reviewProject(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return proj, false
	}
	switch proj.Role {
	case ops.ProjectOwner, ops.ProjectEditor:
		return proj, true
	case ops.ProjectReviewer:
		http.Redirect(w, r, "/p/"+proj.Slug+"/read", http.StatusSeeOther)
	default:
		s.fail(w, r, ops.Forbidden("Review is for the owner, the editors and the reviewers of a project."))
	}
	return proj, false
}

func (s *Server) reviewView(r *http.Request, proj projects.Project, f reviewFilter) (reviewData, error) {
	ctx, p := r.Context(), s.principal(r)
	lang := s.page(r, "", nil).Lang
	d := reviewData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: authFrom(ctx).CSRF, Filter: f, AnyFilter: f.any(), DocTitles: map[string]string{}}
	qo, err := s.Reg.Call(ctx, p, "review.queue", review.QueueIn{ProjectID: proj.ID, DocumentID: f.Doc, Author: f.Author, Kind: f.Kind, State: f.State, Limit: 200})
	if err != nil {
		return d, err
	}
	q := qo.(review.QueueOut)
	d.Total, d.Orphaned, d.ProjectLevel, d.Chapters = q.Total, q.Orphaned, q.ProjectLevel, q.Counts
	d.More = len(q.Items) < q.Total
	idx := map[string]int{}
	for _, c := range q.Counts {
		d.DocTitles[c.DocumentID] = c.Title
	}
	for _, it := range q.Items {
		id, title, crumb := "", i18n.Default.T(lang, "rq.book_whole"), ""
		if it.Document != nil {
			id, title, crumb = it.Document.ID, it.Document.Title, strings.Join(it.Document.Breadcrumb, " / ")
			if title == "" {
				title = i18n.Default.T(lang, "rq.untitled")
			}
		}
		i, ok := idx[id]
		if !ok {
			d.Groups = append(d.Groups, reviewGroup{DocID: id, Title: title, Crumb: crumb})
			i = len(d.Groups) - 1
			idx[id] = i
		}
		t := it.Thread
		rt := reviewThread{ID: t.ThreadID, Author: authorShown(lang, t.Author), Kind: t.Kind, Body: clip(t.Body, 280), When: utcWhen(t.CreatedAt), At: t.CreatedAt, Replies: len(t.Replies)}
		if t.Anchor != nil {
			rt.Orphan = t.Anchor.State != "active"
			rt.Quote = clip(plainSource(map[bool]string{true: t.Anchor.Original, false: t.Anchor.Quote}[rt.Orphan && t.Anchor.Original != ""]), 140)
		}
		if id != "" {
			rt.ReadURL = "/p/" + proj.Slug + "/read/" + id + "#t=" + t.ThreadID
		}
		d.Groups[i].Threads = append(d.Groups[i].Threads, rt)
		d.Groups[i].Open++
		if rt.Orphan {
			d.Groups[i].Orphaned++
		}
	}
	so, err := s.Reg.Call(ctx, p, "suggestion.list", review.SuggestionListIn{ProjectID: proj.ID, Status: "open", Limit: 200})
	if err != nil {
		return d, err
	}
	sl := so.(review.SuggestionListOut)
	d.SugTotal = sl.Total
	// Fetch document list once up front for both missing titles (avoiding N doc.get calls) and book-order sorting.
	rank := map[string]int{}
	if len(sl.Items) > 0 {
		if dl, err := s.Reg.Call(ctx, p, "doc.list", docs.ListIn{ProjectID: proj.ID}); err == nil {
			lo := dl.(docs.ListOut)
			for i, id := range lo.Order {
				rank[id] = i
			}
			for _, m := range lo.Items {
				if d.DocTitles[m.ID] == "" {
					d.DocTitles[m.ID] = m.Title
				}
			}
		}
	}
	sidx := map[string]int{}
	for _, sg := range sl.Items {
		i, ok := sidx[sg.DocumentID]
		if !ok {
			title := d.DocTitles[sg.DocumentID]
			if title == "" {
				if out, err := s.Reg.Call(ctx, p, "doc.get", docs.DocIn{ID: sg.DocumentID}); err == nil {
					title = out.(docs.Item).Item.Title
				}
			}
			if title == "" {
				title = i18n.Default.T(lang, "rq.untitled")
			}
			d.SugGroups = append(d.SugGroups, reviewSugGroup{DocID: sg.DocumentID, Title: title})
			i = len(d.SugGroups) - 1
			sidx[sg.DocumentID] = i
		}
		rs := reviewSug{ID: sg.ID, Author: authorShown(lang, sg.Author), Note: sg.Note, When: utcWhen(sg.CreatedAt), At: sg.CreatedAt, Stale: sg.Stale}
		for _, c := range sg.Changes {
			rs.Changes = append(rs.Changes, reviewChange{Old: clip(plainSource(c.Original), 200), New: clip(c.Text, 200), Insert: c.Kind == "insert"})
		}
		d.SugGroups[i].Sugs = append(d.SugGroups[i].Sugs, rs)
	}
	// suggestions come newest first; the page lists them as the book runs, and within a chapter as the chapter does
	if len(d.SugGroups) > 1 || len(sl.Items) > 1 {
		sort.SliceStable(d.SugGroups, func(a, b int) bool { return rank[d.SugGroups[a].DocID] < rank[d.SugGroups[b].DocID] })
		start := map[string]int{}
		for _, sg := range sl.Items {
			if len(sg.Changes) > 0 {
				start[sg.ID] = sg.Changes[0].Start
			}
		}
		for i := range d.SugGroups {
			g := d.SugGroups[i].Sugs
			sort.SliceStable(g, func(a, b int) bool { return start[g[a].ID] < start[g[b].ID] })
		}
	}
	d.ReadURL = "/p/" + proj.Slug + "/read"
	return d, nil
}

func (s *Server) reviewPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.reviewProject(w, r)
	if !ok {
		return
	}
	d, err := s.reviewView(r, proj, filterFrom(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error = fl["msg"], fl["err"]
	}
	pg := s.page(r, "Review of "+proj.Title, d)
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "review", pg)
}

func (s *Server) reviewBack(w http.ResponseWriter, r *http.Request, slug, msg, errMsg string) {
	s.flashSet(r, map[string]string{"msg": msg, "err": errMsg})
	http.Redirect(w, r, "/p/"+slug+"/review", http.StatusSeeOther)
}

func (s *Server) uiReview(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	f := filterFrom(r)
	step := r.PostFormValue("step")
	lang := s.page(r, "", nil).Lang
	tr := func(key string, args ...any) string { return i18n.Default.T(lang, key, args...) }
	switch r.PathValue("action") {
	case "resolve", "reopen":
		op := "comment." + r.PathValue("action")
		if _, err := s.Reg.Call(ctx, p, op, review.GetIn{ProjectID: proj.ID, CommentID: r.PostFormValue("thread_id")}); err != nil {
			s.reviewFail(w, r, proj, err)
			return
		}
		s.reviewBack(w, r, proj.Slug, tr("rq.f_"+map[string]string{"resolve": "resolved", "reopen": "reopened"}[r.PathValue("action")]), "")
	case "resolve_all":
		in := review.ResolveAllIn{ProjectID: proj.ID, DocumentID: f.Doc, Author: f.Author, Kind: f.Kind, State: f.State, Preview: step != "apply"}
		if step == "apply" {
			n, err := strconv.Atoi(r.PostFormValue("expect_count"))
			if err != nil {
				s.fail(w, r, ops.Invalid("expect_count", "Look at the preview first."))
				return
			}
			in.ExpectCount = &n
		}
		out, err := s.Reg.Call(ctx, p, "review.resolve_all", in)
		if err != nil {
			if e := ops.AsError(err); e.Code == ops.CodeConflict {
				s.reviewBack(w, r, proj.Slug, "", tr("rq.f_changed"))
				return
			}
			s.reviewFail(w, r, proj, err)
			return
		}
		res := out.(review.ResolveAllOut)
		if step != "apply" {
			d, err := s.reviewView(r, proj, f)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			d.Plan = &reviewPlan{Kind: "resolve", Count: res.Count, Total: d.Total}
			s.render(w, r, http.StatusOK, "review", s.page(r, "Review of "+proj.Title, d))
			return
		}
		s.reviewBack(w, r, proj.Slug, i18n.Default.N(lang, "rq.f_res", res.Resolved), "")
	case "accept", "reject":
		op := "suggestion." + r.PathValue("action")
		var in any = review.SuggestionRef{ProjectID: proj.ID, SuggestionID: r.PostFormValue("suggestion_id")}
		if r.PathValue("action") == "accept" {
			in = review.SuggestionAcceptIn{ProjectID: proj.ID, SuggestionID: r.PostFormValue("suggestion_id")}
		}
		if _, err := s.Reg.Call(ctx, p, op, in); err != nil {
			if e := ops.AsError(err); e.Code == ops.CodeConflict {
				s.reviewBack(w, r, proj.Slug, "", e.Message+" "+tr("rq.f_reject_hint"))
				return
			}
			s.reviewFail(w, r, proj, err)
			return
		}
		s.reviewBack(w, r, proj.Slug, tr("rq.f_"+map[string]string{"accept": "accepted", "reject": "rejected"}[r.PathValue("action")]), "")
	case "accept_all":
		ids := r.PostForm["suggestion_id"]
		in := review.SuggestionAcceptAllIn{ProjectID: proj.ID, DocumentID: r.PostFormValue("document_id"), SuggestionIDs: ids, Preview: step != "apply"}
		out, err := s.Reg.Call(ctx, p, "suggestion.accept_all", in)
		if err != nil {
			if e := ops.AsError(err); e.Code == ops.CodeConflict {
				s.reviewBack(w, r, proj.Slug, "", e.Message)
				return
			}
			s.reviewFail(w, r, proj, err)
			return
		}
		res := out.(review.SuggestionAcceptAllOut)
		if step != "apply" {
			d, err := s.reviewView(r, proj, f)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			pl := &reviewPlan{Kind: "accept", Count: len(res.Applied), Total: len(ids), DocID: in.DocumentID, Title: d.DocTitles[in.DocumentID], IDs: ids}
			for _, k := range res.Skipped {
				pl.Skipped = append(pl.Skipped, reviewSkip{Reason: tr("rq.why_" + k.Reason), Message: k.Message})
			}
			s.render(w, r, http.StatusOK, "review", s.page(r, "Review of "+proj.Title, withPlan(d, pl)))
			return
		}
		msg := i18n.Default.N(lang, "rq.f_acc", len(res.Applied))
		if n := len(res.Skipped); n > 0 {
			msg += " " + i18n.Default.N(lang, "rq.f_left", n)
		}
		s.reviewBack(w, r, proj.Slug, msg, "")
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

func withPlan(d reviewData, p *reviewPlan) reviewData { d.Plan = p; return d }

// reviewFail shows an op's refusal on the review page when it is one a person can act on, else as an error page.
func (s *Server) reviewFail(w http.ResponseWriter, r *http.Request, proj projects.Project, err error) {
	e := ops.AsError(err)
	switch e.Code {
	case ops.CodeInvalid, ops.CodeForbidden, ops.CodeNotFound:
		if e.Code == ops.CodeNotFound && r.PathValue("action") == "" {
			break
		}
		s.reviewBack(w, r, proj.Slug, "", e.Message)
		return
	}
	s.fail(w, r, err)
}

// authorShown is the name a review list shows for an author. A Word author (a
// name from an imported file, kind label) is never shown as a bare name: it
// could be a member's name, or "Owner", or "Guest: Bob".
func authorShown(lang string, a review.AuthorOut) string {
	if a.Kind == "label" {
		return i18n.Default.T(lang, "rq.word_name", "name", a.Name)
	}
	return a.Name
}
