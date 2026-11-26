package server

import (
	"net/http"
	"sync"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/shortcuts"
)

// The keyboard shortcuts window (F-1250). The fragment is made from the registry (internal/shortcuts) and reads nothing of
// a project: which page the person is on, and whether AI is on and whether they can edit there, come from the page itself,
// in the query. A page that has no editing to speak of (the library, settings) sends no edit, and is shown every key.

type shortcutsView struct {
	Page
	Sections []shortcuts.Section
}

var knownScopes = func() map[string]bool {
	m := map[string]bool{}
	for _, sc := range shortcuts.AllScopes {
		m[sc] = true
	}
	return m
}()

func (s *Server) uiShortcuts(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	q := r.URL.Query()
	scope := q.Get("scope")
	if !knownScopes[scope] {
		scope = shortcuts.ScopeApp
	}
	pg := s.page(r, "", nil) // once: every message below is looked up in this page's language, with no second read of the preferences
	t := func(k string) string { return pg.T(k) }
	v := shortcutsView{Page: pg, Sections: shortcuts.Build(t, scope, q.Get("ai") == "1", q.Get("edit") != "0")}
	// The words follow the person's language, so a shared cache must not keep it; the browser revalidates (the fragment has an ETag).
	w.Header().Set("Cache-Control", "private, no-cache")
	s.fragment(w, r, http.StatusOK, "shortcuts", "shortcuts-dialog", v)
}

var hintCache sync.Map // language -> the palette's key hints, JSON

// KeyHints is the keys the palette and the format bar draw beside a command, as the JSON the palette dialog carries.
func (p Page) KeyHints() string {
	if v, ok := hintCache.Load(p.Lang); ok {
		return v.(string)
	}
	h := shortcuts.Hints(func(k string) string { return p.T(k) })
	hintCache.Store(p.Lang, h)
	return h
}
