package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The outline: the manuscript as a list of synopses, statuses and targets, in
// reading order, with roll-ups for every part and chapter that contains others.
// The page is server-rendered and readable without JavaScript. Editing,
// reordering and adding are done by js/outline.js through doc.update, doc.move
// and doc.create; after each change it asks for the "outline-body" fragment again.

// outlineRow is one document in the outline.
type outlineRow struct {
	docs.OutlineItem
	Title     string // what the row shows ("Untitled" for an empty one)
	Number    string // the chapter number, as in the writing view's tree
	KindLabel string
	InTitle   string // the title of the document it sits in, for people who cannot see the indent
	Level     int    // indent steps, 0 to maxOutlineLevel
	Container bool   // it holds other documents, so it shows roll-ups
	Words     int    // what the row shows: its own words, or the sum of its contents
	Target    int
	Pct       int // Words over Target as a percentage, capped at 100 for the bar
	Over      bool
	Status    string
	StatusTxt string
	Mix       string // for a container: its documents by status, in words
	// What the sorted modes add: where the scene sits in the book, whose eyes it is told through, and when it happens.
	Crumb   string
	Pov     string
	PovID   string
	Date    string // the story date as written; "Undated" when there is none
	Dated   bool   // it reads as a date, so the timeline can place it
	DateRaw string
}

// outlineSort is one way to order the outline; the first is the manuscript's own.
type outlineSort struct{ Key, Query, Label string }

var outlineSorts = []outlineSort{{"order", "", "Manuscript order"}, {"timeline", "timeline", "Story timeline"}, {"pov", "pov", "Point of view"}}

// outlineGroup is one point of view in the "Point of view" order.
type outlineGroup struct {
	Label  string
	Entity string // the linked character's id, when there is one
	Docs   int
	Words  int
	Rows   []outlineRow
}

// outlineView is the data of the outline page and its fragment.
type outlineView struct {
	Project   projects.Project
	Slug      string
	ProjectID string
	CanEdit   bool
	CSRF      string
	Rows      []outlineRow
	Totals    docs.Rollup
	Chapters  int
	Pct       int
	TotalMix  string
	Sort      string // order, timeline or pov
	Sorted    bool   // timeline or pov: a flat list of scenes, not the manuscript's tree
	Groups    []outlineGroup
	Cols      string // the columns shown by default: any of date, pov, status, words
	Undated   int    // in the timeline order: scenes with no story date, which come last
	NoPov     bool   // in the pov order: true when no scene has a point of view yet
}

const maxOutlineLevel = 5

var statusLabels = map[string]string{"idea": "Idea", "outline": "Outline", "draft": "Draft", "revise": "Revise", "done": "Done"}

func statusMix(c docs.StatusCounts) string {
	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+label)
		}
	}
	add(c.Idea, "idea")
	add(c.Outline, "outline")
	add(c.Draft, "draft")
	add(c.Revise, "revise")
	add(c.Done, "done")
	return strings.Join(parts, ", ")
}

func percent(words, target int) int {
	if target <= 0 {
		return 0
	}
	p := int(math.Round(float64(words) / float64(target) * 100))
	if p > 100 {
		p = 100
	}
	return p
}

// buildOutline turns outline.get into the rows the template shows. Chapter numbers
// come from the same counter the writing view's tree uses.
func buildOutline(proj projects.Project, out docs.OutlineOut, canEdit bool, csrf string) outlineView {
	v := outlineView{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CanEdit: canEdit, CSRF: csrf, Totals: out.Totals,
		TotalMix: statusMix(out.Totals.Status), Pct: percent(out.Totals.Words, out.Totals.TargetWords), Sort: "order", Cols: "status words"}
	if out.Sort == "timeline" || out.Sort == "pov" {
		v.Sort, v.Sorted = out.Sort, true
	}
	if out.Sort == "timeline" {
		v.Cols = "date status words"
	}
	title := make(map[string]string, len(out.Items))
	for _, it := range out.Items {
		title[it.ID] = displayTitle(it.Title)
	}
	var num chapterCounter
	v.Rows = make([]outlineRow, 0, len(out.Items))
	for _, it := range out.Items {
		row := outlineRow{OutlineItem: it, Title: displayTitle(it.Title), KindLabel: docKindLabel(it.Kind), Container: it.ChildCount > 0,
			Level: it.Depth, Words: it.WordCount, Target: it.TargetWords, Status: it.Status, StatusTxt: statusLabels[it.Status]}
		if row.Level > maxOutlineLevel {
			row.Level = maxOutlineLevel
		}
		row.Pov, row.DateRaw, row.Dated, row.Date = it.Pov, it.StoryDate, it.StoryDated, it.StoryDate
		if it.PovEntity != nil {
			row.Pov, row.PovID = it.PovEntity.Name, it.PovEntity.ID
		}
		if row.Date == "" {
			row.Date = "Undated"
		} else if !it.StoryDated {
			row.Date += " (not a date)"
		}
		if v.Sorted {
			row.Level = 0
			var parts []string
			for _, c := range it.Breadcrumb {
				parts = append(parts, displayTitle(c.Title))
			}
			row.Crumb = strings.Join(parts, " / ")
			row.InTitle = row.Crumb
			if !it.StoryDated {
				v.Undated++
			}
		} else {
			row.Number = num.label(it.Kind)
			if it.Kind == "chapter" {
				v.Chapters++
			}
			if it.ParentID != "" {
				row.InTitle = title[it.ParentID]
			}
		}
		if row.Container {
			row.Words, row.Target = it.Rollup.Words, it.Rollup.TargetWords
			row.Mix = statusMix(it.Rollup.Status)
		}
		row.Pct = percent(row.Words, row.Target)
		row.Over = row.Target > 0 && row.Words > row.Target
		v.Rows = append(v.Rows, row)
	}
	if out.Sort == "pov" {
		byID := make(map[string]outlineRow, len(v.Rows))
		for _, r := range v.Rows {
			byID[r.ID] = r
		}
		for _, g := range out.Groups {
			og := outlineGroup{Label: g.Label, Docs: g.Documents, Words: g.Words}
			if g.Entity != nil {
				og.Entity = g.Entity.ID
				og.Label = g.Entity.Name
			}
			if og.Label == "" {
				og.Label = "No point of view"
			}
			for _, id := range g.IDs {
				if r, ok := byID[id]; ok {
					og.Rows = append(og.Rows, r)
				}
			}
			v.Groups = append(v.Groups, og)
		}
		v.NoPov = len(v.Groups) == 1 && v.Groups[0].Label == "No point of view"
	}
	return v
}

// outlineFor loads the outline of a project the caller can open.
func (s *Server) outlineFor(r *http.Request, projectID string) (outlineView, error) {
	p := s.principal(r)
	po, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: projectID})
	if err != nil {
		return outlineView{}, err
	}
	proj := po.(projects.Item).Item.Addressed()
	if proj.Trashed {
		return outlineView{}, ops.NotFound("project")
	}
	sort := r.URL.Query().Get("sort")
	if sort != "timeline" && sort != "pov" {
		sort = ""
	}
	oo, err := s.Reg.Call(r.Context(), p, "outline.get", docs.OutlineIn{ProjectID: projectID, Sort: sort})
	if err != nil {
		return outlineView{}, err
	}
	canEdit := proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor
	return buildOutline(proj, oo.(docs.OutlineOut), canEdit, authFrom(r.Context()).CSRF), nil
}

func (s *Server) outlinePage(w http.ResponseWriter, r *http.Request) {
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
	start := time.Now()
	v, err := s.outlineFor(r, pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.Log.Debug("outline", "rows", len(v.Rows), "build", time.Since(start))
	s.render(w, r, http.StatusOK, "outline", s.page(r, "Outline of "+v.Project.Title, v))
}

// uiOutline is the list and its summary as a fragment, for the outline script to merge after a change.
func (s *Server) uiOutline(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	v, err := s.outlineFor(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "outline", "outline-body", v)
}
