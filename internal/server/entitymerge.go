package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// Merging two entities, the field definitions, and the export. The merge page shows the dry run the entity.merge op
// returns and nothing it computes itself, and the merge applies the same request: what the person was shown is what
// happens, because the op's own planner makes both.

// ---- merge --------------------------------------------------------------------------------------------------

type mergeOverride struct {
	entities.OverrideConflict
	ProjectName string
}

type mergeData struct {
	Compendium compendiums.Compendium
	Slug       string
	CSRF       string
	Source     entities.Detail
	Q          string
	Candidates []entityRow
	Into       string
	Plan       *entities.MergePlan
	Overrides  []mergeOverride
	Choices    map[string]string
	Summary    string
	Body       string
	NoAlias    bool
	Error      string
	Field      string
	CanMerge   bool // the plan has nothing left to decide
	// Fingerprint identifies the plan the person was shown. The merge button carries it, and a merge whose plan, worked
	// out again at that moment, is a different one is refused: the entities changed since the preview.
	Fingerprint string
}

// planFingerprint is a short, stable hash of everything a plan says it will do, and of the revisions of the two sheets it
// was worked out from: the plan's counts do not move when a summary, a note or a field value is edited, and the merge
// must still not go ahead on text the person never saw.
func planFingerprint(p entities.MergePlan, sourceRev, targetRev int) string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(append(b, []byte(fmt.Sprintf("|%d|%d", sourceRev, targetRev))...))
	return hex.EncodeToString(h[:8])
}

func mergeParams(r *http.Request) (into string, choices map[string]string, summary, body string, noAlias bool) {
	_ = r.ParseForm()
	into = strings.TrimSpace(r.FormValue("into"))
	choices = map[string]string{}
	for k, v := range r.Form {
		if strings.HasPrefix(k, "fc.") && len(v) > 0 && (v[0] == "source" || v[0] == "target") {
			choices[strings.TrimPrefix(k, "fc.")] = v[0]
		}
	}
	summary, body = r.FormValue("summary"), r.FormValue("body")
	noAlias = r.FormValue("no_alias") == "1"
	return
}

func (s *Server) mergeView(r *http.Request, c compendiums.Compendium, src entities.Detail) (mergeData, error) {
	into, choices, summary, body, noAlias := mergeParams(r)
	d := mergeData{Compendium: c, Slug: c.Slug, CSRF: authFrom(r.Context()).CSRF, Source: src, Q: strings.TrimSpace(r.FormValue("q")), Into: into,
		Choices: choices, Summary: summary, Body: body, NoAlias: noAlias}
	p := s.principal(r)
	if into == "" || !entityIDPattern.MatchString(into) {
		d.Into = ""
		// Pick the target: entities of the same kind, found by a name or alias fragment.
		lo, err := s.Reg.Call(r.Context(), p, "entity.list", entities.ListIn{ID: c.ID, Kind: src.Kind, Q: d.Q, Limit: 20})
		if err != nil {
			e := ops.AsError(err)
			if e.Code != ops.CodeInvalid {
				return d, err
			}
			d.Error = e.Message
			return d, nil
		}
		for _, e := range lo.(entities.ListOut).Items {
			if e.ID == src.ID {
				continue
			}
			d.Candidates = append(d.Candidates, entityRow{ID: e.ID, Kind: e.Kind, KindLabel: kindSingular(e.Kind), Name: e.Name, Summary: e.Summary, Aliases: e.Aliases, Color: colorOK(e.Color)})
		}
		return d, nil
	}
	nameAs := !noAlias
	out, err := s.Reg.Call(r.Context(), p, "entity.merge", entities.MergeIn{ID: src.ID, IntoID: into, DryRun: true, FieldChoices: choices, Summary: summary, Body: body, NameAsAlias: &nameAs})
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
			return d, err
		}
		d.Error, d.Field, d.Into = e.Message, e.Field, ""
		return d, nil
	}
	plan := out.(entities.MergeOut).Plan
	d.Plan = &plan
	var targetRev int
	if to, terr := s.Reg.Call(r.Context(), p, "entity.get", entities.GetIn{ID: into}); terr == nil {
		targetRev = to.(entities.DetailItem).Item.Revision
	}
	d.Fingerprint = planFingerprint(plan, src.Revision, targetRev)
	d.CanMerge = len(plan.Unresolved) == 0
	for _, oc := range plan.OverrideConflicts {
		mo := mergeOverride{OverrideConflict: oc, ProjectName: "A project you cannot open"}
		if po, perr := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: oc.ProjectID}); perr == nil {
			mo.ProjectName = po.(projects.Item).Item.Addressed().Title
		}
		d.Overrides = append(d.Overrides, mo)
	}
	return d, nil
}

func (s *Server) mergeSource(w http.ResponseWriter, r *http.Request) (compendiums.Compendium, entities.Detail, bool) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return c, entities.Detail{}, false
	}
	e, ok := s.entityInCompendium(w, r, c, r.PathValue("id"))
	if !ok {
		return c, e, false
	}
	if !compEditable(c) {
		s.fail(w, r, ops.Forbidden("Only an owner or an editor of the Compendium can merge entities."))
		return c, e, false
	}
	return c, e, true
}

func (s *Server) entityMergePage(w http.ResponseWriter, r *http.Request) {
	c, src, ok := s.mergeSource(w, r)
	if !ok {
		return
	}
	d, err := s.mergeView(r, c, src)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "entity-merge", s.page(r, "Merge "+src.Name, d))
}

// uiEntityMerge: the preview button shows the dry run again with the person's answers; the merge button applies
// the same request.
func (s *Server) uiEntityMerge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, ok := s.entityCompendium(w, r, id)
	if !ok {
		return
	}
	src, ok := s.entityInCompendium(w, r, c, id)
	if !ok {
		return
	}
	if r.PostFormValue("do") != "merge" {
		d, err := s.mergeView(r, c, src)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, r, http.StatusOK, "entity-merge", s.page(r, "Merge "+src.Name, d))
		return
	}
	into, choices, summary, body, noAlias := mergeParams(r)
	nameAs := !noAlias
	// What Merge will do is what the preview said: the plan is worked out again now, and if it is not the plan the
	// person was shown (either entity changed since, or the form is from before plans were fingerprinted) nothing is
	// merged, and the current preview is shown with a plain message.
	cur, verr := s.mergeView(r, c, src)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	if cur.Plan != nil && len(cur.Plan.Unresolved) == 0 && cur.Fingerprint != r.PostFormValue("plan") {
		cur.Error = "Something changed since you looked at this preview, so nothing was merged. The preview below is the current one. Check it, then merge again."
		cur.Field = ""
		s.render(w, r, http.StatusConflict, "entity-merge", s.page(r, "Merge "+src.Name, cur))
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "entity.merge", entities.MergeIn{ID: id, IntoID: into, FieldChoices: choices, Summary: summary, Body: body, NameAsAlias: &nameAs})
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
			s.fail(w, r, err)
			return
		}
		d, verr := s.mergeView(r, c, src)
		if verr != nil {
			s.fail(w, r, verr)
			return
		}
		d.Error, d.Field = e.Message, e.Field
		s.render(w, r, http.StatusUnprocessableEntity, "entity-merge", s.page(r, "Merge "+src.Name, d))
		return
	}
	target := out.(entities.MergeOut).Plan.TargetID
	http.Redirect(w, r, "/c/"+c.Slug+"/e/"+target+"?merged="+id, http.StatusSeeOther)
}

// ---- field definitions --------------------------------------------------------------------------------------

type fieldKindGroup struct {
	Kind, Plural, Singular string
	Defs                   []entities.FieldDef
}

type fieldsData struct {
	Compendium compendiums.Compendium
	Slug       string
	CSRF       string
	CanEdit    bool
	Groups     []fieldKindGroup
	Types      []string
	Error      string
	Scope      string // kind:key of the definition the error belongs to, or "new"
	Values     map[string]string
	Saved      string
}

func (s *Server) fieldsView(r *http.Request, c compendiums.Compendium) (fieldsData, error) {
	d := fieldsData{Compendium: c, Slug: c.Slug, CSRF: authFrom(r.Context()).CSRF, CanEdit: compEditable(c), Types: []string{"text", "number", "date", "choice"}, Values: map[string]string{}}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "entity.field.list", entities.FieldListIn{ID: c.ID})
	if err != nil {
		return d, err
	}
	by := map[string][]entities.FieldDef{}
	for _, f := range out.(entities.FieldListOut).Items {
		by[f.Kind] = append(by[f.Kind], f)
	}
	for _, k := range entityKinds {
		d.Groups = append(d.Groups, fieldKindGroup{Kind: k.ID, Plural: k.Plural, Singular: k.Singular, Defs: by[k.ID]})
	}
	return d, nil
}

var fieldsSaved = map[string]string{
	"set":    "The field is saved.",
	"remove": "The field is removed.",
}

func (s *Server) fieldsPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	d, err := s.fieldsView(r, c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = fieldsSaved[r.URL.Query().Get("saved")]
	s.render(w, r, http.StatusOK, "entity-fields", s.page(r, "Fields of "+c.Name, d))
}

func (s *Server) uiFields(w http.ResponseWriter, r *http.Request) {
	c, ok := s.entCompByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	action := r.PathValue("action")
	kind, key := r.PostFormValue("kind"), strings.TrimSpace(r.PostFormValue("key"))
	var err error
	scope := "new"
	if action == "set" && formBool(r, "existing") {
		scope = kind + ":" + key
	}
	switch action {
	case "set":
		var choices []string
		for _, l := range strings.Split(strings.ReplaceAll(r.PostFormValue("choices"), "\r", ""), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				choices = append(choices, l)
			}
		}
		in := entities.FieldSetIn{ID: c.ID, Kind: kind, Key: key, Label: strings.TrimSpace(r.PostFormValue("label")), Type: r.PostFormValue("type"), Choices: choices}
		_, err = s.Reg.Call(r.Context(), s.principal(r), "entity.field.set", in)
	case "remove":
		scope = kind + ":" + key
		_, err = s.Reg.Call(r.Context(), s.principal(r), "entity.field.remove", entities.FieldRemoveIn{ID: c.ID, Kind: kind, Key: key, DropValues: formBool(r, "drop_values")})
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	if err == nil {
		http.Redirect(w, r, "/c/"+c.Slug+"/fields?saved="+action+"#"+kind, http.StatusSeeOther)
		return
	}
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	d, verr := s.fieldsView(r, c)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	d.Error, d.Scope = e.Message, scope
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			d.Values[k] = v[0]
		}
	}
	s.render(w, r, http.StatusUnprocessableEntity, "entity-fields", s.page(r, "Fields of "+c.Name, d))
}

// ---- export -------------------------------------------------------------------------------------------------

// uiCompendiumExport is POST /c/{slug}/export: compendium.export, answered with the zip itself. The bundle is
// deterministic and recorded; the headers say what it held.
func (s *Server) uiCompendiumExport(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.export", entities.ExportIn{ID: c.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f := out.(entities.ExportFile)
	w.Header().Set("X-Export-Id", f.ExportID)
	w.Header().Set("X-Export-Entities", strconv.Itoa(f.Entities))
	w.Header().Set("X-Export-Sha256", f.SHA256)
	writeDownload(w, f)
}

// registerEntities adds the entity bible's pages and the routes behind their buttons.
func (s *Server) registerEntities(m *http.ServeMux) {
	m.HandleFunc("GET /c/{slug}/entities", s.entitiesPage)
	m.HandleFunc("GET /c/{slug}/e/{id}", s.entitySheetPage)
	m.HandleFunc("GET /c/{slug}/e/{id}/merge", s.entityMergePage)
	m.HandleFunc("GET /c/{slug}/fields", s.fieldsPage)
	s.uiRoute("POST /c/{slug}/export", s.uiCompendiumExport)
	s.uiRoute("POST /ui/compendiums/{id}/entities", s.uiEntityCreate)
	s.uiRoute("POST /ui/compendiums/{id}/fields/{action}", s.uiFields)
	s.uiRoute("POST /ui/entities/{id}/merge", s.uiEntityMerge)
	s.uiRoute("POST /ui/entities/{id}/{action}", s.uiEntity)
	s.uiRoute("POST /ui/md-preview", s.uiMarkdownPreview)
	s.registerEntityProject(m)
}
