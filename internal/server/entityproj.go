package server

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// A project's view of the entity bible: the entities it can see (its primary Compendium, the Compendiums it
// references, single linked entities) as the book reads them, with the book's own overrides. Setting or clearing an
// override never changes the Compendium's record; the page says so. Being on a project is not being on the
// Compendium: what the caller cannot read is counted as "hidden" and never named.

var viaLabels = map[string]string{"primary": "Primary Compendium", "reference": "Referenced Compendium", "link": "Linked entity"}

type projEntityRow struct {
	ID, Kind, KindLabel, Name, Summary, Color string
	Via, ViaLabel, Compendium                 string
	Overridden                                bool
}

type projEntityListData struct {
	Project projects.Project
	Slug    string
	CSRF    string
	Kinds   []entityKind
	Kind, Q string
	Rows    []projEntityRow
	Total   int
	Hidden  bool
	Next    string
	Prev    string
	Error   string
	Live    bool
	NoBible bool
	Saved   string
	Src     string
}

func (s *Server) projectEntitiesPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	d := projEntityListData{Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, Kinds: entityKinds, Kind: q.Get("kind"), Q: strings.TrimSpace(q.Get("q")), Live: true, Saved: projSheetSaved[q.Get("saved")], Src: fragSrc(r, "/p/"+proj.Slug+"/entities")}
	if !validKind(d.Kind) {
		d.Kind = ""
	}
	off := 0
	if n, err := strconv.Atoi(q.Get("offset")); err == nil && n > 0 {
		off = n
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.entity.list", entities.ProjectListIn{ID: proj.ID, Kind: d.Kind, Q: d.Q, Limit: entityPageSize, Offset: off})
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid {
			s.fail(w, r, err)
			return
		}
		d.Error = e.Message
	} else {
		res := out.(entities.ProjectListOut)
		d.Total, d.Hidden = res.Total, res.Hidden
		for _, e := range res.Items {
			d.Rows = append(d.Rows, projEntityRow{ID: e.ID, Kind: e.Kind, KindLabel: kindSingular(e.Kind), Name: e.Name, Summary: e.Summary, Color: colorOK(e.Color),
				Via: e.Via, ViaLabel: viaLabels[e.Via], Compendium: e.CompendiumName, Overridden: e.Overridden})
		}
		base := url.Values{}
		if d.Kind != "" {
			base.Set("kind", d.Kind)
		}
		if d.Q != "" {
			base.Set("q", d.Q)
		}
		if res.NextOffset > 0 {
			nv := cloneValues(base)
			nv.Set("offset", strconv.Itoa(res.NextOffset))
			d.Next = "/p/" + proj.Slug + "/entities?" + nv.Encode()
		}
		if off > 0 {
			pv := cloneValues(base)
			if prev := off - entityPageSize; prev > 0 {
				pv.Set("offset", strconv.Itoa(prev))
			}
			d.Prev = "/p/" + proj.Slug + "/entities?" + pv.Encode()
		}
		d.NoBible = res.Total == 0 && !res.Hidden && d.Kind == "" && d.Q == ""
	}
	if q.Get("frag") == "1" {
		w.Header().Set("Cache-Control", "private, no-cache")
		s.fragment(w, r, http.StatusOK, "entity-list", "project-entity-list", d)
		return
	}
	s.render(w, r, http.StatusOK, "project-entities", s.page(r, "Entities of "+proj.Title, d))
}

type projSheetField struct {
	entities.FieldDef
	Effective string
	Base      string
	Override  string
	Blanked   bool
	Has       bool
}

type projSheetData struct {
	Project     projects.Project
	Slug        string
	CSRF        string
	CanEdit     bool // the caller can change this book's override
	E           entities.ProjectEntity
	Kind        string
	ViaLabel    string
	BodyHTML    template.HTML
	Fields      []projSheetField
	Notes       string
	CompSlug    string // the Compendium's own address, when the caller is on it
	Saved       string
	Error       string
	Scope       string
	Values      map[string]string
	OverrideSet bool
	BaseName    string // the Compendium's own name and summary, whatever this book changes
	BaseSummary string
	OvName      string // what this book sets, empty when it does not
	OvSummary   string
	IsLink      bool
	Color       string
	Live        bool
	Where       *whereData
}

var projSheetSaved = map[string]string{
	"override": "This book's override is saved. The Compendium's own entry is unchanged.",
	"cleared":  "The override is cleared. This book reads the Compendium's entry again.",
	"unlinked": "The entity is no longer linked into this project.",
}

func (s *Server) projectSheetView(r *http.Request, proj projects.Project, pe entities.ProjectEntity) (projSheetData, error) {
	d := projSheetData{Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, CanEdit: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor, E: pe,
		Kind: kindSingular(pe.Kind), Color: colorOK(pe.Color), ViaLabel: viaLabels[pe.Via], Values: map[string]string{}, Live: true}
	d.BodyHTML = template.HTML(text.RenderHTML(pe.Body, text.HTMLOptions{HeadingShift: 2}))
	d.Where = s.whereInProject(r, proj, pe.ID, "/p/"+proj.Slug+"/e/"+pe.ID)
	d.OverrideSet = pe.Override != nil
	d.IsLink = pe.Via == "link"
	d.BaseName, d.BaseSummary = pe.Name, pe.Summary
	if pe.Base != nil {
		d.BaseName, d.BaseSummary = pe.Base.Name, pe.Base.Summary
	}
	if pe.Override != nil {
		d.Notes = pe.Override.Notes
		if pe.Override.Name != nil {
			d.OvName = *pe.Override.Name
		}
		if pe.Override.Summary != nil {
			d.OvSummary = *pe.Override.Summary
		}
	}
	fo, err := s.Reg.Call(r.Context(), s.principal(r), "entity.field.list", entities.FieldListIn{ID: pe.CompendiumID, Kind: pe.Kind})
	if err == nil {
		for _, f := range fo.(entities.FieldListOut).Items {
			row := projSheetField{FieldDef: f, Effective: pe.Fields[f.Key]}
			if pe.Base != nil {
				row.Base = pe.Base.Fields[f.Key]
			} else {
				row.Base = pe.Fields[f.Key]
			}
			if pe.Override != nil {
				if v, ok := pe.Override.Fields[f.Key]; ok {
					row.Has, row.Override, row.Blanked = true, v, v == ""
				}
			}
			d.Fields = append(d.Fields, row)
		}
	}
	if co, cerr := s.Reg.Call(r.Context(), s.principal(r), "compendium.get", compendiums.GetIn{ID: pe.CompendiumID}); cerr == nil {
		d.CompSlug = s.addressed(r, co.(compendiums.DetailItem).Item.Compendium).Slug
	}
	return d, nil
}

func (s *Server) projectSheetPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !entityIDPattern.MatchString(id) {
		s.fail(w, r, ops.NotFound("entity"))
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.entity.get", entities.ProjectGetIn{ID: proj.ID, Entity: id})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.projectSheetView(r, proj, out.(entities.ProjectItem).Item)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = projSheetSaved[r.URL.Query().Get("saved")]
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "project-entity", s.page(r, d.E.Name+" · "+proj.Title, d))
}

func (s *Server) projectSheetRedirect(w http.ResponseWriter, r *http.Request, proj projects.Project, id, saved string) {
	http.Redirect(w, r, "/p/"+proj.Slug+"/e/"+id+"?saved="+url.QueryEscape(saved), http.StatusSeeOther)
}

// uiEntityOverride sets or clears this book's override of an entity; uiEntityLink removes a single-entity link.
func (s *Server) uiEntityOverride(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	id := r.PostFormValue("entity_id")
	p := s.principal(r)
	var err error
	switch r.PathValue("action") {
	case "set":
		name, summary, notes := strings.TrimSpace(r.PostFormValue("name")), strings.TrimSpace(r.PostFormValue("summary")), strings.TrimSpace(r.PostFormValue("notes"))
		fields := map[string]any{}
		keys := []string{}
		for k := range r.PostForm {
			if strings.HasPrefix(k, "f.") {
				keys = append(keys, strings.TrimPrefix(k, "f."))
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := strings.TrimSpace(r.PostFormValue("f." + k))
			switch {
			case r.PostFormValue("blank."+k) == "1":
				fields[k] = ""
			case v != "":
				fields[k] = v
			default:
				fields[k] = nil // stop overriding this field
			}
		}
		_, err = s.Reg.Call(r.Context(), p, "project.entity.override.set", entities.OverrideSetIn{ID: proj.ID, EntityID: id, Name: &name, Summary: &summary, Fields: fields, Notes: &notes})
		if err == nil {
			s.projectSheetRedirect(w, r, proj, id, "override")
			return
		}
	case "clear":
		_, err = s.Reg.Call(r.Context(), p, "project.entity.override.clear", entities.OverrideClearIn{ID: proj.ID, EntityID: id})
		if err == nil {
			s.projectSheetRedirect(w, r, proj, id, "cleared")
			return
		}
	case "unlink":
		_, err = s.Reg.Call(r.Context(), p, "project.entity.link.remove", entities.LinkRemoveIn{ID: proj.ID, EntityID: id})
		if err == nil {
			http.Redirect(w, r, "/p/"+proj.Slug+"/entities?saved=unlinked", http.StatusSeeOther)
			return
		}
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	out, gerr := s.Reg.Call(r.Context(), p, "project.entity.get", entities.ProjectGetIn{ID: proj.ID, Entity: id})
	if gerr != nil {
		s.fail(w, r, gerr)
		return
	}
	d, verr := s.projectSheetView(r, proj, out.(entities.ProjectItem).Item)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	d.Error, d.Scope = e.Message, "override"
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			d.Values[k] = v[0]
		}
	}
	s.render(w, r, http.StatusUnprocessableEntity, "project-entity", s.page(r, d.E.Name+" · "+proj.Title, d))
}

func (s *Server) registerEntityProject(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/entities", s.projectEntitiesPage)
	m.HandleFunc("GET /p/{slug}/e/{id}", s.projectSheetPage)
	s.uiRoute("POST /ui/projects/{id}/entity-override/{action}", s.uiEntityOverride)
}
