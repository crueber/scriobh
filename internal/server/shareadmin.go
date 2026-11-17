package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/share"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// The owner's page for share links, /p/{slug}/share: make a read-only or comment-only link (the whole book or chosen
// chapters, a passphrase, an expiry), see how each has been used, change or revoke it. The link itself is shown once,
// on the answer to the form that made it, and nowhere again: the list holds no token and the server keeps only its
// hash. All of it goes through share.create, list, update, usage and revoke, which are the owner's, browser session only.

func (s *Server) registerShareAdmin(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/share", s.sharePage)
	s.uiRoute("POST /ui/projects/{id}/share/{action}", s.uiShare)
}

type shareDocOpt struct {
	ID, Title string
	Depth     int
	Checked   bool
}

type shareLinkView struct {
	share.Out
	Usage                                *share.UsageOut
	What, ExpiresText, LastUsed, Created string
	ExpiresDate                          string
	Active                               bool
}

type shareOnce struct{ Path, Label string }

type shareAdminData struct {
	Project         projects.Project
	Slug, ProjectID string
	CSRF            string
	Links           []shareLinkView
	Docs            []shareDocOpt
	Once            *shareOnce
	Flash, Error    string
	Form            map[string]string
	Chosen          map[string]bool
	Today           string
}

func (s *Server) shareProject(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return proj, false
	}
	if proj.Role != ops.ProjectOwner {
		s.fail(w, r, ops.Forbidden("Only the owner of a project makes and manages its share links."))
		return proj, false
	}
	return proj, true
}

func (s *Server) shareAdminView(r *http.Request, proj projects.Project) (shareAdminData, error) {
	ctx, p := r.Context(), s.principal(r)
	lang := s.page(r, "", nil).Lang
	d := shareAdminData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: authFrom(ctx).CSRF, Form: map[string]string{"scope": "read", "contents": "whole", "ghosts": "omit"}, Chosen: map[string]bool{}, Today: time.Now().UTC().Format("2006-01-02")}
	lo, err := s.Reg.Call(ctx, p, "share.list", share.ListIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	titles := map[string]string{}
	dl, err := s.Reg.Call(ctx, p, "doc.list", docs.ListIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	list := dl.(docs.ListOut)
	byID := map[string]docs.Meta{}
	for _, m := range list.Items {
		byID[m.ID] = m
	}
	depth, skip := map[string]int{}, map[string]bool{}
	for _, id := range list.Order {
		m, ok := byID[id]
		if !ok {
			continue
		}
		if m.Kind == "note" || skip[m.ParentID] {
			skip[id] = true
			continue
		}
		depth[id] = depth[m.ParentID]
		if m.ParentID != "" {
			depth[id]++
		}
		t := strings.TrimSpace(m.Title)
		if t == "" {
			t = i18n.Default.T(lang, "shl.untitled")
		}
		titles[id] = t
		d.Docs = append(d.Docs, shareDocOpt{ID: id, Title: t, Depth: depth[id]})
	}
	for _, l := range lo.(share.ListOut).Items {
		v := shareLinkView{Out: l, Active: l.Status == "active", Created: utcWhen(l.CreatedAt), LastUsed: utcWhen(l.LastUsedAt)}
		switch {
		case l.Whole:
			v.What = i18n.Default.T(lang, "shl.what_whole")
		case len(l.DocumentIDs) == 1:
			v.What = i18n.Default.T(lang, "shl.what_one", "title", titles[l.DocumentIDs[0]])
		default:
			v.What = i18n.Default.T(lang, "shl.what_many", "n", commas(len(l.DocumentIDs)))
		}
		if l.ExpiresAt != "" {
			v.ExpiresText = utcWhen(l.ExpiresAt)
			if t, err := time.Parse(time.RFC3339Nano, l.ExpiresAt); err == nil {
				v.ExpiresDate = t.UTC().Format("2006-01-02")
			}
		}
		if uo, err := s.Reg.Call(ctx, p, "share.usage", share.RefIn{ProjectID: proj.ID, ShareID: l.ID}); err == nil {
			u := uo.(share.UsageOut)
			v.Usage = &u
		}
		d.Links = append(d.Links, v)
	}
	return d, nil
}

func (s *Server) sharePage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.shareProject(w, r)
	if !ok {
		return
	}
	d, err := s.shareAdminView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error = fl["msg"], fl["err"]
		if fl["once_path"] != "" {
			d.Once = &shareOnce{Path: fl["once_path"], Label: fl["once_label"]}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "share-links", s.page(r, "Share links of "+proj.Title, d))
}

func endOfDay(date string) (string, bool) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(date))
	if err != nil {
		return "", false
	}
	return t.Add(24*time.Hour - time.Second).UTC().Format(time.RFC3339), true
}

func (s *Server) uiShare(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	back := func(msg, errMsg string) {
		s.flashSet(r, map[string]string{"msg": msg, "err": errMsg})
		http.Redirect(w, r, "/p/"+proj.Slug+"/share", http.StatusSeeOther)
	}
	switch r.PathValue("action") {
	case "create":
		in := share.CreateIn{ProjectID: proj.ID, Label: strings.TrimSpace(r.PostFormValue("label")), Scope: r.PostFormValue("scope"), Passphrase: r.PostFormValue("passphrase"),
			AllowDownload: r.PostFormValue("download") == "on", Ghosts: r.PostFormValue("ghosts")}
		if r.PostFormValue("contents") == "chosen" {
			in.DocumentIDs = r.PostForm["doc"]
		}
		if v := strings.TrimSpace(r.PostFormValue("expires")); v != "" {
			e, ok := endOfDay(v)
			if !ok {
				s.shareFail(w, r, proj, ops.Invalid("expires", "Write the expiry as a date."))
				return
			}
			in.ExpiresAt = e
		}
		out, err := s.Reg.Call(ctx, p, "share.create", in)
		if err != nil {
			s.shareFail(w, r, proj, err)
			return
		}
		res := out.(share.CreateOut)
		if authFrom(ctx).SessionID != "" { // Post/Redirect/Get: a reload of the page asks for nothing and makes no second link
			s.flashSet(r, map[string]string{"once_path": res.Path, "once_label": res.Item.Label})
			http.Redirect(w, r, "/p/"+proj.Slug+"/share", http.StatusSeeOther)
			return
		}
		d, err := s.shareAdminView(r, proj)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Once = &shareOnce{Path: res.Path, Label: res.Item.Label}
		w.Header().Set("Cache-Control", "no-store")
		s.render(w, r, http.StatusOK, "share-links", s.page(r, "Share links of "+proj.Title, d))
	case "update":
		id := r.PostFormValue("share_id")
		label, scope, ghosts := strings.TrimSpace(r.PostFormValue("label")), r.PostFormValue("scope"), r.PostFormValue("ghosts")
		dl := r.PostFormValue("download") == "on"
		in := share.UpdateIn{ProjectID: proj.ID, ShareID: id, Label: &label, Scope: &scope, Ghosts: &ghosts, AllowDownload: &dl}
		switch {
		case r.PostFormValue("remove_passphrase") == "on":
			e := ""
			in.Passphrase = &e
		case r.PostFormValue("passphrase") != "":
			pp := r.PostFormValue("passphrase")
			in.Passphrase = &pp
		}
		switch v := strings.TrimSpace(r.PostFormValue("expires")); {
		case r.PostFormValue("clear_expiry") == "on" || v == "":
			e := ""
			in.ExpiresAt = &e
		default:
			e, ok := endOfDay(v)
			if !ok {
				back("", "Write the expiry as a date.")
				return
			}
			in.ExpiresAt = &e
		}
		if _, err := s.Reg.Call(ctx, p, "share.update", in); err != nil {
			s.shareFail(w, r, proj, err)
			return
		}
		back("The link was changed.", "")
	case "revoke":
		if _, err := s.Reg.Call(ctx, p, "share.revoke", share.RefIn{ProjectID: proj.ID, ShareID: r.PostFormValue("share_id")}); err != nil {
			s.shareFail(w, r, proj, err)
			return
		}
		back("The link was revoked. Anyone who has it now sees that it is not available.", "")
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

// shareFail shows a refusal a person can act on on the page, with what they typed (never the passphrase).
func (s *Server) shareFail(w http.ResponseWriter, r *http.Request, proj projects.Project, err error) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict && e.Code != ops.CodeForbidden {
		s.fail(w, r, err)
		return
	}
	d, derr := s.shareAdminView(r, proj)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	for _, k := range []string{"label", "scope", "contents", "expires", "ghosts"} {
		if v := r.PostFormValue(k); v != "" {
			d.Form[k] = v
		}
	}
	d.Form["download"] = r.PostFormValue("download")
	for _, id := range r.PostForm["doc"] {
		d.Chosen[id] = true
	}
	for i := range d.Docs {
		d.Docs[i].Checked = d.Chosen[d.Docs[i].ID]
	}
	d.Error = e.Message
	s.render(w, r, http.StatusUnprocessableEntity, "share-links", s.page(r, "Share links of "+proj.Title, d))
}
