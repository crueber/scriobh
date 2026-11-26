package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// "Where this appears" on an entity sheet. In a book it is the documents that mention the entity, in reading order,
// with a snippet and a count each, and the first appearance on top (mention.by_entity and entity.first_appearance).
// In a Compendium it is the books that mention it that the caller can open, and how many more there are that they
// cannot (entity.appears_in): those are counted and never named. Everything is read through the ops, so what the
// caller may not read is left out by the same rules as everywhere else.

const wherePage = 20

// whereRow is one document in a book that mentions the entity.
type whereRow struct {
	Title     string
	Crumb     string
	Href      string
	Snippet   string
	Count     int
	Explicit  int
	Ambiguous bool
	First     bool
}

// whereBook is one book that mentions an entity of a Compendium.
type whereBook struct {
	Title     string
	Href      string
	Documents int
	Mentions  int
	FirstHref string
	FirstText string
	Indexing  bool
}

// whereData is the section's content, for one book or for a Compendium's books.
type whereData struct {
	Rows      []whereRow
	Books     []whereBook
	Documents int
	Mentions  int
	Hidden    int // books that mention it which the caller cannot open
	Indexing  bool
	FirstHref string
	FirstText string
	Next      string
	Prev      string
	Failed    bool
	Compact   bool // the page draws its own heading for the section
}

func crumbText(titles ...string) string { return strings.Join(titles, " / ") }

// whereInProject asks where an entity appears in a book the caller can open.
func (s *Server) whereInProject(r *http.Request, proj projects.Project, entityID, base string) *whereData {
	p := s.principal(r)
	off := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("w")); err == nil && n > 0 {
		off = n
	}
	w := &whereData{}
	out, err := s.Reg.Call(r.Context(), p, "mention.by_entity", mentions.ByEntityIn{ID: proj.ID, Entity: entityID, Limit: wherePage, Offset: off})
	if err != nil {
		// Not readable here is the same answer as none: nothing about the bible is told.
		if ops.AsError(err).Code != ops.CodeNotFound {
			w.Failed = true
		}
		return w
	}
	o := out.(mentions.ByEntityOut)
	w.Documents, w.Mentions, w.Indexing = o.Documents, o.Mentions, o.Indexing
	if fo, ferr := s.Reg.Call(r.Context(), p, "entity.first_appearance", mentions.FirstIn{ID: proj.ID, Entity: entityID}); ferr == nil {
		if items := fo.(mentions.FirstOut).Items; len(items) == 1 && items[0].Document != nil {
			d := items[0].Document
			w.FirstHref, w.FirstText = "/p/"+proj.Slug+"/d/"+d.ID, displayTitle(d.Title)
			if len(d.Breadcrumb) > 0 {
				w.FirstText = displayTitle(d.Breadcrumb[len(d.Breadcrumb)-1].Title) + " / " + w.FirstText
			}
		}
	}
	for i, a := range o.Items {
		row := whereRow{Title: displayTitle(a.Document.Title), Href: "/p/" + proj.Slug + "/d/" + a.Document.ID, Snippet: a.Snippet, Count: a.Count, Explicit: a.Explicit, Ambiguous: a.Ambiguous, First: off == 0 && i == 0}
		var parts []string
		for _, c := range a.Document.Breadcrumb {
			parts = append(parts, displayTitle(c.Title))
		}
		row.Crumb = crumbText(parts...)
		w.Rows = append(w.Rows, row)
	}
	if o.NextOffset > 0 {
		w.Next = base + "?" + url.Values{"w": {strconv.Itoa(o.NextOffset)}}.Encode() + "#where"
	}
	if off > 0 {
		prev := off - wherePage
		if prev < 0 {
			prev = 0
		}
		q := ""
		if prev > 0 {
			q = "?" + url.Values{"w": {strconv.Itoa(prev)}}.Encode()
		}
		w.Prev = base + q + "#where"
	}
	return w
}

// whereAcross asks which books mention an entity of a Compendium.
func (s *Server) whereAcross(r *http.Request, entityID string) *whereData {
	p := s.principal(r)
	w := &whereData{}
	out, err := s.Reg.Call(r.Context(), p, "entity.appears_in", mentions.AppearsIn{ID: entityID})
	if err != nil {
		w.Failed = true
		return w
	}
	o := out.(mentions.AppearsOut)
	w.Hidden = o.HiddenProjects
	for _, a := range o.Items {
		po, perr := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: a.ProjectID})
		if perr != nil {
			w.Hidden++
			continue
		}
		pr := po.(projects.Item).Item.Addressed()
		b := whereBook{Title: pr.Title, Href: "/p/" + pr.Slug, Documents: a.Documents, Mentions: a.Mentions, Indexing: a.Indexing}
		if a.First != nil {
			b.FirstHref, b.FirstText = "/p/"+pr.Slug+"/d/"+a.First.ID, displayTitle(a.First.Title)
		}
		w.Books = append(w.Books, b)
		w.Documents += a.Documents
		w.Mentions += a.Mentions
		w.Indexing = w.Indexing || a.Indexing
	}
	return w
}
