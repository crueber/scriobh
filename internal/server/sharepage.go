package server

import (
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/domain/share"
	"github.com/crueber/scriobh/internal/i18n"
)

// The public page of a share link. A browser that navigates to /s/{token} (it asks for text/html) gets a page; the
// same address answers the JSON manifest to a caller that does not, exactly as before, so nothing that used it
// changes. There are two pages and no more:
//
//   - the shell, for a link that opens: it holds nothing of the project, not even its title. The script reads the
//     link from the address and fetches the contents through /s/{token}/api/..., sending the passphrase's proof as
//     a header kept in memory or sessionStorage (a navigation cannot send one, so this page cannot be the thing
//     that is locked: it is the same for every link that opens, and the locked part is what it fetches).
//   - the gone page, for everything else: a token that opens nothing, a revoked or expired link, a project in the
//     trash, a path that is not an endpoint. One body, whatever the reason: it never says which.
//
// Neither page has the application's frame, a cookie, a script from anywhere but /static, or a link into the
// application. Referrer-Policy, noindex, no-store and the CSP come from the same middleware as every /s/ answer.

// wantsHTML reports whether the caller is a browser navigating, not a script reading JSON.
func wantsHTML(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html")
}

// sharePageLang is the language of a stranger's page: the first language of Accept-Language the catalogue has,
// else English. Nothing is stored.
func sharePageLang(r *http.Request, dev bool) (string, string) {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if len(tag) > 20 || tag == "" {
			continue
		}
		if l, ok := i18n.Default.Resolve(tag); ok && (!l.Test || dev) { // the pseudo-locales are for a development build
			return l.Tag, l.Dir
		}
	}
	return "en", "ltr"
}

func (s *Server) renderSharePage(w http.ResponseWriter, r *http.Request, status int, name, title string) {
	lang, dir := sharePageLang(r, s.testLocales())
	p := Page{Lang: lang, UIDir: dir, Bare: true, Version: s.Cfg.Version}
	p.Title = i18n.Default.T(lang, title)
	s.render(w, r, status, name, p)
}

// shareGonePage is the one answer for a link that opens nothing.
func (s *Server) shareGonePage(w http.ResponseWriter, r *http.Request) {
	s.renderSharePage(w, r, http.StatusNotFound, "share-gone", "sh.gone_h")
}

// shareShell is the page of a link that opens. Opening it is not a view (the manifest the script then fetches is).
func (s *Server) shareShell(w http.ResponseWriter, r *http.Request, _ share.Link) {
	s.renderSharePage(w, r, http.StatusOK, "share", "sh.page_title")
}
