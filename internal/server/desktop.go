package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/desktopidp"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/desktop"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// Desktop mode (SCRIOBH_DESKTOP=1): the launch gate, the embedded issuer on this listener, the first-run local
// profile, and the pages for the identity provider and for a model on this computer. With the variable unset none of
// this exists: no route below is registered, the Host check is not in the chain, and s.desk is nil.
//
// Sign-in is the application's ordinary OIDC code path against the issuer at /desktop-idp; there is no header, cookie
// or switch in the session middleware that knows about desktop mode. The launch cookie is read in three places only:
// by the issuer's authorization endpoint, by the profile form, and to decide whether to start the sign-in without
// showing the sign-in page first.

// desktopSignedOut marks a window whose person chose to sign out, so the sign-in page is shown instead of signing
// them straight back in.
const desktopSignedOut = "scriobh_desktop_out"

type desktopRT struct {
	launch *desktopidp.Launch
	// issuer is nil when the instance signs in through an external provider.
	issuer *desktopidp.Server
	svc    *desktop.Service
}

// initDesktop builds the desktop parts. It is called by New, before the routes, and only in desktop mode.
func (s *Server) initDesktop(key []byte) error {
	rt := &desktopRT{launch: desktopidp.NewLaunch(s.Cfg.LaunchToken)}
	rt.svc = &desktop.Service{On: true, Embedded: s.Cfg.DesktopEmbedded(), DataDir: s.Cfg.DataDir, Version: s.Cfg.Version, Box: secrets.New(key)}
	s.Reg.Env().SetService(desktop.ServiceName, rt.svc)
	if s.Cfg.DesktopEmbedded() {
		k, err := desktopidp.LoadKey(s.Cfg.DataDir, s.Cfg.Version, time.Now())
		if err != nil {
			return err
		}
		if k.Rotated {
			s.Log.Info("desktop: a new signing key was made for the embedded issuer")
		}
		rt.issuer, err = desktopidp.New(desktopidp.Config{
			Base: s.Cfg.BaseURLString, RedirectURI: s.Cfg.OIDCCallbackURL, Launch: rt.launch, Key: k, ProfilePath: "/desktop/profile",
			Profile: func(ctx context.Context) (desktopidp.Profile, bool, error) { return desktop.LoadProfile(ctx, s.DB) },
			Refused: func(endpoint, reason string) {
				s.Log.Warn("desktop: the embedded issuer refused a request", "endpoint", endpoint, "reason", reason)
			},
		})
		if err != nil {
			return err
		}
		// The application reaches its own issuer over loopback, at the address it bound and nowhere else.
		s.OIDC.HTTP = auth.PinnedClient(func() string { return strings.TrimPrefix(s.Cfg.BaseURLString(), "http://") })
	}
	s.desk = rt
	return nil
}

// observeOutbound counts this process's outbound connections for desktop.info, while it serves.
func (s *Server) observeOutbound() (stop func()) {
	if s.desk == nil {
		return func() {}
	}
	return netguard.Observe(func(_, address string) {
		s.desk.svc.Outbound.Add(1)
		if !netguard.IsLoopbackAddress(address) {
			s.desk.svc.OutboundElsewhere.Add(1)
		}
	})
}

func (s *Server) registerDesktop(m *http.ServeMux) {
	if s.desk == nil {
		return
	}
	m.HandleFunc("GET /desktop/start", s.desktopStart)
	m.HandleFunc("GET /desktop/identity", s.desktopIdentityPage)
	m.HandleFunc("GET /desktop/identity/verify", s.desktopIdentityVerify)
	s.uiRoute("POST /ui/desktop/identity/{action}", s.uiDesktopIdentity)
	m.HandleFunc("GET /desktop/local-model", s.desktopLocalModelPage)
	s.uiRoute("POST /ui/desktop/local-model/{action}", s.uiDesktopLocalModel)
	if s.desk.issuer != nil {
		m.HandleFunc("GET /desktop/profile", s.desktopProfilePage)
		m.HandleFunc("POST /desktop/profile", s.desktopProfileSubmit)
		m.Handle(desktopIssuerMount, http.StripPrefix(desktopidp.MountPath, s.desk.issuer.Handler()))
	}
}

// desktopHealthz is `/healthz` in desktop mode: the same answer, plus, when the request carries the shell's
// challenge, the proof that this process holds the launch token (see desktopidp.Launch.Proof). The shell checks it
// before it sends its window the token, so a program that took over the port of a server that died gets nothing.
// A request with no challenge, or a malformed one, is answered as before, with no proof. Outside desktop mode this
// is the plain handler.
func (s *Server) desktopHealthz(next http.Handler) http.Handler {
	if s.desk == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := r.Header.Values(desktopidp.ChallengeHeader); len(c) == 1 {
			if proof, ok := s.desk.launch.Proof(c[0]); ok {
				w.Header().Set(desktopidp.ProofHeader, proof)
			}
		}
		next.ServeHTTP(w, r)
	})
}

const desktopIssuerMount = desktopidp.MountPath + "/"

// desktopHost refuses, in desktop mode, every request whose Host header is not this instance's own loopback address
// and port: a web page that reached the port through a name of its own (DNS rebinding) is turned away before any
// handler runs. Outside desktop mode it is not in the chain.
func (s *Server) desktopHost(next http.Handler) http.Handler {
	if !s.Cfg.Desktop {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !desktopidp.HostAllowed(r.Host, s.Cfg.BaseURLString()) {
			s.Log.Warn("desktop: a request for another host was refused")
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// desktopStart is where the shell opens its window: GET /desktop/start?t=<launch token>. The right token, once, is
// exchanged for the launch cookie and the window goes on to the application; anything else is the page an unknown
// address gets. The token is in the query, which no log line of this program carries.
func (s *Server) desktopStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Another site's page (an image, a frame, a link) cannot use or spend the token. A request with no Sec-Fetch headers
	// (the shell's webview, curl) is the shell's own.
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		s.notFound(w, r)
		return
	}
	t := ""
	if v := r.URL.Query()["t"]; len(v) == 1 {
		t = v[0]
	}
	cookie, ok := s.desk.launch.Redeem(t)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.desk.launch.SetCookie(w, cookie)
	http.SetCookie(w, &http.Cookie{Name: desktopSignedOut, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	if s.desk.issuer == nil && !s.principal(r).Authenticated() {
		// The instance signs in through an external provider. Its sign-in page is another site, which the shell's
		// window does not open: the way in is an ordinary browser, at this address. (A window that still holds a
		// session goes on to the library and keeps working.)
		p := s.page(r, "", map[string]any{"Address": s.Cfg.BaseURLString()})
		p.Lang, p.UIDir = sharePageLang(r, s.testLocales())
		p.Title = p.T("desktop.ext_title")
		s.render(w, r, http.StatusOK, "desktop-external", p)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// desktopAutoSignIn reports whether a signed-out request for the library should begin the sign-in at once: the
// shell's own window, on an instance that signs in through the embedded issuer, whose person has not just signed out.
func (s *Server) desktopAutoSignIn(r *http.Request) bool {
	if s.desk == nil || s.desk.issuer == nil || !s.desk.launch.CookieOK(r) {
		return false
	}
	_, err := r.Cookie(desktopSignedOut)
	return err != nil
}

// desktopLoginGate answers a sign-in that cannot succeed (the embedded issuer, and a browser that is not the shell's
// window) with words instead of the issuer's bare refusal. It reports whether it answered.
func (s *Server) desktopLoginGate(w http.ResponseWriter, r *http.Request) bool {
	if s.desk == nil || s.desk.issuer == nil {
		return false
	}
	if !s.desk.launch.CookieOK(r) {
		s.render(w, r, http.StatusForbidden, "error", s.page(r, s.tr(r, "desktop.window_h"), map[string]any{
			"Heading": s.tr(r, "desktop.window_h"), "Message": s.tr(r, "desktop.window_p"), "Code": 403}))
		return true
	}
	http.SetCookie(w, &http.Cookie{Name: desktopSignedOut, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return false
}

// desktopAfterLogout remembers that the person signed out.
func (s *Server) desktopAfterLogout(w http.ResponseWriter) {
	if s.desk != nil && s.desk.issuer != nil {
		http.SetCookie(w, &http.Cookie{Name: desktopSignedOut, Value: "1", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
}

// ---- the local profile ---------------------------------------------------

type desktopProfileData struct {
	Token       string
	Name, Email string
	Field       string // the field that is wrong
	Error       string
}

const desktopProfileForm = "profile"

// desktopProfileGate is what both halves of the profile form check first: the shell's own window (anyone else gets
// the page of an unknown address), and no profile yet (the form only ever makes the first).
func (s *Server) desktopProfileGate(w http.ResponseWriter, r *http.Request) bool {
	if !s.desk.launch.CookieOK(r) {
		s.notFound(w, r)
		return false
	}
	if _, ok, err := desktop.LoadProfile(r.Context(), s.DB); err != nil {
		s.fail(w, r, err)
		return false
	} else if ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return false
	}
	return true
}

func (s *Server) desktopProfileRender(w http.ResponseWriter, r *http.Request, status int, d desktopProfileData) {
	d.Token = s.desk.launch.FormToken(desktopProfileForm)
	p := s.page(r, "", d)
	p.Lang, p.UIDir = sharePageLang(r, s.testLocales())
	p.Title = p.T("desktop.profile_title")
	s.render(w, r, status, "desktop-profile", p)
}

func (s *Server) desktopProfilePage(w http.ResponseWriter, r *http.Request) {
	if !s.desktopProfileGate(w, r) {
		return
	}
	s.desktopProfileRender(w, r, http.StatusOK, desktopProfileData{})
}

// desktopProfileSubmit makes the local profile. Nobody is signed in yet, so the form's token is tied to the launch
// cookie instead of a session; a form posted from another site is refused by the cross-origin check before this runs,
// and does not carry the cookie (SameSite=Strict) either. Then the ordinary sign-in begins.
func (s *Server) desktopProfileSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.desktopProfileGate(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil || !s.desk.launch.FormTokenOK(desktopProfileForm, r.PostFormValue("_csrf")) {
		s.fail(w, r, ops.Forbidden("This form expired. Reload the page and try again."))
		return
	}
	lang, _ := sharePageLang(r, s.testLocales())
	rawName, rawEmail := r.PostFormValue("name"), r.PostFormValue("email")
	name, email, field, problem := desktopidp.CleanProfile(rawName, rawEmail)
	if field != "" {
		if len(rawName) > 400 {
			rawName = rawName[:400]
		}
		if len(rawEmail) > 400 {
			rawEmail = rawEmail[:400]
		}
		s.desktopProfileRender(w, r, http.StatusUnprocessableEntity, desktopProfileData{Name: strings.ToValidUTF8(rawName, ""), Email: strings.ToValidUTF8(rawEmail, ""),
			Field: field, Error: i18n.Default.T(lang, "desktop.profile_err_"+field+"_"+problem)})
		return
	}
	if _, created, err := desktop.CreateProfile(r.Context(), s.DB, name, email); err != nil {
		s.fail(w, r, err)
		return
	} else if created {
		s.Events.Audit(r.Context(), "", ops.AuditEntry{Action: "desktop.profile.created", TargetType: "settings", TargetID: desktop.KeyProfile}, "")
	}
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// ---- the identity provider -------------------------------------------------

type desktopIdentityData struct {
	CSRF           string
	S              desktop.IdPOut
	Saved          bool
	Verified       bool
	Error, Field   string
	Issuer, Client string // the form as shown
	Admin, Writer  string
	Reader, Port   string
	// Ticket is the address for a web browser, shown once after it is asked for.
	Ticket string
}

func (s *Server) desktopIdentityPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "desktop.idp.get", struct{}{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := desktopIdentityData{CSRF: authFrom(r.Context()).CSRF, S: out.(desktop.IdPOut), Saved: r.URL.Query().Get("saved") == "1", Verified: r.URL.Query().Get("verified") == "1"}
	if x := d.S.External; x != nil {
		d.Issuer, d.Client, d.Admin, d.Writer, d.Reader = x.Issuer, x.ClientID, x.AdminGroup, x.WriterGroup, x.ReaderGroup
		if x.Port != 0 {
			d.Port = strconv.Itoa(x.Port)
		}
	}
	fl := s.flashTake(r)
	if fl != nil {
		d.Ticket = fl["ticket"]
	}
	if fl != nil && fl["err"] != "" {
		d.Error, d.Field = fl["err"], fl["field"]
		if fl["form"] == "1" {
			d.Issuer, d.Client, d.Admin, d.Writer, d.Reader, d.Port = fl["issuer"], fl["client_id"], fl["admin_group"], fl["writer_group"], fl["reader_group"], fl["port"]
		}
	}
	s.render(w, r, http.StatusOK, "desktop-identity", s.page(r, s.tr(r, "desktop.id_title"), d))
}

func (s *Server) desktopIdentityBack(w http.ResponseWriter, r *http.Request, msg string, extra map[string]string) {
	fl := map[string]string{"err": msg}
	for k, v := range extra {
		fl[k] = v
	}
	s.flashSet(r, fl)
	http.Redirect(w, r, "/desktop/identity", http.StatusSeeOther)
}

func (s *Server) uiDesktopIdentity(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	call := func(op string, in any, done string) {
		if _, err := s.Reg.Call(r.Context(), p, op, in); err != nil {
			e := ops.AsError(err)
			if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
				s.fail(w, r, err)
				return
			}
			s.desktopIdentityBack(w, r, redactPosted(r, e.Message), map[string]string{"field": e.Field})
			return
		}
		http.Redirect(w, r, "/desktop/identity"+done, http.StatusSeeOther)
	}
	switch r.PathValue("action") {
	case "save":
		form := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
		in := desktop.SetIn{Issuer: form("issuer"), ClientID: form("client_id"), AdminGroup: form("admin_group"), WriterGroup: form("writer_group"), ReaderGroup: form("reader_group")}
		if v := r.PostFormValue("client_secret"); v != "" {
			in.ClientSecret = &v
		} else if formBool(r, "no_secret") {
			in.ClientSecret = new(string)
		}
		if v := form("port"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				n = -1
			}
			in.Port = n
		}
		if _, err := s.Reg.Call(r.Context(), p, "desktop.idp.set", in); err != nil {
			e := ops.AsError(err)
			if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
				s.fail(w, r, err)
				return
			}
			// What was typed comes back, except the secret.
			s.desktopIdentityBack(w, r, redactPosted(r, e.Message), map[string]string{"field": e.Field, "form": "1", "issuer": scrubValue(in.Issuer), "client_id": scrubValue(in.ClientID),
				"admin_group": in.AdminGroup, "writer_group": in.WriterGroup, "reader_group": in.ReaderGroup, "port": form("port")})
			return
		}
		http.Redirect(w, r, "/desktop/identity?saved=1", http.StatusSeeOther)
	case "switch":
		call("desktop.idp.switch", desktop.SwitchIn{To: desktop.ModeExternal, Confirm: strings.TrimSpace(r.PostFormValue("confirm")), Identity: strings.TrimSpace(r.PostFormValue("identity"))}, "")
	case "back":
		call("desktop.idp.switch", desktop.SwitchIn{To: desktop.ModeEmbedded, Confirm: strings.TrimSpace(r.PostFormValue("confirm"))}, "")
	case "cancel":
		call("desktop.idp.cancel", struct{}{}, "")
	case "ticket":
		// The application's own window does not open another site's page, and the provider's sign-in is one. The
		// administrator is given an address for an ordinary web browser instead, good once and for five minutes.
		t, err := s.desk.svc.SealTicket(p.UserID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.flashSet(r, map[string]string{"ticket": s.Cfg.BaseURLString() + "/desktop/identity/verify?ticket=" + url.QueryEscape(t)})
		http.Redirect(w, r, "/desktop/identity#di-switch", http.StatusSeeOther)
	default:
		s.notFound(w, r)
	}
}

// desktopCandidate is the relying party for the provider an administrator entered: the same code as the sign-in,
// pointed at that provider, with its own flow cookie.
func (s *Server) desktopCandidate(ctx context.Context) (*auth.OIDC, desktop.External, error) {
	issuer, clientID, secret, x, err := s.desk.svc.Candidate(ctx, s.DB)
	if err != nil {
		return nil, desktop.External{}, err
	}
	rp := auth.NewOIDCFor(s.Cfg, s.Sess.Key, auth.Provider{Issuer: issuer, ClientID: clientID, ClientSecret: secret})
	rp.Log = s.Log
	return rp, x, nil
}

// desktopIdentityVerify begins a sign-in at the provider the administrator entered, to prove the settings work and
// to learn who the administrator is there. It is a navigation, as /auth/login is (a form may not redirect to another
// site under this application's policy); it changes nothing by itself, and is refused when another site started it.
func (s *Server) desktopIdentityVerify(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if s.desk.issuer == nil || !desktopidp.OwnNavigation(r, s.Cfg.BaseURLString()) {
		s.fail(w, r, ops.Forbidden("Start this from the identity page."))
		return
	}
	if ticket != "" {
		// A web browser with the administrator's ticket and no session: the address was made in the application's
		// window, which cannot show the provider's page itself.
		if _, ok := s.desk.svc.OpenTicket(ticket, false); !ok {
			s.desktopPlain(w, r, http.StatusForbidden, "desktop.id_ticket_bad_h", "desktop.id_ticket_bad_p")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: desktopTicketCookie, Value: ticket, Path: "/auth", MaxAge: int(desktop.ProofTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode})
	} else if _, ok := s.adminOnly(w, r); !ok {
		return
	}
	back := func(msg string) {
		if ticket != "" {
			s.desktopPlainText(w, r, http.StatusBadGateway, s.tr(r, "desktop.id_ticket_bad_h"), msg)
			return
		}
		s.desktopIdentityBack(w, r, msg, nil)
	}
	rp, _, err := s.desktopCandidate(r.Context())
	if err != nil {
		back(ops.AsError(err).Message)
		return
	}
	dest, err := rp.Begin(r.Context(), w, "/desktop/identity")
	if err != nil {
		s.Log.Warn("desktop: the external provider could not be reached to prove its settings", "err", err)
		back(s.tr(r, "desktop.id_err_unreachable"))
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// desktopTicketCookie carries the administrator's ticket across the provider's redirect in the web browser.
const desktopTicketCookie = "scriobh_desktop_ticket"

// desktopPlain answers with a page of a heading and a paragraph, for a browser nobody is signed in on.
func (s *Server) desktopPlain(w http.ResponseWriter, r *http.Request, status int, headingKey, messageKey string) {
	lang, _ := sharePageLang(r, s.testLocales())
	s.desktopPlainText(w, r, status, i18n.Default.T(lang, headingKey), i18n.Default.T(lang, messageKey))
}

func (s *Server) desktopPlainText(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	s.render(w, r, status, "error", s.page(r, heading, map[string]any{"Heading": heading, "Message": message, "Code": status}))
}

// desktopLinkCallback finishes that sign-in. It reports whether the callback was its own. It is the administrator's
// either by their session (the callback came back to the window they began in) or by their ticket (it came back to
// the web browser they opened the ticket's address in); the identity the provider showed is sealed into a proof and
// recorded through desktop.idp.verify as that administrator. Nothing is switched here.
func (s *Server) desktopLinkCallback(w http.ResponseWriter, r *http.Request) bool {
	if s.desk == nil || s.desk.issuer == nil {
		return false
	}
	rp, x, err := s.desktopCandidate(r.Context())
	if err != nil || !rp.Owns(r) {
		return false
	}
	p := s.principal(r)
	byTicket := false
	if c, cerr := r.Cookie(desktopTicketCookie); cerr == nil {
		http.SetCookie(w, &http.Cookie{Name: desktopTicketCookie, Value: "", Path: "/auth", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		if !p.Authenticated() {
			id, ok := s.desk.svc.OpenTicket(c.Value, true)
			u, uerr := s.Users.Get(r.Context(), id)
			if !ok || uerr != nil || u.Role != ops.RoleAdmin {
				s.desktopPlain(w, r, http.StatusForbidden, "desktop.id_ticket_bad_h", "desktop.id_ticket_bad_p")
				return true
			}
			p, byTicket = ops.Principal{UserID: u.ID, Email: u.Email, Name: u.DisplayName, Role: u.Role, Via: "session"}, true
		}
	}
	if !byTicket {
		var ok bool
		if p, ok = s.adminOnly(w, r); !ok {
			return true
		}
	}
	back := func(msg string) {
		if byTicket {
			s.desktopPlainText(w, r, http.StatusForbidden, s.tr(r, "desktop.id_ticket_bad_h"), msg)
			return
		}
		s.desktopIdentityBack(w, r, msg, nil)
	}
	cl, issuer, _, err := rp.Complete(r.Context(), w, r)
	if err != nil {
		var denied *auth.DeniedError
		msg := s.tr(r, "desktop.id_err_unreachable")
		if errors.As(err, &denied) {
			msg = s.tr(r, "desktop.id_err_refused")
		}
		s.Log.Warn("desktop: the sign-in at the external provider did not complete", "err", err)
		back(msg)
		return true
	}
	switch {
	case cl.Subject == "" || cl.Email == "" || !cl.EmailVerified:
		// Sign-in after the switch needs a verified address, as on any instance: say so now, not after the restart.
		back(s.tr(r, "desktop.id_err_email"))
		return true
	case x.AdminGroup != "" && !contains(cl.Groups, x.AdminGroup):
		back(s.tr(r, "desktop.id_err_group"))
		return true
	}
	proof, err := s.desk.svc.SealProof(r.Context(), s.DB, p.UserID, desktop.Identity{Issuer: issuer, Subject: cl.Subject, Email: strings.ToLower(strings.TrimSpace(cl.Email)), Name: cl.Name})
	if err == nil {
		_, err = s.Reg.Call(r.Context(), p, "desktop.idp.verify", desktop.VerifyIn{Proof: proof})
	}
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
			s.fail(w, r, err)
			return true
		}
		back(e.Message)
		return true
	}
	if byTicket {
		// Nobody is signed in on this browser, and nobody is signed in by this: the page says to go back to the application.
		s.desktopPlain(w, r, http.StatusOK, "desktop.id_ticket_done_h", "desktop.id_ticket_done_p")
		return true
	}
	http.Redirect(w, r, "/desktop/identity?verified=1", http.StatusSeeOther)
	return true
}

// ---- a model on this computer ----------------------------------------------

type desktopModelData struct {
	CSRF   string
	Looked bool
	Found  bool
	Models []desktop.Model
	Added  bool
	Error  string
}

func (s *Server) desktopLocalModelPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminOnly(w, r); !ok {
		return
	}
	d := desktopModelData{CSRF: authFrom(r.Context()).CSRF, Added: r.URL.Query().Get("added") == "1"}
	// Nothing is asked of the model server here: the page only shows what the button's request found.
	if fl := s.flashTake(r); fl != nil {
		d.Error = fl["err"]
		if fl["found"] != "" {
			var out desktop.DetectOut
			if json.Unmarshal([]byte(fl["found"]), &out) == nil {
				d.Looked, d.Found, d.Models = true, out.Found, out.Models
			}
		}
	}
	s.render(w, r, http.StatusOK, "desktop-local-model", s.page(r, s.tr(r, "desktop.lm_title"), d))
}

func (s *Server) uiDesktopLocalModel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	switch r.PathValue("action") {
	case "detect":
		out, err := s.Reg.Call(r.Context(), p, "desktop.localmodel.detect", struct{}{})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if b, err := json.Marshal(out); err == nil {
			s.flashSet(r, map[string]string{"found": string(b)})
		}
		http.Redirect(w, r, "/desktop/local-model#lm-result", http.StatusSeeOther)
	case "use":
		model := strings.TrimSpace(r.PostFormValue("model"))
		if model == "" || len(model) > 200 {
			s.flashSet(r, map[string]string{"err": s.tr(r, "desktop.lm_err_model")})
			http.Redirect(w, r, "/desktop/local-model", http.StatusSeeOther)
			return
		}
		in := aicore.ProviderCreateIn{Name: s.tr(r, "desktop.lm_provider_name"), Kind: "ollama", Scope: "instance", BaseURL: "http://" + desktop.OllamaAddress, Local: true, DefaultModel: model}
		if _, err := s.Reg.Call(r.Context(), p, "ai.provider.create", in); err != nil {
			e := ops.AsError(err)
			if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
				s.fail(w, r, err)
				return
			}
			s.flashSet(r, map[string]string{"err": e.Message})
			http.Redirect(w, r, "/desktop/local-model", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/desktop/local-model?added=1", http.StatusSeeOther)
	default:
		s.notFound(w, r)
	}
}
