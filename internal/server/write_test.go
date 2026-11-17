package server

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// ---- fixtures for the template lint ------------------------------------------

// writeFixture is the write page's data: a small nested manuscript with every
// kind of row (part, chapters, scenes, an untitled note) and hostile text.
func writeFixture(p projects.Project, role string, withDoc bool, guard ...string) writeData {
	meta := func(id, parent, kind, title string, pos float64, words int) docs.Meta {
		return docs.Meta{ID: id, ProjectID: p.ID, ParentID: parent, Kind: kind, Title: title, Position: pos, WordCount: words, Revision: 3}
	}
	items := []docs.Meta{
		meta("doc_a", "", "part", "Part One <i>The Shore</i>", 1, 0),
		meta("doc_b", "doc_a", "chapter", "The Last Ferry", 1, 3102),
		meta("doc_c", "doc_a", "chapter", "A House With Two Doors", 2, 2140),
		meta("doc_d", "doc_c", "scene", "The kitchen", 1, 810),
		meta("doc_e", "", "chapter", "", 2, 0),
		meta("doc_f", "", "note", "Research & \"notes\"", 3, 12),
	}
	order := []string{"doc_a", "doc_b", "doc_c", "doc_d", "doc_e", "doc_f"}
	canEdit := role == ops.ProjectEditor || role == ops.ProjectOwner
	cur := ""
	if withDoc {
		cur = "doc_c"
	}
	proj := p
	proj.Role = role
	tv := buildTree(items, order, proj, cur, canEdit, "tok_writer")
	d := writeData{Project: proj, Slug: p.Slug, ProjectID: p.ID, CanEdit: canEdit, Tree: tv, Prefs: defaultPrefs(), Count: len(order)}
	byID := map[string]docs.Meta{}
	for _, m := range items {
		byID[m.ID] = m
	}
	d.Jump = buildJump(tv, byID)
	if withDoc {
		doc := docs.Doc{Meta: items[2], Content: "She said \"hello\" & left.\n\n</pre><script>alert(1)</script>\n\n## A heading\n\n- one\n- two\n\n***\n\nSee [[The Last Ferry]].", Ancestors: []docs.Crumb{{ID: "doc_a", Title: "Part One <i>The Shore</i>", Kind: "part"}}}
		d.Doc = &doc
		d.Crumbs = doc.Ancestors
		d.Parent = &doc.Ancestors[0]
		d.Number = "2"
		d.Position = 3
		d.Prev, d.Next = &d.Jump[1], &d.Jump[3]
		if !canEdit {
			d.ReadOnly = "role"
		}
		if len(guard) > 0 {
			d.ReadOnly = "length"
			d.Doc.Guard = "readonly"
		}
	}
	return d
}

// ---- helpers --------------------------------------------------------------------

type writeEnv struct {
	s      *Server
	owner  caller
	pid    string
	slug   string
	chap   string // first (seeded) document
	pidOth string // another project of the same owner
	docOth string // a document in that other project
}

func newWriteEnv(t *testing.T) writeEnv {
	t.Helper()
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	ctx := context.Background()
	op := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(ctx, op, "project.create", projects.CreateIn{Title: "The Salt Road", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	lo, err := s.Reg.Call(ctx, op, "doc.list", docs.ListIn{ProjectID: pr.ID})
	if err != nil {
		t.Fatal(err)
	}
	out2, err := s.Reg.Call(ctx, op, "project.create", projects.CreateIn{Title: "Other Book", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	po := out2.(projects.Item).Item
	do, err := s.Reg.Call(ctx, op, "doc.create", docs.CreateIn{ProjectID: po.ID, Title: "Secret chapter title"})
	if err != nil {
		t.Fatal(err)
	}
	return writeEnv{s: s, owner: owner, pid: pr.ID, slug: pr.Slug, chap: lo.(docs.ListOut).Order[0], pidOth: po.ID, docOth: do.(docs.Item).Item.ID}
}

func (e writeEnv) call(t *testing.T, op string, in any) any {
	t.Helper()
	out, err := e.s.Reg.Call(context.Background(), ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}, op, in)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (e writeEnv) create(t *testing.T, title, kind, parent, content string) string {
	t.Helper()
	return e.call(t, "doc.create", docs.CreateIn{ProjectID: e.pid, Title: title, Kind: kind, ParentID: parent, Content: content}).(docs.Item).Item.ID
}

// post sends a UI form POST as c, with the session's CSRF token unless token is "-".
func (s *Server) post(t *testing.T, c caller, path string, form url.Values, token string, fetch bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if fetch {
		req.Header.Set(fetchHeader, "1")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
		if token == "" {
			_, info, err := s.Sess.Lookup(context.Background(), c.cookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			token = s.Sess.CSRFToken(info.ID)
		}
	}
	if token != "-" {
		req.Header.Set("X-CSRF-Token", token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// ---- the page --------------------------------------------------------------------

func TestWritePage(t *testing.T) {
	e := newWriteEnv(t)
	hostile := "She said \"hello\" & left.\n\n</pre><script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n"
	part := e.create(t, "Part One", "part", "", "")
	chap := e.create(t, "The Harbour", "chapter", part, hostile)

	// /p/{slug} resumes at the first document in reading order, in place (no redirect).
	rec := e.s.do(e.owner, "GET", "/p/"+e.slug, "")
	if rec.Code != 200 {
		t.Fatalf("GET /p/slug = %d", rec.Code)
	}
	body := rec.Body.String()
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("the writing view's Cache-Control is %q: Back would show the text from before the last save", cc)
	}
	if !strings.Contains(body, `data-doc="`+e.chap+`"`) {
		t.Errorf("the project page did not open the first document %s", e.chap)
	}
	for _, need := range []string{`id="tree"`, `role="tree"`, `id="editor"`, `id="doc-plain"`, `/static/js/editor.`, `/static/js/doc.`, `/static/css/write.`} {
		if !strings.Contains(body, need) {
			t.Errorf("page is missing %s", need)
		}
	}

	// A document page: the body is present as inert text and escaped.
	rec = e.s.do(e.owner, "GET", "/p/"+e.slug+"/d/"+chap, "")
	if rec.Code != 200 {
		t.Fatalf("GET document = %d", rec.Code)
	}
	body = rec.Body.String()
	lint(t, "write page", body)
	structure(t, "write page", body)
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
		t.Error("Markdown reached the page as HTML")
	}
	if !strings.Contains(body, "&lt;/pre&gt;&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the escaped body is not in the page for a reader without JavaScript")
	}
	if !regexp.MustCompile(`(?s)<pre id="doc-plain"[^>]*>\n`).MatchString(body) {
		t.Error("the body must follow a newline after <pre> (the parser drops the first one)")
	}
	if !strings.Contains(body, `aria-current="page"`) || strings.Count(body, `aria-current="page"`) < 1 {
		t.Error("the current document is not marked in the tree")
	}
	if !strings.Contains(body, `data-can-edit="true"`) {
		t.Error("an owner must get an editable page")
	}

	// Foreign and trashed ids look exactly like missing ones; nothing leaks.
	missing := e.s.do(e.owner, "GET", "/p/"+e.slug+"/d/doc_00000000000000000000000000", "")
	foreign := e.s.do(e.owner, "GET", "/p/"+e.slug+"/d/"+e.docOth, "")
	if missing.Code != 404 || foreign.Code != 404 {
		t.Fatalf("missing %d, foreign %d; want 404 and 404", missing.Code, foreign.Code)
	}
	if strings.Contains(foreign.Body.String(), "Secret chapter title") || foreign.Body.String() != missing.Body.String() {
		t.Error("the foreign-document page differs from the missing-document page")
	}
	e.call(t, "doc.trash", docs.DocIn{ID: chap})
	trashed := e.s.do(e.owner, "GET", "/p/"+e.slug+"/d/"+chap, "")
	if trashed.Code != 404 || strings.Contains(trashed.Body.String(), "The Harbour") {
		t.Errorf("a trashed document: %d, and it must not show its title", trashed.Code)
	}

	// A stranger and a signed-out visitor.
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	if rec := e.s.do(stranger, "GET", "/p/"+e.slug+"/d/"+e.chap, ""); rec.Code != 404 {
		t.Errorf("a stranger gets %d, want 404", rec.Code)
	}
	if rec := e.s.do(caller{name: "anon"}, "GET", "/p/"+e.slug+"/d/"+e.chap, ""); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login") {
		t.Errorf("signed out: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestWritePageForReadersAndLongDocuments(t *testing.T) {
	e := newWriteEnv(t)
	reader := e.s.newCaller(t, "reader", ops.RoleReader)
	e.s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	rec := e.s.do(reader, "GET", "/p/"+e.slug, "")
	if rec.Code != 200 {
		t.Fatalf("reader: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-can-edit="false"`) || !strings.Contains(body, `data-readonly="role"`) {
		t.Error("a project reader must get a read-only page")
	}
	if strings.Contains(body, `data-row-menu`) || strings.Contains(body, `id="add-btn"`) {
		t.Error("a reader sees editing controls")
	}

	long := strings.Repeat("word ", docs.ReadOnlyWords+10)
	big := e.create(t, "Too long", "chapter", "", long)
	rec = e.s.do(e.owner, "GET", "/p/"+e.slug+"/d/"+big, "")
	if !strings.Contains(rec.Body.String(), `data-guard="readonly"`) || !strings.Contains(rec.Body.String(), `data-readonly="length"`) {
		t.Error("a document over the limit must open read-only with its reason")
	}
}

func TestEmptyProjectPage(t *testing.T) {
	e := newWriteEnv(t)
	e.call(t, "doc.trash", docs.DocIn{ID: e.chap})
	rec := e.s.do(e.owner, "GET", "/p/"+e.slug, "")
	if rec.Code != 200 {
		t.Fatalf("empty project: %d", rec.Code)
	}
	body := rec.Body.String()
	lint(t, "empty project", body)
	structure(t, "empty project", body)
	if !strings.Contains(body, "No chapters yet. Add one to begin.") || strings.Contains(body, "/static/js/editor.") {
		t.Error("an empty project shows the empty state and loads no editor")
	}
	// Without JavaScript the add button is a real form that redirects to the new page.
	rec = e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/docs", url.Values{"kind": {"chapter"}}, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/p/"+e.slug+"/d/") {
		t.Errorf("add chapter without JS: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

// ---- the tree fragment ----------------------------------------------------------------

func TestTreeFragment(t *testing.T) {
	s := newWebServer(t)
	d := writeFixture(fixtureProjects()[0], "editor", true)
	html := renderFragment(t, s, fragmentFixture{"tree", "tree", d.Tree})
	lint(t, "tree", html)

	if !strings.Contains(html, `role="tree"`) {
		t.Fatal("not a tree")
	}
	type row struct {
		level, set, pos, expanded, current, selected string
		kind, words                                  string
	}
	var rows []row
	for _, tg := range tags(html) {
		if tg.attrs["role"] != "treeitem" {
			continue
		}
		rows = append(rows, row{tg.attrs["aria-level"], tg.attrs["aria-setsize"], tg.attrs["aria-posinset"], tg.attrs["aria-expanded"], tg.attrs["aria-current"], tg.attrs["aria-selected"], tg.attrs["data-kind"], tg.attrs["data-words"]})
		if tg.attrs["tabindex"] != "0" && tg.attrs["tabindex"] != "-1" {
			t.Errorf("a row has no roving tabindex: %v", tg.attrs)
		}
	}
	if len(rows) != 6 {
		t.Fatalf("%d rows, want 6", len(rows))
	}
	levels := []string{"1", "2", "2", "3", "1", "1"}
	for i, r := range rows {
		if r.level != levels[i] {
			t.Errorf("row %d level %s, want %s", i, r.level, levels[i])
		}
	}
	// Only folders carry aria-expanded; exactly one row is current.
	wantExp := []string{"true", "", "true", "", "", ""}
	cur := 0
	for i, r := range rows {
		if r.expanded != wantExp[i] {
			t.Errorf("row %d aria-expanded %q, want %q", i, r.expanded, wantExp[i])
		}
		if r.current == "page" {
			cur++
			if i != 2 || r.selected != "true" {
				t.Errorf("the wrong row is current: %d %v", i, r)
			}
		}
	}
	if cur != 1 {
		t.Errorf("%d current rows", cur)
	}
	// Set sizes and positions among siblings: three at the top, two in the part, one scene.
	if rows[0].set != "3" || rows[0].pos != "1" || rows[4].pos != "2" || rows[5].pos != "3" || rows[1].set != "2" || rows[2].pos != "2" || rows[3].set != "1" {
		t.Errorf("posinset/setsize wrong: %+v", rows)
	}
	// Folders show sums: the part 3102+2140+810, the chapter 2140+810; the project total at the foot.
	for _, want := range []string{">6,052<", ">2,950<", ">3,102<", ">810<", ">6,064<"} {
		if !strings.Contains(html, want) {
			t.Errorf("tree does not show %s", want)
		}
	}
	// Chapters are numbered across parts; parts, scenes and notes are not. Untitled rows say so.
	if got := regexp.MustCompile(`class="tree-num">(\d+)<`).FindAllStringSubmatch(html, -1); len(got) != 3 || got[0][1] != "1" || got[2][1] != "3" {
		t.Errorf("chapter numbers: %v", got)
	}
	if !strings.Contains(html, ">Untitled<") {
		t.Error("an untitled document must be shown as Untitled")
	}
	// Titles are text, never markup.
	if strings.Contains(html, "<i>The Shore</i>") || !strings.Contains(html, "&lt;i&gt;The Shore&lt;/i&gt;") {
		t.Error("a title reached the tree as markup")
	}
	// Indent guides: one hairline per ancestor level.
	if n := strings.Count(html, `class="guide"`); n != 1+1+2 {
		t.Errorf("%d guides, want 4", n)
	}
	// A reader's tree has no row actions.
	rd := renderFragment(t, s, fragmentFixture{"tree", "tree", writeFixture(fixtureProjects()[0], "reader", true).Tree})
	if strings.Contains(rd, "data-row-menu") {
		t.Error("a reader's tree offers row actions")
	}
}

// ---- the routes that change the tree ----------------------------------------------------

func TestTreeRoutes(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s

	// Create: a fragment with the new row, its id in a header.
	rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/docs", url.Values{"kind": {"scene"}, "title": {"Opening"}, "parent_id": {e.chap}, "current": {e.chap}}, "", true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := rec.Header().Get("X-Doc-Id")
	if id == "" || !strings.Contains(rec.Body.String(), `id="tr-`+id+`"`) || !strings.Contains(rec.Body.String(), `data-parent="`+e.chap+`"`) {
		t.Errorf("create did not return the new row: %s", rec.Body)
	}
	// Move it to the top level, first.
	rec = s.post(t, e.owner, "/ui/docs/"+id+"/move", url.Values{"parent_id": {""}, "after": {""}}, "", true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `data-id="`+id+`" data-parent="`+e.chap+`"`) {
		t.Fatalf("move: %d", rec.Code)
	}
	if first := regexp.MustCompile(`data-id="([^"]+)"`).FindStringSubmatch(rec.Body.String()); first[1] != id {
		t.Errorf("the moved document is not first: %s", first[1])
	}
	// Rename another document through its own revision.
	rec = s.post(t, e.owner, "/ui/docs/"+id+"/rename", url.Values{"title": {"Prologue"}}, "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), ">Prologue<") {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	// Trash and restore.
	rec = s.post(t, e.owner, "/ui/docs/"+id+"/trash", nil, "", true)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "Prologue") {
		t.Fatalf("trash: %d", rec.Code)
	}
	rec = s.post(t, e.owner, "/ui/docs/"+id+"/restore", nil, "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), ">Prologue<") {
		t.Fatalf("restore: %d", rec.Code)
	}
	// A bad move is the op's error as JSON, not a page.
	rec = s.post(t, e.owner, "/ui/docs/"+id+"/move", url.Values{"parent_id": {id}}, "", true)
	if rec.Code != 422 || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Errorf("a move into itself: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// Every tree route needs a session and the CSRF token, and says 404 for what the caller cannot see.
func TestTreeRoutesAreGuarded(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	routes := []string{
		"/ui/projects/" + e.pid + "/docs", "/ui/docs/" + e.chap + "/move", "/ui/docs/" + e.chap + "/trash",
		"/ui/docs/" + e.chap + "/restore", "/ui/docs/" + e.chap + "/rename",
	}
	for _, r := range routes {
		form := url.Values{"title": {"x"}, "kind": {"chapter"}}
		if rec := s.post(t, caller{name: "anon"}, r, form, "-", true); rec.Code != http.StatusUnauthorized || strings.Contains(rec.Header().Get("Location"), "login") {
			t.Errorf("%s anonymous (fetch): %d, want a plain 401 JSON (no redirect)", r, rec.Code)
		}
		if rec := s.post(t, caller{name: "anon"}, r, form, "-", false); rec.Code != http.StatusSeeOther {
			t.Errorf("%s anonymous (form): %d, want a redirect to sign in", r, rec.Code)
		}
		if rec := s.post(t, e.owner, r, form, "-", true); rec.Code != http.StatusForbidden {
			t.Errorf("%s without a CSRF token: %d, want 403", r, rec.Code)
		}
		if rec := s.post(t, e.owner, r, form, "wrong", true); rec.Code != http.StatusForbidden {
			t.Errorf("%s with a wrong token: %d, want 403", r, rec.Code)
		}
		if rec := s.post(t, stranger, r, form, "", true); rec.Code != http.StatusNotFound {
			t.Errorf("%s as a stranger: %d, want 404", r, rec.Code)
		}
		if rec := s.post(t, reader, r, form, "", true); rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Errorf("%s as a reader: %d, want 404 or 403", r, rec.Code)
		}
	}
	// A document id from another project cannot be moved or trashed through this project.
	if rec := s.post(t, stranger, "/ui/docs/"+e.docOth+"/trash", nil, "", true); rec.Code != 404 {
		t.Errorf("trash a foreign document: %d", rec.Code)
	}
	// The tree fragment and the token endpoint are for signed-in callers.
	if rec := s.do(caller{name: "anon"}, "GET", "/ui/projects/"+e.pid+"/tree", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("tree fragment, anonymous: %d", rec.Code)
	}
	if rec := s.do(stranger, "GET", "/ui/projects/"+e.pid+"/tree", ""); rec.Code != 404 {
		t.Errorf("tree fragment, stranger: %d", rec.Code)
	}
	if rec := s.do(e.owner, "GET", "/ui/csrf", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"token"`) {
		t.Errorf("csrf endpoint: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(caller{name: "anon"}, "GET", "/ui/csrf", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("csrf endpoint, anonymous: %d", rec.Code)
	}
}

// ---- the writing view's script budget -----------------------------------------------------

// docs/ARCHITECTURE.md: the writing view's glue is loaded on document routes
// only, with the editor, and stays within 20 KB gzip; the editor bundle within 180 KB.
func TestWritingViewBudgets(t *testing.T) {
	static := staticFS(t)
	read := func(p string) []byte {
		b, err := fs.ReadFile(static, p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	glue := 0
	for _, f := range writeGlue {
		n := gzipSize(read(f))
		t.Logf("%s: %d bytes gzip", f, n)
		glue += n
	}
	if glue > 21*1024+512 {
		t.Errorf("writing-view glue is %d bytes gzip, budget is %d", glue, 21*1024+512)
	}
	// Fetched on demand, each with its own budget (lazyJS in web_test.go).
	for f, max := range lazyJS {
		if n := gzipSize(read(f)); n > max {
			t.Errorf("%s is %d bytes gzip, budget is %d", f, n, max)
		} else {
			t.Logf("%s: %d bytes gzip", f, n)
		}
	}
	if n := gzipSize(read("js/editor.js")); n > 180*1024 {
		t.Errorf("editor bundle is %d bytes gzip, budget is %d", n, 180*1024)
	} else {
		t.Logf("editor.js: %d bytes gzip", n)
	}
	// None of them is part of the shell, and no other page loads them.
	s := newWebServer(t)
	for _, f := range pageFixtures() {
		doc := renderPage(t, s, f)
		onDoc := f.name == "write" && f.page.Data.(writeData).Doc != nil
		for _, a := range []string{"/static/js/editor.", "/static/js/doc.", "/static/js/tree.", "/static/css/editor.", "/static/css/inspector."} {
			if has := strings.Contains(doc, a); has != onDoc {
				t.Errorf("%s (%s): loads %s = %v, want %v", f.name, f.page.Title, a, has, onDoc)
			}
		}
		if has := strings.Contains(doc, "/static/js/modes."); has {
			t.Errorf("%s (%s): modes.js is loaded although the person has not turned a mode on", f.name, f.page.Title)
		}
		if has := strings.Contains(doc, "/static/css/write."); has != (f.name == "write") {
			t.Errorf("%s: write.css loaded = %v", f.name, has)
		}
	}
}

// Front and back matter are created at the ends of the book and never take a chapter number;
// the numbers of the chapters in between follow reading order after every reorder.
func TestFrontAndBackMatterAreNotNumbered(t *testing.T) {
	e := newWriteEnv(t)
	post := func(kind, title string) {
		t.Helper()
		// parent_id and after name a chapter on purpose: the server places these two kinds itself.
		rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/docs", url.Values{"kind": {kind}, "title": {title}, "parent_id": {e.chap}, "after": {e.chap}}, "", true)
		if rec.Code != http.StatusCreated {
			t.Fatalf("add %s: %d %s", kind, rec.Code, rec.Body)
		}
	}
	two := e.create(t, "Second", "chapter", "", "two words")
	part := e.create(t, "Part Two", "part", "", "")
	three := e.create(t, "Third", "chapter", part, "three")
	post("back_matter", "Acknowledgements")
	post("front_matter", "Title page")
	rows := func() (out []string) {
		html := e.s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/tree", "").Body.String()
		for _, tg := range tags(html) {
			if tg.attrs["role"] == "treeitem" {
				out = append(out, tg.attrs["data-kind"]+"|"+tg.attrs["aria-label"])
			}
		}
		return out
	}
	label := func(r string) string {
		n := strings.SplitN(strings.SplitN(r, "|", 2)[1], ",", 2)[0]
		if _, err := strconv.Atoi(n); err != nil {
			return ""
		}
		return n
	}
	check := func(when string, kinds []string, numbers []string) {
		t.Helper()
		got := rows()
		if len(got) != len(kinds) {
			t.Fatalf("%s: %d rows %v", when, len(got), got)
		}
		for i, r := range got {
			if k := strings.SplitN(r, "|", 2)[0]; k != kinds[i] {
				t.Errorf("%s: row %d is %s, want %s (%v)", when, i, k, kinds[i], got)
			}
			if n := label(r); n != numbers[i] {
				t.Errorf("%s: row %d (%s) numbered %q, want %q", when, i, r, n, numbers[i])
			}
		}
	}
	check("as created", []string{"front_matter", "chapter", "chapter", "part", "chapter", "back_matter"}, []string{"", "1", "2", "", "3", ""})
	// Move the third chapter to the top level, ahead of the second: numbers follow reading order.
	e.call(t, "doc.move", docs.MoveIn{ID: three, ParentID: "", After: &e.chap})
	check("after moving a chapter", []string{"front_matter", "chapter", "chapter", "chapter", "part", "back_matter"}, []string{"", "1", "2", "3", "", ""})
	// Put the part first among the chapters, and the second chapter into it.
	e.call(t, "doc.move", docs.MoveIn{ID: part, ParentID: "", After: new(string)})
	e.call(t, "doc.move", docs.MoveIn{ID: two, ParentID: part})
	got := rows()
	if len(got) != 6 || !strings.HasPrefix(got[len(got)-1], "back_matter|") {
		t.Fatalf("back matter left the end: %v", got)
	}
	n := 0
	for i, r := range got {
		switch {
		case strings.HasPrefix(r, "chapter|"):
			n++
			if label(r) != strconv.Itoa(n) {
				t.Errorf("row %d is chapter %d but reads %q: %s", i, n, label(r), r)
			}
		case label(r) != "":
			t.Errorf("row %d is not a chapter and has a number: %s", i, r)
		}
	}
	if n != 3 {
		t.Errorf("%d chapters, want 3: %v", n, got)
	}
	// The writing view's label for a chapter reads the same number, and matter has none.
	page := e.s.do(e.owner, "GET", "/p/"+e.slug+"/d/"+two, "").Body.String()
	if !strings.Contains(page, "Chapter 1") && !strings.Contains(page, "Chapter 2") && !strings.Contains(page, "Chapter 3") {
		t.Error("a chapter's page does not say which chapter it is")
	}
}

// An inspector left open is drawn open by the server on the next page (a cookie), so a wide screen never shifts
// after the editor can be typed in; a tab it does not know is ignored.
func TestInspectorIsDrawnOpenFromTheCookie(t *testing.T) {
	e := newWriteEnv(t)
	get := func(cookie string) string {
		req := httptest.NewRequest("GET", "/p/"+e.slug, nil)
		req.AddCookie(e.owner.cookie)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "scriobh_insp", Value: cookie})
		}
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if b := get(""); strings.Contains(b, "is-open") {
		t.Error("the inspector is open without being asked")
	}
	if b := get("versions"); !strings.Contains(b, `class="inspector is-open pre"`) {
		t.Error("the inspector was not drawn open for the cookie")
	}
	if b := get("scene"); !strings.Contains(b, `class="inspector is-open pre"`) || !strings.Contains(b, `id="ipanel-scene"`) {
		t.Error("the In this scene tab is not drawn or remembered")
	}
	if b := get("<script>"); strings.Contains(b, "is-open") {
		t.Error("an unknown cookie value opened the inspector")
	}
}
