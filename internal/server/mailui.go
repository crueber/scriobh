package server

import (
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/mail"
	"github.com/crueber/scriobh/internal/ops"
)

// The mail screens, all plain server-rendered forms (no script): the administrator's /admin/mail, the person's Kindle
// address and email digest on /settings, and Send to Kindle, which is a section on the Compile page leading to
// /p/{slug}/kindle (the preview, the confirmation and the project's send log). Every change is one of the mail.* and
// kindle.* ops, which are for a person in a browser: these screens are their only path. The relay's password and the
// preview's token are never put in a URL, the flash, a data- attribute or the browser's storage: the password field is
// write-only, and the token travels only in a hidden field of the page the preview form's POST answers with.

func (s *Server) registerMailUI(m *http.ServeMux) {
	m.HandleFunc("GET /admin/mail", s.adminMailPage)
	s.uiRoute("POST /ui/admin/mail/{action}", s.uiAdminMail)
	s.uiRoute("POST /ui/settings/kindle/{action}", s.uiSettingsKindle)
	s.uiRoute("POST /ui/settings/digest", s.uiSettingsDigest)
	m.HandleFunc("GET /p/{slug}/kindle", s.kindlePage)
	s.uiRoute("POST /ui/projects/{id}/kindle/{action}", s.uiKindle)
}

// mailWord is the catalogue's sentence for a failure class (never what a relay said).
func (s *Server) mailWord(r *http.Request, class string) string {
	pg := s.page(r, "", nil)
	if class == "" || class == "ok" {
		return pg.T("mail.class.ok")
	}
	if k := "mail.class." + class; i18n.Default.Has(k) {
		return pg.T(k)
	}
	return pg.T("mail.class.internal")
}

// ---- the administrator's page ----------------------------------------------------------------------------------

type mailOutRow struct {
	Kind, State, Error, Domain, When string
	Attempts                         int
}

type mailTestView struct {
	OK   bool
	Text string
}

type mailAdminData struct {
	Tab, CSRF       string
	Cfg             mail.ConfigOut
	Out             mail.OutboxOut
	Rows            []mailOutRow
	Flash, Error    string
	Field           string
	Values          map[string]string
	Domains         string // one to a line, as saved
	OffReason       string // a catalogue key, or ""
	HoldWhy         string
	LastTest        string
	Test            *mailTestView
	PasswordCleared bool
	PasswordLost    bool // a refused save: the password typed was not kept
	Refused         bool // the form is showing what was typed, not what is saved
}

func (s *Server) mailAdminView(r *http.Request) (mailAdminData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := mailAdminData{Tab: "mail", CSRF: authFrom(ctx).CSRF, Values: map[string]string{}}
	co, err := s.Reg.Call(ctx, p, "mail.config.get", mail.ConfigGetIn{})
	if err != nil {
		return d, err
	}
	d.Cfg = co.(mail.ConfigOut)
	oo, err := s.Reg.Call(ctx, p, "mail.outbox.status", mail.OutboxIn{})
	if err != nil {
		return d, err
	}
	d.Out = oo.(mail.OutboxOut)
	loc := s.userZone(r)
	for _, e := range d.Out.Items {
		row := mailOutRow{Kind: e.Kind, State: e.State, Domain: e.Domain, Attempts: e.Attempts, When: whenIn(e.CreatedAt, loc)}
		if e.Error != "" {
			row.Error = s.mailWord(r, e.Error)
		}
		d.Rows = append(d.Rows, row)
	}
	d.Domains = strings.Join(d.Cfg.KindleDomains, "\n")
	switch d.Cfg.DisabledReason {
	case "Switched off by an administrator.":
		d.OffReason = "mail.admin.off_by_admin"
	case "The relay's settings are no longer complete.":
		d.OffReason = "mail.admin.off_incomplete"
	}
	if d.Cfg.Hold != "" {
		d.HoldWhy = s.mailWord(r, d.Cfg.Hold)
	}
	if d.Cfg.LastTestAt != "" {
		d.LastTest = s.tr(r, "mail.admin.last_test", "when", whenIn(d.Cfg.LastTestAt, loc), "result", s.mailWord(r, d.Cfg.LastTestClass))
	}
	return d, nil
}

func (s *Server) adminMailTitle(r *http.Request) string {
	return s.tr(r, "mail.settings.title") + " · " + s.tr(r, "adm.title")
}

func (s *Server) adminMailPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminOnly(w, r); !ok {
		return
	}
	d, err := s.mailAdminView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error = fl["msg"], fl["err"]
		d.Field = fl["field"]
		d.PasswordCleared = fl["password_cleared"] == "1"
		if fl["refused"] == "1" { // a refused save: the form shows what was typed
			d.Refused, d.PasswordLost = true, fl["pwlost"] == "1"
			for k, v := range fl {
				if strings.HasPrefix(k, "v:") {
					d.Values[strings.TrimPrefix(k, "v:")] = v
				}
			}
		}
		if fl["domains"] != "" {
			d.Domains = fl["domains"]
		}
		if fl["test"] != "" {
			d.Test = &mailTestView{OK: fl["test"] == "ok", Text: fl["test_text"]}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "admin-mail", s.page(r, s.adminMailTitle(r), d))
}

// mailKeep keeps what was typed into a refused form, except the password.
func mailKeep(r *http.Request) map[string]string {
	v := map[string]string{}
	for _, k := range []string{"host", "port", "security", "username", "from_address", "from_name", "max_attachment_mb", "tls_ca_pem", "allow_private"} {
		v[k] = r.PostFormValue(k)
	}
	return v
}

// mailAdminFail keeps a refusal, and what was typed except the password, for the page after a redirect: the address in the
// browser stays /admin/mail, and a reload repeats nothing.
func (s *Server) mailAdminFail(w http.ResponseWriter, r *http.Request, err error, keep map[string]string) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	fl := map[string]string{"err": redactPosted(r, e.Message), "field": e.Field}
	if typed := scrubValue(r.PostFormValue("domains")); typed != "" { // a refused allow-list comes back as it was typed
		fl["domains"] = typed
	}
	if keep != nil {
		fl["refused"] = "1"
		if r.PostFormValue("password") != "" {
			fl["pwlost"] = "1"
		}
		for k, v := range scrubValues(keep) {
			fl["v:"+k] = v
		}
	}
	s.flashSet(r, fl)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/admin/mail", http.StatusSeeOther)
}

func (s *Server) uiAdminMail(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminOnly(w, r); !ok {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	if err := s.refuseAnyCredentials(r); err != nil { // a credential in the relay's host is refused and never shown again
		s.mailAdminFail(w, r, err, mailKeep(r))
		return
	}
	back := func(vals map[string]string, frag string) {
		s.flashSet(r, vals)
		http.Redirect(w, r, "/admin/mail"+frag, http.StatusSeeOther)
	}
	switch r.PathValue("action") {
	case "save":
		// An empty field is "keep" (nil), never "remove": the password, the certificate authority and the allow-list are
		// removed only by their own boxes.
		str := func(k string) *string { v := strings.TrimSpace(r.PostFormValue(k)); return &v }
		port, mb := atoi(r.PostFormValue("port")), atoi(r.PostFormValue("max_attachment_mb"))
		ap := r.PostFormValue("allow_private") == "1"
		in := mail.ConfigSetIn{Host: str("host"), Port: &port, Security: str("security"), AllowPrivate: &ap, Username: str("username"), FromAddress: str("from_address"), FromName: str("from_name")}
		if mb > 0 {
			in.MaxAttachmentMB = &mb
		}
		if r.PostFormValue("remove_password") == "1" {
			empty := ""
			in.Password = &empty
		} else if pw := r.PostFormValue("password"); pw != "" {
			in.Password = &pw
		}
		if r.PostFormValue("remove_ca") == "1" {
			empty := ""
			in.TLSCAPEM = &empty
		} else if ca := strings.TrimSpace(r.PostFormValue("tls_ca_pem")); ca != "" {
			in.TLSCAPEM = &ca
		}
		out, err := s.Reg.Call(ctx, p, "mail.config.set", in)
		if err != nil {
			s.mailAdminFail(w, r, err, mailKeep(r))
			return
		}
		vals := map[string]string{"msg": s.tr(r, "mail.settings.saved")}
		if out.(mail.ConfigSetOut).PasswordCleared {
			vals["password_cleared"] = "1"
		}
		back(vals, "")
	case "enable", "disable":
		on := r.PathValue("action") == "enable"
		if _, err := s.Reg.Call(ctx, p, "mail.config.set", mail.ConfigSetIn{Enabled: &on}); err != nil {
			s.mailAdminFail(w, r, err, nil)
			return
		}
		back(map[string]string{"msg": s.tr(r, map[bool]string{true: "mail.admin.now_on", false: "mail.admin.now_off"}[on])}, "")
	case "test":
		out, err := s.Reg.Call(ctx, p, "mail.config.test", mail.TestIn{})
		if err != nil {
			e := ops.AsError(err)
			switch e.Code {
			case ops.CodeRateLimited:
				back(map[string]string{"test": "fail", "test_text": s.tr(r, "mail.admin.test_soon", "n", e.RetryAfterSeconds)}, "#test")
			case ops.CodeConflict:
				back(map[string]string{"test": "fail", "test_text": e.Message}, "#test")
			default:
				s.fail(w, r, err)
			}
			return
		}
		t := out.(mail.TestOut)
		if t.OK {
			back(map[string]string{"test": "ok", "test_text": s.tr(r, "mail.admin.test_ok", "to", t.To)}, "#test")
		} else {
			back(map[string]string{"test": "fail", "test_text": s.tr(r, "mail.admin.test_fail", "why", s.mailWord(r, t.Class))}, "#test")
		}
	case "domains":
		cur, err := s.Reg.Call(ctx, p, "mail.config.get", mail.ConfigGetIn{})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		have := map[string]bool{}
		for _, dm := range cur.(mail.ConfigOut).KindleDomains {
			have[dm] = true
		}
		list := []string{}
		reset := r.PostFormValue("reset") == "1"
		if !reset {
			for _, f := range strings.FieldsFunc(r.PostFormValue("domains"), func(c rune) bool { return c == '\n' || c == '\r' || c == ',' || c == ' ' || c == '\t' }) {
				list = append(list, strings.ToLower(f))
			}
		}
		// Widening is a deliberate step: a domain that is not already allowed needs the tick. Taking one away, or going back
		// to the default, does not.
		if !reset && r.PostFormValue("understood") != "1" {
			for _, dm := range list {
				if !have[dm] {
					s.mailAdminFail(w, r, ops.Invalid("understood", s.tr(r, "mail.admin.domains_need")), nil)
					return
				}
			}
		}
		// the domains this adds are named to the op, which refuses the change without them
		var adding []string
		for _, dm := range list {
			if !have[dm] {
				adding = append(adding, dm)
			}
		}
		if _, err := s.Reg.Call(ctx, p, "mail.config.set", mail.ConfigSetIn{KindleDomains: &list, ConfirmDomains: adding}); err != nil {
			s.mailAdminFail(w, r, err, nil)
			return
		}
		back(map[string]string{"msg": s.tr(r, map[bool]string{true: "mail.admin.domains_default_saved", false: "mail.admin.domains_saved"}[reset])}, "#domains")
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

// ---- the person's settings: the Kindle address and the digest -------------------------------------------------------------

// mailAccount is what the Kindle and digest sections of /settings show.
type mailAccount struct {
	Digest       mail.DigestOut
	Kindle       mail.KindleAddressOut
	Zone         string
	DomainsText  string
	How          string
	Flash, Error string
	Field        string
	Where        string // "kindle" or "digest": which section the message belongs to
	Typed        string // a refused address as it was typed (not a secret)
}

func (s *Server) mailAccountView(r *http.Request) *mailAccount {
	ctx, p := r.Context(), s.principal(r)
	a := &mailAccount{Zone: s.userZone(r).String()}
	if o, err := s.Reg.Call(ctx, p, "mail.digest.get", mail.DigestGetIn{}); err == nil {
		a.Digest = o.(mail.DigestOut)
	}
	if o, err := s.Reg.Call(ctx, p, "kindle.address.get", mail.KindleAddressIn{}); err == nil {
		a.Kindle = o.(mail.KindleAddressOut)
		a.DomainsText = strings.Join(a.Kindle.Domains, ", ")
	}
	switch a.Digest.Frequency {
	case "daily":
		a.How = s.tr(r, "mail.digest.how_daily")
	case "weekly":
		a.How = s.tr(r, "mail.digest.how_weekly")
	}
	if fl := s.flashTake(r); fl != nil {
		a.Flash, a.Error, a.Field, a.Where, a.Typed = fl["msg"], fl["err"], fl["field"], fl["where"], fl["typed"]
	}
	return a
}

func (s *Server) uiSettingsKindle(w http.ResponseWriter, r *http.Request) {
	ctx, p := r.Context(), s.principal(r)
	back := func(vals map[string]string) {
		vals["where"] = "kindle"
		s.flashSet(r, vals)
		http.Redirect(w, r, "/settings#kindle", http.StatusSeeOther)
	}
	refuse := func(err error) {
		e := ops.AsError(err)
		if e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict {
			back(map[string]string{"err": redactPosted(r, e.Message), "field": e.Field, "typed": scrubValue(strings.TrimSpace(r.PostFormValue("address")))})
			return
		}
		s.fail(w, r, err)
	}
	if err := s.refuseAnyCredentials(r); err != nil {
		refuse(err)
		return
	}
	switch r.PathValue("action") {
	case "save":
		if _, err := s.Reg.Call(ctx, p, "kindle.address.set", mail.KindleAddressSetIn{Address: strings.TrimSpace(r.PostFormValue("address"))}); err != nil {
			refuse(err)
			return
		}
		back(map[string]string{"msg": s.tr(r, "mail.kindle.saved")})
	case "delete":
		if _, err := s.Reg.Call(ctx, p, "kindle.address.delete", mail.KindleAddressIn{}); err != nil {
			refuse(err)
			return
		}
		back(map[string]string{"msg": s.tr(r, "mail.kindle.deleted")})
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

func (s *Server) uiSettingsDigest(w http.ResponseWriter, r *http.Request) {
	ctx, p := r.Context(), s.principal(r)
	back := func(vals map[string]string) {
		vals["where"] = "digest"
		s.flashSet(r, vals)
		http.Redirect(w, r, "/settings#digest", http.StatusSeeOther)
	}
	on := r.PostFormValue("do") == "on"
	if _, err := s.Reg.Call(ctx, p, "mail.digest.set", mail.DigestSetIn{Enabled: on, Frequency: r.PostFormValue("frequency")}); err != nil {
		e := ops.AsError(err)
		switch {
		case e.Code == ops.CodeInvalid && e.Field == "frequency":
			back(map[string]string{"err": s.tr(r, "mail.digest.needs_freq"), "field": "frequency"})
		case e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict:
			back(map[string]string{"err": e.Message, "field": e.Field})
		default:
			s.fail(w, r, err)
		}
		return
	}
	back(map[string]string{"msg": s.tr(r, map[bool]string{true: "mail.digest.saved_on", false: "mail.digest.saved_off"}[on])})
}

// ---- Send to Kindle ---------------------------------------------------------------------------------------------------

// kindleCompile is the line the Compile page shows: what is missing, or that a preview can be made.
type kindleCompile struct {
	State   string // "off" (mail is not on), "noaddr" (no stored address), "ready"
	Address string
}

func (s *Server) kindleCompileState(r *http.Request) kindleCompile {
	o, err := s.Reg.Call(r.Context(), s.principal(r), "kindle.address.get", mail.KindleAddressIn{})
	if err != nil {
		return kindleCompile{State: "off"}
	}
	a := o.(mail.KindleAddressOut)
	switch {
	case !a.MailAvailable:
		return kindleCompile{State: "off"}
	case a.Address == "":
		return kindleCompile{State: "noaddr"}
	}
	return kindleCompile{State: "ready", Address: a.Address}
}

type kindleRow struct {
	mail.KindleLogEntry
	When, Size string
}

type kindleData struct {
	Project         projects.Project
	Slug, ProjectID string
	CSRF            string
	K               kindleCompile
	Sender          string
	Preview         *mail.KindlePreviewOut
	PreviewSize     string
	Token           string
	Log             []kindleRow
	Flash, Error    string
	CanSeeAddress   bool
}

func (s *Server) kindleView(r *http.Request, proj projects.Project) (kindleData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := kindleData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: authFrom(ctx).CSRF, K: s.kindleCompileState(r),
		CanSeeAddress: proj.Role == ops.ProjectOwner || p.Role == ops.RoleAdmin}
	if o, err := s.Reg.Call(ctx, p, "kindle.address.get", mail.KindleAddressIn{}); err == nil {
		d.Sender = o.(mail.KindleAddressOut).Sender
	}
	lo, err := s.Reg.Call(ctx, p, "kindle.log", mail.KindleProjectIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	loc := s.userZone(r)
	for _, e := range lo.(mail.KindleLogOut).Items {
		d.Log = append(d.Log, kindleRow{KindleLogEntry: e, When: whenIn(e.At, loc), Size: humanBytes(e.Bytes)})
	}
	return d, nil
}

// kindleDoor is the Compile page's door: owners and editors; to anyone else the page is not there.
func (s *Server) kindleDoor(w http.ResponseWriter, r *http.Request, proj projects.Project) bool {
	if proj.Role != ops.ProjectOwner && proj.Role != ops.ProjectEditor {
		s.fail(w, r, ops.NotFound("page"))
		return false
	}
	return true
}

func (s *Server) kindleRender(w http.ResponseWriter, r *http.Request, status int, d kindleData) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, "kindle", s.page(r, s.tr(r, "mail.kindle.page_title", "title", d.Project.Title), d))
}

func (s *Server) kindlePage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok || !s.kindleDoor(w, r, proj) {
		return
	}
	d, err := s.kindleView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error = fl["msg"], fl["err"]
	}
	s.kindleRender(w, r, http.StatusOK, d)
}

// kindleError says what went wrong in the catalogue's words, by the reason the op gives.
func (s *Server) kindleError(r *http.Request, e *ops.Error) string {
	reason, _ := e.Data["reason"].(string)
	switch {
	case e.Code == ops.CodeRateLimited:
		return s.tr(r, "mail.kindle.err_rate_limited", "n", e.RetryAfterSeconds)
	case reason == "mail_off":
		return s.tr(r, "mail.kindle.mail_off")
	case reason == "no_kindle_address":
		return s.tr(r, "mail.kindle.no_address")
	case reason == "preview_gone":
		return s.tr(r, "mail.kindle.err_preview_gone")
	case reason == "preview_changed":
		return s.tr(r, "mail.kindle.changed")
	case reason == "queue_full":
		return s.tr(r, "mail.kindle.err_queue_full")
	case reason == "kindle_domain":
		return s.tr(r, "mail.kindle.err_kindle_domain")
	case reason == ops.ReasonProjectTrashed:
		return s.tr(r, "mail.kindle.err_trashed")
	case e.Code == ops.CodeTooLarge:
		return e.Message // the op says the sizes
	}
	return s.tr(r, "mail.kindle.err_other", "why", e.Message)
}

func (s *Server) uiKindle(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok || !s.kindleDoor(w, r, proj) {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	fail := func(err error) {
		e := ops.AsError(err)
		status := map[ops.Code]int{ops.CodeConflict: http.StatusConflict, ops.CodeRateLimited: http.StatusTooManyRequests, ops.CodeTooLarge: http.StatusRequestEntityTooLarge}[e.Code]
		if status == 0 {
			s.fail(w, r, err)
			return
		}
		d, verr := s.kindleView(r, proj)
		if verr != nil {
			s.fail(w, r, verr)
			return
		}
		d.Error = s.kindleError(r, e)
		s.kindleRender(w, r, status, d)
	}
	switch r.PathValue("action") {
	case "preview":
		out, err := s.Reg.Call(ctx, p, "kindle.preview", mail.KindleProjectIn{ProjectID: proj.ID})
		if err != nil {
			fail(err)
			return
		}
		pv := out.(mail.KindlePreviewOut)
		d, err := s.kindleView(r, proj)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Preview, d.PreviewSize, d.Token, d.Sender = &pv, humanBytes(pv.Bytes), pv.Token, pv.Sender
		s.kindleRender(w, r, http.StatusOK, d)
	case "send":
		if _, err := s.Reg.Call(ctx, p, "kindle.send", mail.KindleSendIn{ProjectID: proj.ID, Token: r.PostFormValue("token")}); err != nil {
			fail(err)
			return
		}
		s.flashSet(r, map[string]string{"msg": s.tr(r, "mail.kindle.queued")})
		http.Redirect(w, r, "/p/"+proj.Slug+"/kindle#log", http.StatusSeeOther)
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}
