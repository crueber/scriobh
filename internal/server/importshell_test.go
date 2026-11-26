package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The import review as a server-rendered shell: a file is staged once, the review page is drawn from the draft, every change is a
// small form that answers the review body, and a reload, a second tab and the Back button all see the draft as it is.

const testWns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

// testDocx is a small Word document: each chapter a Heading 1 and a paragraph.
func testDocx(t *testing.T, chapters ...string) []byte {
	t.Helper()
	var body strings.Builder
	for _, c := range chapters {
		body.WriteString(`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>` + c + `</w:t></w:r></w:p>`)
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">` + strings.Repeat(c+" over the grey stones the gulls wheeled. ", 10) + `</w:t></w:r></w:p>`)
	}
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
		"word/document.xml":   `<?xml version="1.0" encoding="UTF-8"?><w:document ` + testWns + `><w:body>` + body.String() + `</w:body></w:document>`,
		"word/styles.xml":     `<?xml version="1.0"?><w:styles ` + testWns + `><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(parts))
	for k := range parts {
		names = append(names, k)
	}
	sort.Strings(names) // the same chapters are the same bytes
	for _, k := range names {
		w, _ := zw.Create(k)
		_, _ = w.Write([]byte(parts[k]))
	}
	_ = zw.Close()
	return buf.Bytes()
}

type stagePart struct {
	field, filename string
	data            []byte
}

// stageForm posts the chooser's form the way a browser does: the fields first, then the files, with the CSRF token first.
func (s *Server) stageForm(t *testing.T, c caller, query string, fields [][2]string, files []stagePart, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	tok := s.Sess.CSRFToken(sessionOf(t, s, c))
	if !htmx {
		_ = mw.WriteField("_csrf", tok)
	}
	for _, f := range fields {
		_ = mw.WriteField(f[0], f[1])
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="`+f.field+`"; filename="`+f.filename+`"`)
		h.Set("Content-Type", "application/octet-stream")
		w, _ := mw.CreatePart(h)
		_, _ = w.Write(f.data)
	}
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/ui/import/stage"+query, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c.cookie)
	if htmx {
		req.Header.Set("HX-Request", "true")
		req.Header.Set("X-CSRF-Token", tok)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func (s *Server) stageDocx(t *testing.T, c caller, name string, chapters ...string) string {
	t.Helper()
	rec := s.stageForm(t, c, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", name, testDocx(t, chapters...)}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("staging: %d %.300s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/import/d/imd_") {
		t.Fatalf("staging went to %q", loc)
	}
	return strings.TrimPrefix(loc, "/import/d/")
}

var reRev = regexp.MustCompile(`name="rev" value="(\d+)"`)

func revOf(t *testing.T, body string) string {
	t.Helper()
	m := reRev.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no rev in the page: %.400s", body)
	}
	return m[1]
}

func (s *Server) update(t *testing.T, c caller, id string, htmx bool, vals url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/ui/import/d/"+id+"/update", strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c.cookie)
	if htmx {
		req.Header.Set("HX-Request", "true")
		req.Header.Set("X-CSRF-Token", s.Sess.CSRFToken(sessionOf(t, s, c)))
	} else {
		vals.Set("_csrf", s.Sess.CSRFToken(sessionOf(t, s, c)))
		req = httptest.NewRequest("POST", "/ui/import/d/"+id+"/update", strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func titlesOf(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<span class="imp-title"[^>]*>([^<]*)</span>`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestStagingAFileGivesAReviewPageDrawnFromTheDraft(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "The Salt Road.docx", "The Road", "The Mill", "Snow")
	code, body := s.getPage(t, w, "/import/d/"+id)
	if code != 200 {
		t.Fatalf("the review: %d", code)
	}
	for _, need := range []string{`id="imp-h"`, `class="imp-sum"`, "3 documents", "A Word document", `id="imp-t"`, `value="The Salt Road"`, `data-go`, `id="imp-review"`, `hx-history="false"`, `name="rev" value="1"`, `data-act="up"`} {
		if need == `data-act="up"` {
			continue // an edit panel is drawn when it is asked for
		}
		if !strings.Contains(body, need) {
			t.Errorf("the review lacks %s", need)
		}
	}
	if got := titlesOf(body); strings.Join(got, "|") != "The Road|The Mill|Snow" {
		t.Errorf("rows: %v", got)
	}
	// A reload is the same page; so is the chooser's list, which names it for Resume.
	_, again := s.getPage(t, w, "/import/d/"+id)
	if strings.Join(titlesOf(again), "|") != "The Road|The Mill|Snow" || revOf(t, again) != "1" {
		t.Error("a reload shows another review")
	}
	_, chooser := s.getPage(t, w, "/import")
	if !strings.Contains(chooser, "Imports in progress") || !strings.Contains(chooser, `href="/import/d/`+id+`"`) || !strings.Contains(chooser, `/ui/import/d/`+id+`/discard`) {
		t.Error("the chooser does not list the draft with Resume and Discard")
	}
	// Staging the same bytes again is the same draft.
	if id2 := s.stageDocx(t, w, "The Salt Road.docx", "The Road", "The Mill", "Snow"); id2 != id {
		t.Errorf("the same file made a second draft: %s", id2)
	}
}

func TestAPageThatIsNotTheOwnersIsNotFound(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	id := s.stageDocx(t, w, "a.docx", "One", "Two")
	for _, p := range []string{"/import/d/" + id, "/ui/import/d/" + id + "/row?key=x", "/ui/import/d/" + id + "/more?after=0", "/ui/import/d/" + id + "/progress"} {
		if code, _ := s.getPage(t, other, p); code != 404 {
			t.Errorf("another writer on %s: %d, want 404", p, code)
		}
		if code, _ := s.getPage(t, reader, p); code != 403 {
			t.Errorf("a reader on %s: %d, want 403", p, code)
		}
		if rec := s.do(caller{name: "anon"}, "GET", p, ""); rec.Code != 303 {
			t.Errorf("signed out on %s: %d", p, rec.Code)
		}
	}
	for _, a := range []string{"update", "apply", "discard"} {
		if rec := s.postForm(t, other, "/ui/import/d/"+id+"/"+a, url.Values{"rev": {"1"}, "act": {"undo"}}); rec.Code != 404 {
			t.Errorf("another writer posting %s: %d, want 404", a, rec.Code)
		}
		if rec := s.postForm(t, reader, "/ui/import/d/"+id+"/"+a, url.Values{"rev": {"1"}}); rec.Code != 403 {
			t.Errorf("a reader posting %s: %d, want 403", a, rec.Code)
		}
	}
	// The stage form: a reader may not send a file, and a form with no token is refused before its body is read.
	if rec := s.stageForm(t, reader, "", [][2]string{{"kind", "docx"}}, []stagePart{{"file", "a.docx", testDocx(t, "x")}}, false); rec.Code != 403 {
		t.Errorf("a reader staging: %d, want 403", rec.Code)
	}
	req := httptest.NewRequest("POST", "/ui/import/stage", strings.NewReader("--x--"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.AddCookie(w.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("a stage form without a token: %d", rec.Code)
	}
}

func TestEachChangeIsASmallFormThatAnswersTheReviewBody(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One", "Two", "Three")
	_, page := s.getPage(t, w, "/import/d/"+id)
	key := regexp.MustCompile(`data-key="([^"]+)"`).FindStringSubmatch(page)[1] + ""
	keys := []string{}
	for _, m := range regexp.MustCompile(`<li class="imp-node d0" data-kind="[a-z_]+" data-key="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		keys = append(keys, m[1])
	}
	if len(keys) != 3 || key == "" {
		t.Fatalf("keys: %v", keys)
	}
	// The edit panel is fetched when asked for, with the controls of that row.
	code, panel := s.getPage(t, w, "/ui/import/d/"+id+"/row?key="+url.QueryEscape(keys[1]))
	for _, need := range []string{`data-act="up"`, `data-act="down"`, `data-act="skip"`, `data-act="title"`, `data-act="kind"`, `data-act="parent"`, `data-act="merge"`, `name="rev" value="1"`} {
		if !strings.Contains(panel, need) {
			t.Errorf("the panel lacks %s", need)
		}
	}
	if code != 200 || strings.Contains(panel, `id="imp-review"`) {
		t.Errorf("the panel is a fragment of its own: %d", code)
	}
	// Move the second chapter up: the fragment is the review body with the new order and the words for a screen reader.
	rec := s.update(t, w, id, true, url.Values{"rev": {"1"}, "act": {"up"}, "key": {keys[1]}, "edit": {keys[1]}})
	b := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(b, `<div id="imp-review"`) || strings.Join(titlesOf(b), "|") != "Two|One|Three" || !strings.Contains(b, `hx-swap-oob="innerHTML:#imp-live"`) || !strings.Contains(b, "Moved Two above One: now 1 of 3.") {
		t.Fatalf("move: %d %.600s", rec.Code, b)
	}
	if revOf(t, b) != "2" || !strings.Contains(b, `<details class="imp-edit" data-key="`+keys[1]+`" open>`) {
		t.Error("the revision did not move on, or the row just acted on is not open")
	}
	// The control that cannot go further hands focus to the row's Edit summary.
	if !strings.Contains(b, `id="imp-s-`) || !strings.Contains(b, ` data-focus`) {
		t.Error("nothing is marked to take focus")
	}
	// A stale tab: its change is not made, the current version comes back with a notice, status 422 so htmx swaps it.
	rec = s.update(t, w, id, true, url.Values{"rev": {"1"}, "act": {"down"}, "key": {keys[0]}, "edit": {keys[0]}})
	b = rec.Body.String()
	if rec.Code != 422 || !strings.Contains(b, "This import was changed in another tab. This is the current version. Your change was not made.") || strings.Join(titlesOf(b), "|") != "Two|One|Three" || !strings.Contains(b, `id="imp-notice"`) {
		t.Fatalf("stale: %d %.500s", rec.Code, b)
	}
	// The same change with no script: a redirect that carries the notice, and a page that shows it.
	rec = s.update(t, w, id, false, url.Values{"rev": {"1"}, "act": {"down"}, "key": {keys[0]}})
	// the page is drawn at once with the notice, and nothing of the draft goes in an address
	if rec.Code != 422 || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "changed in another tab") || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("stale without script: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// A title that is empty is refused in words, and nothing is stored.
	rec = s.update(t, w, id, true, url.Values{"rev": {"2"}, "act": {"title"}, "key": {keys[0]}, "edit": {keys[0]}, "title": {"  "}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "A title cannot be empty.") {
		t.Errorf("an empty title: %d", rec.Code)
	}
	// Rename, retype, leave out, put back, undo.
	rec = s.update(t, w, id, true, url.Values{"rev": {"2"}, "act": {"title"}, "key": {keys[0]}, "edit": {keys[0]}, "title": {"Opening <b>"}})
	b = rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, "Opening &lt;b&gt;") || strings.Contains(b, "Opening <b>") {
		t.Errorf("a title with markup in it: %d", rec.Code)
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {"3"}, "act": {"skip"}, "key": {keys[2]}})
	b = rec.Body.String()
	if !strings.Contains(b, "Left out of the import (1)") || !strings.Contains(b, `data-act="back"`) || len(titlesOf(b)) < 2 {
		t.Errorf("left out: %.400s", b)
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {"4"}, "act": {"back"}, "key": {keys[2]}})
	if b = rec.Body.String(); strings.Contains(b, "Left out of the import (") || !strings.Contains(strings.Join(titlesOf(b), "|"), "Three") {
		t.Errorf("put back: %.300s", b)
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {"5"}, "act": {"undo"}})
	if b = rec.Body.String(); !strings.Contains(b, "The last change was undone.") || !strings.Contains(b, "Left out of the import (1)") {
		t.Errorf("undo: %.300s", b)
	}
	// Options: the reading of the file changes, and the controls say which it is.
	rec = s.update(t, w, id, true, url.Values{"rev": {"6"}, "act": {"options"}, "opt.docx_changes": {"accept"}, "opt.split_scenes": {"none", "h2"}})
	b = rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, `value="accept" checked`) || !strings.Contains(b, `name="opt.split_scenes" value="h2" checked`) {
		t.Errorf("options: %d %.300s", rec.Code, b)
	}
}

func TestApplyFromTheReviewMakesOneProjectAndAReloadSaysSo(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One", "Two")
	_, page := s.getPage(t, w, "/import/d/"+id)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	// project fields are saved without a new revision
	rec := s.update(t, w, id, true, url.Values{"rev": {"1"}, "act": {"project"}, "title": {"My Book"}, "kind": {"novella"}, "author": {"W. Wren"}})
	if rec.Code != 204 {
		t.Fatalf("project fields: %d", rec.Code)
	}
	_, page = s.getPage(t, w, "/import/d/"+id)
	if !strings.Contains(page, `value="My Book"`) || revOf(t, page) != "1" {
		t.Error("the project's title was not kept with the draft")
	}
	form := url.Values{"rev": {"1"}, "token": {tok}, "title": {"My Book"}, "kind": {"novella"}, "author": {"W. Wren"}}
	req := httptest.NewRequest("POST", "/ui/import/d/"+id+"/apply", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-CSRF-Token", s.Sess.CSRFToken(sessionOf(t, s, w)))
	req.AddCookie(w.cookie)
	first := httptest.NewRecorder()
	s.Handler().ServeHTTP(first, req)
	if first.Code != 204 || !strings.HasPrefix(first.Header().Get("HX-Redirect"), "/p/my-book/d/doc_") {
		t.Fatalf("apply: %d %s %.300s", first.Code, first.Header().Get("HX-Redirect"), first.Body.String())
	}
	// A second press from another tab: told so in the review's words, and no second project.
	req = httptest.NewRequest("POST", "/ui/import/d/"+id+"/apply", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-CSRF-Token", s.Sess.CSRFToken(sessionOf(t, s, w)))
	req.AddCookie(w.cookie)
	second := httptest.NewRecorder()
	s.Handler().ServeHTTP(second, req)
	if second.Code != 422 || !strings.Contains(second.Body.String(), "This import was already applied.") {
		t.Errorf("a second apply: %d %.300s", second.Code, second.Body.String())
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM projects WHERE title = 'My Book'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d projects", n)
	}
	// The address shows the finished result, from the row that keeps only counts; the staged file is gone.
	code, done := s.getPage(t, w, "/import/d/"+id)
	if code != 200 || !strings.Contains(done, "Imported 2 documents") || !strings.Contains(done, `href="/p/my-book/d/doc_`) || strings.Contains(done, `data-go`) {
		t.Errorf("after the apply: %d %.400s", code, done)
	}
	if !strings.Contains(s.do(w, "GET", "/import", "").Body.String(), "Imported 2 documents") {
		t.Error("the chooser does not say what was imported")
	}
}

func TestDiscardGoesBackToTheChooserWhichNoLongerListsIt(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One")
	rec := s.postForm(t, w, "/ui/import/d/"+id+"/discard", url.Values{})
	if rec.Code != 303 || rec.Header().Get("Location") != "/import" {
		t.Fatalf("discard: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if code, _ := s.getPage(t, w, "/import/d/"+id); code != 404 {
		t.Errorf("a discarded draft's page: %d", code)
	}
	if _, chooser := s.getPage(t, w, "/import"); strings.Contains(chooser, "Imports in progress") {
		t.Error("the chooser still lists it")
	}
}

func TestAFolderIsStagedFromItsPartsAndWhatWasNotSentIsListed(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	md := func(title string) []byte {
		return []byte("# " + title + "\n\n" + strings.Repeat("tide came in. ", 40) + "\n")
	}
	files := []stagePart{
		{"f", "salt-road/01-the-start.md", md("The Start")},
		{"f", "salt-road/part-two/01-crossing.md", md("The Crossing")},
		{"f", "salt-road/.git/config", []byte("[core]\n")},
		{"f", "salt-road/cover.png", []byte("not really a png")},
	}
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "md"}, {"kind", "zip"}, {"panel", "md"}, {"opt.layout", "auto"}}, files, false)
	if rec.Code != 303 {
		t.Fatalf("a folder: %d %.300s", rec.Code, rec.Body.String())
	}
	_, page := s.getPage(t, w, rec.Header().Get("Location"))
	for _, need := range []string{"The Start", "The Crossing", `value="Salt Road"`, "cover.png", "not a Markdown file", "1 hidden file was not read"} {
		if !strings.Contains(page, need) {
			t.Errorf("the folder's review lacks %q", need)
		}
	}
	if strings.Contains(page, ".git/config</span>") && !strings.Contains(page, "hidden file or folder") {
		t.Error("a hidden file is listed with no reason")
	}
	// With script the browser sends only Markdown and lists the rest in a field, which comes before the files.
	rec = s.stageForm(t, w, "", [][2]string{{"fmt", "md"}, {"kind", "zip"}, {"panel", "md"}, {"unsent", "pictures/a.png\t2048\ncover.png\t17"}}, files[:2], true)
	if rec.Code != 204 {
		t.Fatalf("a folder with script: %d %.300s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("HX-Redirect")
	if _, page = s.getPage(t, w, loc); !strings.Contains(page, "pictures/a.png") || !strings.Contains(page, "2 files will not be imported") {
		t.Errorf("what the script left out is not listed: %.400s", page)
	}
}

func TestAFileThatIsNotWhatItSaysIsRefusedInWordsAndKeepsNothing(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "notes.docx", []byte("only words")}}, false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "That is not a Word document") || !strings.Contains(rec.Body.String(), `id="imp-f-docx" type="radio" name="imp-fmt" value="docx" checked`) {
		t.Errorf("a refused file, no script: %d %.300s", rec.Code, rec.Body.String())
	}
	rec = s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}}, []stagePart{{"file", "notes.docx", []byte("only words")}}, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `id="imp-stage-err"`) || strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("a refused file, htmx: %d %.300s", rec.Code, rec.Body.String())
	}
	rec = s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}}, nil, false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Choose a file first.") {
		t.Errorf("no file: %d", rec.Code)
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM import_drafts`).Scan(&n)
	if n != 0 {
		t.Errorf("refused files left %d drafts", n)
	}
}

func TestAddingToAProjectStagesForItAndTheReviewLivesUnderItsPage(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)
	out, err := s.Reg.Call(t.Context(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Existing", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	rec := s.stageForm(t, w, "?into="+pr.Slug, [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "more.docx", testDocx(t, "Extra One", "Extra Two")}}, false)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.HasPrefix(loc, "/p/"+pr.Slug+"/import/d/imd_") {
		t.Fatalf("staging into a project: %d %s", rec.Code, loc)
	}
	_, page := s.getPage(t, w, loc)
	if !strings.Contains(page, "What would be added") || strings.Contains(page, `id="imp-t"`) || strings.Contains(page, `name="slug"`) {
		t.Errorf("the review of an addition: %.400s", page)
	}
	// Not another person's, and not under another path.
	if rec := s.stageForm(t, other, "?into="+pr.Slug, [][2]string{{"kind", "docx"}}, []stagePart{{"file", "x.docx", testDocx(t, "x")}}, false); rec.Code != 404 {
		t.Errorf("a stranger staging into a project: %d", rec.Code)
	}
	id := strings.TrimPrefix(loc, "/p/"+pr.Slug+"/import/d/")
	if rec := s.do(w, "GET", "/import/d/"+id, ""); rec.Code != 303 || rec.Header().Get("Location") != loc {
		t.Errorf("a project's draft under /import: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Applying adds to the project and keeps what was there.
	_, page = s.getPage(t, w, loc)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	rec = s.postForm(t, w, "/ui/import/d/"+id+"/apply", url.Values{"rev": {"1"}, "token": {tok}, "slug": {pr.Slug}})
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/p/"+pr.Slug+"/d/doc_") {
		t.Errorf("apply into a project: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

// import.apply with a stage_id honours an Idempotency-Key: the same key and body replay the first answer and write once; the same key
// with another body is refused.
func TestApplyByStageIDHonoursAnIdempotencyKey(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One", "Two")
	code, pv := restJSON(t, s, w, "GET", "/api/v1/imports/drafts/"+id+"?preview=true", "")
	if code != 200 {
		t.Fatalf("draft.get: %d %v", code, pv)
	}
	tok := pv["preview"].(map[string]any)["token"].(string)
	rev := int(pv["item"].(map[string]any)["revision"].(float64))
	body, _ := json.Marshal(map[string]any{"stage_id": id, "rev": rev, "token": tok, "title": "Keyed"})
	do := func(key, b string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/imports", strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.AddCookie(w.cookie)
		req.Header.Set("X-CSRF-Token", s.Sess.CSRFToken(sessionOf(t, s, w)))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	a := do("key-1", string(body))
	if a.Code != 201 {
		t.Fatalf("first: %d %s", a.Code, a.Body.String())
	}
	b := do("key-1", string(body))
	if b.Code != 201 || b.Body.String() != a.Body.String() || b.Header().Get("Idempotent-Replay") != "true" {
		t.Errorf("a replay: %d replay-header=%v\n%s\nvs\n%s", b.Code, b.Header(), b.Body.String(), a.Body.String())
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM projects WHERE title = 'Keyed'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d projects after a replay", n)
	}
	other, _ := json.Marshal(map[string]any{"stage_id": id, "rev": rev, "token": tok, "title": "Another"})
	if c := do("key-1", string(other)); c.Code != 422 && c.Code != 409 {
		t.Errorf("the same key with another body: %d %s", c.Code, c.Body.String())
	}
}

// The REST routes of the draft: signed out 401, an instance reader 403, another writer and a missing draft 404, and a key pinned to a
// project reaches nothing of an import (an import names no project the key could be pinned to).
func TestImportDraftOpsAuthorization(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	id := s.stageDocx(t, w, "a.docx", "One", "Two")
	pid := idOf(t, s.do(w, "POST", "/api/v1/projects", `{"title":"Pinned"}`).Body.Bytes())
	pinned, _ := s.mintKey(t, w, `{"name":"pinned","project_id":"`+pid+`"}`)
	routes := []struct{ method, path, body string }{
		{"GET", "/api/v1/imports/drafts", ""},
		{"GET", "/api/v1/imports/drafts/" + id + "?preview=true", ""},
		{"POST", "/api/v1/imports/drafts/" + id, `{"base_revision":1,"undo":true}`},
		{"DELETE", "/api/v1/imports/drafts/" + id, ""},
		{"POST", "/api/v1/imports/preview", `{"stage_id":"` + id + `"}`},
		{"POST", "/api/v1/imports", `{"stage_id":"` + id + `","rev":1,"token":"imp_x"}`},
		{"POST", "/api/v1/imports/stage?kind=docx", ""},
	}
	for _, r := range routes {
		if rec := s.do(caller{name: "anon"}, r.method, r.path, r.body); rec.Code != 401 {
			t.Errorf("signed out, %s %s: %d, want 401", r.method, r.path, rec.Code)
		}
		if rec := s.do(reader, r.method, r.path, r.body); rec.Code != 403 {
			t.Errorf("reader, %s %s: %d, want 403", r.method, r.path, rec.Code)
		}
		if rec := s.doKey(pinned, r.method, r.path, r.body); rec.Code != 404 && rec.Code != 403 {
			t.Errorf("a key pinned to a project, %s %s: %d, want a refusal", r.method, r.path, rec.Code)
		}
		if strings.Contains(r.path, id) {
			if rec := s.do(other, r.method, r.path, r.body); rec.Code != 404 {
				t.Errorf("another writer, %s %s: %d, want 404", r.method, r.path, rec.Code)
			}
		}
	}
	// A list holds the caller's own.
	if rec := s.do(other, "GET", "/api/v1/imports/drafts", ""); strings.Contains(rec.Body.String(), id) {
		t.Error("another person's list names the draft")
	}
	if rec := s.do(w, "GET", "/api/v1/imports/drafts", ""); !strings.Contains(rec.Body.String(), id) {
		t.Error("the owner's list lacks the draft")
	}
}

// Nothing the review keeps is in what a browser stores: the page sets htmx's history cache to nothing, and a review's body is marked not to be
// snapshotted, so the first lines of a manuscript are not copied into local storage by the page's history.
func TestReviewIsNotKeptInTheBrowsersHistory(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One", "Two")
	_, page := s.getPage(t, w, "/import/d/"+id)
	if !strings.Contains(page, `"historyCacheSize":0`) || !strings.Contains(page, `id="imp-review" class="imp-review" hx-history="false"`) {
		t.Error("the review can be snapshotted by htmx's history")
	}
	if !strings.Contains(page, "no-store") && s.do(w, "GET", "/import/d/"+id, "").Header().Get("Cache-Control") != "no-store" {
		t.Error("the review can be cached")
	}
}

// A browser sends the fields in the order of the page: a form whose options come after its file, and a checkbox that sends a hidden
// "off" before its own value, are read as the last value of each, and the file is staged when the whole form is read.
func TestOptionsAfterTheFileAndACheckboxAreRead(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("_csrf", s.Sess.CSRFToken(sessionOf(t, s, w)))
	_ = mw.WriteField("kind", "docx")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="a.docx"`)
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write(testDocx(t, "One", "Two"))
	_ = mw.WriteField("opt.docx_headings", "parts")
	_ = mw.WriteField("opt.split_scenes", "none")
	_ = mw.WriteField("opt.split_scenes", "h2")
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/ui/import/stage", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(w.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatalf("stage: %d %.200s", rec.Code, rec.Body.String())
	}
	_, o := restJSON(t, s, w, "GET", "/api/v1/imports/drafts", "")
	opts := o["items"].([]any)[0].(map[string]any)["options"].(map[string]any)
	if opts["docx_headings"] != "parts" || opts["split_scenes"] != "h2" {
		t.Errorf("options: %v", opts)
	}
}

// A project with a passphrase: a person whose session has not unlocked it cannot stage a file for it, by the op or by the form, and the
// draft of a person who can is the owner's alone. The one whose session is unlocked can.
func TestStagingForALockedProjectNeedsAnUnlockedSession(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	docx := testDocx(t, "One")
	stage := func(c caller, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, bytes.NewReader(docx))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.AddCookie(c.cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := stage(w.editor, "/api/v1/imports/stage?kind=docx&project="+w.pid); rec.Code != 404 {
		t.Errorf("a locked session staging for the project: %d %.200s, want 404", rec.Code, rec.Body.String())
	}
	if rec := s.stageForm(t, w.editor, "?into="+w.slug, [][2]string{{"kind", "docx"}}, []stagePart{{"file", "a.docx", docx}}, false); rec.Code == 303 && strings.Contains(rec.Header().Get("Location"), "/import/d/") {
		t.Errorf("a locked session staged by the form: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec := stage(w.owner, "/api/v1/imports/stage?kind=docx&project="+w.pid)
	if rec.Code != 201 {
		t.Fatalf("an unlocked session staging for the project: %d %.200s", rec.Code, rec.Body.String())
	}
	var out struct {
		StageID string `json:"stage_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec := s.do(w.editor, "GET", "/api/v1/imports/drafts/"+out.StageID, ""); rec.Code != 404 {
		t.Errorf("another person reading the owner's draft: %d", rec.Code)
	}
}

// testDocxStyled is a Word file of the paragraphs given, with Heading 1 and Heading 2 defined.
func testDocxStyled(t *testing.T, inner string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8"?><w:document ` + testWns + `><w:body>` + inner + `</w:body></w:document>`},
		{"word/styles.xml", `<?xml version="1.0"?><w:styles ` + testWns + `><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style></w:styles>`},
	} {
		w, _ := zw.Create(f[0])
		_, _ = w.Write([]byte(f[1]))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// docxWith is a Word file of the paragraphs given as WordprocessingML, with no styles.
func docxWith(t *testing.T, inner string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8"?><w:document ` + testWns + `><w:body>` + inner + `</w:body></w:document>`},
	} {
		w, _ := zw.Create(f[0])
		_, _ = w.Write([]byte(f[1]))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// A book with only page breaks is one document with a plain message, its suggestions are forms, one press makes a chapter, and no
// key of the catalogue shows through as itself.
func TestTheReviewOfABookWithOnlyPageBreaks(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	para := func(x string) string {
		return `<w:p><w:r><w:t>` + strings.Repeat(x+" the tide came in over the stones. ", 8) + `</w:t></w:r></w:p>`
	}
	brk := `<w:p><w:r><w:br w:type="page"/></w:r></w:p>`
	data := docxWith(t, para("One")+brk+para("Two")+brk+para("Three"))
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "pages.docx", data}}, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("staging: %d %.300s", rec.Code, rec.Body.String())
	}
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/import/d/")
	_, page := s.getPage(t, w, "/import/d/"+id)
	for _, want := range []string{"No chapter breaks were found by heading, style or", "Found 2 page breaks.", "Possible chapter starts", "Start a chapter here", "Use all 2 page breaks", "How it was cut"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, key := range []string{"import.cut_", "import.why_", "import.sug_", "import.found_", "import.use_", "import.caps_", "import.big_", "import.left_"} {
		if strings.Contains(page, key) {
			t.Errorf("a catalogue key shows through: %s", key)
		}
	}
	ord := regexp.MustCompile(`name="ord" value="(\d+)"`).FindStringSubmatch(page)
	if ord == nil {
		t.Fatalf("no suggestion form: %.600s", page)
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, page)}, "act": {"break"}, "ord": {ord[1]}})
	b := rec.Body.String()
	if rec.Code != 200 || len(titlesOf(b)) != 2 || !strings.Contains(b, "Started a chapter: Chapter 2 begins here, after Chapter 1.") {
		t.Errorf("a chapter was not started: %d %.500s", rec.Code, b)
	}
	// the bulk button is the radio: both go through act=cut
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, b)}, "act": {"cut"}, "opt.docx_split": {"page_breaks"}})
	if b = rec.Body.String(); rec.Code != 200 || len(titlesOf(b)) != 3 || !strings.Contains(b, `value="page_breaks" checked`) {
		t.Errorf("page breaks: %d %.400s", rec.Code, b)
	}
}

// The ways to cut change nothing until the writer says so (so the arrow keys can walk the radios), a control's accessible name is its
// visible text (WCAG 2.5.3), and the parts override is offered when the file has two heading levels.
func TestTheCutFormHasAnApplyButtonAndNamesThatHoldTheirText(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	para := func(x string) string {
		return `<w:p><w:r><w:t>` + strings.Repeat(x+" the tide came in over the stones. ", 8) + `</w:t></w:r></w:p>`
	}
	brk := `<w:p><w:r><w:br w:type="page"/></w:r></w:p>`
	h := func(l int, x string) string {
		return `<w:p><w:pPr><w:pStyle w:val="Heading` + strconv.Itoa(l) + `"/></w:pPr><w:r><w:t>` + x + `</w:t></w:r></w:p>`
	}
	stage := func(inner string) (string, string) {
		rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "a.docx", testDocxStyled(t, inner)}}, false)
		id := strings.TrimPrefix(rec.Header().Get("Location"), "/import/d/")
		_, page := s.getPage(t, w, "/import/d/"+id)
		return id, page
	}
	id, page := stage(para("One") + brk + para("Two") + brk + para("Three"))
	form := regexp.MustCompile(`(?s)<form class="imp-opts imp-cutform".*?</form>`).FindString(page)
	if form == "" || strings.Contains(form, `hx-trigger="change"`) || !strings.Contains(form, `id="imp-cut-apply"`) {
		t.Errorf("the cut form changes on every arrow press or has no button: %.400s", form)
	}
	if strings.Contains(page, `aria-label="Start a chapter at`) || !strings.Contains(page, `aria-describedby="imp-sugl-`) {
		t.Errorf("the suggestion's button is named by its text and described by its line")
	}
	// posting the radio without the form's button does nothing; the form's own post cuts
	rec := s.update(t, w, id, true, url.Values{"rev": {revOf(t, page)}, "act": {"cut"}, "opt.docx_split": {"page_breaks"}, "opt.docx_headings": {"auto"}})
	if b := rec.Body.String(); len(titlesOf(b)) != 3 {
		t.Errorf("cut: %.300s", b)
	}
	// two heading levels offer the parts override, and it makes parts
	id, page = stage(h(1, "The Beginning") + h(2, "Ch A") + para("A") + h(2, "Ch B") + para("B") + h(1, "The Middle") + h(2, "Ch C") + para("C") + h(2, "Ch D") + para("D"))
	if !strings.Contains(page, `id="imp-cut-parts"`) {
		t.Fatalf("no parts checkbox: %.200s", page)
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, page)}, "act": {"cut"}, "opt.docx_split": {"auto"}, "opt.docx_headings": {"auto", "parts"}})
	if b := rec.Body.String(); !strings.Contains(b, `id="imp-cut-parts" type="checkbox" name="opt.docx_headings" value="parts" checked`) || !strings.Contains(b, `data-kind="part"`) {
		t.Errorf("parts: %.300s", b)
	}
}

// The writer's own breaks on the review: the lists are fragments of the owner's draft, a page at a time; each act is one small form; no
// catalogue key shows through; a name holds its visible text.
func TestTheWritersOwnBreaksOnTheReview(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	id := s.stageDocx(t, w, "a.docx", "One", "Two", "Three")
	_, page := s.getPage(t, w, "/import/d/"+id)
	keys := []string{}
	for _, m := range regexp.MustCompile(`<li class="imp-node d0" data-kind="[a-z_]+" data-key="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		keys = append(keys, m[1])
	}
	if len(keys) != 3 {
		t.Fatalf("keys: %v", keys)
	}
	// the list of paragraphs: another writer, a reader and a signed-out caller get what every page of the draft gives them
	look := "/ui/import/d/" + id + "/look?look=split&edit=" + url.QueryEscape(keys[1])
	if code, _ := s.getPage(t, other, look); code != 404 {
		t.Errorf("another writer: %d", code)
	}
	if code, _ := s.getPage(t, reader, look); code != 403 {
		t.Errorf("a reader: %d", code)
	}
	if rec := s.do(caller{name: "anon"}, "GET", look, ""); rec.Code != 303 {
		t.Errorf("signed out: %d", rec.Code)
	}
	if code, _ := s.getPage(t, w, "/ui/import/d/"+id+"/look?look=nothing&edit="+url.QueryEscape(keys[1])); code != 422 {
		t.Errorf("a look at nothing: %d", code)
	}
	if code, _ := s.getPage(t, w, "/ui/import/d/"+id+"/look?look=split&edit=nope.md"); code != 404 {
		t.Errorf("a chapter that is not there: %d", code)
	}
	// each chapter here has two paragraphs (a heading's text, then one): the first is not offered, so a one-paragraph chapter has no list
	code, frag := s.getPage(t, w, look)
	if code != 200 || strings.Contains(frag, `id="imp-review"`) || strings.Contains(frag, `value="splitat"`) || !strings.Contains(frag, "There is nowhere in this chapter to split it") {
		t.Fatalf("the list: %d %.500s", code, frag)
	}
	// the panel carries the controls, each name holds its visible text, and no catalogue key shows through
	_, panel := s.getPage(t, w, "/ui/import/d/"+id+"/row?key="+url.QueryEscape(keys[1]))
	for _, want := range []string{`>Join with previous</button>`, `aria-label="Join with previous: Two"`, `>This and everything above is front matter</button>`, `aria-label="This and everything above is front matter: Two"`,
		`>This and everything below is back matter</button>`, `>Split this chapter</button>`, `aria-label="Split this chapter: Two"`, `value="joinprev"`, `value="matter"`} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel lacks %s", want)
		}
	}
	_, full := s.getPage(t, w, "/import/d/"+id)
	for _, key := range []string{"import.join_prev", "import.matter_", "import.split_", "import.pat_", "import.in_", "import.prev_", "import.live_"} {
		if strings.Contains(panel+full, key) {
			t.Errorf("a catalogue key shows through: %s", key)
		}
	}
	if !strings.Contains(full, `id="imp-pat"`) || !strings.Contains(full, `name="test"`) || !strings.Contains(full, "Cut at lines that match a pattern") {
		t.Error("the pattern section is not on the page")
	}
	// join with previous: one press
	rev := revOf(t, full)
	rec := s.update(t, w, id, true, url.Values{"rev": {rev}, "act": {"joinprev"}, "key": {keys[1]}, "edit": {keys[1]}})
	b := rec.Body.String()
	if rec.Code != 200 || strings.Join(titlesOf(b), "|") != "One|Three" || !strings.Contains(b, "Joined Two into One") {
		t.Fatalf("join with previous: %d %.400s", rec.Code, b)
	}
	// the first cannot: a reason, shown, and nothing stored
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, b)}, "act": {"joinprev"}, "key": {keys[0]}, "edit": {keys[0]}})
	if b = rec.Body.String(); rec.Code != 422 || !strings.Contains(b, "There is no document before it that it can be joined into.") {
		t.Errorf("the first: %d %.300s", rec.Code, b)
	}
	// the pattern: a test cuts nothing and says what it matched; a pattern that cannot be read is refused in words, and kept in the field
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, b)}, "act": {"pattern"}, "opt.docx_split_pattern": {"three"}, "opt.docx_split_match": {"starts"}, "test": {"1"}})
	if b = rec.Body.String(); rec.Code != 200 || !strings.Contains(b, "It matches 1 paragraph:") || !strings.Contains(b, `value="three"`) || len(titlesOf(b)) != 2 {
		t.Errorf("a test: %d %.600s", rec.Code, b)
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, b)}, "act": {"pattern"}, "opt.docx_split_pattern": {"(abc"}, "opt.docx_split_match": {"regex"}, "test": {"1"}})
	if b = rec.Body.String(); rec.Code != 422 || !strings.Contains(b, "That is not an expression this program reads.") {
		t.Errorf("a pattern that cannot be read: %d %.400s", rec.Code, b)
	}
	// matter, and a split with a paragraph that is not there
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, b)}, "act": {"matter"}, "side": {"front"}, "key": {keys[0]}, "edit": {keys[0]}})
	if b = rec.Body.String(); rec.Code != 200 || !strings.Contains(b, "up to and including One, is front matter now.") {
		t.Errorf("matter: %d %.300s", rec.Code, b)
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, b)}, "act": {"splitat"}, "ord": {"9999"}, "key": {keys[0]}, "edit": {keys[0]}})
	if b = rec.Body.String(); rec.Code != 422 || !strings.Contains(b, "A chapter cannot start there, or one already does.") {
		t.Errorf("a paragraph that is not there: %d %.300s", rec.Code, b)
	}
}

// With no script the list is part of the page: "Show more" is a link to the same page asking for more of it, drawn from the start, so
// nothing is skipped. A book with no marks splits at its first break even though its one document is renamed by it.
func TestTheListOfParagraphsWorksWithNoScript(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, `<w:p><w:r><w:t>Paragraph number %d of a book with no marks, with a few words more.</w:t></w:r></w:p>`, i)
	}
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "plain.docx", docxWith(t, b.String())}}, false)
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/import/d/")
	_, page := s.getPage(t, w, "/import/d/"+id)
	key := regexp.MustCompile(`data-key="([^"]+)"`).FindStringSubmatch(page)[1]
	count := func(p string) int {
		return len(regexp.MustCompile(`name="ord" value="\d+" required`).FindAllString(p, -1))
	}
	_, p1 := s.getPage(t, w, "/import/d/"+id+"?edit="+url.QueryEscape(key)+"&look=split")
	if count(p1) != 40 || !strings.Contains(p1, "&amp;after=40") {
		t.Fatalf("the first page: %d paragraphs", count(p1))
	}
	_, p2 := s.getPage(t, w, "/import/d/"+id+"?edit="+url.QueryEscape(key)+"&look=split&after=40")
	if count(p2) != 80 || !strings.Contains(p2, "Paragraph number 1 ") {
		t.Errorf("the second page holds all of them so far: %d", count(p2))
	}
	// and a split at one of them, by the key the one document had
	ord := regexp.MustCompile(`name="ord" value="(\d+)"`).FindAllStringSubmatch(p1, -1)[5][1]
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, page)}, "act": {"splitat"}, "ord": {ord}, "key": {key}, "edit": {key}})
	if out := rec.Body.String(); rec.Code != 200 || len(titlesOf(out)) != 2 || !strings.Contains(out, "Split.") {
		t.Errorf("split: %d %.400s", rec.Code, out)
	}
}

// What the writer's own breaks promise a keyboard and a screen reader: a refused control is reachable and says why; what is inside a long
// chapter stays open and in place, and is made again, after a chapter is started from it; "Show more" moves focus to the first new place; a
// pattern that was refused keeps its section open; and with no script the page after a split opens at the chapter that was made.
func TestTheWritersOwnBreaksAreReachableAndStayInPlace(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	para := func(i int) string {
		return `<w:p><w:r><w:t>` + strings.Repeat("word ", 100) + fmt.Sprint(i) + `.</w:t></w:r></w:p>`
	}
	brk := `<w:p><w:r><w:br w:type="page"/></w:r></w:p>`
	var b strings.Builder
	b.WriteString(`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>The Long One</w:t></w:r></w:p>`)
	for i := 0; i < 160; i++ {
		if i > 0 && i%8 == 0 {
			b.WriteString(brk) // twenty places
		}
		b.WriteString(para(i))
	}
	b.WriteString(`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>The Short One</w:t></w:r></w:p>` + para(999))
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "long.docx", testDocxStyled(t, b.String())}}, false)
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/import/d/")
	_, page := s.getPage(t, w, "/import/d/"+id)
	keys := []string{}
	for _, m := range regexp.MustCompile(`<li class="imp-node d0" data-kind="[a-z_]+" data-key="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		keys = append(keys, m[1])
	}
	if len(keys) != 2 {
		t.Fatalf("keys: %v", keys)
	}
	// the first chapter cannot join with the one before: the button is reachable (no disabled) and described by the reason
	_, panel := s.getPage(t, w, "/ui/import/d/"+id+"/row?key="+url.QueryEscape(keys[0]))
	if !strings.Contains(panel, `aria-disabled="true" aria-describedby="imp-c-`) || strings.Contains(panel, `disabled aria-label="Join with previous`) || !strings.Contains(panel, "-prevwhy") {
		t.Errorf("the refused control is not reachable and explained: %.600s", panel)
	}
	// what is inside: the first page has the heading for focus; the next page's first place takes focus
	_, in1 := s.getPage(t, w, "/ui/import/d/"+id+"/look?look=inside&edit="+url.QueryEscape(keys[0]))
	if !strings.Contains(in1, `id="imp-in-h-`) || !strings.Contains(in1, ` data-focus`) || !strings.Contains(in1, `name="look" value="inside"`) {
		t.Fatalf("what is inside: %.500s", in1)
	}
	_, more := s.getPage(t, w, "/ui/import/d/"+id+"/look?look=inside&edit="+url.QueryEscape(keys[0])+"&after=10&more=1")
	if strings.Count(more, ` data-focus`) != 1 || !strings.Contains(more, `<li class="imp-sug">`) {
		t.Errorf("more places: %d focus marks", strings.Count(more, ` data-focus`))
	}
	// start a chapter from it: the panel is still there, drawn again, with focus on the next place; the one made is a chapter of the review
	ord := regexp.MustCompile(`name="ord" value="(\d+)"`).FindStringSubmatch(in1)[1]
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, page)}, "act": {"splitat"}, "ord": {ord}, "key": {keys[0]}, "edit": {keys[0]}, "look": {"inside"}})
	out := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(out, `class="imp-inside"`) || !strings.Contains(out, "Split.") || len(titlesOf(out)) != 3 {
		t.Fatalf("started from what is inside: %d %.400s", rec.Code, out)
	}
	if strings.Count(out, `id="imp-ls-`) < 5 || !regexp.MustCompile(`id="imp-ls-[^"]+" type="submit"[^>]* data-focus`).MatchString(out) {
		t.Errorf("focus is not on the next place: %d", strings.Count(out, ` data-focus`))
	}
	// with no script: the redirect carries the chapter that was made, so the page opens at it
	panelOut := out[strings.Index(out, `class="imp-inside"`):]
	nk := regexp.MustCompile(`name="key" value="([^"]+)"`).FindStringSubmatch(panelOut)[1] // the chapter that begins there: the panel follows the places to come
	rec = s.update(t, w, id, false, url.Values{"rev": {revOf(t, out)}, "act": {"splitat"}, "ord": {regexp.MustCompile(`name="ord" value="(\d+)"`).FindAllStringSubmatch(panelOut, -1)[3][1]}, "key": {nk}, "edit": {nk}})
	if loc := rec.Header().Get("Location"); rec.Code != 303 || !strings.Contains(loc, "reveal=") || !strings.Contains(loc, "#imp-r-") {
		t.Errorf("the redirect: %d %q %.300s", rec.Code, loc, rec.Body.String())
	}
	// a pattern that was refused keeps its section open
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, page)}, "act": {"pattern"}, "opt.docx_split_pattern": {"(abc"}, "opt.docx_split_match": {"regex"}, "test": {"1"}})
	if body := rec.Body.String(); rec.Code != 422 && rec.Code != 409 || !strings.Contains(body, `<details class="imp-pat" open`) {
		t.Errorf("the refused pattern: %d %.200s", rec.Code, body)
	}
}
