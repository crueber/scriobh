package server

import (
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// A project's two doors to other people's data, as pages: the Compendium it uses (/p/{slug}/compendium) and the
// people on it (/p/{slug}/people). Membership of a project and membership of its Compendium are separate, and
// these pages say so in the places where a person could otherwise assume the opposite.

var projCompSaved = map[string]string{
	"set":        "The primary Compendium is changed.",
	"cleared":    "The project no longer has a primary Compendium.",
	"created":    "The Compendium is made and is now this project's primary.",
	"ref-add":    "Referenced. The project now shows that Compendium's entities, read-only.",
	"ref-remove": "No longer referenced. Overrides the project made for its entities are kept, and apply again if it comes back.",
	"role":       "The role is changed.",
	"out":        "Removed from the project.",
}

// ---- the project's Compendium ----------------------------------------------------------------

type projCompData struct {
	Project   projects.Project
	Slug      string
	CSRF      string
	IsOwner   bool
	Wiring    compendiums.Wiring
	Slugs     map[string]string        // Compendium id to slug, for the links to the Compendiums the project uses
	Choices   []compendiums.Compendium // Compendiums the caller can read, for a reference
	Editable  []compendiums.Compendium // Compendiums the caller can edit, for a primary
	Preview   *compPreview
	SavedText string
	Error     string
	Field     string
	Values    map[string]string
}

type compPreview struct {
	TargetID, TargetName string
	Clear                bool
	Out                  compendiums.PrimaryPlan // what changes, and the fingerprint the change must carry (compendium.primary.preview)
	Stale                bool                    // the plan moved after the last preview: this is a fresh one
}

func (s *Server) projCompView(r *http.Request, proj projects.Project) (projCompData, error) {
	p, ctx := s.principal(r), r.Context()
	d := projCompData{Project: proj, Slug: proj.Slug, CSRF: authFrom(ctx).CSRF, IsOwner: s.gitOwner(r, proj), Values: map[string]string{}}
	w, err := s.Reg.Call(ctx, p, "project.compendium.get", compendiums.ProjectIn{ID: proj.ID})
	if err != nil {
		return d, err
	}
	d.Wiring = w.(compendiums.WiringItem).Item
	// Every Compendium the page names is one the caller can read, so the list holds its slug.
	if lo, err := s.Reg.Call(ctx, p, "compendium.list", compendiums.ListIn{}); err == nil {
		d.Slugs = map[string]string{}
		used := map[string]bool{}
		if d.Wiring.Primary != nil {
			used[d.Wiring.Primary.CompendiumID] = true
		}
		for _, ref := range d.Wiring.References {
			used[ref.CompendiumID] = true
		}
		addr := s.compAddresses(r)
		for _, c := range lo.(compendiums.ListOut).Items {
			d.Slugs[c.ID] = c.Slug
			if a, ok := addr[c.ID]; ok {
				d.Slugs[c.ID] = a
			}
			if !d.IsOwner || used[c.ID] {
				continue
			}
			d.Choices = append(d.Choices, c)
			if c.Role == "owner" || c.Role == "editor" {
				d.Editable = append(d.Editable, c)
			}
		}
	}
	return d, nil
}

// projectCompendiumOpen is where the project's Compendium tab goes: to the Compendium when the book has a primary one that the caller is on,
// and otherwise to the wiring page, which offers to make or attach one and says why when the Compendium is not shared with the caller. The
// address is made from what the server knows; nothing of the request is put in it.
func (s *Server) projectCompendiumOpen(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.compendium.get", compendiums.ProjectIn{ID: proj.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if pr := out.(compendiums.WiringItem).Item.Primary; pr != nil {
		if a := s.compAddresses(r)[pr.CompendiumID]; a != "" {
			http.Redirect(w, r, "/c/"+a+"/entities", http.StatusSeeOther) // the tab opens the entries, the first thing in a Compendium
			return
		}
	}
	http.Redirect(w, r, "/p/"+proj.Address+"/compendium", http.StatusSeeOther)
}

func (s *Server) projectCompendiumPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	d, err := s.projCompView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.SavedText = projCompSaved[r.URL.Query().Get("saved")]
	s.render(w, r, http.StatusOK, "project-compendium", s.page(r, "Compendium of "+proj.Title, d))
}

func (s *Server) projCompFail(w http.ResponseWriter, r *http.Request, proj projects.Project, msg, field string, prev *compPreview) {
	d, err := s.projCompView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Error, d.Field, d.Preview = msg, field, prev
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			d.Values[k] = v[0]
		}
	}
	status := http.StatusUnprocessableEntity
	if msg == "" {
		status = http.StatusOK
	}
	s.render(w, r, status, "project-compendium", s.page(r, "Compendium of "+proj.Title, d))
}

// projCompPreview asks compendium.primary.preview what changing the primary Compendium would do.
func (s *Server) projCompPreview(r *http.Request, proj projects.Project, target string, stale bool) (*compPreview, error) {
	p, ctx := s.principal(r), r.Context()
	out, err := s.Reg.Call(ctx, p, "compendium.primary.preview", compendiums.PrimaryIn{ID: proj.ID, CompendiumID: target})
	if err != nil {
		return nil, err
	}
	pv := &compPreview{TargetID: target, Clear: target == "", Out: out.(compendiums.PrimaryOut).Plan, Stale: stale}
	if target != "" {
		if c, cerr := s.Reg.Call(ctx, p, "compendium.get", compendiums.GetIn{ID: target}); cerr == nil {
			pv.TargetName = c.(compendiums.DetailItem).Item.Name
		}
	}
	return pv, nil
}

// uiProjectCompendium applies one action of the page.
func (s *Server) uiProjectCompendium(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if !s.gitOwner(r, proj) {
		s.fail(w, r, ops.Forbidden("Only the project's owner can change which Compendiums it uses."))
		return
	}
	p, ctx := s.principal(r), r.Context()
	action := r.PathValue("action")
	target := r.PostFormValue("compendium_id")
	var err error
	saved := ""
	switch action {
	case "preview", "clear-preview":
		if action == "clear-preview" {
			target = ""
		}
		if pv, perr := s.projCompPreview(r, proj, target, false); perr == nil {
			s.projCompFail(w, r, proj, "", "", pv)
			return
		} else {
			err = perr
		}
	case "set", "clear":
		if action == "clear" {
			target = ""
		}
		_, err = s.Reg.Call(ctx, p, "compendium.primary.set", compendiums.PrimaryIn{ID: proj.ID, CompendiumID: target, Fingerprint: r.PostFormValue("fingerprint")})
		saved = map[string]string{"set": "set", "clear": "cleared"}[action]
		if e := ops.AsError(err); err != nil && e.Code == ops.CodeConflict && e.Data["reason"] == "plan_changed" {
			// something it depends on moved since the preview was drawn: show the new one, and do nothing
			if pv, perr := s.projCompPreview(r, proj, target, true); perr == nil {
				s.projCompFail(w, r, proj, "", "", pv)
				return
			}
		}
	case "create":
		starter := r.PostFormValue("starter") == "1"
		_, err = s.Reg.Call(ctx, p, "project.compendium.create", compendiums.ProjectCreateIn{ID: proj.ID, Name: r.PostFormValue("name"), Kind: r.PostFormValue("kind"),
			Description: r.PostFormValue("description"), StarterFields: &starter})
		saved = "created"
	case "ref-add":
		_, err = s.Reg.Call(ctx, p, "project.compendium.ref.add", compendiums.RefAddIn{ID: proj.ID, CompendiumID: target})
		saved = "ref-add"
	case "ref-remove":
		_, err = s.Reg.Call(ctx, p, "project.compendium.ref.remove", compendiums.RefRemoveIn{ID: proj.ID, CompendiumID: target})
		saved = "ref-remove"
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	if err != nil {
		e := ops.AsError(err)
		switch e.Code {
		case ops.CodeInvalid, ops.CodeConflict, ops.CodeForbidden:
			s.projCompFail(w, r, proj, e.Message, e.Field, nil)
		default:
			s.fail(w, r, err)
		}
		return
	}
	http.Redirect(w, r, "/p/"+proj.Slug+"/compendium?saved="+saved, http.StatusSeeOther)
}

// ---- the people on a project --------------------------------------------------------------------------

type peopleData struct {
	Project     projects.Project
	Slug        string
	CSRF        string
	IsOwner     bool
	HasPrimary  bool
	Members     []projects.Member
	ShowMembers bool
	SavedText   string
	Result      string // what happened to Compendium access when someone was added
	Error       string
	Field       string
	Values      map[string]string
}

var grantWords = map[string]string{
	"not_requested":         "They were not added to the Compendium, because that was not asked for. The Compendium's owner can add them from its page.",
	"no_primary_compendium": "The project has no primary Compendium yet, so there was nothing to add them to.",
	"already_member":        "They already have access to the Compendium, and nothing about it was changed.",
	"not_compendium_owner":  "Only the Compendium's owner can give access to it, so they were added to the project only. Ask the owner to add them.",
	"restricted_key":        "A restricted key cannot give access to a Compendium, so they were added to the project only.",
}

func (s *Server) peopleView(r *http.Request, proj projects.Project) (peopleData, error) {
	p, ctx := s.principal(r), r.Context()
	d := peopleData{Project: proj, Slug: proj.Slug, CSRF: authFrom(ctx).CSRF, IsOwner: s.gitOwner(r, proj), Values: map[string]string{}}
	if w, err := s.Reg.Call(ctx, p, "project.compendium.get", compendiums.ProjectIn{ID: proj.ID}); err == nil {
		d.HasPrimary = w.(compendiums.WiringItem).Item.HasPrimary
	}
	if d.IsOwner {
		mo, err := s.Reg.Call(ctx, p, "project.member.list", projects.MemberListIn{ID: proj.ID})
		if err != nil {
			return d, err
		}
		d.Members, d.ShowMembers = mo.(projects.MemberListOut).Items, true
	}
	return d, nil
}

func (s *Server) peoplePage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	d, err := s.peopleView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	d.SavedText = projCompSaved[q.Get("saved")]
	if k := q.Get("saved"); k == "role" || k == "out" {
		d.SavedText = s.tr(r, "ppl.s_"+k)
	}
	// What the last action wants to say (a name, what was granted) comes from the server's one-shot flash, never from
	// the address: the query only says which line to look for.
	fl := s.flashTake(r)
	if q.Get("saved") == "added" {
		name := fl["name"]
		if r := []rune(name); len(r) > 60 {
			name = string(r[:60])
		}
		if name == "" {
			name = s.tr(r, "ppl.the_person")
		}
		d.SavedText = s.tr(r, "ppl.s_added", "name", name)
		switch g := fl["grant"]; g {
		case "granted":
			d.Result = s.tr(r, "ppl.g_granted_"+map[bool]string{true: "reader", false: "editor"}[fl["role"] == "reader"])
		default:
			if g != "" {
				d.Result = s.tr(r, "ppl.g_"+g)
			}
		}
	}
	if q.Get("saved") == "out" {
		if cr := fl["role"]; cr != "" {
			d.Result = s.tr(r, "ppl.out_"+map[string]string{"owner": "owner", "editor": "editor", "reader": "reader"}[cr])
		}
	}
	s.render(w, r, http.StatusOK, "people", s.page(r, "People on "+proj.Title, d))
}

func (s *Server) peopleFail(w http.ResponseWriter, r *http.Request, proj projects.Project, msg, field string) {
	d, err := s.peopleView(r, proj)
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
	s.render(w, r, http.StatusUnprocessableEntity, "people", s.page(r, "People on "+proj.Title, d))
}

// noSuchPersonMessage: one sentence for every address that cannot be added, so the form is not a way to list who is
// on this server.
const noSuchPersonMessage = compNoSuchPerson

func (s *Server) uiPeople(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if !s.gitOwner(r, proj) {
		s.fail(w, r, ops.Forbidden(s.tr(r, "ppl.only_owner_change")))
		return
	}
	p, ctx := s.principal(r), r.Context()
	base := "/p/" + proj.Slug + "/people?saved="
	switch r.PathValue("action") {
	case "add":
		in := projects.MemberAddIn{ID: proj.ID, Email: r.PostFormValue("email"), Role: r.PostFormValue("role")}
		switch r.PostFormValue("compendium") { // "default" leaves it to the role: editors yes, reviewers and readers no
		case "yes":
			t := true
			in.AddToCompendium = &t
		case "no":
			f := false
			in.AddToCompendium = &f
		}
		out, err := s.Reg.Call(ctx, p, "project.member.add", in)
		if err != nil {
			e := ops.AsError(err)
			switch e.Code {
			case ops.CodeNotFound:
				s.peopleFail(w, r, proj, noSuchPersonMessage, "email")
			case ops.CodeInvalid, ops.CodeConflict, ops.CodeForbidden:
				s.peopleFail(w, r, proj, e.Message, e.Field)
			default:
				s.fail(w, r, err)
			}
			return
		}
		res := out.(projects.MemberAddOut)
		g := res.Compendium.Reason
		if res.Compendium.Granted {
			g = "granted"
		}
		s.flashSet(r, map[string]string{"name": res.Item.DisplayName, "grant": g, "role": res.Compendium.Role})
		http.Redirect(w, r, base+"added", http.StatusSeeOther)
		return
	case "role":
		if _, err := s.Reg.Call(ctx, p, "project.member.update", projects.MemberUpdateIn{ID: proj.ID, UserID: r.PostFormValue("user_id"), Role: r.PostFormValue("role")}); err != nil {
			s.peopleOrFail(w, r, proj, err)
			return
		}
		http.Redirect(w, r, base+"role", http.StatusSeeOther)
		return
	case "remove":
		out, err := s.Reg.Call(ctx, p, "project.member.remove", projects.MemberRemoveIn{ID: proj.ID, UserID: r.PostFormValue("user_id")})
		if err != nil {
			s.peopleOrFail(w, r, proj, err)
			return
		}
		s.flashSet(r, map[string]string{"role": strings.TrimSpace(out.(projects.MemberRemoveOut).CompendiumRole)})
		http.Redirect(w, r, base+"out", http.StatusSeeOther)
		return
	}
	s.peopleFail(w, r, proj, s.tr(r, "ppl.not_recognised"), "")
}

func (s *Server) peopleOrFail(w http.ResponseWriter, r *http.Request, proj projects.Project, err error) {
	e := ops.AsError(err)
	if e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict {
		s.peopleFail(w, r, proj, e.Message, e.Field)
		return
	}
	s.fail(w, r, err)
}

func (s *Server) registerProjectCompendium(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/compendium", s.projectCompendiumPage)
	m.HandleFunc("GET /p/{slug}/compendium/open", s.projectCompendiumOpen)
	s.uiRoute("POST /ui/projects/{id}/compendium/{action}", s.uiProjectCompendium)
	m.HandleFunc("GET /p/{slug}/people", s.peoplePage)
	s.uiRoute("POST /ui/projects/{id}/people/{action}", s.uiPeople)
}
