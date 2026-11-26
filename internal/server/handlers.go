package server

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func (s *Server) principal(r *http.Request) ops.Principal { return authFrom(r.Context()).P }

// ---- pages -------------------------------------------------------------

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		if s.desktopAutoSignIn(r) {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		s.render(w, r, http.StatusOK, "signin", s.page(r, "Sign in", nil))
		return
	}
	state, q := libraryState(r), trimQ(r)
	d, err := s.libraryData(r, state, q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "library", s.page(r, libraryTitle(state), d))
}

// ---- project UI routes ---------------------------------------------------

type projectForm struct {
	Values map[string]string
	Errors map[string]string
	CSRF   string
	Status int
}

func (s *Server) uiProjectNew(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	s.projectForm(w, r, http.StatusOK, map[string]string{"kind": "blank", "language": "en"}, nil)
}

func (s *Server) projectForm(w http.ResponseWriter, r *http.Request, status int, vals, errs map[string]string) {
	f := projectForm{Values: vals, Errors: errs, CSRF: authFrom(r.Context()).CSRF, Status: status}
	if isHTMX(r) {
		s.fragment(w, r, status, "new-project", "new-project-form", f)
		return
	}
	s.render(w, r, status, "new-project", s.page(r, "New project", f))
}

func (s *Server) checkCSRF(r *http.Request) bool {
	a := authFrom(r.Context())
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" {
		tok = r.PostFormValue("_csrf")
	}
	return a.SessionID != "" && s.Sess.CheckCSRF(a.SessionID, tok)
}

func (s *Server) uiProjectCreate(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r) // signed in and CSRF-checked by uiRoute
	goal, _ := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(r.PostFormValue("word_goal")), ",", ""))
	in := projects.CreateIn{
		Title: r.PostFormValue("title"), Kind: r.PostFormValue("kind"), Subtitle: r.PostFormValue("subtitle"),
		AuthorName: r.PostFormValue("author_name"), Language: r.PostFormValue("language"),
		WordGoal: goal, CoverColor: r.PostFormValue("cover_color"),
	}
	out, err := s.Reg.Call(r.Context(), p, "project.create", in)
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeInvalid {
			vals := map[string]string{"title": in.Title, "kind": in.Kind, "subtitle": in.Subtitle, "author_name": in.AuthorName,
				"language": in.Language, "word_goal": r.PostFormValue("word_goal"), "cover_color": in.CoverColor}
			field := e.Field
			if field == "" {
				field = "form"
			}
			s.projectForm(w, r, http.StatusUnprocessableEntity, vals, map[string]string{field: e.Message})
			return
		}
		s.fail(w, r, err)
		return
	}
	dest := "/p/" + out.(projects.Item).Item.Addressed().Slug
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// ---- auth ----------------------------------------------------------------

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.principal(r).Authenticated() {
		http.Redirect(w, r, auth.SafeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	if s.desktopLoginGate(w, r) {
		return
	}
	dest, err := s.OIDC.Begin(r.Context(), w, r.URL.Query().Get("next"))
	if err != nil {
		s.signInFailed(w, r, err)
		return
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if s.desktopLinkCallback(w, r) {
		return
	}
	cl, issuer, next, err := s.OIDC.Complete(r.Context(), w, r)
	if err != nil {
		s.signInFailed(w, r, err)
		return
	}
	res, err := s.Users.SignIn(r.Context(), issuer, cl)
	if err != nil {
		s.Events.Audit(r.Context(), "", ops.AuditEntry{Action: "auth.denied", Detail: map[string]any{"reason": err.Error()}}, s.Sess.IPHash(auth.ClientIP(r, s.Cfg.TrustProxy)))
		s.signInFailed(w, r, err)
		return
	}
	ip := auth.ClientIP(r, s.Cfg.TrustProxy)
	tok, exp, err := s.Sess.Create(r.Context(), res.User.ID, r.UserAgent(), ip)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.Sess.SetCookie(w, tok, exp)
	s.Events.Audit(r.Context(), res.User.ID, ops.AuditEntry{Action: "auth.login", TargetType: "user", TargetID: res.User.ID}, s.Sess.IPHash(ip))
	if res.OfferAdminClaim && s.Cfg.DesktopEmbedded() {
		// The first-user rule, without its page: the local profile is the only person there is, so the claim the
		// first user is offered is made for them. It is the same claim (Users.ClaimAdmin), open once.
		if err := s.Users.ClaimAdmin(r.Context(), res.User.ID); err == nil {
			s.Events.Audit(r.Context(), res.User.ID, ops.AuditEntry{Action: "auth.admin_claimed", TargetType: "user", TargetID: res.User.ID}, "")
			res.OfferAdminClaim = false
		}
	}
	if res.OfferAdminClaim {
		http.Redirect(w, r, "/auth/claim-admin", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, auth.SafeNext(next), http.StatusSeeOther)
}

func (s *Server) signInFailed(w http.ResponseWriter, r *http.Request, err error) {
	var denied *auth.DeniedError
	var idp *auth.ErrIdPUnavailable
	var mismatch *auth.ErrIssuerMismatch
	var rejected *auth.ErrClientRejected
	switch {
	case errors.As(err, &mismatch):
		// A configuration error only the operator can fix. The page names the setting and the configured value
		// (the operator's own string); what the provider reported is for the log, because it came from the network.
		s.Log.Error("sign-in is misconfigured: the identity provider's issuer does not match SCRIOBH_OIDC_ISSUER",
			"configured_issuer", mismatch.Configured, "reported_issuer", mismatch.Reported, "err", mismatch.Err)
		s.render(w, r, http.StatusInternalServerError, "error", s.page(r, "Sign-in is not set up correctly", map[string]any{
			"Heading": "Sign-in is not set up correctly",
			"Message": "The identity provider reports a different issuer than the one this instance is configured with (SCRIOBH_OIDC_ISSUER is " + mismatch.Configured +
				"). The two must match exactly, down to a trailing slash. Ask the person who runs Scríobh to fix it; the server log shows both values. People who are already signed in can keep working.",
			"Code": 500, "Retry": "/auth/login"}))
	case errors.As(err, &rejected):
		s.Log.Error("sign-in is misconfigured: the identity provider rejected this application's client credentials",
			"issuer", rejected.Issuer, "client_id", rejected.ClientID, "provider_error", rejected.Code)
		s.render(w, r, http.StatusInternalServerError, "error", s.page(r, "Sign-in is not set up correctly", map[string]any{
			"Heading": "Sign-in is not set up correctly",
			"Message": "The identity provider at " + rejected.Issuer + " did not accept this instance's client ID or client secret (SCRIOBH_OIDC_CLIENT_ID, SCRIOBH_OIDC_CLIENT_SECRET). " +
				"Ask the person who runs Scríobh to check them against the provider's application settings. People who are already signed in can keep working.",
			"Code": 500, "Retry": "/auth/login"}))
	case errors.As(err, &denied):
		s.render(w, r, http.StatusForbidden, "error", s.page(r, "Sign-in refused", map[string]any{
			"Heading": "Sign-in refused", "Message": denied.Reason, "Code": 403, "Retry": "/auth/login"}))
	case errors.As(err, &idp):
		s.Log.Warn("identity provider unreachable", "issuer", idp.Issuer, "err", idp.Err)
		s.render(w, r, http.StatusBadGateway, "error", s.page(r, "Cannot reach the identity provider", map[string]any{
			"Heading": "Cannot reach the identity provider",
			"Message": "Scríobh signs in through " + idp.Issuer + ", which did not answer. People who are already signed in can keep working. Try again in a moment.",
			"Code":    502, "Retry": "/auth/login"}))
	default:
		s.Log.Error("sign-in", "err", err)
		s.fail(w, r, err)
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	a := authFrom(r.Context())
	if a.SessionID != "" {
		if !s.checkCSRF(r) {
			s.fail(w, r, ops.Forbidden("This form expired. Reload the page and try again."))
			return
		}
		if c, err := r.Cookie(auth.SessionCookie); err == nil {
			_ = s.Sess.Revoke(r.Context(), c.Value)
		}
		if pr := s.presenceService(); pr != nil {
			pr.ForgetSession(a.SessionID) // signing out takes the person off the lists now
		}
		s.Events.Audit(r.Context(), a.P.UserID, ops.AuditEntry{Action: "auth.logout", TargetType: "user", TargetID: a.P.UserID}, "")
	}
	s.Sess.ClearCookie(w)
	s.desktopAfterLogout(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) claimPage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	if !s.Users.AdminClaimOpen(r.Context(), p.UserID) {
		s.fail(w, r, ops.NotFound("page"))
		return
	}
	s.render(w, r, http.StatusOK, "claim", s.page(r, "Claim administration", map[string]any{
		"Name": p.Name, "Email": p.Email, "Issuer": s.OIDC.Issuer(),
	}))
}

func (s *Server) claimSubmit(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r) {
		s.fail(w, r, ops.Forbidden("This form expired. Reload the page and try again."))
		return
	}
	if err := s.Users.ClaimAdmin(r.Context(), p.UserID); err != nil {
		var denied *auth.DeniedError
		if errors.As(err, &denied) {
			s.fail(w, r, ops.NotFound("page"))
			return
		}
		s.fail(w, r, err)
		return
	}
	s.Events.Audit(r.Context(), p.UserID, ops.AuditEntry{Action: "auth.admin_claimed", TargetType: "user", TargetID: p.UserID}, "")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// uiRoute registers a state-changing UI route. Every such route goes through
// here, so a session and the per-session CSRF token are enforced in one place
// and the authorization tests can check every declared route without knowing
// what it does. Handlers receive a parsed form.
func (s *Server) uiRoute(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if !s.principal(r).Authenticated() {
			s.fail(w, r, ops.Unauthenticated())
			return
		}
		if err := r.ParseForm(); err != nil {
			s.fail(w, r, ops.Invalid("", "The form could not be read."))
			return
		}
		if !s.checkCSRF(r) {
			// The reason is in the data, so that a script can tell a token that went stale (ask for a new one) from a person who may not.
			s.fail(w, r, &ops.Error{Code: ops.CodeForbidden, Message: "This form expired. Reload the page and try again.", Data: map[string]any{"reason": "csrf"}})
			return
		}
		h(w, r)
	})
}

// bodySlotCount is how many pictures' bodies the server reads at once through uiRouteStream. A body is read a part at a
// time and one picture is held while it is checked, so two at once is at most two pictures and their cleaning.
const bodySlotCount = 2

// The pace a cover body must keep: no more than bodyIdle spent waiting for a byte (a connection that sends nothing for
// that long is dropped, however much time the absolute limits would leave), no more than bodyCeiling in all, and, once
// bodyGrace of waiting has gone by, an average of at least bodyFloor bytes for each second spent waiting. The ceiling lets
// a writer on a slow uplink send a 25 MB picture for as long as bytes keep coming; the floor is what stops a sender who
// dribbles a byte every few seconds, never idle for long enough to be dropped, from holding a place until the ceiling.
// writeGrace is what the reply may take after the last allowed moment.
//
// The floor is 32 KiB/s: a picture of the default largest size (25 MiB) at that rate takes 13.3 minutes, inside the
// ceiling, so a real slow uplink that can finish a largest picture at all is never below it; a slower one could not
// finish it inside 15 minutes anyway (25 MiB in 15 minutes is 28.4 KiB/s). It is measured over the time spent waiting
// inside Read, never over the time the server spends working between reads.
const (
	bodyIdle    = 15 * time.Second
	bodyCeiling = 15 * time.Minute
	bodyGrace   = 30 * time.Second
	bodyFloor   = 32 << 10 // bytes a second
	writeGrace  = time.Minute
)

// errTooSlow ends a body that outlived its ceiling or kept below the floor.
var errTooSlow = errors.New("the upload took longer than the server allows")

// pacedBody is a request body whose read deadline follows its progress: http.Server's ReadTimeout is an absolute limit
// for the whole request, which a slow uplink would run into and a trickling sender would hold a slot until, so for this
// route each Read is given bodyIdle to be answered (up to a ceiling), and a sender who stays below a floor is dropped.
// The deadline is set when a Read starts, not when bytes last arrived, so the limits measure the time spent waiting for the
// sender and not the time the server spends on what it has already read.
type pacedBody struct {
	io.ReadCloser
	rc            *http.ResponseController
	start         time.Time
	idle, ceiling time.Duration
	grace         time.Duration // waiting before the floor applies
	floor         int64         // bytes a second, over the time spent waiting
	waited        time.Duration // spent inside Read so far
	got           int64
	done          bool
}

// arm sets the write deadline once (the reply, however long the body took).
func (b *pacedBody) arm() {
	_ = b.rc.SetWriteDeadline(b.start.Add(b.ceiling + writeGrace))
}

func (b *pacedBody) Read(p []byte) (int, error) {
	if b.done {
		return b.ReadCloser.Read(p)
	}
	now := time.Now()
	if now.Sub(b.start) > b.ceiling {
		return 0, errTooSlow
	}
	dl := now.Add(b.idle)
	if end := b.start.Add(b.ceiling); dl.After(end) {
		dl = end
	}
	_ = b.rc.SetReadDeadline(dl)
	n, err := b.ReadCloser.Read(p)
	b.waited += time.Since(now)
	b.got += int64(n)
	switch {
	case err == io.EOF:
		// the body is whole: no read deadline may cancel the request while it is being worked on
		b.done = true
		_ = b.rc.SetReadDeadline(time.Time{})
	case err == nil && b.floor > 0 && b.waited > b.grace && float64(b.got) < float64(b.floor)*b.waited.Seconds():
		return 0, errTooSlow
	}
	return n, err
}

// bodyOwner holds the users who have a cover body being read, one each.
type bodyOwner struct {
	mu sync.Mutex
	in map[string]bool
}

func (o *bodyOwner) take(user string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.in[user] {
		return false
	}
	if o.in == nil {
		o.in = map[string]bool{}
	}
	o.in[user] = true
	return true
}

func (o *bodyOwner) release(user string) {
	o.mu.Lock()
	delete(o.in, user)
	o.mu.Unlock()
}

// uiRouteStream registers a UI route whose form carries pictures (the cover page). It does what uiRoute does in another
// order, so that nothing of a large body costs anything for someone who may not send it:
//
//  1. the session;
//  2. authorize, the route's own check of the project and the caller's role, which reads no body (it answers the
//     request itself when it refuses, and may hand back the request with what it learned in its context);
//  3. the CSRF token, when it is in the X-CSRF-Token header (a wrong one is refused here);
//  4. a place: one body at a time for each user, and at most bodySlotCount at once in all, so that one person cannot hold
//     every place; when there is none, busy answers (429) in the page's own words;
//  5. the body's pace (pacedBody): a read deadline that moves with the bytes, in place of the absolute ReadTimeout;
//  6. the CSRF token when it is the form's first field, which must be _csrf (a small field, read with a limit);
//  7. only then the body: h reads it a part at a time from the multipart reader (nil for a form that is not multipart,
//     whose fields are already in r.PostForm). Nothing of a multipart body is parsed whole. The handler decides what a part goes to: the cover form holds one picture at a time; the import form
//     writes a part it must read before the rest of the form to a file of its own, which it removes on every way out (and data/tmp is swept of
//     the ones a dead request left, imports.SweepTmp).
//
// A route that does not carry pictures uses uiRoute.
func (s *Server) uiRouteStream(pattern string, authorize func(http.ResponseWriter, *http.Request) (*http.Request, bool),
	busy func(w http.ResponseWriter, r *http.Request, own bool), h func(http.ResponseWriter, *http.Request, *multipart.Reader)) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if !s.principal(r).Authenticated() {
			s.fail(w, r, ops.Unauthenticated())
			return
		}
		r, ok := authorize(w, r)
		if !ok {
			return
		}
		expired := ops.Forbidden("This form expired. Reload the page and try again.")
		a := authFrom(r.Context())
		tok := r.Header.Get("X-CSRF-Token")
		if tok != "" && !(a.SessionID != "" && s.Sess.CheckCSRF(a.SessionID, tok)) {
			s.fail(w, r, expired)
			return
		}
		user := s.principal(r).UserID
		if !s.bodyUsers.take(user) {
			busy(w, r, true)
			return
		}
		defer s.bodyUsers.release(user)
		select {
		case s.bodySlots <- struct{}{}:
			defer func() { <-s.bodySlots }()
		default:
			busy(w, r, false)
			return
		}
		pb := &pacedBody{ReadCloser: r.Body, rc: http.NewResponseController(w), start: time.Now(),
			idle: s.bodyIdle, ceiling: s.bodyCeiling, grace: s.bodyGrace, floor: s.bodyFloor}
		pb.arm()
		r.Body = pb
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "multipart/form-data" {
			if err := r.ParseForm(); err != nil {
				s.fail(w, r, ops.Invalid("", "The form could not be read."))
				return
			}
			if !s.checkCSRF(r) {
				s.fail(w, r, expired)
				return
			}
			h(w, r, nil)
			return
		}
		mr, err := r.MultipartReader()
		if err != nil {
			s.fail(w, r, ops.Invalid("", "The form could not be read."))
			return
		}
		if tok == "" {
			part, err := mr.NextPart()
			if err != nil || part.FormName() != "_csrf" || part.FileName() != "" {
				s.fail(w, r, expired)
				return
			}
			b, _ := io.ReadAll(io.LimitReader(part, 256))
			if !(a.SessionID != "" && s.Sess.CheckCSRF(a.SessionID, string(b))) {
				s.fail(w, r, expired)
				return
			}
		}
		h(w, r, mr)
	})
}
