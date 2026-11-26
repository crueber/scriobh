package server

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/shortcuts"
	"github.com/crueber/scriobh/internal/testutil"
)

// The shortcuts window (F-1250) is a fragment made from the registry. It names no project and reads none, so what the server
// decides is who may have it (anyone signed in, of any role), which sections it shows first, and that it holds nothing a page
// may not: no script, no style attribute.

var reRow = regexp.MustCompile(`data-row="([a-z0-9_]+)"`)

func shownRows(body string) []string {
	var ids []string
	for _, m := range reRow.FindAllStringSubmatch(body, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

func TestAuthzShortcutsFragment(t *testing.T) {
	s := newAuthzServer(t)
	if rec := s.do(caller{name: "anonymous"}, "GET", "/ui/shortcuts?scope=write", ""); rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound && rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d, want a sign-in", rec.Code)
	} else if rec.Code != http.StatusUnauthorized && !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
		t.Errorf("anonymous is sent to %q", rec.Header().Get("Location"))
	}
	// a script that asks (X-Scriobh-Fetch) is told 401, not sent through a sign-in page it would put in the dialog
	req := httptest.NewRequest("GET", "/ui/shortcuts?scope=write", nil)
	req.Header.Set("X-Scriobh-Fetch", "1")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous script: %d, want 401", rec.Code)
	}
	for _, role := range []string{ops.RoleAdmin, ops.RoleWriter, ops.RoleReader} {
		c := s.newCaller(t, "kb-"+role, role)
		rec := s.do(c, "GET", "/ui/shortcuts?scope=app", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", role, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "private, no-cache" {
			t.Errorf("%s: Cache-Control %q", role, cc)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("%s: no validator", role)
		}
	}
}

func TestShortcutsFragmentIsMadeFromTheRegistry(t *testing.T) {
	s := newAuthzServer(t)
	c := s.newCaller(t, "kb-writer", ops.RoleWriter)
	get := func(q string) string {
		rec := s.do(c, "GET", "/ui/shortcuts?"+q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", q, rec.Code)
		}
		return rec.Body.String()
	}
	body := get("scope=write&ai=1&edit=1")
	// the sections of the page come first, in the registry's order, and are marked
	order := regexp.MustCompile(`class="kb-group( is-here)?" aria-labelledby="kb-g-([a-z]+)"`).FindAllStringSubmatch(body, -1)
	var ids []string
	for _, m := range order {
		ids = append(ids, m[2])
		if (m[1] != "") != (m[2] == "write" || m[2] == "undo" || m[2] == "alts" || m[2] == "ai" || m[2] == "chapters" || m[2] == "compendium" || m[2] == "comments" || m[2] == "tree") {
			t.Errorf("%s: marked as this page or not wrongly: %q", m[2], m[1])
		}
	}
	if len(ids) < 8 || ids[0] != "write" || ids[len(ids)-1] != "app" {
		t.Errorf("sections for the writing view: %v", ids)
	}
	if !strings.Contains(get("scope=read"), `aria-labelledby="kb-g-read"`) || !regexp.MustCompile(`(?s)<section class="kb-group is-here" aria-labelledby="kb-g-read"`).MatchString(get("scope=read")) {
		t.Error("the reading view does not put its keys first")
	}
	for sc, want := range map[string]string{"outline": "outline", "cards": "cards", "status": "status", "research": "research", "timeline": "timeline"} {
		if first := regexp.MustCompile(`aria-labelledby="kb-g-([a-z]+)"`).FindStringSubmatch(get("scope=" + sc + "&edit=1"))[1]; first != want {
			t.Errorf("%s starts with %s", sc, first)
		}
		// a person who cannot edit there is shown none of its keys: the script that has them is not loaded for them
		if strings.Contains(get("scope="+sc+"&edit=0"), `aria-labelledby="kb-g-`+want+`"`) {
			t.Errorf("%s: a reader is shown the %s section", sc, want)
		}
	}
	if first := regexp.MustCompile(`aria-labelledby="kb-g-([a-z]+)"`).FindStringSubmatch(get("scope=nonsense"))[1]; first != "app" {
		t.Errorf("an unknown scope starts with %s", first)
	}

	// AI off leaves the writing partner's rows out, and edit=0 leaves what needs editing out
	noAI := get("scope=write&ai=0&edit=1")
	if strings.Contains(noAI, `data-row="ai_menu"`) || strings.Contains(noAI, `kb-g-ai"`) {
		t.Error("ai=0 still shows the writing partner")
	}
	if !strings.Contains(body, `data-row="ai_menu"`) || !strings.Contains(body, `data-row="ai_retry"`) {
		t.Error("ai=1 lacks the writing partner")
	}
	if strings.Contains(get("scope=write&ai=1&edit=0"), `data-row="bold"`) || !strings.Contains(get("scope=write&ai=1&edit=0"), `data-row="switcher"`) {
		t.Error("edit=0 leaves bold in, or drops the keys a reader has")
	}

	// every row shown is a registry entry, and with AI and edit on every entry is shown across the scopes
	seen := map[string]bool{}
	for _, sc := range shortcuts.AllScopes {
		for _, id := range shownRows(get("scope=" + sc + "&ai=1&edit=1")) {
			if _, ok := shortcuts.ByID(id); !ok {
				t.Errorf("%s shows %q, which is not in the registry", sc, id)
			}
			seen[id] = true
		}
	}
	var missing []string
	for _, e := range shortcuts.Entries {
		if !seen[e.ID] {
			missing = append(missing, e.ID)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("never shown: %v", missing)
	}

	// the caps: one kbd to a key, with the modifier named for the Mac swap; the words are the catalogue's
	for _, want := range []string{
		`<kbd data-k="Alt">Alt</kbd><kbd data-k="Shift">Shift</kbd><kbd data-code="KeyB">B</kbd>`,
		`<kbd data-k="Mod">Ctrl</kbd><kbd data-k="Shift">Shift</kbd><kbd>Z</kbd>`,
		`<span class="kb-alt" data-os="mac" hidden>`, // the Mac's Backspace for the trash
		`<dt>Split the chapter at the cursor</dt>`,
		`<span class="sr-only">Up arrow</span>`,
		`id="dialog-title"`, `data-dialog-close`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the fragment lacks %s", want)
		}
	}
	if strings.Contains(body, "<script") || strings.Contains(body, " style=") || regexp.MustCompile(`\son[a-z]+="`).MatchString(body) {
		t.Error("an inline script, handler or style")
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<header") {
		t.Error("a fragment, not a page")
	}
}

// The fragment is asked for on first use, so its time is what the person waits for: about seventy rows from one page and one pass.
func TestShortcutsFragmentIsQuick(t *testing.T) {
	testutil.Timing(t)
	s := newAuthzServer(t)
	c := s.newCaller(t, "kb-quick", ops.RoleWriter)
	first := s.do(c, "GET", "/ui/shortcuts?scope=write&ai=1&edit=1", "")
	t.Logf("fragment: %d bytes, %d gzip", first.Body.Len(), gzipSize(first.Body.Bytes()))
	const n = 60
	var d []time.Duration
	for i := 0; i < n; i++ {
		t0 := time.Now()
		rec := s.do(c, "GET", "/ui/shortcuts?scope=write&ai=1&edit=1", "")
		d = append(d, time.Since(t0))
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Code)
		}
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	p95 := d[n*95/100]
	t.Logf("fragment: median %v, p95 %v", d[n/2], p95)
	if p95 > 20*time.Millisecond {
		t.Errorf("p95 is %v, the budget is 20 ms", p95)
	}
}

// Every signed-in page carries the header icon, the account-menu line and the hints the palette reads; the page says its scope.
func TestShortcutsEntryPointsAreOnEverySignedInPage(t *testing.T) {
	s := newWebServer(t)
	n := 0
	for _, f := range pageFixtures() {
		body := renderPage(t, s, f)
		if !strings.Contains(body, `class="topbar"`) || !strings.Contains(body, `id="palette"`) {
			continue // a page with no frame, or no person signed in
		}
		n++
		for _, want := range []string{`class="icon-btn kb-open" data-kb-open data-scope="`, `aria-keyshortcuts="Shift+/"`, `data-none="`, `<dialog id="palette"`, `data-keys="{&#34;k&#34;:{`, `#keyboard`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s", f.name, want)
			}
		}
		if got := strings.Count(body, "data-kb-open"); got != 2 {
			t.Errorf("%s has %d openers, want the header icon and the account-menu line", f.name, got)
		}
		if regexp.MustCompile(`aria-keyshortcuts="[^"]*Mod`).MatchString(html.UnescapeString(body)) {
			t.Errorf("%s has an aria-keyshortcuts that says Mod", f.name)
		}
		if strings.Contains(body, `data-pc=`) {
			t.Errorf("%s has a hand-typed key", f.name)
		}
	}
	if n < 10 {
		t.Errorf("only %d signed-in pages checked", n)
	}
	// the writing view's buttons carry the registry's tokens (the fixtures' paths are not the real ones, so the scope is checked by ScopeOf)
	checked := 0
	for _, f := range pageFixtures() {
		body := html.UnescapeString(renderPage(t, s, f)) // a + in an attribute is written &#43;
		if f.name != "write" || !strings.Contains(body, `id="fmtbar"`) {
			continue
		}
		checked++
		for _, want := range []string{`data-cmd="redo"`, `data-keys="Mod+Shift+Z Mod+Y"`, `data-keys="Mod+Z"`, `data-keys="Mod+Alt+2"`, `data-keys="Alt+Shift+A"`, `data-keys="Mod+E"`, `aria-keyshortcuts="Alt+PageDown"`} {
			if !strings.Contains(body, want) && !strings.Contains(body, strings.Replace(want, "Alt+PageDown", "Alt+PageUp", 1)) {
				t.Errorf("the writing view lacks %s", want)
			}
		}
	}
	if checked == 0 {
		t.Error("no writing view was rendered")
	}
}

// shortcutsFixture is the window as the lint test renders it, for a page of the given scope.
func shortcutsFixture(scope string, ai, edit bool) shortcutsView {
	pg := Page{Lang: "en", UIDir: "ltr"}
	return shortcutsView{Page: pg, Sections: shortcuts.Build(func(k string) string { return pg.T(k) }, scope, ai, edit)}
}
