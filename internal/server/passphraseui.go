package server

import (
	"net/http"
	"strconv"

	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The owner's page for the project passphrase, /p/{slug}/passphrase (F-252): what a passphrase does and does not
// protect, set, change and remove it, the idle limit, and which API keys may reach the project while it is set. It is one
// of the few pages a locked member may open (gateExemptPage), so it never calls project.get, which is gated: it
// reads the project's title, the person's role and project.passphrase.get, and shows nothing else of the project. Every
// change goes through project.passphrase.set or .clear. No passphrase is ever put back into a page: after an error the
// password boxes come back empty.

func (s *Server) registerPassphrase(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/passphrase", s.passphrasePage)
	s.uiRoute("POST /ui/projects/{id}/passphrase/{action}", s.uiPassphrase)
}

type passKeyRow struct {
	projectgate.KeyUse
	First, Last string
}

type passData struct {
	ID, Slug, Title, CSRF, Role string
	Status                      projectgate.Status
	IsOwner                     bool
	AdminCanClear               bool // an administrator who is a member with another role: the way back for a forgotten passphrase
	Keys                        []passKeyRow
	Was                         []string // the keys approved when the page was drawn
	Idle                        int
	Saved, Error, Field         string
	Form                        string // which form the error belongs to
}

// passView reads what the page shows.
func (s *Server) passView(r *http.Request, id string) (passData, error) {
	p, ctx := s.principal(r), r.Context()
	d := passData{ID: id, CSRF: authFrom(ctx).CSRF, Idle: projectgate.DefaultIdleMinutes}
	out, err := s.Reg.Call(ctx, p, "project.passphrase.get", projectgate.ProjectIn{ID: id})
	if err != nil {
		return d, err
	}
	d.Status = out.(projectgate.StatusOut).Item
	// The role and the title come from the list, which gives a locked project as its title and the person's own state only.
	lo, err := s.Reg.Call(ctx, p, "project.list", projects.ListIn{State: projects.StateAll})
	if err != nil {
		return d, err
	}
	for _, pr := range lo.(projects.ListOut).Items {
		if pr.ID == id {
			d.Title, d.Role, d.Slug = pr.Title, pr.Role, pr.Addressed().Slug
		}
	}
	d.IsOwner = d.Role == ops.ProjectOwner
	d.AdminCanClear = p.Role == ops.RoleAdmin && !d.IsOwner && d.Role != "" && d.Status.Protected
	if d.Status.IdleMinutes > 0 {
		d.Idle = d.Status.IdleMinutes
	}
	for _, k := range d.Status.Keys {
		d.Keys = append(d.Keys, passKeyRow{KeyUse: k, First: utcWhen(k.FirstAt), Last: utcWhen(k.LastAt)})
		if k.Approved {
			d.Was = append(d.Was, k.KeyID)
		}
	}
	return d, nil
}

func (s *Server) passphrasePage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	id, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, r.PathValue("slug"))
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return
	}
	d, err := s.passView(r, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = r.URL.Query().Get("saved")
	s.render(w, r, http.StatusOK, "passphrase", s.page(r, s.tr(r, "pass.page_title"), d))
}

// passFail draws the page again with one message on one field. Nothing the person typed is sent back except the idle
// limit, which is not a secret.
func (s *Server) passFail(w http.ResponseWriter, r *http.Request, id, form, field, msg string, status int) {
	d, err := s.passView(r, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Error, d.Field, d.Form = msg, field, form
	if n, err := strconv.Atoi(r.PostFormValue("idle_minutes")); err == nil && n > 0 {
		d.Idle = n
	}
	s.render(w, r, status, "passphrase", s.page(r, s.tr(r, "pass.page_title"), d))
}

// uiPassphrase applies one of the page's forms.
func (s *Server) uiPassphrase(w http.ResponseWriter, r *http.Request) {
	p, ctx, id := s.principal(r), r.Context(), r.PathValue("id")
	action := r.PostFormValue
	form := r.PathValue("action")
	idle, _ := strconv.Atoi(action("idle_minutes"))
	var err error
	saved := ""
	switch form {
	case "set":
		if action("passphrase") != action("repeat") {
			s.passFail(w, r, id, form, "repeat", s.tr(r, "pass.err_repeat"), http.StatusUnprocessableEntity)
			return
		}
		_, err = s.Reg.Call(ctx, p, "project.passphrase.set", projectgate.SetIn{ID: id, Passphrase: action("passphrase"), Current: action("current"), IdleMinutes: idle})
		saved = "set"
	case "options":
		in := projectgate.SetIn{ID: id, Current: action("current"), IdleMinutes: idle}
		allow := action("allow_keys") == "true"
		in.AllowKeys = &allow
		was, now := map[string]bool{}, map[string]bool{}
		for _, k := range r.PostForm["was"] {
			was[k] = true
		}
		for _, k := range r.PostForm["approved"] {
			now[k] = true
			if !was[k] {
				in.ApproveKeys = append(in.ApproveKeys, k)
			}
		}
		for k := range was {
			if !now[k] {
				in.UnapproveKeys = append(in.UnapproveKeys, k)
			}
		}
		_, err = s.Reg.Call(ctx, p, "project.passphrase.set", in)
		saved = "options"
	case "clear":
		_, err = s.Reg.Call(ctx, p, "project.passphrase.clear", projectgate.ClearIn{ID: id, Current: action("current")})
		saved = "cleared"
	case "admin-clear":
		_, err = s.Reg.Call(ctx, p, "project.passphrase.clear", projectgate.ClearIn{ID: id, ConfirmName: action("confirm_name")})
		saved = "cleared"
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	if err != nil {
		e := ops.AsError(err)
		field := e.Field
		msg := e.Message
		switch {
		case e.Code == ops.CodeRateLimited:
			field, msg = "current", s.gateWait(r, e.RetryAfterSeconds)
		case e.Code == ops.CodeConflict && e.Data["reason"] == "passphrase_changed":
			field, msg = "current", s.tr(r, "project.gate.changed")
		case e.Code == ops.CodeInvalid && e.Message == "That passphrase is not right.": // the op's one answer to a wrong passphrase, whichever form carried it
			field, msg = "current", s.tr(r, "project.gate.wrong")
		case e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict && e.Code != ops.CodeForbidden:
			s.fail(w, r, err)
			return
		}
		status := e.Code.HTTPStatus()
		if e.Code == ops.CodeRateLimited && e.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
		}
		s.passFail(w, r, id, form, field, msg, status)
		return
	}
	slug := s.lookup(ctx, `SELECT slug FROM projects WHERE id = ?`, id)
	http.Redirect(w, r, "/p/"+s.gateAddress(r, id, slug)+"/passphrase?saved="+saved, http.StatusSeeOther)
}

// gateWait says how long to wait, in words a person can use: seconds under a minute and a half, minutes after.
func (s *Server) gateWait(r *http.Request, secs int) string {
	if secs <= 0 {
		return s.tr(r, "project.gate.wait")
	}
	if secs < 90 {
		return s.trN(r, "project.gate.wait_s", secs)
	}
	return s.trN(r, "project.gate.wait_m", (secs+59)/60)
}
