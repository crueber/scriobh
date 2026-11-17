package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

type typoWorld struct {
	s                                              *Server
	owner, editor, reviewer, reader, stranger, key caller
	pid, slug, did                                 string
	writeURL, readURL                              string
}

func newTypoWorld(t *testing.T) *typoWorld {
	t.Helper()
	s := newAuthzServer(t)
	w := &typoWorld{s: s}
	w.owner = s.newCaller(t, "owner", ops.RoleWriter)
	w.editor = s.newCaller(t, "editor", ops.RoleWriter)
	w.reviewer = s.newCaller(t, "reviewer", ops.RoleReader)
	w.reader = s.newCaller(t, "reader", ops.RoleReader)
	w.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	rec := s.do(w.owner, "POST", "/api/v1/projects", `{"title":"Typeset Book"}`)
	w.pid = idOf(t, rec.Body.Bytes())
	var pj struct{ Item struct{ Slug string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &pj)
	w.slug = pj.Item.Slug
	s.addMember(t, w.pid, w.editor.userID, ops.ProjectEditor)
	s.addMember(t, w.pid, w.reviewer.userID, ops.ProjectReviewer)
	s.addMember(t, w.pid, w.reader.userID, ops.ProjectReader)
	rec = s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/docs", `{"title":"Chapter one","content":"Some words."}`)
	w.did = idOf(t, rec.Body.Bytes())
	w.writeURL = "/p/" + w.slug + "/d/" + w.did
	w.readURL = "/p/" + w.slug + "/read/" + w.did
	return w
}

func (w *typoWorld) page(t *testing.T, c caller, path string) string {
	t.Helper()
	rec := w.s.do(c, "GET", path, "")
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %.200s", path, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

var paperTag = regexp.MustCompile(`<article class="paper[^>]*>`)

func (w *typoWorld) paperTags(t *testing.T, c caller, path string) []string {
	t.Helper()
	return paperTag.FindAllString(w.page(t, c, path), -1)
}

const typoBase = "/api/v1/projects/%s/typography"

func (w *typoWorld) put(c caller, body string) *httptest.ResponseRecorder {
	return w.s.do(c, "PUT", strings.Replace(typoBase, "%s", w.pid, 1), body)
}

// A project that sets nothing renders byte for byte as before: no attribute, the exact tag the templates always
// wrote, and after setting and resetting the whole page is the same bytes.
func TestNoTypographyRendersAsBefore(t *testing.T) {
	w := newTypoWorld(t)
	for _, path := range []string{w.writeURL, w.readURL} {
		tags := w.paperTags(t, w.owner, path)
		if len(tags) != 1 || strings.Contains(tags[0], " data-t") {
			t.Fatalf("%s: %v", path, tags)
		}
	}
	for _, path := range []string{w.writeURL, w.readURL} {
		if strings.Contains(w.page(t, w.owner, path), "typography") {
			t.Errorf("%s links typography.css though nothing is set: the page is not what it was", path)
		}
	}
	want := w.paperTags(t, w.owner, w.writeURL)[0]
	if want != `<article class="paper" id="paper" lang="en" dir="ltr" data-size="19" data-measure="66" data-lh="165" data-font="serif" aria-labelledby="doc-title-h">` {
		t.Fatalf("the writing view's paper tag is not what it always was: %s", want)
	}
	before := w.page(t, w.owner, w.writeURL)
	beforeRead := w.page(t, w.owner, w.readURL)
	if rec := w.put(w.editor, `{"font":"system-sans","size":120,"line_height":"1.8","measure":55,"paragraphs":"indent","scene_break":"rule"}`); rec.Code != 200 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if rec := w.s.do(w.editor, "POST", "/api/v1/projects/"+w.pid+"/typography/reset", ""); rec.Code != 200 {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body)
	}
	if after := w.page(t, w.owner, w.writeURL); after != before {
		t.Fatal("the writing view differs after a set and a reset")
	}
	if after := w.page(t, w.owner, w.readURL); after != beforeRead {
		t.Fatal("the reading view differs after a set and a reset")
	}
}

// What the pages carry once it is set: the three bundled faces through data-font, the rest through data attributes, for
// the people who may read the project, and never in a share link's page.
func TestTypographyReachesTheWritingAndReadingViews(t *testing.T) {
	w := newTypoWorld(t)
	if rec := w.put(w.owner, `{"font":"mono","size":115,"line_height":"1.35","measure":60,"paragraphs":"indent","scene_break":"rule"}`); rec.Code != 200 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []caller{w.owner, w.editor, w.reviewer, w.reader} {
		for _, path := range []string{w.writeURL, w.readURL} {
			tag := w.paperTags(t, c, path)[0]
			for _, want := range []string{`data-font="mono"`, ` data-ts="115"`, ` data-tlh="135"`, ` data-tm="60"`, ` data-tp="indent"`, ` data-tb="rule"`} {
				if !strings.Contains(tag, want) {
					t.Errorf("%s as %s: %q missing from %s", path, c.name, want, tag)
				}
			}
		}
	}
	// a system face is a data attribute, not a data-font value
	w.put(w.owner, `{"font":"system-serif"}`)
	if tag := w.paperTags(t, w.owner, w.writeURL)[0]; !strings.Contains(tag, ` data-tf="sys-serif"`) || !strings.Contains(tag, `data-font="serif"`) {
		t.Errorf("system face: %s", tag)
	}
	// the person's override wins in their own browser and in nobody else's
	w.put(w.owner, `{"size":120,"measure":70}`)
	if rec := w.s.do(w.reader, "PUT", "/api/v1/me/typography", `{"size":105,"paragraphs":"indent"}`); rec.Code != 200 {
		t.Fatalf("account set: %d %s", rec.Code, rec.Body)
	}
	rt := w.paperTags(t, w.reader, w.readURL)[0]
	if !strings.Contains(rt, ` data-ts="105"`) || !strings.Contains(rt, ` data-tm="70"`) || !strings.Contains(rt, ` data-tp="indent"`) {
		t.Errorf("the reader's own override and the project's measure: %s", rt)
	}
	if ot := w.paperTags(t, w.owner, w.readURL)[0]; !strings.Contains(ot, ` data-ts="120"`) || strings.Contains(ot, ` data-tp=`) {
		t.Errorf("the override leaked to the owner: %s", ot)
	}
	if rec := w.s.do(w.reader, "POST", "/api/v1/me/typography/reset", ""); rec.Code != 200 {
		t.Fatalf("account reset: %d", rec.Code)
	}
	if rt := w.paperTags(t, w.reader, w.readURL)[0]; !strings.Contains(rt, ` data-ts="120"`) {
		t.Errorf("after the reset the project's size applies: %s", rt)
	}
	// the stylesheet is linked by the two views and by nothing else that draws a manuscript
	if !strings.Contains(w.page(t, w.owner, w.writeURL), "typography") || !strings.Contains(w.page(t, w.owner, w.readURL), "typography") {
		t.Error("a view does not link typography.css")
	}
}

// Who may do what: any member reads, an owner or editor writes, a stranger gets the 404.
func TestTypographyRoles(t *testing.T) {
	w := newTypoWorld(t)
	good := `{"size":110}`
	for _, tc := range []struct {
		who      caller
		get, set int
	}{
		{w.owner, 200, 200}, {w.editor, 200, 200}, {w.reviewer, 200, 403}, {w.reader, 200, 403}, {w.stranger, 404, 404}, {caller{}, 401, 401},
	} {
		if rec := w.s.do(tc.who, "GET", strings.Replace(typoBase, "%s", w.pid, 1), ""); rec.Code != tc.get {
			t.Errorf("%s get: %d, want %d", tc.who.name, rec.Code, tc.get)
		}
		if rec := w.put(tc.who, good); rec.Code != tc.set {
			t.Errorf("%s set: %d, want %d", tc.who.name, rec.Code, tc.set)
		}
		if rec := w.s.do(tc.who, "POST", "/api/v1/projects/"+w.pid+"/typography/reset", ""); rec.Code != tc.set {
			t.Errorf("%s reset: %d, want %d", tc.who.name, rec.Code, tc.set)
		}
	}
	// the answer carries the fixed schema for a screen
	rec := w.s.do(w.reader, "GET", strings.Replace(typoBase, "%s", w.pid, 1), "")
	var out struct {
		Schema struct {
			Fonts       []string
			Sizes       []int
			LineHeights []string `json:"line_heights"`
			Measures    []int
			Notice      string
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Schema.Fonts) != 6 || len(out.Schema.Sizes) != 9 || len(out.Schema.LineHeights) != 15 || len(out.Schema.Measures) != 9 || !strings.Contains(out.Schema.Notice, "never use these") {
		t.Errorf("schema: %+v", out.Schema)
	}
}

// Anything off the list is refused, field by field, and nothing is stored.
func TestTypographyRefusesWhatIsNotOnTheList(t *testing.T) {
	w := newTypoWorld(t)
	for _, body := range []string{
		`{"font":"Comic Sans"}`, `{"font":"url(http://x/y.woff2)"}`, `{"font":"serif; } body { display:none"}`, `{"size":99}`, `{"size":141}`, `{"size":112}`, `{"size":-5}`,
		`{"line_height":"1.2"}`, `{"line_height":"1.30"}`, `{"line_height":"2.05"}`, `{"line_height":"1.3; x:y"}`, `{"line_height":"expression(alert(1))"}`,
		`{"measure":39}`, `{"measure":81}`, `{"measure":65}`, `{"measure":0.5}`, `{"paragraphs":"hanging"}`, `{"scene_break":"fleuron"}`,
		`{"css":"body{}"}`, `{"font":"serif","style":"x"}`, `{"size":"110"}`, `{"stylesheet":"https://x/y.css"}`, `[]`, `"x"`,
	} {
		rec := w.put(w.editor, body)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("%s: %d %s, want a 4xx", body, rec.Code, rec.Body)
		}
	}
	rec := w.s.do(w.editor, "GET", strings.Replace(typoBase, "%s", w.pid, 1), "")
	var out struct{ Project struct{ Set bool } }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Project.Set {
		t.Fatal("a refused call stored something")
	}
	// the same list for the person's override
	if rec := w.s.do(w.editor, "PUT", "/api/v1/me/typography", `{"font":"x"}`); rec.Code != 422 {
		t.Errorf("account set with a bad font: %d", rec.Code)
	}
}

// A preview stores nothing; a trashed project and a locked project refuse a write; a key with write scope may set it and one
// with read scope may not.
func TestTypographyDryRunTrashGateAndKeys(t *testing.T) {
	w := newTypoWorld(t)
	path := strings.Replace(typoBase, "%s", w.pid, 1)
	if rec := w.s.doDry(w.owner, "PUT", path, `{"size":130}`); rec.Code != 200 {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body)
	}
	var out struct{ Project struct{ Set bool } }
	_ = json.Unmarshal(w.s.do(w.owner, "GET", path, "").Body.Bytes(), &out)
	if out.Project.Set {
		t.Fatal("a preview stored the typography")
	}
	// keys
	wk, _ := w.s.mintKey(t, w.owner, `{"name":"w","scope":"write","project_id":"`+w.pid+`"}`)
	rk, _ := w.s.mintKey(t, w.owner, `{"name":"r","scope":"read","project_id":"`+w.pid+`"}`)
	do := func(key, method, body string) int {
		req := typoReq(method, path, body)
		req.Header.Set("Authorization", "Bearer "+key)
		return typoServe(w.s, req).Code
	}
	if c := do(wk, "PUT", `{"size":110}`); c != 200 {
		t.Errorf("a write-scope key: %d", c)
	}
	if c := do(rk, "PUT", `{"size":115}`); c != http.StatusForbidden {
		t.Errorf("a read-scope key: %d", c)
	}
	if c := do(rk, "GET", ""); c != 200 {
		t.Errorf("a read-scope key reading: %d", c)
	}
	// a key pinned to a project cannot reach the person's own override
	if c := func() int {
		req := typoReq("GET", "/api/v1/me/typography", "")
		req.Header.Set("Authorization", "Bearer "+wk)
		return typoServe(w.s, req).Code
	}(); c < 400 {
		t.Errorf("a pinned key read the account override: %d", c)
	}
	// the gate
	if rec := w.s.do(w.owner, "PUT", "/api/v1/projects/"+w.pid+"/passphrase", `{"passphrase":"a long enough one"}`); rec.Code != 200 {
		t.Fatalf("passphrase: %d", rec.Code)
	}
	for _, m := range []string{"GET", "PUT"} {
		if rec := w.s.do(w.editor, m, path, `{"size":105}`); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "project_locked") {
			t.Errorf("%s while locked: %d %s", m, rec.Code, rec.Body)
		}
	}
	// the page of a locked project is not drawn at all
	if rec := w.s.do(w.editor, "GET", w.readURL, ""); rec.Code == 200 {
		t.Errorf("a locked reading view: %d", rec.Code)
	}
	if rec := w.s.do(w.owner, "DELETE", "/api/v1/projects/"+w.pid+"/passphrase", `{"current":"a long enough one"}`); rec.Code != 200 {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	// the trash
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/trash", ""); rec.Code != 200 {
		t.Fatalf("trash: %d", rec.Code)
	}
	if rec := w.put(w.owner, `{"size":120}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "project_trashed") {
		t.Errorf("set on a trashed project: %d %s", rec.Code, rec.Body)
	}
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/typography/reset", ""); rec.Code != http.StatusConflict {
		t.Errorf("reset on a trashed project: %d", rec.Code)
	}
}

// Typography is for the two views: a share link's page, an export, the compile preview's files and the static site do
// not carry it, whatever the project and the reader set.
func TestTypographyNeverReachesSharesExportsOrTheSite(t *testing.T) {
	w := newTypoWorld(t)
	w.put(w.owner, `{"font":"system-mono","size":140,"line_height":"2","measure":40,"paragraphs":"indent","scene_break":"rule"}`)
	w.s.do(w.owner, "PUT", "/api/v1/me/typography", `{"size":135}`)
	rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/shares", `{"label":"t","scope":"read"}`)
	if rec.Code >= 300 {
		t.Fatalf("share: %d %s", rec.Code, rec.Body)
	}
	var sh struct{ Path string }
	_ = json.Unmarshal(rec.Body.Bytes(), &sh)
	if !strings.HasPrefix(sh.Path, "/s/") {
		t.Fatalf("share path %q", sh.Path)
	}
	hreq := typoReq("GET", sh.Path, "")
	hreq.Header.Set("Accept", "text/html")
	page := typoServe(w.s, hreq)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "reading-paper") {
		t.Fatalf("the share page: %d %.600s", page.Code, page.Body)
	}
	if b := page.Body.String(); strings.Contains(b, " data-ts=") || strings.Contains(b, " data-tlh=") || strings.Contains(b, "typography.css") || strings.Contains(b, "data-tf=") {
		t.Errorf("a share link's page carries typography")
	}
	for _, p := range []string{"/api/v1/projects/" + w.pid + "/export/markdown", "/api/v1/projects/" + w.pid + "/export/html"} {
		rec := w.s.do(w.owner, "POST", p, `{}`)
		if strings.Contains(rec.Body.String(), "data-ts") || strings.Contains(rec.Body.String(), "system-mono") {
			t.Errorf("%s carries typography", p)
		}
	}
}

func typoReq(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func typoServe(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// A key reading the project's typography is told no personal override and gets the project's own as the effective result; the
// person's own ops answer their key as they always did (an unpinned key may set and read the override of the person it acts for).
func TestKeysAndThePersonalOverride(t *testing.T) {
	w := newTypoWorld(t)
	w.put(w.owner, `{"size":110}`)
	if rec := w.s.do(w.owner, "PUT", "/api/v1/me/typography", `{"line_height":"1.3"}`); rec.Code != 200 {
		t.Fatalf("override: %d", rec.Code)
	}
	secret, _ := w.s.mintKey(t, w.owner, `{"name":"k","scope":"write"}`)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := typoReq(method, path, body)
		req.Header.Set("Authorization", "Bearer "+secret)
		return typoServe(w.s, req)
	}
	var out struct {
		Mine      struct{ Set bool }
		Effective struct {
			Size       int
			LineHeight string `json:"line_height"`
		}
	}
	rec := call("GET", strings.Replace(typoBase, "%s", w.pid, 1), "")
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out.Mine.Set || out.Effective.LineHeight != "" || out.Effective.Size != 110 {
		t.Errorf("a key reading the project's typography: %d %s", rec.Code, rec.Body)
	}
	// the account ops, for the person the key acts for
	if rec := call("PUT", "/api/v1/me/typography", `{"measure":60}`); rec.Code != 200 {
		t.Fatalf("a key setting the override: %d %s", rec.Code, rec.Body)
	}
	out.Mine.Set = false
	rec = call("GET", "/api/v1/me/typography", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || !out.Mine.Set {
		t.Errorf("a key reading the override it set: %d %s", rec.Code, rec.Body)
	}
}
