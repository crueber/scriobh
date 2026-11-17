package server

import (
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Phase 6 publish and connect pages: who sees each, what each says when the server has the feature off, and the
// flows that matter (the secret shown once and never again, a refused address shown in the server's words, a publish
// that names its folder, an options save that sends only what changed).

func (s *Server) getPage(t *testing.T, c caller, path string) (int, string) {
	t.Helper()
	rec := s.do(c, "GET", path, "")
	return rec.Code, rec.Body.String()
}

func follow(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want a redirect, got %d %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if i := strings.Index(loc, "#"); i >= 0 {
		loc = loc[:i]
	}
	return loc
}

func TestTheSitePageIsForEditorsAndTheOwner(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	for _, c := range []struct {
		who  caller
		want int
	}{{w.owner, 200}, {w.editor, 200}, {w.reader, 404}, {w.stranger, 404}} {
		if code, _ := w.s.getPage(t, c.who, "/p/the-salt-road/site"); code != c.want {
			t.Errorf("%s: %d, want %d", c.who.name, code, c.want)
		}
	}
	if rec := w.s.postForm(t, w.reader, "/ui/projects/"+w.pid+"/site/publish", nil); rec.Code != 404 {
		t.Errorf("a reader's publish: %d", rec.Code)
	}
}

func TestTheSitePageShowsThePreviewPublishesAndSavesOnlyWhatChanged(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	_, body := w.s.getPage(t, w.owner, "/p/the-salt-road/site")
	for _, want := range []string{"First post", "Never published", "Publish to the server", `href="https://example.org/blog/"`, `id="site-out"`} {
		if !strings.Contains(html.UnescapeString(body), want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// the options form, sent back as it is apart from one field, changes only that field
	vals := url.Values{"title": {"The Salt Road"}, "tagline": {"A new line"}, "base_url": {"https://example.org/blog/"}, "posts": {"chapters"}, "feed_content": {"full"},
		"accent": {"moss"}, "posts_per_page": {"2"}, "feed_items": {"20"}, "image_max_width": {"760"}, "parts_as_categories": {"on"}, "show_dates": {"on"}, "show_author": {"on"}, "show_reading_time": {"on"}}
	if loc := follow(t, w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/site/options", vals)); !strings.HasPrefix(loc, "/p/the-salt-road/site") {
		t.Fatalf("redirect to %s", loc)
	}
	opts := w.must(t, w.owner, "GET", w.base()+"/site/options", "").Body.String()
	if !strings.Contains(opts, `"tagline":"A new line"`) || !strings.Contains(opts, `"posts_per_page":2`) || !strings.Contains(opts, `"base_url":"https://example.org/blog/"`) {
		t.Errorf("options after the save: %s", opts)
	}
	// publish through the form: the page then says where, and that nothing changed since
	loc := follow(t, w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/site/publish", nil))
	_, body = w.s.getPage(t, w.owner, loc)
	if !strings.Contains(body, `id="site-done"`) || !strings.Contains(body, "--") || !strings.Contains(body, "/public</code>") || !strings.Contains(body, "Up to date.") {
		t.Errorf("after publishing the page does not say so")
	}
	if _, err := os.Stat(filepath.Join(w.root)); err != nil {
		t.Errorf("nothing was published: %v", err)
	}
	// the download is the zip
	rec := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/site/build", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || len(unzip(t, rec.Body.Bytes())) == 0 {
		t.Errorf("download: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	// a document's address, through its edit form; an address another post holds is refused in words
	first := w.docs["First post"]
	follow(t, w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/site/doc", url.Values{"document_id": {first}, "role": {"auto"}, "slug": {"hello-there"}, "tags": {"sea, boats"}}))
	rows := w.must(t, w.owner, "GET", w.base()+"/site/documents", "").Body.String()
	if !strings.Contains(rows, `"slug":"hello-there"`) || !strings.Contains(rows, `"tags":["sea","boats"]`) {
		t.Errorf("the document settings: %s", rows)
	}
}

func TestTheSitePageSaysWhenTheServerHasItOff(t *testing.T) {
	w := newSiteWorld(t, map[string]string{"SCRIOBH_SITE": "off"})
	_, body := w.s.getPage(t, w.owner, "/p/the-salt-road/site")
	if !strings.Contains(body, `id="site-off"`) || !strings.Contains(body, "SCRIOBH_SITE=off") || strings.Contains(body, `id="site-options"`) {
		t.Error("the page does not say plainly that the server has it off, or shows more")
	}
	w2 := newSiteWorld(t, map[string]string{"SCRIOBH_SITE_DIR": "-"})
	_, body = w2.s.getPage(t, w2.owner, "/p/the-salt-road/site")
	if !strings.Contains(body, `id="site-nopub"`) || strings.Contains(body, `id="site-publish"`) {
		t.Error("without a folder the page must offer only the download")
	}
}

var reSecretBox = regexp.MustCompile(`id="wh-secret"[^>]*value="([^"]+)"`)

func TestTheWebhooksPageShowsTheSecretOnceAndTheServersOwnRefusal(t *testing.T) {
	w := newShareWorld(t, nil)
	page := "/p/shared-book/webhooks"
	for _, c := range []struct {
		who  caller
		want int
	}{{w.owner, 200}, {w.editor, 403}, {w.reviewer, 403}, {w.stranger, 404}} {
		if code, _ := w.s.getPage(t, c.who, page); code != c.want {
			t.Errorf("%s: %d, want %d", c.who.name, code, c.want)
		}
	}
	act := "/ui/projects/" + w.pid + "/webhooks/"
	// an address inside the network is refused, in the server's own words, with what was typed kept
	rec := w.s.postForm(t, w.owner, act+"create", url.Values{"name": {"Local"}, "url": {"https://10.1.2.3/hook"}, "event": {"comment.created"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a private address: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `value="https://10.1.2.3/hook"`) || !strings.Contains(body, `aria-invalid="true"`) {
		t.Error("the refusal is not shown with the address kept and the field marked")
	}
	// a good one: the secret is on the page the redirect lands on, once
	loc := follow(t, w.s.postForm(t, w.owner, act+"create", url.Values{"name": {"Ada"}, "url": {"https://hooks.example.org/in/tok123"}, "event": {"document.updated", "comment.created"}}))
	if strings.Contains(loc, "whsec") || strings.Contains(loc, "secret") {
		t.Fatalf("the redirect names the secret: %s", loc)
	}
	_, body = w.s.getPage(t, w.owner, loc)
	m := reSecretBox.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the secret is not shown after making the webhook")
	}
	secret := m[1]
	_, again := w.s.getPage(t, w.owner, page)
	if strings.Contains(again, secret) || strings.Contains(again, `id="wh-secret"`) {
		t.Error("the secret is shown a second time")
	}
	if strings.Contains(again, "tok123") || !strings.Contains(again, "https://hooks.example.org") {
		t.Error("the sealed path is shown, or the host is not")
	}
	if strings.Contains(w.logs.String(), secret) {
		t.Error("the secret is in the log")
	}
	var id string
	if mm := regexp.MustCompile(`id="hook-(whk_[^"]+)"`).FindStringSubmatch(again); mm != nil {
		id = mm[1]
	} else {
		t.Fatal("no webhook listed")
	}
	// switch off, then on; rotate shows a new secret once; delete
	follow(t, w.s.postForm(t, w.owner, act+"disable", url.Values{"webhook_id": {id}}))
	if _, b := w.s.getPage(t, w.owner, page); !strings.Contains(b, "is-off") {
		t.Error("switched off is not shown")
	}
	follow(t, w.s.postForm(t, w.owner, act+"enable", url.Values{"webhook_id": {id}}))
	loc = follow(t, w.s.postForm(t, w.owner, act+"rotate", url.Values{"webhook_id": {id}, "keep_old_hours": {"0"}}))
	_, b := w.s.getPage(t, w.owner, loc)
	if m2 := reSecretBox.FindStringSubmatch(b); m2 == nil || m2[1] == secret {
		t.Error("a rotation does not show a new secret")
	}
	if _, b := w.s.getPage(t, w.owner, page+"?log="+id); !strings.Contains(b, `id="wh-log"`) {
		t.Error("the delivery log does not open")
	}
	follow(t, w.s.postForm(t, w.owner, act+"delete", url.Values{"webhook_id": {id}}))
	if _, b := w.s.getPage(t, w.owner, page); strings.Contains(b, "hook-"+id) {
		t.Error("the webhook is still listed after delete")
	}
	if rec := w.s.postForm(t, w.editor, act+"create", url.Values{"name": {"x"}, "url": {"https://a.example.org"}, "event": {"comment.created"}}); rec.Code != 403 {
		t.Errorf("an editor's create: %d", rec.Code)
	}
}

func TestTheWebhooksPageSaysWhenTheServerHasThemOff(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_WEBHOOKS": "off"})
	_, body := w.s.getPage(t, w.owner, "/p/shared-book/webhooks")
	if !strings.Contains(body, `id="wh-off"`) || !strings.Contains(body, "SCRIOBH_WEBHOOKS=off") || strings.Contains(body, `id="wh-new"`) {
		t.Error("the page does not say plainly that webhooks are off, or still offers to make one")
	}
}

func TestTheConnectPageDerivesItsAddressAndSaysWhenMCPIsOff(t *testing.T) {
	w := newShareWorld(t, nil)
	req := httptest.NewRequest("GET", "/settings/mcp?set=bible", nil)
	req.Host = "write.example.net:8443"
	req.AddCookie(w.owner.cookie)
	rec := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec, req)
	body := html.UnescapeString(rec.Body.String())
	if rec.Code != 200 || !strings.Contains(body, "http://write.example.net:8443/mcp?groups=bible") || strings.Contains(body, "readonly=1") {
		t.Errorf("the address is not this request's with the chosen groups: %d", rec.Code)
	}
	for _, want := range []string{`"command": "scriobh-cli"`, "scriobh-cli mcp --groups bible", "Authorization: Bearer scr_", `<code>entity</code>`, `href="/settings#keys"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(body, "<code>webhook</code>") || strings.Contains(body, "<code>key</code>") {
		t.Error("an excluded group is listed as a tool group")
	}
	_, def := w.s.getPage(t, w.owner, "/settings/mcp")
	if !strings.Contains(html.UnescapeString(def), "/mcp?groups=core&readonly=1") {
		t.Error("the default is the manuscript, read only")
	}
	off := newShareWorld(t, map[string]string{"SCRIOBH_MCP": "off"})
	_, body = off.s.getPage(t, off.owner, "/settings/mcp")
	if !strings.Contains(body, `id="mcp-off"`) || strings.Contains(body, `id="mcp-url"`) {
		t.Error("with MCP off the page must say so and give no address")
	}
	if code, _ := w.s.getPage(t, caller{name: "anonymous"}, "/settings/mcp"); code == 200 {
		t.Error("the page is open to someone signed out")
	}
}

func TestTheProjectMenuOffersSiteAndWebhooksByRole(t *testing.T) {
	w := newShareWorld(t, nil)
	read := "/p/shared-book/read/" + w.doc1
	has := func(c caller, link string) bool {
		return strings.Contains(w.s.do(c, "GET", read, "").Body.String(), `href="/p/shared-book/`+link+`"`)
	}
	for _, c := range []struct {
		who            caller
		site, webhooks bool
	}{{w.owner, true, true}, {w.editor, true, false}, {w.reviewer, false, false}} {
		if has(c.who, "site") != c.site || has(c.who, "webhooks") != c.webhooks {
			t.Errorf("%s: site %v webhooks %v", c.who.name, has(c.who, "site"), has(c.who, "webhooks"))
		}
	}
}

var reCodeOrOp = regexp.MustCompile(`(?is)<code\b.*?</code>|<span class="cx-their">.*?</span>|<select id="so-about".*?</select>|<textarea[^>]*>.*?</textarea>|<pre class="cx-code".*?</pre>|<span class="[^"]*cx-op[^"]*">.*?</span>|<input [^>]*>|<option [^>]*>|<a href="https?://[^"]*"[^>]*>.*?</a>`)

// Every word on the Phase 6 publish and connect pages comes from the catalogue. Identifiers (in code), the server's own
// messages (the op's refusal or reason, shown as it is), a person's typed values and addresses are let through.
func TestThePseudoLocaleLeavesNoEnglishOnThePhase6Pages(t *testing.T) {
	sw := newSiteWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	sw.populate(t)
	w := newShareWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	if rec := sw.s.do(sw.owner, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	if rec := w.s.do(w.owner, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	follow(t, w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/webhooks/create", url.Values{"name": {"Ada"}, "url": {"https://hooks.example.org/x"}, "event": {"comment.created"}}))
	_, withSecret := w.s.getPage(t, w.owner, "/p/shared-book/webhooks")
	pages := map[string]string{"webhooks with a secret": withSecret}
	_, pages["site"] = sw.s.getPage(t, sw.owner, "/p/the-salt-road/site")
	_, pages["webhooks"] = w.s.getPage(t, w.owner, "/p/shared-book/webhooks")
	_, pages["connect"] = w.s.getPage(t, w.owner, "/settings/mcp")
	allowed := map[string]bool{"ada": true, "salt": true, "road": true, "first": true, "post": true, "second": true, "third": true, "about": true, "notes": true, "from": true, "the": true, "coast": true}
	for name, body := range pages {
		if !strings.Contains(body, "⟦") {
			t.Errorf("%s: nothing in the pseudo-locale", name)
			continue
		}
		var bad []string
		for _, word := range englishIn(reCodeOrOp.ReplaceAllString(body, " ")) {
			if !allowed[strings.ToLower(word)] {
				bad = append(bad, word)
			}
		}
		if len(bad) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", name, strings.Join(bad, " "))
		}
	}
}

// The swatches on the site page draw the generated site's accents: their colours must be the generator's.
func TestTheSiteSwatchesAreTheGeneratorsAccents(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "site", "css.go"))
	if err != nil {
		t.Fatal(err)
	}
	css, err := fs.ReadFile(staticFS(t), "css/connect.css")
	if err != nil {
		t.Fatal(err)
	}
	pairs := regexp.MustCompile(`"(\w+)":\s*\{"(#[0-9A-Fa-f]{6})", "(#[0-9A-Fa-f]{6})"\}`).FindAllStringSubmatch(string(src), -1)
	if len(pairs) != 5 {
		t.Fatalf("found %d accents in css.go", len(pairs))
	}
	for _, p := range pairs {
		for i, pos := range []string{"first", "last"} {
			want := ".a-" + p[1] + " i:" + pos + "-child { background: " + p[2+i] + "; }"
			if !strings.Contains(string(css), want) {
				t.Errorf("connect.css lacks %s", want)
			}
		}
	}
}
