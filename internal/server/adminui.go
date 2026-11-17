package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/admin"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// The instance administrators' console: /admin/users, /admin/quotas, /admin/diagnostics. Every page and form is an
// instance administrator's and answers anyone else with the 404 of a page that is not there. All of it goes through
// the admin.* ops (and backup.* by a link to Settings, where that screen already is).

func (s *Server) registerAdminUI(m *http.ServeMux) {
	m.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.adminOnly(w, r); ok {
			http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		}
	})
	m.HandleFunc("GET /admin/users", s.adminUsersPage)
	m.HandleFunc("GET /admin/users/{id}/disable", s.adminDisablePage)
	m.HandleFunc("GET /admin/quotas", s.adminQuotasPage)
	m.HandleFunc("GET /admin/diagnostics", s.adminDiagnosticsPage)
	m.HandleFunc("GET /admin/diagnostics/download", s.adminDiagnosticsDownload)
	s.uiRoute("POST /ui/admin/users/{action}", s.uiAdminUsers)
}

type adminRow struct {
	admin.UserOut
	Storage, Seen, Made string
}

type adminQuotaLine struct {
	Name, Used, Limit string
	Percent           int
	Over              bool
}

type adminQuotaUser struct {
	admin.UserQuota
	Storage, Limit string
	Percent        int
}

type adminDiag struct {
	Version, Go, OS, Arch string
	Applied, Known        int
	Pending               int
	Tables                int
	Queues                []admin.QueueState
	Disk                  []admin.DiskLine
	DiskTotal             string
	LogLines              int
}

type adminData struct {
	Tab, CSRF        string
	Q, Status        string
	Rows             []adminRow
	Total, Active    int
	Disabled, Admins int
	FromIdP          bool
	Flash, Error     string
	Page, Pages      int
	PrevURL, NextURL string
	Confirm          *adminRow
	Lines            []adminQuotaLine
	QuotaUsers       []adminQuotaUser
	Note             string
	Diag             *adminDiag
}

func (s *Server) adminOnly(w http.ResponseWriter, r *http.Request) (ops.Principal, bool) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return p, false
	}
	if p.Role != ops.RoleAdmin {
		s.fail(w, r, ops.NotFound("page"))
		return p, false
	}
	return p, true
}

func admPct(used, limit int64) int {
	if limit <= 0 {
		return 0
	}
	n := int(used * 100 / limit)
	if n > 100 {
		n = 100
	}
	return n
}

func (s *Server) adminBase(r *http.Request, tab string) adminData {
	d := adminData{Tab: tab, CSRF: authFrom(r.Context()).CSRF}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error = fl["msg"], fl["err"]
	}
	return d
}

func (s *Server) adminRender(w http.ResponseWriter, r *http.Request, status int, d adminData, title string) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, "admin", s.page(r, title, d))
}

const adminPageSize = 50

func (s *Server) adminUsers(r *http.Request, p ops.Principal, q, status string, offset int) (admin.ListOut, []adminRow, error) {
	out, err := s.Reg.Call(r.Context(), p, "admin.user.list", admin.ListIn{Q: q, Status: status, Limit: adminPageSize, Offset: offset})
	if err != nil {
		return admin.ListOut{}, nil, err
	}
	l := out.(admin.ListOut)
	rows := make([]adminRow, 0, len(l.Items))
	for _, u := range l.Items {
		rows = append(rows, adminRow{UserOut: u, Storage: humanBytes(u.StorageBytes), Seen: utcWhen(u.LastSeenAt), Made: utcWhen(u.CreatedAt)})
	}
	return l, rows, nil
}

func (s *Server) adminUsersPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	q, status := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("status")
	if status != "active" && status != "disabled" {
		status = ""
	}
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if pg < 1 {
		pg = 1
	}
	l, rows, err := s.adminUsers(r, p, q, status, (pg-1)*adminPageSize)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := s.adminBase(r, "users")
	d.Q, d.Status, d.Rows, d.Total, d.Active, d.Disabled, d.Admins, d.FromIdP = q, status, rows, l.Total, l.Active, l.Disabled, l.Admins, l.RolesFromIdP
	d.Page, d.Pages = pg, (l.Total+adminPageSize-1)/adminPageSize
	link := func(n int) string {
		v := url.Values{}
		if q != "" {
			v.Set("q", q)
		}
		if status != "" {
			v.Set("status", status)
		}
		v.Set("page", strconv.Itoa(n))
		return "/admin/users?" + v.Encode()
	}
	if pg > 1 {
		d.PrevURL = link(pg - 1)
	}
	if pg < d.Pages {
		d.NextURL = link(pg + 1)
	}
	s.adminRender(w, r, http.StatusOK, d, "People on this server")
}

// adminPerson reads one person by id (not out of a page of the list: there is no limit on how many people there are).
func (s *Server) adminPerson(r *http.Request, p ops.Principal, id string) (adminRow, bool) {
	out, err := s.Reg.Call(r.Context(), p, "admin.user.get", admin.UserRef{ID: id})
	if err != nil {
		return adminRow{}, false
	}
	u := out.(admin.UserItem).Item
	return adminRow{UserOut: u, Storage: humanBytes(u.StorageBytes), Seen: utcWhen(u.LastSeenAt), Made: utcWhen(u.CreatedAt)}, true
}

// adminDisablePage asks for the person's name before anything is switched off.
func (s *Server) adminDisablePage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	u, found := s.adminPerson(r, p, r.PathValue("id"))
	if !found {
		s.fail(w, r, ops.NotFound("person"))
		return
	}
	d := s.adminBase(r, "users")
	d.Confirm = &u
	s.adminRender(w, r, http.StatusOK, d, "Switch off "+u.Name)
}

func (s *Server) adminQuotasPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "admin.quota.list", admin.QuotaIn{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := out.(admin.QuotaOut)
	d := s.adminBase(r, "quotas")
	lang := s.page(r, "", nil).Lang
	d.Note = q.Note
	if lang != "en" { // the op's own sentence is English; the page says it in the person's language
		d.Note = i18n.Default.T(lang, "adm.q_note")
	}
	names := map[string]string{"Attachment storage, whole instance": "adm.ql_instance", "Attachment storage, the fullest project or Compendium": "adm.ql_fullest",
		"Largest single file allowed": "adm.ql_file", "People": "adm.ql_people", "Projects": "adm.ql_projects", "Live API keys": "adm.ql_keys"}
	noLimit := i18n.Default.T(lang, "adm.no_limit")
	for _, l := range q.Instance {
		name := l.Name
		if k := names[l.Name]; k != "" {
			name = i18n.Default.T(lang, k)
		}
		line := adminQuotaLine{Name: name, Percent: admPct(l.Used, l.Limit), Over: l.Limit > 0 && l.Used >= l.Limit}
		if l.Unit == "bytes" {
			line.Used, line.Limit = humanBytes(l.Used), noLimit
			if l.Limit > 0 {
				line.Limit = humanBytes(l.Limit)
			}
			if l.Name == "Largest single file allowed" {
				line.Used = ""
			}
		} else {
			line.Used, line.Limit = commas(int(l.Used)), noLimit
		}
		d.Lines = append(d.Lines, line)
	}
	for _, u := range q.Users {
		qu := adminQuotaUser{UserQuota: u, Storage: humanBytes(u.StorageBytes), Limit: noLimit, Percent: admPct(u.StorageBytes, u.StorageLimit)}
		if u.StorageLimit > 0 {
			qu.Limit = humanBytes(u.StorageLimit)
		}
		d.QuotaUsers = append(d.QuotaUsers, qu)
	}
	s.adminRender(w, r, http.StatusOK, d, "Limits and use")
}

func (s *Server) adminDiagnosticsPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "admin.diagnostics", admin.DiagnosticsIn{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	b := out.(admin.Bundle)
	dg := &adminDiag{Version: b.Version, Go: b.GoVersion, OS: b.OS, Arch: b.Arch, Applied: b.Migrations.Applied, Known: b.Migrations.Known, Pending: b.Migrations.Pending,
		Tables: len(b.Tables), Queues: b.Queues, Disk: b.Disk, LogLines: len(b.Log)}
	var total int64
	for _, l := range b.Disk {
		total += l.Bytes
	}
	dg.DiskTotal = humanBytes(total)
	d := s.adminBase(r, "diagnostics")
	d.Diag = dg
	s.adminRender(w, r, http.StatusOK, d, "Diagnostics")
}

func (s *Server) adminDiagnosticsDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "admin.diagnostics.download", admin.DiagnosticsIn{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a := out.(admin.Archive)
	h := w.Header()
	h.Set("Content-Type", a.ContentType)
	h.Set("Content-Disposition", ops.ContentDisposition(a.Filename))
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(a.Body)
}

func (s *Server) uiAdminUsers(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	back := func(msg, errMsg string) {
		s.flashSet(r, map[string]string{"msg": msg, "err": errMsg})
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
	}
	id := r.PostFormValue("id")
	u, found := s.adminPerson(r, p, id)
	if !found {
		s.fail(w, r, ops.NotFound("person"))
		return
	}
	switch r.PathValue("action") {
	case "disable":
		if strings.TrimSpace(r.PostFormValue("confirm_name")) != u.Name {
			d := s.adminBase(r, "users")
			d.Confirm, d.Error = &u, "That is not the name. Type it exactly as it is shown, or cancel."
			s.adminRender(w, r, http.StatusUnprocessableEntity, d, "Switch off "+u.Name)
			return
		}
		if _, err := s.Reg.Call(r.Context(), p, "admin.user.disable", admin.UserRef{ID: id}); err != nil {
			e := ops.AsError(err)
			if e.Code == ops.CodeConflict {
				back("", e.Message)
				return
			}
			s.fail(w, r, err)
			return
		}
		back(u.Name+" is switched off. Their sessions ended, their keys stopped working, and everything they wrote is where it was.", "")
	case "enable":
		if _, err := s.Reg.Call(r.Context(), p, "admin.user.enable", admin.UserRef{ID: id}); err != nil {
			s.fail(w, r, err)
			return
		}
		back(u.Name+" is switched on again.", "")
	case "role":
		if _, err := s.Reg.Call(r.Context(), p, "admin.user.set_role", admin.RoleIn{ID: id, Role: r.PostFormValue("role")}); err != nil {
			e := ops.AsError(err)
			if e.Code == ops.CodeConflict || e.Code == ops.CodeInvalid {
				back("", e.Message)
				return
			}
			s.fail(w, r, err)
			return
		}
		back("The role of "+u.Name+" was changed.", "")
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}
