package server

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The entity bible: a Compendium's entities as a list, a sheet each, the field definitions they can hold, merging
// two of them, and an export of the lot. Every page is built from the entity ops and every change goes through
// them; a person with no project open can do all of it at /c/{slug}/.... The project's own view of the same
// entities (what it overrides, what it links) is in entityproj.go.

type entityKind struct{ ID, Plural, Singular string }

var entityKinds = []entityKind{
	{"character", "Characters", "Character"},
	{"place", "Places", "Place"},
	{"item", "Items", "Item"},
	{"org", "Organisations", "Organisation"},
	{"concept", "Concepts", "Concept"},
}

func kindSingular(k string) string {
	for _, x := range entityKinds {
		if x.ID == k {
			return x.Singular
		}
	}
	return k
}

func kindPlural(k string) string {
	for _, x := range entityKinds {
		if x.ID == k {
			return x.Plural
		}
	}
	return k
}

func validKind(k string) bool {
	for _, x := range entityKinds {
		if x.ID == k {
			return true
		}
	}
	return false
}

var entityIDPattern = regexp.MustCompile(`^ent_[0-9A-Za-z]+$`)

const entityPageSize = 50

// compEditable reports whether the caller may change what a Compendium holds: an editor or owner of one that is not in
// the trash (the ops refuse writes to a trashed one). An archived Compendium stays writable: an archive is a shelf, as
// it is for projects, and the pages only say so.
func compEditable(c compendiums.Compendium) bool {
	return (c.Role == "owner" || c.Role == "editor") && !c.Trashed
}

func roleWords(role string) string {
	switch role {
	case "owner":
		return "an owner"
	case "editor":
		return "an editor"
	}
	return "a reader"
}

// ---- the list -----------------------------------------------------------------------------------------------

type entityRow struct {
	ID, Kind, KindLabel, Name, Summary, Color string
	Aliases                                   []string
	MatchedIn                                 string
	Parts                                     []snipPart
	Trashed                                   bool
}

type entityListData struct {
	Compendium compendiums.Compendium
	Slug       string
	CSRF       string
	CanEdit    bool
	Kinds      []entityKind
	Kind, Q    string
	State      string
	Rows       []entityRow
	Total      int
	Searching  bool
	Next       string // the address of the next page, empty on the last
	Prev       string
	Error      string
	Field      string
	Values     map[string]string
	Degraded   bool
	Empty      bool // the Compendium holds no entities at all
	Saved      string
	SavedText  string
	RoleWords  string
	QEsc       string // Q, escaped for use in an address
	Src        string // the address that answers with just the list, for a live refresh
	Live       bool
}

func colorOK(c string) string {
	if len(c) == 7 && c[0] == '#' {
		return c
	}
	return ""
}

func (s *Server) entityListView(r *http.Request, c compendiums.Compendium) (entityListData, error) {
	p := s.principal(r)
	q := r.URL.Query()
	d := entityListData{Compendium: c, Slug: c.Slug, CSRF: authFrom(r.Context()).CSRF, CanEdit: compEditable(c), Kinds: entityKinds,
		Kind: q.Get("kind"), Q: strings.TrimSpace(q.Get("q")), State: "live", Values: map[string]string{}, RoleWords: roleWords(c.Role)}
	d.QEsc = url.QueryEscape(d.Q)
	d.Src = fragSrc(r, "/c/"+c.Slug+"/entities")
	if !validKind(d.Kind) {
		d.Kind = ""
	}
	if q.Get("state") == "trashed" && d.CanEdit {
		d.State = "trashed"
	}
	base := url.Values{}
	if d.Kind != "" {
		base.Set("kind", d.Kind)
	}
	if d.Q != "" {
		base.Set("q", d.Q)
	}
	if d.State == "trashed" {
		base.Set("state", "trashed")
	}
	if d.Q != "" {
		d.Searching = true
		off := 0
		if n, err := strconv.Atoi(q.Get("offset")); err == nil && n > 0 {
			off = n
		}
		out, err := s.Reg.Call(r.Context(), p, "search.compendium", search.CompendiumQueryIn{ID: c.ID, Q: d.Q, Limit: 25, Offset: off})
		if err != nil {
			e := ops.AsError(err)
			if e.Code != ops.CodeInvalid {
				return d, err
			}
			d.Error, d.Field = e.Message, "q"
			return d, nil
		}
		res := out.(search.CompendiumQueryOut)
		d.Total, d.Degraded = res.Total, res.Degraded
		for _, h := range res.Items {
			if d.Kind != "" && h.Kind != d.Kind {
				continue
			}
			d.Rows = append(d.Rows, entityRow{ID: h.EntityID, Kind: h.Kind, KindLabel: kindSingular(h.Kind), Name: h.Name, MatchedIn: h.MatchedIn, Parts: snippetParts(h.SnippetHTML)})
		}
		if res.NextOffset > 0 {
			nv := cloneValues(base)
			nv.Set("offset", strconv.Itoa(res.NextOffset))
			d.Next = "/c/" + c.Slug + "/entities?" + nv.Encode()
		}
		if off > 0 {
			pv := cloneValues(base)
			if prev := off - 25; prev > 0 {
				pv.Set("offset", strconv.Itoa(prev))
			}
			d.Prev = "/c/" + c.Slug + "/entities?" + pv.Encode()
		}
		return d, nil
	}
	cursor := q.Get("cursor")
	out, err := s.Reg.Call(r.Context(), p, "entity.list", entities.ListIn{ID: c.ID, Kind: d.Kind, State: d.State, Limit: entityPageSize, Cursor: cursor})
	if err != nil {
		return d, err
	}
	res := out.(entities.ListOut)
	d.Total = res.Total
	for _, e := range res.Items {
		d.Rows = append(d.Rows, entityRow{ID: e.ID, Kind: e.Kind, KindLabel: kindSingular(e.Kind), Name: e.Name, Summary: e.Summary, Aliases: e.Aliases, Color: colorOK(e.Color), Trashed: e.Trashed})
	}
	if res.NextCursor != "" {
		nv := cloneValues(base)
		nv.Set("cursor", res.NextCursor)
		d.Next = "/c/" + c.Slug + "/entities?" + nv.Encode()
	}
	d.Empty = d.Kind == "" && d.State == "live" && cursor == "" && res.Total == 0
	return d, nil
}

// fragSrc is the address of this page's list-only answer: the same query, plus frag=1, without a one-off notice.
func fragSrc(r *http.Request, path string) string {
	v := cloneValues(r.URL.Query())
	v.Set("frag", "1")
	v.Del("saved")
	return path + "?" + v.Encode()
}

func cloneValues(v url.Values) url.Values {
	o := url.Values{}
	for k, x := range v {
		o[k] = append([]string(nil), x...)
	}
	return o
}

func (s *Server) entitiesPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	d, err := s.entityListView(r, c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = r.URL.Query().Get("saved")
	d.SavedText = entitySaved[d.Saved]
	d.Live = true
	if r.URL.Query().Get("frag") == "1" {
		w.Header().Set("Cache-Control", "private, no-cache")
		s.fragment(w, r, http.StatusOK, "entity-list", "entity-list", d)
		return
	}
	s.render(w, r, http.StatusOK, "entities", s.page(r, "Entities of "+c.Name, d))
}

// uiEntityCreate adds an entity from the list page.
func (s *Server) uiEntityCreate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.entCompByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	name, kind := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("kind")
	out, err := s.Reg.Call(r.Context(), s.principal(r), "entity.create", entities.CreateIn{ID: c.ID, Kind: kind, Name: name})
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
			s.fail(w, r, err)
			return
		}
		d, verr := s.entityListView(r, c)
		if verr != nil {
			s.fail(w, r, verr)
			return
		}
		d.Error, d.Field = e.Message, e.Field
		if e.Code == ops.CodeConflict {
			d.Field = "name"
		}
		d.Values = map[string]string{"name": name, "kind": kind}
		s.render(w, r, http.StatusUnprocessableEntity, "entities", s.page(r, "Entities of "+c.Name, d))
		return
	}
	http.Redirect(w, r, "/c/"+c.Slug+"/e/"+out.(entities.Item).Item.ID+"?saved=created", http.StatusSeeOther)
}

// ---- the sheet ---------------------------------------------------------------------------------------------

type fieldRow struct {
	entities.FieldDef
	Value string
	Rows  int
	Multi bool // a text field that holds a paragraph: a text area; every other text field is one line
}

// longFormKeys are the starter fields that hold a paragraph rather than a word or a phrase.
var longFormKeys = map[string]bool{"appearance": true, "voice": true, "wants": true, "fears": true, "secrets": true, "arc": true, "history": true, "description": true}

// multiline reports whether a text field wants a text area: it is a long-form field, or its value already runs over
// a line.
func multiline(key, value string) bool {
	return longFormKeys[key] || strings.Contains(value, "\n") || utf8.RuneCountInString(value) > 100
}

type relRow struct {
	entities.Relation
	Phrase string // "mother of Máire", read from this entity's side
}

type linkTarget struct{ ID, Title string }

type entityPageData struct {
	Compendium compendiums.Compendium
	Slug       string
	CSRF       string
	CanEdit    bool
	E          entities.Detail
	Kind       string
	Color      string
	RoleWords  string
	SavedText  string
	BodyHTML   template.HTML
	Fields     []fieldRow
	Orphans    []fieldRow // values under keys with no definition (kept, shown read-only)
	Relations  []relRow
	Swatches   []projects.PaletteSwatch
	Projects   []linkTarget
	Kinds      []string
	Saved      string
	Followed   bool
	Merged     string
	Error      string
	Scope      string // which form the error belongs to: details, alias, relation, link
	Field      string
	Values     map[string]string
	Collision  *entityCollision
	Conflict   *entityConflict
	Live       bool
	Where      *whereData
}

type entityCollision struct{ ID, Name, Via string }

type entityConflict struct {
	Revision int
	Theirs   entities.Detail
}

var entitySaved = map[string]string{
	"created":   "The entity is added.",
	"saved":     "Saved.",
	"alias":     "Alias added.",
	"unalias":   "Alias removed.",
	"relation":  "Relationship added.",
	"unrelate":  "Relationship removed.",
	"trashed":   "Moved to the trash. Its name and aliases are free for other entities now.",
	"restored":  "Restored from the trash.",
	"linked":    "Linked into the project.",
	"fieldsset": "Field saved.",
}

func (s *Server) entityInCompendium(w http.ResponseWriter, r *http.Request, c compendiums.Compendium, id string) (entities.Detail, bool) {
	if !entityIDPattern.MatchString(id) {
		s.fail(w, r, ops.NotFound("entity"))
		return entities.Detail{}, false
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "entity.get", entities.GetIn{ID: id})
	if err != nil {
		s.fail(w, r, err)
		return entities.Detail{}, false
	}
	e := out.(entities.DetailItem).Item
	// An entity of another Compendium is as missing here as one that never existed.
	if e.CompendiumID != c.ID {
		s.fail(w, r, ops.NotFound("entity"))
		return entities.Detail{}, false
	}
	return e, true
}

func (s *Server) entitySheetView(r *http.Request, c compendiums.Compendium, e entities.Detail) (entityPageData, error) {
	p := s.principal(r)
	d := entityPageData{Compendium: c, Slug: c.Slug, CSRF: authFrom(r.Context()).CSRF, CanEdit: compEditable(c), E: e, Kind: kindSingular(e.Kind), Color: colorOK(e.Color), RoleWords: roleWords(c.Role),
		Swatches: projects.Swatches(), Values: map[string]string{}, Live: true}
	d.BodyHTML = template.HTML(text.RenderHTML(e.Body, text.HTMLOptions{HeadingShift: 2}))
	if !e.Trashed {
		d.Where = s.whereAcross(r, e.ID)
	}
	fo, err := s.Reg.Call(r.Context(), p, "entity.field.list", entities.FieldListIn{ID: c.ID, Kind: e.Kind})
	if err != nil {
		return d, err
	}
	seen := map[string]bool{}
	for _, f := range fo.(entities.FieldListOut).Items {
		seen[f.Key] = true
		rows := 2
		d.Fields = append(d.Fields, fieldRow{FieldDef: f, Value: e.Fields[f.Key], Rows: rows, Multi: multiline(f.Key, e.Fields[f.Key])})
	}
	var orphan []string
	for k := range e.Fields {
		if !seen[k] && e.Fields[k] != "" {
			orphan = append(orphan, k)
		}
	}
	sort.Strings(orphan)
	for _, k := range orphan {
		d.Orphans = append(d.Orphans, fieldRow{FieldDef: entities.FieldDef{Key: k, Label: k, Type: "text"}, Value: e.Fields[k]})
	}
	for _, rel := range e.Relations {
		d.Relations = append(d.Relations, relRow{Relation: rel, Phrase: rel.Label + " " + rel.OtherName})
	}
	if d.CanEdit {
		d.Kinds = []string{"family", "ally", "rival", "mentor", "romantic", "member", "custom"}
		if po, perr := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{}); perr == nil {
			n := 0
			for _, pr := range addressedProjects(po.(projects.ListOut).Items) {
				if (pr.Role == ops.ProjectOwner || pr.Role == ops.ProjectEditor) && n < 100 {
					d.Projects = append(d.Projects, linkTarget{ID: pr.ID, Title: pr.Title})
					n++
				}
			}
		}
	}
	return d, nil
}

func (s *Server) entitySheetPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	e, ok := s.entityInCompendium(w, r, c, r.PathValue("id"))
	if !ok {
		return
	}
	if e.RedirectedFrom != "" {
		// The old id of a merged entity: go to the survivor's own address and say so.
		http.Redirect(w, r, "/c/"+c.Slug+"/e/"+e.ID+"?followed=1", http.StatusSeeOther)
		return
	}
	d, err := s.entitySheetView(r, c, e)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	d.Saved = q.Get("saved")
	d.SavedText = entitySaved[d.Saved]
	d.Followed = q.Get("followed") == "1"
	if m := q.Get("merged"); entityIDPattern.MatchString(m) {
		d.Merged = m
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "entity", s.page(r, e.Name+" · "+c.Name, d))
}

// entityFail shows a refused change beside the form it came from, in plain words, with what the person typed kept.
// A stale revision keeps their text and offers the two ways out.
func (s *Server) entityFail(w http.ResponseWriter, r *http.Request, c compendiums.Compendium, id, scope string, err error) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	out, gerr := s.Reg.Call(r.Context(), s.principal(r), "entity.get", entities.GetIn{ID: id})
	if gerr != nil {
		s.fail(w, r, gerr)
		return
	}
	cur := out.(entities.DetailItem).Item
	d, verr := s.entitySheetView(r, c, cur)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	d.Error, d.Field, d.Scope = e.Message, e.Field, scope
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			d.Values[k] = v[0]
		}
	}
	if scope == "details" && r.PostForm.Has("name") {
		// Keep what the person typed in the details form, not what is saved.
		d.E.Name, d.E.SortName, d.E.Summary = d.Values["name"], d.Values["sort_name"], d.Values["summary"]
		d.E.Body = strings.ReplaceAll(d.Values["body"], "\r\n", "\n")
		if _, ok := d.Values["color"]; ok {
			d.E.Color = colorOK(d.Values["color"])
		}
		for i := range d.Fields {
			if v, ok := d.Values["f."+d.Fields[i].Key]; ok {
				d.Fields[i].Value = v
			}
		}
	}
	if e.Code == ops.CodeConflict && e.Data != nil {
		if rev, ok := e.Data["revision"]; ok {
			n := 0
			switch v := rev.(type) {
			case int:
				n = v
			case float64:
				n = int(v)
			}
			d.Conflict = &entityConflict{Revision: n, Theirs: cur}
			d.Error = "This entity changed since you opened it. Your text is kept below. Keep yours to replace theirs, or use theirs to start again from what is saved."
		} else if oid, ok := e.Data["entity_id"].(string); ok {
			name, _ := e.Data["name"].(string)
			via, _ := e.Data["via"].(string)
			d.Collision = &entityCollision{ID: oid, Name: name, Via: via}
		}
	}
	s.render(w, r, http.StatusUnprocessableEntity, "entity", s.page(r, cur.Name+" · "+c.Name, d))
}

func (s *Server) sheetRedirect(w http.ResponseWriter, r *http.Request, c compendiums.Compendium, id, saved, anchor string) {
	u := "/c/" + c.Slug + "/e/" + id + "?saved=" + url.QueryEscape(saved)
	if anchor != "" {
		u += "#" + anchor
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

// entityCompendium finds the Compendium an entity-addressed UI route belongs to, through entity.get (which
// authorizes: a stranger gets the 404 a missing entity gets).
func (s *Server) entityCompendium(w http.ResponseWriter, r *http.Request, id string) (compendiums.Compendium, bool) {
	if !entityIDPattern.MatchString(id) {
		s.fail(w, r, ops.NotFound("entity"))
		return compendiums.Compendium{}, false
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "entity.get", entities.GetIn{ID: id})
	if err != nil {
		s.fail(w, r, err)
		return compendiums.Compendium{}, false
	}
	return s.entCompByID(w, r, out.(entities.DetailItem).Item.CompendiumID)
}

func intPtr(n int) *int { return &n }

// entCompByID reads one Compendium through compendium.get, which authorizes: a stranger gets the 404 a missing one gets.
func (s *Server) entCompByID(w http.ResponseWriter, r *http.Request, id string) (compendiums.Compendium, bool) {
	return s.compendiumByID(w, r, id)
}

// uiEntity applies one action of the sheet; the path names it.
func (s *Server) uiEntity(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	c, ok := s.entityCompendium(w, r, id)
	if !ok {
		return
	}
	p := s.principal(r)
	ctx := r.Context()
	var err error
	scope := "details"
	switch action {
	case "save":
		name, sortName, summary, body, color := r.PostFormValue("name"), r.PostFormValue("sort_name"), r.PostFormValue("summary"), strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"), r.PostFormValue("color")
		in := entities.UpdateIn{ID: id, Name: &name, SortName: &sortName, Summary: &summary, Body: &body, Color: &color, Fields: map[string]any{}}
		if rev, perr := strconv.Atoi(r.PostFormValue("base_revision")); perr == nil && rev > 0 {
			in.BaseRevision = &rev
		}
		for k, v := range r.PostForm {
			if strings.HasPrefix(k, "f.") && len(v) > 0 {
				in.Fields[strings.TrimPrefix(k, "f.")] = strings.TrimSpace(v[0])
			}
		}
		if _, err = s.Reg.Call(ctx, p, "entity.update", in); err == nil {
			s.sheetRedirect(w, r, c, id, "saved", "")
			return
		}
	case "alias":
		scope = "alias"
		if _, err = s.Reg.Call(ctx, p, "entity.alias.add", entities.AliasAddIn{ID: id, Alias: r.PostFormValue("alias"), Notes: r.PostFormValue("notes")}); err == nil {
			s.sheetRedirect(w, r, c, id, "alias", "aliases")
			return
		}
	case "unalias":
		if _, err = s.Reg.Call(ctx, p, "entity.alias.remove", entities.AliasRemoveIn{ID: id, AliasID: r.PostFormValue("alias_id")}); err == nil {
			s.sheetRedirect(w, r, c, id, "unalias", "aliases")
			return
		}
	case "relation":
		scope = "relation"
		other := strings.TrimSpace(r.PostFormValue("other"))
		toID := ""
		if entityIDPattern.MatchString(other) {
			toID = other
		} else if other != "" {
			var lo any
			if lo, err = s.Reg.Call(ctx, p, "entity.list", entities.ListIn{ID: c.ID, Name: other}); err == nil {
				if items := lo.(entities.ListOut).Items; len(items) > 0 {
					toID = items[0].ID
				} else {
					err = ops.Invalid("other", "No entity in this Compendium has that name or alias. Type the name exactly as it is written on the other sheet.")
				}
			}
		} else {
			err = ops.Invalid("other", "Name the other entity.")
		}
		if err == nil {
			_, err = s.Reg.Call(ctx, p, "entity.relation.add", entities.RelationAddIn{ID: id, ToEntityID: toID, Kind: r.PostFormValue("kind"), Label: r.PostFormValue("label"),
				InverseLabel: r.PostFormValue("inverse_label"), Symmetric: formBool(r, "symmetric"), Notes: r.PostFormValue("notes")})
		}
		if err == nil {
			s.sheetRedirect(w, r, c, id, "relation", "relations")
			return
		}
	case "unrelate":
		if _, err = s.Reg.Call(ctx, p, "entity.relation.remove", entities.RelationRemoveIn{ID: id, RelationID: r.PostFormValue("relation_id")}); err == nil {
			s.sheetRedirect(w, r, c, id, "unrelate", "relations")
			return
		}
	case "trash":
		scope = "trash"
		if _, err = s.Reg.Call(ctx, p, "entity.trash", entities.StateIn{ID: id}); err == nil {
			s.sheetRedirect(w, r, c, id, "trashed", "")
			return
		}
	case "restore":
		scope = "trash"
		if _, err = s.Reg.Call(ctx, p, "entity.restore", entities.StateIn{ID: id}); err == nil {
			s.sheetRedirect(w, r, c, id, "restored", "")
			return
		}
	case "link":
		scope = "link"
		if _, err = s.Reg.Call(ctx, p, "project.entity.link.add", entities.LinkIn{ID: r.PostFormValue("project_id"), EntityID: id}); err == nil {
			s.sheetRedirect(w, r, c, id, "linked", "link")
			return
		}
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	s.entityFail(w, r, c, id, scope, err)
}

// uiMarkdownPreview renders a Markdown draft with the same whitelist renderer the HTML export uses: raw HTML is
// shown as text, links keep only safe addresses, images are notes, and nothing is fetched. The answer is a fragment.
func (s *Server) uiMarkdownPreview(w http.ResponseWriter, r *http.Request) {
	md := r.PostFormValue("body")
	if len(md) > 256<<10 {
		s.fail(w, r, ops.Invalid("body", "That is too long to preview."))
		return
	}
	d := struct{ HTML template.HTML }{template.HTML(text.RenderHTML(md, text.HTMLOptions{HeadingShift: 2}))}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, http.StatusOK, "entity-preview", "entity-preview", d)
}

func fmtCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
