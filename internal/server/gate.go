package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The project passphrase gate on the pages (F-252). The op layer asks the gate for every op that names a project
// (internal/ops/gate.go); this is the same question asked of a request before any page handler runs, so a page
// that reads the database itself, or a fragment written later, is closed too. The two ask the same function and
// cannot disagree about whether a project is locked.

// withCaller records who is asking and from which address bucket in the request's context, for every function that
// gets only a context: the gate-aware role function and the passphrase lockout. The bucket is a keyed hash, never
// the address.
func (s *Server) withCaller(r *http.Request) context.Context {
	ctx := context.WithValue(r.Context(), keyGateMark, &gateMark{})
	if p := authFrom(ctx).P; p.Authenticated() {
		ctx = ops.WithPrincipal(ctx, p)
		// A page's own upkeep (renewing the lease on a chapter, asking whether the project is still open) says it is not
		// a person's use, so a page left open does not keep an unlock alive past its idle limit. Only a browser session
		// may say so: a key's calls are counted whatever it claims.
		if r.Header.Get(backgroundHeader) == "1" && p.SessionID != "" && p.KeyID == "" {
			ctx = ops.Passive(ctx)
		}
	}
	ip := shareClient(auth.ClientIP(r, s.Cfg.TrustProxy))
	return ops.WithClient(ctx, s.Sess.IPHash(ip))
}

// backgroundHeader marks a request a page makes by itself, not for the person (see withCaller).
const backgroundHeader = "X-Scriobh-Background"

// GateInfo is what a page of a protected project that this session has unlocked needs to cover itself (js/gate.js):
// the project's id, title and address, and the idle limit in minutes. It holds nothing else of the project.
type GateInfo struct {
	ID, Slug, Title string
	Idle            int
}

// gateMark is filled in by pageGate when the page's project is protected and open, for Server.page to carry into the layout.
type gateMark struct{ info *GateInfo }

// noteGate records that the request's project is protected and unlocked for this session.
func (s *Server) noteGate(r *http.Request, p ops.Principal, id string) {
	m, _ := r.Context().Value(keyGateMark).(*gateMark)
	g := s.gateService()
	if m == nil || g == nil || p.SessionID == "" || p.KeyID != "" {
		return
	}
	st, err := g.States(ops.Passive(r.Context()), p, []string{id})
	if err != nil || !st[id].Set || st[id].Locked {
		return
	}
	var title, slug string
	_ = s.DB.Read().QueryRowContext(r.Context(), `SELECT title, slug FROM projects WHERE id = ?`, id).Scan(&title, &slug)
	m.info = &GateInfo{ID: id, Slug: s.gateAddress(r, id, slug), Title: title, Idle: st[id].Idle}
}

// gatedPage matches the addresses that name a project: a slug page, or an id on a form or fragment route. The
// capture groups are the kind and the address or id.
//
// The REST shapes (/api/v1/projects/{id}, /api/v1/docs/{id}) are asked too, though every op route there is gated by the
// registry: a route written by hand under them, which the registry does not see, is closed by its address.
var gatedPage = regexp.MustCompile(`^/(?:(p)/([^/]+)|ui/(projects|docs)/([A-Za-z0-9_]+)|(files)/([A-Za-z0-9_]+)|api/v1/(projects|docs)/([A-Za-z0-9_]+))(?:/|$)`)

// gateExemptPage matches the pages that answer a locked member: the unlock page, the forms that unlock and lock,
// and the owner's passphrase settings.
var gateExemptPage = regexp.MustCompile(`^/(?:p/[^/]+/(?:unlock|passphrase)|(?:ui|api/v1)/projects/[A-Za-z0-9_]+/(?:unlock|lock|passphrase))(?:/|$)`)

// pageProject finds the project a page address names, for a signed-in caller who is a member of it. ok is false
// for an address that names no project, or one the caller is not a member of: the handler answers that itself.
func (s *Server) pageProject(r *http.Request, p ops.Principal) (id string, ok bool) {
	m := gatedPage.FindStringSubmatch(r.URL.Path)
	if m == nil || gateExemptPage.MatchString(r.URL.Path) {
		return "", false
	}
	ctx := r.Context()
	switch {
	case m[1] == "p":
		id, ok = projects.IDBySlug(ctx, s.DB, p.UserID, m[2])
	case m[3] == "projects":
		id, ok = m[4], true
	case m[3] == "docs":
		id = s.lookup(ctx, `SELECT project_id FROM documents WHERE id = ?`, m[4])
		ok = id != ""
	case m[7] == "projects":
		id, ok = m[8], true
	case m[7] == "docs":
		id = s.lookup(ctx, `SELECT project_id FROM documents WHERE id = ?`, m[8])
		ok = id != ""
	case m[5] == "files":
		id = s.lookup(ctx, `SELECT COALESCE(project_id,'') FROM attachments WHERE id = ?`, m[6])
		ok = id != ""
	}
	return id, ok
}

// pageGate refuses a page of a project the caller has not unlocked. It answers nil for everything else; a caller
// who is not a member gets the 404 the handler gives, whether or not the project has a passphrase.
func (s *Server) pageGate(r *http.Request) error {
	p := s.principal(r)
	if !p.Authenticated() {
		return nil
	}
	id, ok := s.pageProject(r, p)
	if !ok {
		return nil
	}
	env := s.Reg.Env()
	if env == nil || env.ProjectGate == nil || env.ProjectMember == nil {
		return nil
	}
	// A key pinned elsewhere is answered by the op layer with the 404 for what its pin does not reach: the gate must not
	// name another project's title to it.
	if p.CompendiumID != "" || (p.ProjectID != "" && p.ProjectID != id) {
		return nil
	}
	if _, member := env.ProjectMember(r.Context(), p.UserID, id); !member {
		return nil
	}
	if r.Header.Get("Accept") == "text/event-stream" || (isPresencePath(r.URL.Path) && p.KeyID == "") { // a stream, and a presence beat, are not activity
		return env.ProjectGate(ops.Passive(r.Context()), p, id)
	}
	// An early check: a key's use is counted by the call itself, a refusal here (where the call never comes) is counted now.
	err := env.ProjectGate(ops.Precheck(r.Context()), p, id)
	if err == nil && !strings.HasPrefix(r.URL.Path, "/api/") && !isHTMX(r) && !isFetch(r) {
		s.noteGate(r, p, id)
	}
	return err
}

// failLocked answers a refused page. A person at a page is sent to the unlock page, which brings them back; a
// fragment or a script gets the JSON refusal, and an htmx request a redirect, as sign-in does.
func (s *Server) failLocked(w http.ResponseWriter, r *http.Request, err error) {
	e := ops.AsError(err)
	reason, _ := ops.IsLocked(err)
	if reason != ops.ReasonProjectLocked {
		s.fail(w, r, err)
		return
	}
	slug, _ := e.Data["slug"].(string)
	id, _ := e.Data["project_id"].(string)
	dest := "/p/" + s.gateAddress(r, id, slug) + "/unlock?next=" + url.QueryEscape(withoutFind(r.URL.RequestURI()))
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/") || isFetch(r):
		writeJSONError(w, e)
	case isHTMX(r):
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusForbidden)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		http.Redirect(w, r, dest, http.StatusSeeOther)
	default:
		s.fail(w, r, err)
	}
}

// gateAddress is what a page address names the project by for this caller: the slug, or slug~suffix when another
// project they can read has it (see projects.Project.Address).
func (s *Server) gateAddress(r *http.Request, id, slug string) string {
	if got, ok := projects.IDBySlug(r.Context(), s.DB, s.principal(r).UserID, slug); ok && got == id {
		return slug
	}
	if len(id) >= 6 {
		return slug + "~" + id[len(id)-6:]
	}
	return slug
}

// gateService is the running gate, for shutdown.
func (s *Server) gateService() *projectgate.Service {
	g, _ := ops.Service[*projectgate.Service](s.Reg.Env(), projectgate.ServiceName)
	return g
}

// ---- the unlock page and its forms -------------------------------------------------------------------

func (s *Server) registerGate(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/unlock", s.gateUnlockPage)
	s.uiRoute("POST /ui/projects/{id}/unlock", s.uiGateUnlock)
	s.uiRoute("POST /ui/projects/{id}/lock", s.uiGateLock)
}

type gateData struct {
	ID, Title, Next, Error, Slug string
	Waiting                      bool // the lockout is running: the page says how long, and that the right passphrase waits too
}

// safeNext keeps the page the person was sent from only if it is a page of this project. The path is looked at as the server will
// read it: a dot segment (or its percent-encoded form) or a backslash anywhere in it sends the person to the project's home.
func safeNext(next, address string) string {
	home := "/p/" + address
	if next != home && !strings.HasPrefix(next, home+"/") && !strings.HasPrefix(next, home+"?") {
		return home
	}
	if strings.HasPrefix(next, home+"/unlock") {
		return home
	}
	path := next
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if dec, err := url.PathUnescape(path); err != nil || strings.Contains(dec, "\\") {
		return home
	} else {
		for _, seg := range strings.Split(dec, "/") {
			if seg == ".." || seg == "." {
				return home
			}
		}
	}
	return next
}

// withoutFind removes a search-by-meaning passage from an address: the first words of the manuscript must not travel into the unlock
// page's address and history.
func withoutFind(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	q := u.Query()
	if !q.Has("find") {
		return uri
	}
	q.Del("find")
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) gateUnlockPage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	slug := r.PathValue("slug")
	id, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, slug)
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "project.passphrase.get", projectgate.ProjectIn{ID: id})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	next := safeNext(r.URL.Query().Get("next"), slug)
	if st := out.(projectgate.StatusOut).Item; !st.Protected || st.Unlocked {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "project-unlock", s.page(r, "Unlock", gateData{ID: id, Title: s.lookup(r.Context(), `SELECT title FROM projects WHERE id = ?`, id), Next: next, Slug: slug}))
}

func (s *Server) uiGateUnlock(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	id := r.PathValue("id")
	_, err := s.Reg.Call(r.Context(), p, "project.unlock", projectgate.UnlockIn{ID: id, Passphrase: r.PostFormValue("passphrase")})
	slug := s.lookup(r.Context(), `SELECT slug FROM projects WHERE id = ?`, id)
	address := s.gateAddress(r, id, slug)
	next := safeNext(r.PostFormValue("next"), address)
	if err != nil {
		e := ops.AsError(err)
		changed := e.Code == ops.CodeConflict && e.Data["reason"] == "passphrase_changed"
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeRateLimited && !changed {
			s.fail(w, r, err)
			return
		}
		pg := s.page(r, "Unlock", nil)
		msg := pg.T("project.gate.wrong")
		status := e.Code.HTTPStatus()
		if e.Code == ops.CodeRateLimited {
			msg = s.gateWait(r, e.RetryAfterSeconds)
			if e.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", itoa(e.RetryAfterSeconds))
			}
		} else if changed {
			msg = pg.T("project.gate.changed")
		} else if e.Field != "passphrase" {
			msg = e.Message // an empty or over-long try: the op's own words
		}
		pg.Data = gateData{ID: id, Title: s.lookup(r.Context(), `SELECT title FROM projects WHERE id = ?`, id), Next: next, Error: msg, Slug: address, Waiting: e.Code == ops.CodeRateLimited && e.RetryAfterSeconds > 0}
		s.render(w, r, status, "project-unlock", pg)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", next)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) uiGateLock(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "project.lock", projectgate.ProjectIn{ID: r.PathValue("id")}); err != nil {
		s.fail(w, r, err)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
