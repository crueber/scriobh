package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The Compendium screens that are about the Compendium itself: the library's list of them, the dialog that makes
// one, the overview page with its people and references, and the state changes (archive, trash, restore, delete
// for good). Everything is read and written through the compendium.* ops; the sentences are written here. Who may
// see a Compendium at all is the op's answer: a person who is not on it gets the same 404 as for one that does not
// exist, and nothing on these pages says which.

// ---- words -------------------------------------------------------------------------------------

var compRoleWords = map[string]string{
	"owner":  "You own this Compendium. You can change everything in it, decide who has access, archive it, and delete it.",
	"editor": "You can read everything in this Compendium and add or change its entities and research. You cannot share it, archive it, or delete it.",
	"reader": "You can read everything in this Compendium. You cannot change anything in it.",
}

var compSaved = map[string]string{
	"created":     "The Compendium is made. Add entities, research and people from its tabs.",
	"updated":     "Saved.",
	"archived":    "Archived. It stays readable, and the projects that use it still do.",
	"unarchived":  "Brought back to the list.",
	"trashed":     "Moved to the trash. Projects stop showing its entities until you restore it.",
	"restored":    "Taken out of the trash.",
	"purged":      "Deleted for good.",
	"member-add":  "The person now has access.",
	"member-role": "The role is changed.",
	"member-out":  "The person no longer has access to this Compendium. What they wrote is kept, and their place on projects is not changed.",
	"ref-add":     "Referenced. Its entities are now readable here, and cannot be changed from here.",
	"ref-remove":  "No longer referenced.",
}

func flashText(m map[string]string, q url.Values) string { return m[q.Get("saved")] }

// compNoSuchPerson is the one answer for every address that cannot be added, whatever the reason.
const compNoSuchPerson = "No one with that address can be added. Check the address, and that the person has signed in to Scríobh before."

// ---- the library's Compendiums ----------------------------------------------------------------

type compendiumsData struct {
	Items     []compendiums.Compendium
	State     string // active, archived or trashed
	CSRF      string
	IsWriter  bool
	SavedText string
	Error     string
}

func compState(r *http.Request) string {
	switch v := r.URL.Query().Get("state"); v {
	case compendiums.StateArchived, compendiums.StateTrashed:
		return v
	}
	if v := r.FormValue("view"); v == compendiums.StateArchived || v == compendiums.StateTrashed {
		return v
	}
	return compendiums.StateActive
}

func (s *Server) compendiumsData(r *http.Request, state string) (compendiumsData, error) {
	p := s.principal(r)
	out, err := s.Reg.Call(r.Context(), p, "compendium.list", compendiums.ListIn{State: state})
	if err != nil {
		return compendiumsData{}, err
	}
	return compendiumsData{Items: s.addressedAll(r, out.(compendiums.ListOut).Items), State: state, CSRF: authFrom(r.Context()).CSRF,
		IsWriter: p.Role == ops.RoleAdmin || p.Role == ops.RoleWriter}, nil
}

func (s *Server) compendiumsPage(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	d, err := s.compendiumsData(r, compState(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.SavedText = flashText(compSaved, r.URL.Query())
	s.render(w, r, http.StatusOK, "compendiums", s.page(r, "Compendiums", d))
}

// ---- making one -------------------------------------------------------------------------------

type compNewData struct {
	CSRF     string
	Values   map[string]string
	Errors   map[string]string
	Projects []projects.Project // projects the caller owns that have no primary Compendium yet
	Status   int
}

func (s *Server) compNewData(r *http.Request) compNewData {
	d := compNewData{CSRF: authFrom(r.Context()).CSRF, Values: map[string]string{"kind": "fiction", "starter": "1"}, Errors: map[string]string{}}
	p := s.principal(r)
	out, err := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{})
	if err != nil {
		return d
	}
	n := 0
	for _, pr := range addressedProjects(out.(projects.ListOut).Items) {
		if n >= 50 || (pr.Role != ops.ProjectOwner && p.Role != ops.RoleAdmin) {
			continue
		}
		w, err := s.Reg.Call(r.Context(), p, "project.compendium.get", compendiums.ProjectIn{ID: pr.ID})
		if err != nil || w.(compendiums.WiringItem).Item.HasPrimary {
			continue
		}
		d.Projects = append(d.Projects, pr)
		n++
	}
	return d
}

func (s *Server) compNewForm(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	d := s.compNewData(r)
	if v := r.URL.Query().Get("project"); v != "" {
		d.Values["project_id"] = v
	}
	if isHTMX(r) {
		s.fragment(w, r, http.StatusOK, "compendium-forms", "compendium-new", d)
		return
	}
	s.render(w, r, http.StatusOK, "compendium-new", s.page(r, "New Compendium", d))
}

func (s *Server) uiCompendiumCreate(w http.ResponseWriter, r *http.Request) {
	in := compendiums.CreateIn{Name: r.PostFormValue("name"), Kind: r.PostFormValue("kind"), Description: r.PostFormValue("description"), ProjectID: r.PostFormValue("project_id")}
	starter := r.PostFormValue("starter") == "1"
	in.StarterFields = &starter
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.create", in)
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
			s.fail(w, r, err)
			return
		}
		d := s.compNewData(r)
		d.Values = map[string]string{"name": r.PostFormValue("name"), "kind": r.PostFormValue("kind"), "description": r.PostFormValue("description"), "project_id": r.PostFormValue("project_id")}
		if starter {
			d.Values["starter"] = "1"
		}
		field := e.Field
		if field == "" {
			field = "form"
		}
		d.Errors[field] = e.Message
		if isHTMX(r) {
			s.fragment(w, r, http.StatusUnprocessableEntity, "compendium-forms", "compendium-new", d)
			return
		}
		s.render(w, r, http.StatusUnprocessableEntity, "compendium-new", s.page(r, "New Compendium", d))
		return
	}
	s.cRedirect(w, r, "/c/"+out.(compendiums.Item).Item.Slug+"?saved=created")
}

// cRedirect sends the whole page on: a form in the dialog answers with HX-Redirect so the page is not drawn
// inside the dialog.
func (s *Server) cRedirect(w http.ResponseWriter, r *http.Request, to string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// ---- the overview ------------------------------------------------------------------------------

type compOverview struct {
	Compendium compendiums.Compendium
	Detail     compendiums.Detail
	Slug       string
	CSRF       string
	IsOwner    bool
	CanEdit    bool
	RoleWords  string
	Members    []compendiums.Member
	ShowPeople bool // the member list is the owner's to see
	RefChoices []compendiums.Compendium
	SavedText  string
	Error      string
	Field      string
	Values     map[string]string
}

func (s *Server) compOverviewData(r *http.Request, c compendiums.Compendium) (compOverview, error) {
	p := s.principal(r)
	ctx := r.Context()
	out, err := s.Reg.Call(ctx, p, "compendium.get", compendiums.GetIn{ID: c.ID})
	if err != nil {
		return compOverview{}, err
	}
	det := out.(compendiums.DetailItem).Item
	det.Compendium = s.addressed(r, det.Compendium)
	d := compOverview{Compendium: det.Compendium, Detail: det, Slug: det.Slug, CSRF: authFrom(ctx).CSRF, Values: map[string]string{},
		IsOwner: det.Role == "owner", CanEdit: compEditable(det.Compendium), RoleWords: compRoleWords[det.Role]}
	if d.IsOwner {
		mo, err := s.Reg.Call(ctx, p, "compendium.member.list", compendiums.MemberListIn{ID: c.ID})
		if err == nil {
			d.Members, d.ShowPeople = mo.(compendiums.MemberListOut).Items, true
		}
		lo, err := s.Reg.Call(ctx, p, "compendium.list", compendiums.ListIn{})
		if err == nil {
			have := map[string]bool{c.ID: true}
			for _, ref := range det.References {
				have[ref.CompendiumID] = true
			}
			for _, o := range lo.(compendiums.ListOut).Items {
				if !have[o.ID] {
					d.RefChoices = append(d.RefChoices, o)
				}
			}
		}
	}
	return d, nil
}

func (s *Server) compendiumPage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.compendiumBySlug(w, r)
	if !ok {
		return
	}
	d, err := s.compOverviewData(r, c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.SavedText = flashText(compSaved, r.URL.Query())
	s.render(w, r, http.StatusOK, "compendium", s.page(r, c.Name, d))
}

func (s *Server) compOverviewFail(w http.ResponseWriter, r *http.Request, c compendiums.Compendium, msg, field string) {
	d, err := s.compOverviewData(r, c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Error, d.Field = msg, field
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			d.Values[k] = v[0]
		}
	}
	s.render(w, r, http.StatusUnprocessableEntity, "compendium", s.page(r, c.Name, d))
}

// uiCompendium applies one action of the overview page or of a card. The path names it.
func (s *Server) uiCompendium(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, ok := s.compendiumByID(w, r, id)
	if !ok {
		return
	}
	p, ctx := s.principal(r), r.Context()
	action := r.PathValue("action")
	back := "/c/" + c.Slug
	var err error
	saved := action
	switch action {
	case "archive", "unarchive", "trash", "restore":
		_, err = s.Reg.Call(ctx, p, "compendium."+action, compendiums.StateIn{ID: id})
		saved = map[string]string{"archive": "archived", "unarchive": "unarchived", "trash": "trashed", "restore": "restored"}[action]
		if v := r.PostFormValue("view"); v != "" {
			back = "/compendiums"
			if v != compendiums.StateActive {
				back += "?state=" + url.QueryEscape(v)
			}
		}
	case "update":
		name, kind, desc := r.PostFormValue("name"), r.PostFormValue("kind"), r.PostFormValue("description")
		_, err = s.Reg.Call(ctx, p, "compendium.update", compendiums.UpdateIn{ID: id, Name: &name, Kind: &kind, Description: &desc})
		saved = "updated"
		if err == nil {
			if o, gerr := s.Reg.Call(ctx, p, "compendium.get", compendiums.GetIn{ID: id}); gerr == nil { // the address follows a name only if the slug did
				back = "/c/" + s.addressed(r, o.(compendiums.DetailItem).Item.Compendium).Slug
			}
		}
	case "member-add":
		_, err = s.Reg.Call(ctx, p, "compendium.member.add", compendiums.MemberAddIn{ID: id, Email: r.PostFormValue("email"), Role: r.PostFormValue("role")})
		if e := ops.AsError(err); err != nil && e.Code == ops.CodeNotFound {
			s.compOverviewFail(w, r, c, compNoSuchPerson, "email")
			return
		}
		saved = "member-add"
	case "member-role":
		_, err = s.Reg.Call(ctx, p, "compendium.member.update", compendiums.MemberUpdateIn{ID: id, UserID: r.PostFormValue("user_id"), Role: r.PostFormValue("role")})
		saved = "member-role"
	case "member-remove":
		_, err = s.Reg.Call(ctx, p, "compendium.member.remove", compendiums.MemberRemoveIn{ID: id, UserID: r.PostFormValue("user_id")})
		saved = "member-out"
	case "ref-add":
		_, err = s.Reg.Call(ctx, p, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: id, TargetID: r.PostFormValue("target_id")})
		saved = "ref-add"
	case "ref-remove":
		_, err = s.Reg.Call(ctx, p, "compendium.ref.remove", compendiums.CompendiumRefRemoveIn{ID: id, TargetID: r.PostFormValue("target_id")})
		saved = "ref-remove"
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	if err != nil {
		e := ops.AsError(err)
		switch e.Code {
		case ops.CodeInvalid, ops.CodeConflict:
			s.compOverviewFail(w, r, c, e.Message, e.Field)
		default:
			s.fail(w, r, err)
		}
		return
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	s.cRedirect(w, r, back+sep+"saved="+saved)
}

// ---- delete for good -------------------------------------------------------------------------------

type compPurgeData struct {
	C      compendiums.Compendium
	Detail compendiums.Detail
	CSRF   string
	Error  string
	Typed  string
}

func (s *Server) compPurgeData(r *http.Request, id string) (compPurgeData, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "compendium.get", compendiums.GetIn{ID: id})
	if err != nil {
		return compPurgeData{}, err
	}
	det := out.(compendiums.DetailItem).Item
	det.Compendium = s.addressed(r, det.Compendium)
	return compPurgeData{C: det.Compendium, Detail: det, CSRF: authFrom(r.Context()).CSRF}, nil
}

func (s *Server) compPurgeForm(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	d, err := s.compPurgeData(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.C.Role != "owner" {
		s.fail(w, r, ops.Forbidden("Only the owner can delete a Compendium."))
		return
	}
	if isHTMX(r) {
		s.fragment(w, r, http.StatusOK, "compendium-forms", "compendium-purge", d)
		return
	}
	s.render(w, r, http.StatusOK, "compendium-purge", s.page(r, "Delete "+d.C.Name, d))
}

func (s *Server) uiCompendiumPurge(w http.ResponseWriter, r *http.Request) {
	d, err := s.compPurgeData(r, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Every project that uses it is named in the request exactly as the page listed it; the op refuses a list that
	// no longer matches, and nothing is deleted then.
	var ids []string
	for _, u := range d.Detail.Projects {
		ids = append(ids, u.ID)
	}
	_, err = s.Reg.Call(r.Context(), s.principal(r), "compendium.purge", compendiums.PurgeIn{ID: d.C.ID, ConfirmName: r.PostFormValue("confirm_name"), DetachProjects: ids, HiddenProjects: d.Detail.HiddenProjects})
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
			s.fail(w, r, err)
			return
		}
		d.Error, d.Typed = e.Message, r.PostFormValue("confirm_name")
		if isHTMX(r) {
			s.fragment(w, r, http.StatusUnprocessableEntity, "compendium-forms", "compendium-purge", d)
			return
		}
		s.render(w, r, http.StatusUnprocessableEntity, "compendium-purge", s.page(r, "Delete "+d.C.Name, d))
		return
	}
	s.cRedirect(w, r, "/compendiums?state=trashed&saved=purged")
}

// registerCompendiums adds the Compendium pages and the routes behind their buttons.
func (s *Server) registerCompendiums(m *http.ServeMux) {
	m.HandleFunc("GET /compendiums", s.compendiumsPage)
	m.HandleFunc("GET /ui/compendiums/new", s.compNewForm)
	s.uiRoute("POST /ui/compendiums", s.uiCompendiumCreate)
	m.HandleFunc("GET /c/{slug}", s.compendiumPage)
	m.HandleFunc("GET /ui/compendiums/{id}/purge", s.compPurgeForm)
	s.uiRoute("POST /ui/compendiums/{id}/purge", s.uiCompendiumPurge)
	s.uiRoute("POST /ui/compendiums/{id}/{action}", s.uiCompendium)
	s.registerProjectCompendium(m)
	s.registerCompendiumManage(m)
}
