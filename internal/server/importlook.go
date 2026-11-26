package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// The writer's own breaks on the review: the paragraphs of a chapter to split it at, what is inside a long chapter, the chapters above or
// below a point as front or back matter, and a pattern. The lists come from the server a page at a time (import.draft.get with a look),
// so no chapter's text is sent to the page whole.

// lookPage is how many paragraphs, or places, a list shows at a time.
const lookPage = 40

// impSplit is the list of a chapter's paragraphs, to split it at one.
type impSplit struct {
	Key, RID, Title string
	Paras           []impPara
	More            int
	MoreHref        string
	MoreAfter       int
	Total           int
	FromKey         string
}

type impPara struct {
	ID, Line, Pos string
	Ordinal       int
	Title         bool
}

// impInside is what is inside a long chapter: how many places of each kind, a button for each, and the places a page at a time.
type impInside struct {
	Key, RID, Title string
	Found           []string
	Bulk            []impBulk
	Sugs            []impLookSug
	More            int
	MoreHref        string
	MoreAfter       int
	Total           int
	Nothing         bool
	FocusH          bool // the heading takes focus: no place was just started that the next place follows
	Joined          int  // documents joined into this one: their text is not in what was looked into
}

type impLookSug struct {
	ID, Line, Why, Pos string
	Ordinal            int
	Focus              bool
}

// lookMode is what a request asks to look at: split, inside, or nothing.
func lookMode(s string) string {
	switch s {
	case imports.LookSplit, imports.LookInside:
		return s
	}
	return ""
}

// splitOf builds the list of paragraphs from a look.
func (v *impView) splitOf(l *imports.LookOut, after int) *impSplit {
	rid := rid(l.Key)
	sp := &impSplit{Key: l.Key, RID: rid, Title: l.Title, Total: l.Total, FromKey: l.Key}
	for _, p := range l.Paragraphs {
		pct := 0
		if l.Words > 0 {
			pct = p.Words * 100 / l.Words
		}
		sp.Paras = append(sp.Paras, impPara{ID: fmt.Sprintf("imp-pr-%s-%d", rid, p.Ordinal), Ordinal: p.Ordinal, Line: p.Line, Title: p.Title,
			Pos: v.T("import.sug_pos", "w", commas(p.Words), "p", pct)})
	}
	if end := after + len(l.Paragraphs); end < l.Total {
		sp.More, sp.MoreAfter = l.Total-end, end
		sp.MoreHref = fmt.Sprintf("%s?edit=%s&look=split&after=%d#imp-split-%s", v.PageURL, url.QueryEscape(l.Key), end, rid)
	}
	return sp
}

// insideOf builds what is inside a long chapter from a look.
func (v *impView) insideOf(l *imports.LookOut, after int) *impInside {
	rid := rid(l.Key)
	in := &impInside{Key: l.Key, RID: rid, Title: l.Title, Total: l.Total, Nothing: len(l.Found) == 0}
	for _, f := range l.Found {
		if f.Rule == "size" {
			in.Found = append(in.Found, v.N("import.in_found_size", f.Count, "w", commas(l.PieceWords)))
			in.Bulk = append(in.Bulk, impBulk{"size", v.T("import.in_all_size", "n", commas(f.Count), "w", commas(l.PieceWords))})
			continue
		}
		in.Found = append(in.Found, v.N("import.in_found_"+f.Rule, f.Count))
		in.Bulk = append(in.Bulk, impBulk{f.Rule, v.T("import.in_all_"+f.Rule, "n", commas(f.Count))})
	}
	for _, s := range l.Suggest {
		pct := 0
		if l.Words > 0 {
			pct = s.Words * 100 / l.Words
		}
		in.Sugs = append(in.Sugs, impLookSug{ID: fmt.Sprintf("imp-ls-%s-%d", rid, s.Ordinal), Ordinal: s.Ordinal, Line: s.Line, Why: v.T("import.in_rule_" + s.Rule),
			Pos: v.T("import.sug_pos", "w", commas(s.Words), "p", pct)})
	}
	if end := after + len(l.Suggest); end < l.Total {
		in.More, in.MoreAfter = l.Total-end, end
		in.MoreHref = fmt.Sprintf("%s?edit=%s&look=inside&after=%d#imp-look-%s", v.PageURL, url.QueryEscape(l.Key), end, rid)
	}
	// after a chapter was started here, focus goes to the next place that could start one; else to the heading
	in.FocusH = true
	if v.Focus == "imp-ls" {
		for i := range in.Sugs {
			if in.Sugs[i].Ordinal > v.SugOrd {
				in.Sugs[i].Focus, in.FocusH = true, false
				break
			}
		}
	}
	return in
}

// attachLook puts what a look found where the page draws it: the open row's panel (a list to split at) or the long chapter's row.
func (v *impView) attachLook(l *imports.LookOut, mode string, after int) {
	if l == nil {
		return
	}
	switch mode {
	case imports.LookSplit:
		v.Split = v.splitOf(l, after)
	case imports.LookInside:
		v.Inside = v.insideOf(l, after)
	}
}

// impLookCtx is what a fragment of a look is drawn from.
type impLookCtx struct {
	V *impView
	S *impSplit
	I *impInside
}

// uiImportLook is the list of paragraphs a chapter can be split at, or what is inside a long chapter, a page at a time. With more it is the
// next page only, to be added to the list on the page.
func (s *Server) uiImportLook(w http.ResponseWriter, r *http.Request) {
	if !s.importWriter(w, r) {
		return
	}
	q := r.URL.Query()
	mode := lookMode(q.Get("look"))
	key := q.Get("edit")
	after, _ := strconv.Atoi(q.Get("after"))
	if mode == "" || key == "" || after < 0 {
		s.fail(w, r, ops.Invalid("look", "Ask for a chapter by its key, to split it or to look inside it."))
		return
	}
	v, _, err := s.importReviewView(r, r.PathValue("id"), reviewOpts{open: key, look: mode, lookKey: key, lookAfter: after})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := impLookCtx{V: v, S: v.Split, I: v.Inside}
	more := q.Get("more") != ""
	switch {
	case mode == imports.LookSplit && v.Split != nil && more:
		s.fragment(w, r, http.StatusOK, "import-review", "imp-split-more", ctx)
	case mode == imports.LookSplit && v.Split != nil:
		s.fragment(w, r, http.StatusOK, "import-review", "imp-split", ctx)
	case mode == imports.LookInside && v.Inside != nil && more:
		s.fragment(w, r, http.StatusOK, "import-review", "imp-inside-more", ctx)
	case mode == imports.LookInside && v.Inside != nil:
		s.fragment(w, r, http.StatusOK, "import-review", "imp-inside", ctx)
	default:
		s.fail(w, r, ops.NotFound("document"))
	}
}

// docxLong says a document is long enough to look inside for breaks: the length the import warns at.
func docxLong(words int) bool { return words >= docs.WarnWords }
