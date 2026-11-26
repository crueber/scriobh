package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/shortcuts"
)

// Page is the data every full page receives. Page-specific data is in Data.
type Page struct {
	Title   string
	User    *UserView // nil when signed out
	CSRF    string
	Path    string
	Base    string // BASE URL, for canonical/OIDC display only; links are relative
	Version string
	Data    any
	// What the person's preferences put on <html> before first paint, rendered by
	// the server so no script has to run: theme (light or dark; empty follows the
	// system), density, and motion (reduce or full; empty follows the system).
	Theme, Density, Motion string
	// The light and dark themes in use (empty: Paper and Ink), and the browser chrome colours.
	ThemeLight, ThemeDark string
	ThemeColors           []themeMeta
	Prefs                 map[string]string
	// Banner is set on the pages of a Compendium that is in the trash or archived.
	Banner *compBanner
	// Back is the way back to writing on a Compendium page: the book most recently written that uses it (compBackTo).
	Back *compBack
	// Lang is the language of the interface (the person's, else English) and UIDir its direction; T looks a
	// message up in it. The manuscript's own language and direction are the project's, never these.
	Lang, UIDir string
	// Desktop is desktop mode: the account menu then offers the pages about this computer.
	Desktop bool
	// Bare draws the page without the application's frame (a share link's page, for a person with no account).
	Bare bool
	// Gate is set on a page of a project that has a passphrase and that this session has unlocked: the page covers
	// itself when the project locks (js/gate.js). Nil for every other page.
	Gate *GateInfo
}

// T is the message for key in the page's language, English where the language has none. {name} placeholders
// are filled from (name, value) pairs. Templates call {{$.T "key"}} with $ the page.
func (p Page) T(key string, args ...any) string { return i18n.Default.T(p.Lang, key, args...) }

// N is the message for a count (key.one, key.other), {n} replaced by the count.
func (p Page) N(key string, n int, args ...any) string {
	return i18n.Default.N(p.Lang, key, n, args...)
}

// Msgs is the messages under a prefix, for a page that hands them to its script as data.
func (p Page) Msgs(prefix string) map[string]string { return i18n.Default.With(p.Lang, prefix) }

// UserView is the signed-in user as templates see them.
type UserView struct {
	ID       string
	Name     string
	Email    string
	Role     string
	Initials string
	IsAdmin  bool
	IsWriter bool
}

// tr is a message in the language of the person asking, for the words a handler writes itself (a note after a change, a
// title that is missing). The page's own words come from the template with $.T.
func (s *Server) tr(r *http.Request, key string, args ...any) string {
	return i18n.Default.T(s.page(r, "", nil).Lang, key, args...)
}

// trN is tr for a count: key.one or key.other, with {n} filled in.
func (s *Server) trN(r *http.Request, key string, n int, args ...any) string {
	return i18n.Default.N(s.page(r, "", nil).Lang, key, n, args...)
}

func (s *Server) page(r *http.Request, title string, data any) Page {
	a := authFrom(r.Context())
	p := Page{Title: title, Path: r.URL.Path, Base: s.Cfg.BaseURLString(), Version: s.Cfg.Version, Data: data, CSRF: a.CSRF, Desktop: s.Cfg.Desktop}
	p.ThemeColors = themeMetas(nil)
	p.Lang, p.UIDir = "en", "ltr"
	if m, ok := r.Context().Value(keyGateMark).(*gateMark); ok && m.info != nil && a.P.Authenticated() {
		p.Gate = m.info
	}
	if a.P.Authenticated() && strings.HasPrefix(r.URL.Path, "/c/") {
		p.Banner, p.Back = s.compNav(r)
	}
	if a.P.Authenticated() {
		p.Prefs = account.Prefs(r.Context(), s.DB, a.P.UserID)
		if l, ok := i18n.Default.Resolve(p.Prefs["language"]); ok && (!l.Test || s.testLocales()) {
			p.Lang, p.UIDir = l.Tag, l.Dir
		}
		p.Theme, p.Density, p.Motion = htmlAttrs(p.Prefs)
		p.ThemeLight, p.ThemeDark = themeChoice(p.Prefs)
		p.ThemeColors = themeMetas(p.Prefs)
		p.User = &UserView{ID: a.P.UserID, Name: a.P.Name, Email: a.P.Email, Role: a.P.Role,
			Initials: auth.Initials(a.P.Name), IsAdmin: a.P.Role == ops.RoleAdmin,
			IsWriter: a.P.Role == ops.RoleAdmin || a.P.Role == ops.RoleWriter}
	}
	return p
}

// parseTemplates builds one template set per page: layout + partials + page.
// Files named partials/*.html are shared; a top-level file that does not start
// with "_" is a page; layout.html is the frame.
func (s *Server) parseTemplates(tfs fs.FS) (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"asset":       s.assets.URL,
		"coverClass":  coverClass,
		"avatarClass": func(id string) string { return "avatar-" + strconv.Itoa(avatarIndex(id)) },
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return one
			}
			return many
		},
		"num":     commas,
		"join":    strings.Join,
		"iso":     isolate,
		"int":     func(n int64) int { return int(n) },
		"add":     func(a, b int) int { return a + b },
		"list":    func(v ...string) []string { return v },
		"textdir": textDir,
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
		"keys":         shortcuts.Tokens,
		"kbScope":      shortcuts.ScopeOf,
		"docKind":      docKindLabel,
		"ptabs":        ptabs,
		"inLang":       inLang,
		"outlineSorts": func() []outlineSort { return outlineSorts },
		"ctabs":        ctabs,
		"sha7":         gitShort,
		"utcWhen":      utcWhen,
		"statuses":     func() []string { return statusOrder },
		"statusLabel":  statusLabel,
		"themeColors": func(m []themeMeta) []themeMeta {
			if len(m) == 0 {
				return themeMetas(nil)
			}
			return m
		},
		"docID": func(d *docs.Doc) string {
			if d == nil {
				return ""
			}
			return d.ID
		},
		"palette": func() []projects.PaletteSwatch { return projects.Swatches() },
		"kinds":   func() []projects.KindOption { return projects.KindOptions() },
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, errors.New("dict needs pairs")
			}
			m := make(map[string]any, len(kv)/2)
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, errors.New("dict keys must be strings")
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
	}
	partials, err := fs.Glob(tfs, "partials/*.html")
	if err != nil {
		return nil, err
	}
	pages, err := fs.Glob(tfs, "*.html")
	if err != nil {
		return nil, err
	}
	// The layout and the partials are parsed once and every page and fragment is a clone of that set: parsing
	// them again for each page (and every partial again for each fragment) was a few dozen parses of the same
	// text on every start, about 1.7 s under the race detector, which every server test paid.
	pageBase, err := template.New("layout.html").Funcs(funcs).ParseFS(tfs, append([]string{"layout.html"}, partials...)...)
	if err != nil {
		return nil, fmt.Errorf("template layout.html: %w", err)
	}
	fragBase, err := template.New("partials").Funcs(funcs).ParseFS(tfs, partials...)
	if err != nil {
		return nil, fmt.Errorf("template partials: %w", err)
	}
	out := map[string]*template.Template{}
	for _, pg := range pages {
		if pg == "layout.html" || strings.HasPrefix(pg, "_") {
			continue
		}
		t, err := pageBase.Clone()
		if err == nil {
			t, err = t.ParseFS(tfs, pg)
		}
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", pg, err)
		}
		out[strings.TrimSuffix(pg, ".html")] = t
	}
	// Fragments: each partial is also renderable on its own (htmx responses).
	for _, p := range partials {
		t, err := fragBase.Clone()
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", p, err)
		}
		out["partials/"+strings.TrimSuffix(path.Base(p), ".html")] = t
	}
	return out, nil
}

// Templates returns the names of every parsed page and fragment, for tests.
func (s *Server) Templates() []string {
	var names []string
	for n := range s.tmpl {
		names = append(names, n)
	}
	return names
}

// render writes a full page. Output is buffered so a template error never
// leaves a half-written page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p Page) {
	t, ok := s.tmpl[name]
	if !ok {
		s.Log.Error("missing template", "name", name)
		http.Error(w, "internal error", 500)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		s.Log.Error("render", "name", name, "err", err)
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderBytes renders a page to bytes, for pages built once without a request.
func (s *Server) renderBytes(name string, p Page) ([]byte, error) {
	t, ok := s.tmpl[name]
	if !ok {
		return nil, fmt.Errorf("missing template %s", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fragment renders a partial for an htmx response.
func (s *Server) fragment(w http.ResponseWriter, r *http.Request, status int, name, block string, data any) {
	t, ok := s.tmpl["partials/"+name]
	if !ok {
		s.Log.Error("missing fragment", "name", name)
		http.Error(w, "internal error", 500)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		s.Log.Error("render fragment", "name", name, "err", err)
		http.Error(w, "internal error", 500)
		return
	}
	// A list a page asks for again and again answers with a validator, so a list nobody changed costs a 304 and no
	// body, and the page can tell it is unchanged.
	if status == http.StatusOK && r.Method == http.MethodGet {
		sum := sha256.Sum256(buf.Bytes())
		tag := `"` + hex.EncodeToString(sum[:8]) + `"`
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// fail writes an op error in the form the caller wants: JSON for the API,
// a small page or fragment for browsers.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	e := ops.AsError(err)
	if e.Code == ops.CodeInternal && e.Cause() != nil {
		s.Log.Error("request failed", "route", s.routeOf(r), "err", e.Cause(), "req", r.Context().Value(keyReqID))
	}
	if isMCPPath(r.URL.Path) {
		writeMCPFail(w, e)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || isFetch(r) {
		writeJSONError(w, e)
		return
	}
	if e.Code == ops.CodeRateLimited && e.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
	}
	if e.Code == ops.CodeUnauthenticated {
		if isHTMX(r) {
			w.Header().Set("HX-Redirect", "/auth/login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/auth/login?next="+template.URLQueryEscaper(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	title, msg := "Something went wrong", "Your work is safe. Try again in a moment."
	switch e.Code {
	case ops.CodeNotFound:
		title, msg = "Not found", "There is nothing here, or you do not have access to it."
	case ops.CodeForbidden:
		title, msg = "Not allowed", e.Message
	case ops.CodeRateLimited:
		title, msg = "Slow down a moment", "Too many requests. Try again in a few seconds."
	case ops.CodeInvalid:
		title, msg = "Check that and try again", e.Message
	}
	s.render(w, r, e.Code.HTTPStatus(), "error", s.page(r, title, map[string]any{"Heading": title, "Message": msg, "Code": e.Code.HTTPStatus()}))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, ops.NotFound("page"))
}

func writeJSONError(w http.ResponseWriter, e *ops.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if e.Code == ops.CodeRateLimited && e.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfterSeconds))
	}
	w.WriteHeader(e.Code.HTTPStatus())
	_ = json.NewEncoder(w).Encode(map[string]any{"error": e})
}

func coverClass(hex string) string {
	for i, c := range projects.CoverPalette {
		if strings.EqualFold(c, hex) {
			return "cover-" + strconv.Itoa(i)
		}
	}
	return "cover-0"
}

func avatarIndex(id string) int {
	h := 0
	for _, c := range id {
		h = (h*31 + int(c)) & 0x7fffffff
	}
	return h % len(projects.CoverPalette)
}

// isolate wraps a name a person chose in Unicode isolates (FSI to PDI) so that a name made of right-to-left text, or one that carries an
// override (U+202E), cannot reorder the sentence it is put in: a PDI closes whatever the name left open. Names go through it wherever
// a catalogue sentence or a heading holds one.
func isolate(s string) string { return "\u2068" + s + "\u2069" }
