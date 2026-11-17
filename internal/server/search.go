package server

import (
	"html"
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/ops"
)

// Search pages: one project, or every project the person can open. The page is
// a plain GET form and works without script. The snippet's highlighting comes
// from the op as escaped HTML with <mark>; it is taken apart here and handed to
// the template as text and flags, so nothing from a document ever reaches the
// page as markup.

const (
	searchPage    = 20 // hits per page within one project
	searchPerProj = 5  // hits per project in the library-wide view
	searchProjCap = 40 // projects searched in the library-wide view
)

type snipPart struct {
	Text string
	Mark bool
}

type hitView struct {
	ID, Title, Kind, MatchedIn, URL string
	Words                           int
	Parts                           []snipPart
}

type searchGroup struct {
	Project projects.Project
	Hits    []hitView
	Total   int
	More    string // link to the rest of this project's results
}

type searchData struct {
	Q        string
	Project  *projects.Project // set for a single-project search
	Groups   []searchGroup
	Total    int
	Searched bool // a query was run
	Prev     string
	Next     string
	From, To int
	Degraded bool
	Message  string // what is wrong with the query, when something is
	CSRF     string
	Sem      semSearch // the "By meaning" mode, offered to owners and editors of a project whose index is on
}

// snippetParts splits the op's snippet_html (escaped text with <mark>…</mark>)
// into plain text runs and whether each is a match.
func snippetParts(h string) []snipPart {
	var out []snipPart
	for len(h) > 0 {
		i := strings.Index(h, "<mark>")
		if i < 0 {
			out = append(out, snipPart{Text: html.UnescapeString(h)})
			break
		}
		if i > 0 {
			out = append(out, snipPart{Text: html.UnescapeString(h[:i])})
		}
		h = h[i+len("<mark>"):]
		j := strings.Index(h, "</mark>")
		if j < 0 {
			out = append(out, snipPart{Text: html.UnescapeString(h), Mark: true})
			break
		}
		out = append(out, snipPart{Text: html.UnescapeString(h[:j]), Mark: true})
		h = h[j+len("</mark>"):]
	}
	return out
}

func (s *Server) hitViews(slug string, hits []search.Hit) []hitView {
	out := make([]hitView, 0, len(hits))
	for _, h := range hits {
		out = append(out, hitView{ID: h.DocumentID, Title: displayTitle(h.Title), Kind: docKindLabel(h.Kind), MatchedIn: h.MatchedIn,
			URL: "/p/" + slug + "/d/" + h.DocumentID, Words: h.WordCount, Parts: snippetParts(h.SnippetHTML)})
	}
	return out
}

func offsetParam(r *http.Request) int {
	n := atoiOr(r.URL.Query().Get("offset"), 0)
	if n < 0 || n > 100000 {
		return 0
	}
	return n
}

func (s *Server) searchProject(w http.ResponseWriter, r *http.Request) {
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
	po, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: pid})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	proj := po.(projects.Item).Item.Addressed()
	if proj.Trashed {
		s.fail(w, r, ops.NotFound("project"))
		return
	}
	q, off := strings.TrimSpace(r.URL.Query().Get("q")), offsetParam(r)
	d := searchData{Q: q, Project: &proj, CSRF: authFrom(r.Context()).CSRF, Sem: s.semSearchFor(r, proj, q, false)}
	if q != "" && d.Sem.Mode != "meaning" {
		d.Searched = true
		out, err := s.Reg.Call(r.Context(), p, "search.query", search.QueryIn{ProjectID: pid, Q: q, Limit: searchPage, Offset: off})
		switch e := ops.AsError(err); {
		case err == nil:
			res := out.(search.QueryOut)
			d.Total, d.Degraded = res.Total, res.Degraded
			d.Groups = []searchGroup{{Project: proj, Hits: s.hitViews(proj.Slug, res.Items), Total: res.Total}}
			d.From, d.To = off+1, off+len(res.Items)
			base := "/p/" + proj.Slug + "/search?q=" + urlQuery(q)
			if res.NextOffset > 0 {
				d.Next = base + "&offset=" + itoa(res.NextOffset)
			}
			if off > 0 {
				prev := off - searchPage
				if prev < 0 {
					prev = 0
				}
				d.Prev = base + "&offset=" + itoa(prev)
			}
		case e.Code == ops.CodeInvalid:
			d.Searched, d.Message = false, e.Message
		default:
			s.fail(w, r, err)
			return
		}
	}
	s.render(w, r, http.StatusOK, "search", s.page(r, "Search · "+proj.Title, d))
}

func (s *Server) searchAll(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	d := searchData{Q: q, CSRF: authFrom(r.Context()).CSRF}
	if q != "" {
		lo, err := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{State: projects.StateAll})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Searched = true
		for i, pr := range addressedProjects(lo.(projects.ListOut).Items) {
			if pr.Trashed {
				continue
			}
			if i >= searchProjCap {
				d.Message = "Only the first " + itoa(searchProjCap) + " projects were searched. Search inside a project for the rest."
				break
			}
			out, err := s.Reg.Call(r.Context(), p, "search.query", search.QueryIn{ProjectID: pr.ID, Q: q, Limit: searchPerProj})
			if e := ops.AsError(err); err != nil {
				if e.Code == ops.CodeInvalid {
					d.Searched, d.Message, d.Groups = false, e.Message, nil
					break
				}
				continue // a project the person cannot search is simply not listed
			}
			res := out.(search.QueryOut)
			if res.Total == 0 {
				continue
			}
			g := searchGroup{Project: pr, Hits: s.hitViews(pr.Slug, res.Items), Total: res.Total}
			if res.Total > len(res.Items) {
				g.More = "/p/" + pr.Slug + "/search?q=" + urlQuery(q)
			}
			d.Groups = append(d.Groups, g)
			d.Total += res.Total
			d.Degraded = d.Degraded || res.Degraded
		}
	}
	s.render(w, r, http.StatusOK, "search", s.page(r, "Search", d))
}
