package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

func TestSnippetPartsNeverBecomeMarkup(t *testing.T) {
	got := snippetParts("a &lt;b&gt;x&lt;/b&gt; <mark>harb</mark>our &amp; <mark>more")
	want := []snipPart{{Text: "a <b>x</b> "}, {Text: "harb", Mark: true}, {Text: "our & "}, {Text: "more", Mark: true}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSearchPages(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	e.call(t, "doc.update", docs.UpdateIn{ID: e.chap, BaseRevision: 1, Title: sp("The Harbour"), Content: sp("The harbour was a bowl of <script>alert(1)</script> black mud.")})
	e.create(t, "Second", "chapter", "", "Nothing about the sea here, only the weather.")
	for i := 0; i < 25; i++ {
		e.create(t, "Tide "+itoa(i), "scene", "", "tide tide harbour")
	}

	rec := s.do(e.owner, "GET", "/p/"+e.slug+"/search?q=harbour", "")
	if rec.Code != 200 {
		t.Fatalf("search: %d", rec.Code)
	}
	body := rec.Body.String()
	lint(t, "search", body)
	structure(t, "search", body)
	if !strings.Contains(body, "<mark>") || !strings.Contains(body, "The Harbour") {
		t.Error("no highlighted result")
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("document text reached the search page as markup")
	}
	if !strings.Contains(body, "More results") || !strings.Contains(body, "offset=20") {
		t.Error("26 matches must page")
	}
	page2 := s.do(e.owner, "GET", "/p/"+e.slug+"/search?q=harbour&offset=20", "").Body.String()
	if !strings.Contains(page2, "Earlier results") {
		t.Error("the second page has no way back")
	}
	// Empty, junk and unmatched queries are pages, not errors.
	for _, q := range []string{"", "%22%27", "zzzzqq"} {
		if r := s.do(e.owner, "GET", "/p/"+e.slug+"/search?q="+q, ""); r.Code != 200 {
			t.Errorf("q=%q: %d", q, r.Code)
		}
	}
	if b := s.do(e.owner, "GET", "/p/"+e.slug+"/search?q=zzzzqq", "").Body.String(); !strings.Contains(b, "Nothing matches") {
		t.Error("no empty state")
	}
	// Access.
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	if r := s.do(stranger, "GET", "/p/"+e.slug+"/search?q=harbour", ""); r.Code != 404 {
		t.Errorf("a stranger searching: %d", r.Code)
	}
	if r := s.do(caller{name: "anon"}, "GET", "/p/"+e.slug+"/search?q=harbour", ""); r.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d", r.Code)
	}
	reader := s.newCaller(t, "reader", ops.RoleReader)
	s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	if r := s.do(reader, "GET", "/p/"+e.slug+"/search?q=harbour", ""); r.Code != 200 {
		t.Errorf("a reader may search: %d", r.Code)
	}

	// Every project: grouped by project, and only the person's own.
	all := s.do(e.owner, "GET", "/search?q=harbour", "").Body.String()
	lint(t, "search all", all)
	if !strings.Contains(all, "The Salt Road") || !strings.Contains(all, "All 26 in this project") {
		t.Errorf("the library-wide search is not grouped: %s", all[strings.Index(all, "<main"):])
	}
	if s2 := s.do(stranger, "GET", "/search?q=harbour", "").Body.String(); strings.Contains(s2, "The Salt Road") {
		t.Error("the library-wide search shows someone else's project")
	}
	if r := s.do(caller{name: "anon"}, "GET", "/search?q=x", ""); r.Code != http.StatusSeeOther {
		t.Errorf("signed out: %d", r.Code)
	}
}

func sp(s string) *string { return &s }

func TestExportDialogAndDownload(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	e.call(t, "doc.update", docs.UpdateIn{ID: e.chap, BaseRevision: 1, Title: sp("Chapter one"), Content: sp("It was a dark night, and the sea was loud.\n")})
	form := s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/export", "")
	if form.Code != 200 || !strings.Contains(form.Body.String(), "recorded with a manifest") {
		t.Fatalf("export form: %d", form.Code)
	}
	lint(t, "export page", form.Body.String())
	structure(t, "export page", form.Body.String())

	// The download: a file, an attachment, with what was recorded in headers.
	rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/export", url.Values{"mode": {"single"}}, "", true)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(rec.Body.String(), "the sea was loud") {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("X-Export-Id") == "" || rec.Header().Get("X-Export-Documents") != "1" || len(rec.Header().Get("X-Export-Sha256")) != 64 {
		t.Errorf("manifest headers: %v", rec.Header())
	}
	zip := s.post(t, e.owner, "/ui/projects/"+e.pid+"/export", url.Values{"mode": {"bundle"}, "notes": {"true"}}, "", true)
	if zip.Code != 200 || !strings.Contains(zip.Header().Get("Content-Type"), "zip") || !strings.HasPrefix(zip.Body.String(), "PK") {
		t.Errorf("bundle: %d %s", zip.Code, zip.Header().Get("Content-Type"))
	}
	// A bad mode is refused (a page without script, JSON with it).
	if r := s.post(t, e.owner, "/ui/projects/"+e.pid+"/export", url.Values{"mode": {"pdf"}}, "", false); r.Code != 422 || !strings.Contains(r.Body.String(), "single or bundle") {
		t.Errorf("bad mode: %d", r.Code)
	}
	if r := s.post(t, e.owner, "/ui/projects/"+e.pid+"/export", url.Values{"mode": {"pdf"}}, "", true); r.Code != 422 || !strings.Contains(r.Header().Get("Content-Type"), "json") {
		t.Errorf("bad mode, fetch: %d", r.Code)
	}
	// Guards: session, token, stranger, reader.
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	path := "/ui/projects/" + e.pid + "/export"
	if r := s.post(t, caller{name: "anon"}, path, nil, "-", true); r.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", r.Code)
	}
	if r := s.post(t, e.owner, path, nil, "-", true); r.Code != http.StatusForbidden {
		t.Errorf("no token: %d", r.Code)
	}
	if r := s.post(t, stranger, path, nil, "", true); r.Code != http.StatusNotFound {
		t.Errorf("stranger: %d", r.Code)
	}
	if r := s.post(t, reader, path, nil, "", true); r.Code != http.StatusForbidden && r.Code != http.StatusNotFound {
		t.Errorf("a reader exports: %d", r.Code)
	}
	if r := s.do(reader, "GET", path, ""); r.Code != http.StatusForbidden && r.Code != http.StatusNotFound {
		t.Errorf("a reader gets the export form: %d", r.Code)
	}
}
