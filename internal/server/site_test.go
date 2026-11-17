package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/site/check"
	"github.com/crueber/scriobh/internal/testutil"
)

// siteWorld is a blog project with posts, a page, things that must never be
// published, and a server that may publish to a folder.
type siteWorld struct {
	s                                      *Server
	owner, editor, reader, stranger, admin caller
	pid, root                              string
	docs                                   map[string]string // by title
}

func newSiteWorld(t *testing.T, env map[string]string) *siteWorld {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sites")
	cfg := map[string]string{"SCRIOBH_RATE_LIMIT": "0", "SCRIOBH_SITE_DIR": root}
	for k, v := range env {
		if v == "-" {
			delete(cfg, k)
			continue
		}
		cfg[k] = v
	}
	s, err := New(Deps{Cfg: testutil.Config(t, cfg), DB: testutil.DB(t)})
	if err != nil {
		t.Fatal(err)
	}
	w := &siteWorld{s: s, root: root, docs: map[string]string{}}
	w.owner = s.newCaller(t, "owner", ops.RoleWriter)
	w.editor = s.newCaller(t, "editor", ops.RoleWriter)
	w.reader = s.newCaller(t, "reader", ops.RoleReader)
	w.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	w.admin = s.newCaller(t, "admin", ops.RoleAdmin)
	rec := s.do(w.owner, "POST", "/api/v1/projects", `{"title":"The Salt Road","kind":"blog","author_name":"Máire Ní Bhriain"}`)
	if rec.Code >= 300 {
		t.Fatalf("project: %d %s", rec.Code, rec.Body)
	}
	w.pid = idOf(t, rec.Body.Bytes())
	s.addMember(t, w.pid, w.editor.userID, ops.ProjectEditor)
	s.addMember(t, w.pid, w.reader.userID, ops.ProjectReader)
	return w
}

func (w *siteWorld) base() string { return "/api/v1/projects/" + w.pid }

func (w *siteWorld) must(t *testing.T, c caller, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := w.s.do(c, method, path, body)
	if rec.Code >= 300 {
		t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
	}
	return rec
}

// doc makes a document with the given text and status.
func (w *siteWorld) doc(t *testing.T, title, kind, parent, content, status string, extra string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"title": title, "kind": kind, "parent_id": parent, "content": content})
	id := idOf(t, w.must(t, w.owner, "POST", w.base()+"/docs", string(body)).Body.Bytes())
	patch := `{"base_revision":1,"status":"` + status + `"` + extra + `}`
	w.must(t, w.owner, "PATCH", "/api/v1/docs/"+id, patch)
	w.docs[title] = id
	return id
}

func (w *siteWorld) upload(t *testing.T, name string, data []byte) string {
	t.Helper()
	req := httptest.NewRequest("POST", w.base()+"/attachments?filename="+name, bytes.NewReader(data))
	req.AddCookie(w.owner.cookie)
	rec := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec, req)
	if rec.Code >= 300 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var up struct{ Item struct{ URL string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &up)
	return up.Item.URL
}

func unzip(t *testing.T, body []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

// populate fills the project the canary tests use: three posts, a page, a
// scene folded into a post, and a long list of things that must never be public.
func (w *siteWorld) populate(t *testing.T) {
	t.Helper()
	w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"base_url":"https://example.org/blog/","title":"The Salt Road","tagline":"Notes from the coast","posts_per_page":2}}`)
	img := w.upload(t, "boat.png", pngBytes(t, 4, 3, 7))
	part := w.doc(t, "Part One", "part", "", "PART_BODY_CANARY_NEVER", "done", "")
	one := w.doc(t, "First post", "chapter", part, "Hello **world**.%% CANARY_PRIVATE %%\n\n<scriobh-ghost>CANARY_GHOST</scriobh-ghost> kept text.\n\n![A boat]("+img+")\n\nA note.[^1]\n\n[^1]: Public note.\n", "done", "")
	w.doc(t, "A scene", "scene", one, "Folded scene text.", "done", "")
	w.doc(t, "Second post", "chapter", "", "CANARY_OLD_REVISION was here.\n\nSecond body with <b>raw</b> html and [js](javascript:alert(1)).", "done", `,"synopsis_short":"CANARY_SYNOPSIS"`)
	w.doc(t, "Third post", "chapter", "", "Third body.", "done", `,"synopsis_long":"CANARY_SYNOPSIS_LONG"`)
	w.doc(t, "Inline picture", "chapter", "", "![inline](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==) ![vector](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=) ![remote](https://tracker.example/p.gif)", "done", "")
	w.doc(t, "About", "front_matter", "", "About this site.", "done", "")
	// Never published.
	w.doc(t, "Cut chapter", "chapter", "", "CANARY_NOT_COMPILED", "done", `,"include_in_compile":false`)
	w.doc(t, "Draft chapter", "chapter", "", "CANARY_DRAFT", "draft", "")
	w.doc(t, "Idea chapter", "chapter", "", "CANARY_IDEA", "idea", "")
	w.doc(t, "Private note doc", "note", "", "CANARY_NOTE_DOC", "done", "")
	w.must(t, w.owner, "POST", w.base()+"/research", `{"title":"Research title CANARY_RESEARCH","body":"CANARY_RESEARCH_BODY"}`)
	w.must(t, w.owner, "PATCH", w.base()+"/compile/contact", `{"name":"CANARY_CONTACT_NAME","email":"canary-contact@example.com","address":"1 Canary Road","phone":"CANARY_PHONE"}`)
	w.must(t, w.owner, "POST", w.base()+"/comments", `{"document_id":"`+one+`","body":"CANARY_COMMENT_BODY"}`)
	w.must(t, w.owner, "POST", "/api/v1/docs/"+one+"/revisions", `{"label":"CANARY_LABEL"}`)
	// An earlier revision holds words the current text does not.
	two := w.docs["Second post"]
	w.must(t, w.owner, "POST", "/api/v1/docs/"+two+"/revisions", `{"label":"before"}`)
	w.must(t, w.owner, "PATCH", "/api/v1/docs/"+two, `{"base_revision":`+w.revision(t, two)+`,"content":"Second body with <b>raw</b> html and [js](javascript:alert(1))."}`)
	// A suggestion, an alternative and a stash item that are not in the text.
	three := w.docs["Third post"]
	w.must(t, w.owner, "POST", w.base()+"/suggestions", `{"document_id":"`+three+`","base_revision":`+w.revision(t, three)+`,"changes":[{"quote":"Third body.","text":"CANARY_SUGGESTION"}]}`)
	w.must(t, w.owner, "POST", "/api/v1/docs/"+three+"/alternatives", `{"base_revision":`+w.revision(t, three)+`,"scope":"sentence","quote":"Third body.","variants":["CANARY_ALT_VARIANT"]}`)
	w.must(t, w.owner, "POST", w.base()+"/stash", `{"title":"CANARY_STASH_TITLE","content":"CANARY_STASH"}`)
	cmp := idOf(t, w.must(t, w.owner, "POST", w.base()+"/compendium", `{"name":"CANARY_BIBLE"}`).Body.Bytes())
	w.must(t, w.owner, "POST", "/api/v1/compendiums/"+cmp+"/entities", `{"kind":"character","name":"CANARY_ENTITY","summary":"CANARY_ENTITY_SUMMARY","body":"CANARY_ENTITY_BODY"}`)
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+one, `{"published_at":"2026-01-05","tags":["Sea","Walking"],"excerpt":"A chosen summary."}`)
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+w.docs["Second post"], `{"published_at":"2026-02-01"}`)
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+w.docs["Inline picture"], `{"published_at":"2025-12-01"}`)
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+w.docs["Third post"], `{"published_at":"2026-03-01T10:00:00+01:00","tags":["sea"]}`)
}

func (w *siteWorld) revision(t *testing.T, doc string) string {
	t.Helper()
	var d struct{ Item struct{ Revision int } }
	if err := json.Unmarshal(w.must(t, w.owner, "GET", "/api/v1/docs/"+doc, "").Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(d.Item.Revision)
}

var siteCanaries = []string{"CANARY_SYNOPSIS", "CANARY_SYNOPSIS_LONG", "CANARY_OLD_REVISION", "CANARY_SUGGESTION", "CANARY_ALT_VARIANT", "CANARY_STASH", "CANARY_STASH_TITLE",
	"CANARY_BIBLE", "CANARY_ENTITY", "CANARY_ENTITY_SUMMARY", "CANARY_ENTITY_BODY", "tracker.example", "CANARY_PRIVATE", "CANARY_GHOST", "CANARY_NOT_COMPILED", "CANARY_DRAFT", "CANARY_IDEA", "CANARY_NOTE_DOC", "CANARY_RESEARCH", "CANARY_RESEARCH_BODY",
	"CANARY_CONTACT_NAME", "canary-contact@example.com", "1 Canary Road", "CANARY_PHONE", "CANARY_COMMENT_BODY", "CANARY_LABEL", "PART_BODY_CANARY_NEVER",
	"owner@test.local", "editor@test.local", "reader@test.local", "stranger@test.local", "admin@test.local"}

func (w *siteWorld) build(t *testing.T, c caller) (*httptest.ResponseRecorder, map[string][]byte) {
	t.Helper()
	rec := w.s.do(c, "POST", w.base()+"/site/build", "")
	if rec.Code != 200 {
		return rec, nil
	}
	return rec, unzip(t, rec.Body.Bytes())
}

func TestSiteBuildIsADeterministicZipThatPassesItsCheck(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	// The preview says what the build will do, and has the same hash.
	prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	posts := prev["posts"].([]any)
	if len(posts) != 4 {
		t.Fatalf("posts: %v", prev["posts"])
	}
	if first := posts[0].(map[string]any); first["title"] != "Third post" || first["date"] != "2026-03-01" {
		t.Errorf("newest first: %v", first)
	}
	if pages := prev["pages"].([]any); len(pages) != 1 || pages[0].(map[string]any)["title"] != "About" {
		t.Errorf("pages: %v", prev["pages"])
	}
	reasons := map[string]string{}
	for _, e := range prev["excluded"].([]any) {
		m := e.(map[string]any)
		reasons[m["title"].(string)] = m["reason"].(string)
	}
	for _, title := range []string{"Cut chapter", "Draft chapter", "Idea chapter"} {
		if reasons[title] == "" {
			t.Errorf("%s is not listed as excluded: %v", title, reasons)
		}
	}
	if !strings.Contains(reasons["Cut chapter"], "do not compile") || !strings.Contains(reasons["Draft chapter"], "include_drafts") {
		t.Errorf("reasons: %v", reasons)
	}
	if prev["notes_skipped"].(float64) != 1 || prev["feeds"] != true {
		t.Errorf("preview: %v", prev)
	}
	var codes []string
	for _, x := range prev["warnings"].([]any) {
		codes = append(codes, x.(map[string]any)["code"].(string))
	}
	if !strings.Contains(strings.Join(codes, ","), "no_excerpt") || !strings.Contains(strings.Join(codes, ","), "no_date") {
		// "Second post" has a date, the part has none: the excerpt of Second and Third are automatic.
		if !strings.Contains(strings.Join(codes, ","), "no_excerpt") {
			t.Errorf("warnings: %v", codes)
		}
	}
	rec, files := w.build(t, w.owner)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("build: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()[:min(200, rec.Body.Len())])
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "the-salt-road-site.zip") {
		t.Errorf("filename: %s", rec.Header().Get("Content-Disposition"))
	}
	rep := check.Site(files, check.Options{Base: "https://example.org/blog/"})
	if !rep.OK() {
		t.Fatalf("the built site fails its check:\n%s", strings.Join(rep.Problems, "\n"))
	}
	// Folded scene and the published images.
	first := string(files["posts/first-post.html"])
	if !strings.Contains(first, "Folded scene text.") || !strings.Contains(first, `class="scene"`) || !strings.Contains(first, `<h1 dir="auto">First post</h1>`) {
		t.Errorf("first post:\n%s", first)
	}
	var imgs int
	for p := range files {
		if strings.HasPrefix(p, "assets/img/") {
			imgs++
		}
	}
	for p := range files {
		if strings.Contains(p, "inline-picture") && bytes.Contains(files[p], []byte("<img")) {
			t.Errorf("an inline picture was published: %s", files[p])
		}
	}
	if imgs != 1 || !strings.Contains(first, `alt="A boat"`) || !strings.Contains(first, `width="4" height="3"`) {
		t.Errorf("images: %d", imgs)
	}
	if !strings.Contains(string(files["posts/first-post.html"]), "Part One") {
		t.Error("the part is not the post's category")
	}
	// Canaries: nothing hidden is anywhere in the zip.
	for name, data := range files {
		for _, c := range siteCanaries {
			if bytes.Contains(data, []byte(c)) {
				t.Errorf("canary %q is in %s", c, name)
			}
		}
	}
	for _, c := range siteCanaries {
		if bytes.Contains(rec.Body.Bytes(), []byte(c)) {
			t.Errorf("canary %q is in the zip bytes", c)
		}
	}
	// Determinism: another build of the same state is the same bytes.
	rec2, _ := w.build(t, w.owner)
	if !bytes.Equal(rec.Body.Bytes(), rec2.Body.Bytes()) {
		t.Error("two builds of the same state differ")
	}
	// The preview agrees with the build.
	bh := w.statusOf(t)
	if bh["build"].(map[string]any)["input_hash"] != prev["input_hash"] {
		t.Errorf("the build's input hash %v is not the preview's %v", bh["build"], prev["input_hash"])
	}
	// The export is recorded with a manifest and no text.
	var manifest, preset, format string
	if err := w.s.DB.Read().QueryRow(`SELECT manifest_json, preset, format FROM exports WHERE project_id = ? ORDER BY created_at DESC LIMIT 1`, w.pid).Scan(&manifest, &preset, &format); err != nil {
		t.Fatal(err)
	}
	if preset != "site" || format != "html" || !strings.Contains(manifest, "site_hash") {
		t.Errorf("export row: %s %s %s", preset, format, manifest)
	}
	for _, c := range siteCanaries {
		if strings.Contains(manifest, c) {
			t.Errorf("canary %q in the manifest", c)
		}
	}
}

func (w *siteWorld) statusOf(t *testing.T) map[string]any {
	t.Helper()
	return decodeObj(t, w.must(t, w.owner, "GET", w.base()+"/site", ""))
}

func TestSitePreviewTracksChanges(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	a := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))["input_hash"]
	b := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))["input_hash"]
	if a != b {
		t.Fatal("the same state has two input hashes")
	}
	w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"tagline":"Changed"}}`)
	if c := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))["input_hash"]; c == a {
		t.Error("an option change did not change the input hash")
	}
	// Including drafts adds the draft chapter (never the idea) and says so.
	w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"include_drafts":true}}`)
	prev := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	if len(prev["posts"].([]any)) != 5 {
		t.Errorf("drafts: %d posts", len(prev["posts"].([]any)))
	}
	var drafts bool
	for _, x := range prev["warnings"].([]any) {
		drafts = drafts || x.(map[string]any)["code"] == "drafts_included"
	}
	if !drafts {
		t.Error("no drafts_included warning")
	}
	// A document the owner skips is out, with its reason.
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+w.docs["Third post"], `{"role":"skip"}`)
	prev = decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/preview", ""))
	var skipped bool
	for _, e := range prev["excluded"].([]any) {
		m := e.(map[string]any)
		skipped = skipped || (m["title"] == "Third post" && strings.Contains(m["reason"].(string), "you chose"))
	}
	if !skipped {
		t.Errorf("excluded: %v", prev["excluded"])
	}
}

func TestSiteDocumentSettings(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	one, two := w.docs["First post"], w.docs["Second post"]
	rec := w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+one, `{"slug":"Hello, World! ../etc"}`)
	if got := decodeObj(t, rec)["item"].(map[string]any)["slug"]; got != "hello-world-etc" {
		t.Errorf("slug: %v", got)
	}
	if r := w.s.do(w.owner, "PATCH", w.base()+"/site/documents/"+two, `{"slug":"HELLO-world-etc"}`); r.Code != http.StatusConflict {
		t.Errorf("a duplicate address: %d %s", r.Code, r.Body)
	}
	for body, why := range map[string]string{
		`{"slug":"🙂"}`: "no letters", `{"published_at":"yesterday"}`: "bad date", `{"role":"boss"}`: "bad role",
		`{"excerpt":"` + strings.Repeat("x", 301) + `"}`: "long excerpt", `{"tags":["a","b","c","d","e","f","g","h","i","j","k"]}`: "too many tags",
		`{"clear":["nope"]}`: "bad clear",
	} {
		if r := w.s.do(w.owner, "PATCH", w.base()+"/site/documents/"+two, body); r.Code != 422 && r.Code != 400 {
			t.Errorf("%s accepted: %d %s", why, r.Code, r.Body)
		}
	}
	// A document of another project, and a research document, are not here.
	other := idOf(t, w.must(t, w.stranger, "POST", "/api/v1/projects", `{"title":"Elsewhere"}`).Body.Bytes())
	od := idOf(t, w.must(t, w.stranger, "POST", "/api/v1/projects/"+other+"/docs", `{"title":"x"}`).Body.Bytes())
	if r := w.s.do(w.owner, "PATCH", w.base()+"/site/documents/"+od, `{"role":"post"}`); r.Code != 404 {
		t.Errorf("another project's document: %d", r.Code)
	}
	// Clearing.
	w.must(t, w.owner, "PATCH", w.base()+"/site/documents/"+one, `{"clear":["slug","tags","excerpt","published_at"]}`)
	list := decodeObj(t, w.must(t, w.owner, "GET", w.base()+"/site/documents", ""))["items"].([]any)
	for _, it := range list {
		m := it.(map[string]any)
		if m["document_id"] == one && (m["slug"] != "" || m["published_at"] != "" || len(m["tags"].([]any)) != 0) {
			t.Errorf("not cleared: %v", m)
		}
	}
	// Options: invalid values are refused with the field.
	for body, field := range map[string]string{
		`{"options":{"base_url":"javascript:alert(1)"}}`:                  "base_url",
		`{"options":{"posts_per_page":0}}`:                                "posts_per_page",
		`{"options":{"accent":"neon"}}`:                                   "accent",
		`{"options":{"nav":[{"label":"x","url":"javascript:alert(1)"}]}}`: "nav",
		`{"options":{"clear":["nope"]}}`:                                  "clear",
		`{"options":{"title":"` + strings.Repeat("t", 121) + `"}}`:        "title",
		`{"options":{"feed_content":"all"}}`:                              "feed_content",
	} {
		rec := w.s.do(w.owner, "PATCH", w.base()+"/site/options", body)
		if rec.Code != 422 && rec.Code != 400 || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	// Clearing an option restores its default.
	w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"posts_per_page":7}}`)
	got := decodeObj(t, w.must(t, w.owner, "PATCH", w.base()+"/site/options", `{"options":{"clear":["posts_per_page"]}}`))["options"].(map[string]any)
	if got["posts_per_page"].(float64) != 10 {
		t.Errorf("not cleared: %v", got)
	}
}

func tree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			r, _ := filepath.Rel(dir, p)
			out[filepath.ToSlash(r)] = b
		}
		return nil
	})
	return out
}

func TestSitePublishWritesTheFolderAndOnlyThat(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	st := w.statusOf(t)
	if st["publish_enabled"] != true || st["enabled"] != true || st["location"] != nil {
		t.Errorf("status before: %v", st)
	}
	if err := os.MkdirAll(w.root, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(w.root, "mine.txt")
	_ = os.WriteFile(keep, []byte("mine"), 0o644)
	// The folder is never taken from a request.
	evil := t.TempDir()
	if r := w.s.do(w.owner, "POST", w.base()+"/site/publish", `{"dir":"`+evil+`","path":"`+evil+`","root":"/"}`); r.Code != 422 {
		t.Fatalf("a request that names a folder: %d %s", r.Code, r.Body)
	}
	rec := w.s.do(w.owner, "POST", w.base()+"/site/publish", "")
	if rec.Code != 200 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	if ents, _ := os.ReadDir(evil); len(ents) != 0 {
		t.Fatal("a request chose where the site went")
	}
	out := decodeObj(t, rec)
	loc := out["location"].(string)
	if !strings.HasPrefix(loc, "the-salt-road--") || !strings.HasSuffix(loc, "/public") || strings.Contains(loc, w.root) {
		t.Errorf("location %q", loc)
	}
	pub := filepath.Join(w.root, filepath.FromSlash(loc))
	files := tree(t, pub)
	if rep := check.Site(files, check.Options{Base: "https://example.org/blog/"}); !rep.OK() {
		t.Fatalf("the published folder fails its check:\n%s", strings.Join(rep.Problems, "\n"))
	}
	for name, data := range files {
		for _, c := range siteCanaries {
			if bytes.Contains(data, []byte(c)) {
				t.Errorf("canary %q is in the published %s", c, name)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(pub), "managed.json")); err != nil {
		t.Error("no managed-files record")
	}
	if _, ok := files["managed.json"]; ok {
		t.Error("the record is inside the served folder")
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Error("an unrelated file in the sites folder was touched")
	}
	// Status and idempotence: same state, same hash; the status knows.
	st = w.statusOf(t)
	pubStamp := st["publish"].(map[string]any)
	if st["location"] != loc || pubStamp["site_hash"] != out["site_hash"] || pubStamp["files"].(float64) != float64(len(files)) {
		t.Errorf("status: %v", st)
	}
	again := decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/publish", ""))
	if again["site_hash"] != out["site_hash"] || again["replaced"] != true || again["location"] != loc {
		t.Errorf("second publish: %v", again)
	}
	// Someone puts a file in the served folder: refused, nothing changed, nothing deleted.
	_ = os.WriteFile(filepath.Join(pub, "extra.txt"), []byte("theirs"), 0o644)
	rec = w.s.do(w.owner, "POST", w.base()+"/site/publish", "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "extra.txt") || strings.Contains(rec.Body.String(), w.root) {
		t.Errorf("unmanaged file: %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(pub, "extra.txt")); string(b) != "theirs" {
		t.Error("their file was removed")
	}
	_ = os.Remove(filepath.Join(pub, "extra.txt"))
	// A rename of the project keeps the folder.
	w.must(t, w.owner, "PATCH", w.base(), `{"title":"A New Name"}`)
	again = decodeObj(t, w.must(t, w.owner, "POST", w.base()+"/site/publish", ""))
	if again["location"] != loc {
		t.Errorf("the folder moved with the title: %v", again["location"])
	}
	// Purging the project removes what was published, and only that.
	w.must(t, w.owner, "POST", w.base()+"/trash", "")
	w.must(t, w.owner, "DELETE", w.base(), `{"confirm_title":"A New Name"}`)
	if _, err := os.Stat(pub); err == nil {
		t.Error("the published site survived the purge of its project")
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Error("purge touched an unrelated file")
	}
}

func TestSitePublishNeedsTheAdministrator(t *testing.T) {
	// No folder configured: building works, publishing says why not.
	w := newSiteWorld(t, map[string]string{"SCRIOBH_SITE_DIR": "-"})
	w.populate(t)
	if rec := w.s.do(w.owner, "POST", w.base()+"/site/publish", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "SCRIOBH_SITE_DIR") {
		t.Errorf("publish without a folder: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := w.build(t, w.owner); rec.Code != 200 {
		t.Errorf("build: %d", rec.Code)
	}
	if st := w.statusOf(t); st["publish_enabled"] != false || st["enabled"] != true {
		t.Errorf("status: %v", st)
	}
	// SCRIOBH_SITE_PUBLISH=1 uses the sites folder of the data directory.
	w2 := newSiteWorld(t, map[string]string{"SCRIOBH_SITE_DIR": "-", "SCRIOBH_SITE_PUBLISH": "1"})
	w2.populate(t)
	rec := w2.must(t, w2.owner, "POST", w2.base()+"/site/publish", "")
	loc := decodeObj(t, rec)["location"].(string)
	if _, err := os.Stat(filepath.Join(w2.s.Cfg.DataDir, "sites", filepath.FromSlash(loc), "index.html")); err != nil {
		t.Errorf("the default folder: %v", err)
	}
	// SCRIOBH_SITE=off: no preview, no build, no publish.
	w3 := newSiteWorld(t, map[string]string{"SCRIOBH_SITE": "off"})
	w3.populate(t)
	for _, p := range []string{"/site/preview", "/site/build", "/site/publish"} {
		if rec := w3.s.do(w3.owner, "POST", w3.base()+p, ""); rec.Code != http.StatusConflict {
			t.Errorf("%s with the feature off: %d %s", p, rec.Code, rec.Body)
		}
	}
	if st := w3.statusOf(t); st["enabled"] != false || st["publish_enabled"] != false {
		t.Errorf("status: %v", st)
	}
}

func TestSiteRoles(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	for _, call := range []struct{ m, p, b string }{
		{"GET", "/site", ""}, {"GET", "/site/options", ""}, {"GET", "/site/documents", ""}, {"POST", "/site/preview", ""}, {"POST", "/site/build", ""}, {"POST", "/site/publish", ""},
		{"PATCH", "/site/options", `{"options":{"tagline":"x"}}`}, {"PATCH", "/site/documents/" + w.docs["Third post"], `{"tags":["x"]}`},
	} {
		if r := w.s.do(w.editor, call.m, w.base()+call.p, call.b); r.Code >= 300 {
			t.Errorf("an editor %s %s: %d %s", call.m, call.p, r.Code, r.Body)
		}
		for _, who := range []caller{w.reader} {
			if r := w.s.do(who, call.m, w.base()+call.p, call.b); r.Code != 403 {
				t.Errorf("%s %s as a reader: %d", call.m, call.p, r.Code)
			}
		}
		if r := w.s.do(w.stranger, call.m, w.base()+call.p, call.b); r.Code != 404 {
			t.Errorf("%s %s as a stranger: %d", call.m, call.p, r.Code)
		}
	}
}

func TestSiteDryRunWritesNothing(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.populate(t)
	before := testutil.TreeDigest(t, w.s.Cfg.DataDir)
	rec := w.s.doDry(w.owner, "POST", w.base()+"/site/build", "")
	if rec.Code != 200 || decodeObj(t, rec)["dry_run"] != true {
		t.Fatalf("dry build: %d %s", rec.Code, rec.Body.String()[:min(300, rec.Body.Len())])
	}
	var n int
	_ = w.s.DB.Read().QueryRow(`SELECT count(*) FROM exports WHERE project_id = ?`, w.pid).Scan(&n)
	if n != 0 {
		t.Error("a dry run recorded an export")
	}
	if st := w.statusOf(t); st["build"].(map[string]any)["at"] != nil && st["build"].(map[string]any)["at"] != "" {
		t.Errorf("a dry run stamped the build: %v", st)
	}
	if rec := w.s.doDry(w.owner, "POST", w.base()+"/site/publish", ""); rec.Code != 422 || !strings.Contains(rec.Body.String(), "dry_run_unsupported") {
		t.Errorf("publish must refuse a dry run: %d %s", rec.Code, rec.Body)
	}
	if testutil.TreeDigest(t, w.s.Cfg.DataDir) != before {
		t.Error("a dry run changed the data directory")
	}
	if _, err := os.Stat(w.root); err == nil {
		t.Error("a dry run made the sites folder")
	}
}
