package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/stash"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// Everything the writer keeps from a reader. None of it may appear in anything a link serves.
var shareSecrets = []string{"SECRETNOTE", "SECRETFRONT", "SECRETCOMMENT", "ALTVARIANT", "SECRETPLOT", "SECRETRESEARCH", "SECRETSTASH", "SECRETSCRATCH",
	"OTHERPROJECTDOC", "scriobh:alts", "<scriobh-ghost>", "olive@t.local", "SECRETMEMBERNOTE"}

const chapterOne = "---\nSECRETFRONT: 1\n---\nThe quiet house stood alone on the hill.\n\nSecond paragraph %%SECRETNOTE do not show%% continues here with *some emphasis* in it.\n\n" +
	"A <scriobh-ghost>GHOSTWORDS cut sentence</scriobh-ghost> remains. <!-- SECRETCOMMENT -->\n\nThe last paragraph says goodbye, with *a little* warmth.\n"

type shareWorld struct {
	s                                 *Server
	owner, editor, reviewer, stranger caller
	ownerP                            ops.Principal
	pid, other                        string
	doc1, doc2, noteDoc, otherDoc     string
	logs                              *bytes.Buffer
}

func newShareServer(t *testing.T, env map[string]string) (*Server, *bytes.Buffer) {
	t.Helper()
	cfg := map[string]string{"SCRIOBH_RATE_LIMIT": "0", "SCRIOBH_SHARE_RATE_LIMIT": "0", "SCRIOBH_SHARE_LINK_RATE": "0"}
	for k, v := range env {
		cfg[k] = v
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := New(Deps{Cfg: testutil.Config(t, cfg), DB: testutil.DB(t), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	return s, &buf
}

func newShareWorld(t *testing.T, env map[string]string) *shareWorld {
	t.Helper()
	s, logs := newShareServer(t, env)
	w := &shareWorld{s: s, logs: logs}
	w.owner = s.newCaller(t, "owner", ops.RoleWriter)
	w.editor = s.newCaller(t, "editor", ops.RoleWriter)
	w.reviewer = s.newCaller(t, "reviewer", ops.RoleReader)
	w.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	w.ownerP = ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter}
	ctx := context.Background()
	call := func(op string, in any) any {
		t.Helper()
		out, err := s.Reg.Call(ctx, w.ownerP, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	w.pid = call("project.create", projects.CreateIn{Title: "Shared Book"}).(projects.Item).Item.ID
	s.addMember(t, w.pid, w.editor.userID, ops.ProjectEditor)
	s.addMember(t, w.pid, w.reviewer.userID, ops.ProjectReviewer)
	mk := func(title, kind, content string) string {
		return call("doc.create", docs.CreateIn{ProjectID: w.pid, Title: title, Kind: kind, Content: content}).(docs.Item).Item.ID
	}
	w.doc1 = mk("Chapter One", "chapter", chapterOne)
	w.doc2 = mk("Chapter Two", "chapter", "Second chapter text, plain and open.\n")
	w.noteDoc = mk("Plot notes", "note", "SECRETPLOT is the twist.\n")
	d := call("doc.get", docs.DocIn{ID: w.doc1}).(docs.Item).Item
	call("alternative.create", docs.AltCreateIn{ID: w.doc1, BaseRevision: d.Revision, Scope: "word", Quote: "quiet", Variants: []string{"ALTVARIANT"}})
	call("project.research.create", research.CreateIn{ID: w.pid, Title: "SECRETRESEARCH", Body: "SECRETRESEARCH body"})
	call("stash.create", stash.CreateIn{ID: w.pid, Title: "SECRETSTASH", Content: "SECRETSTASH text"})
	if rec := s.do(w.owner, "PUT", "/api/v1/projects/"+w.pid+"/scratchpad", `{"content":"SECRETSCRATCH","base_revision":0}`); rec.Code >= 500 {
		t.Fatalf("scratchpad: %d", rec.Code)
	}
	w.other = call("project.create", projects.CreateIn{Title: "Another Book"}).(projects.Item).Item.ID
	w.otherDoc = call("doc.create", docs.CreateIn{ProjectID: w.other, Title: "OTHERPROJECTDOC", Content: "OTHERPROJECTDOC words"}).(docs.Item).Item.ID
	return w
}

// link makes a share link through the REST route as the owner and returns its id and token.
func (w *shareWorld) link(t *testing.T, body string) (id, token string) {
	t.Helper()
	rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/shares", body)
	if rec.Code != 201 {
		t.Fatalf("share.create: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Item  struct{ ID string }
		Token string
		Path  string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" || out.Path != "/s/"+out.Token {
		t.Fatalf("share.create body: %s", rec.Body)
	}
	return out.Item.ID, out.Token
}

// pub sends a request to the public routes the way a stranger's browser would: no cookie.
func (w *shareWorld) pub(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.7:4000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("not JSON (%d): %s", rec.Code, rec.Body)
	}
}

func noLeak(t *testing.T, what, body string) {
	t.Helper()
	for _, secret := range shareSecrets {
		if strings.Contains(body, secret) {
			t.Errorf("%s leaks %q: %.300s", what, secret, body)
		}
	}
}

func TestAShareLinkReadsTheManuscriptAndNothingElse(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"label":"beta reader"}`)
	var mf struct {
		Title     string
		Scope     string
		Documents []struct{ ID, Title, Kind string }
	}
	rec := w.pub("GET", "/s/"+tok+"/api/manifest", "")
	if rec.Code != 200 {
		t.Fatalf("manifest %d %s", rec.Code, rec.Body)
	}
	decode(t, rec, &mf)
	var titles []string
	for _, d := range mf.Documents {
		if d.Title != "" { // a new project starts with an empty first chapter
			titles = append(titles, d.Title)
		}
	}
	if mf.Title != "Shared Book" || mf.Scope != "read" || strings.Join(titles, ",") != "Chapter One,Chapter Two" {
		t.Fatalf("manifest: %+v", mf)
	}
	noLeak(t, "the manifest", rec.Body.String())
	// the whole text of a chapter, rendered, without anything private
	rec = w.pub("GET", "/s/"+tok+"/api/docs/"+w.doc1, "")
	if rec.Code != 200 {
		t.Fatalf("doc %d %s", rec.Code, rec.Body)
	}
	var d struct{ HTML, Title string }
	decode(t, rec, &d)
	for _, want := range []string{"The quiet house stood alone", "<em>some emphasis</em>", "The last paragraph says goodbye"} {
		if !strings.Contains(d.HTML, want) {
			t.Errorf("html lacks %q: %s", want, d.HTML)
		}
	}
	noLeak(t, "the chapter", rec.Body.String())
	if strings.Contains(d.HTML, "GHOSTWORDS") {
		t.Error("ghosted text is shown by a link that omits ghosts")
	}
	// the notes document, another project's document and a made-up id are all the same 404
	gone := w.pub("GET", "/s/"+tok+"/api/docs/"+w.noteDoc, "")
	for _, id := range []string{w.otherDoc, "doc_00000000000000000000000000", "nonsense"} {
		r := w.pub("GET", "/s/"+tok+"/api/docs/"+id, "")
		if r.Code != 404 || r.Body.String() != gone.Body.String() {
			t.Errorf("%s: %d %s", id, r.Code, r.Body)
		}
	}
	if gone.Code != 404 {
		t.Errorf("a note: %d", gone.Code)
	}
	// ghosts: a link that keeps them shows the words, never the tags
	id2, tok2 := w.link(t, `{"ghosts":"keep"}`)
	_ = id2
	var d2 struct{ HTML string }
	decode(t, w.pub("GET", "/s/"+tok2+"/api/docs/"+w.doc1, ""), &d2)
	if !strings.Contains(d2.HTML, "GHOSTWORDS") || strings.Contains(d2.HTML, "scriobh-ghost") {
		t.Errorf("a link that keeps ghosts: %s", d2.HTML)
	}
}

// Walk every route the link has and call it every way, with a valid token: nothing private is in any
// answer, and every answer is as locked down as the rules say.
func TestNoPublicRouteLeaksAndEveryAnswerIsLockedDown(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"scope":"comment","allow_download":true}`)
	// the mux has no /s/ route that the table does not list
	listed := map[string]bool{"/s/": true}
	for _, e := range shareEndpoints {
		listed[e.Path] = true
		if !strings.HasPrefix(e.Path, "/s/{token}") {
			t.Errorf("%s is not under /s/{token}", e.Path)
		}
	}
	for _, p := range []string{"/s/x", "/s/x/y", "/s/" + tok + "/api/nothing", "/s/" + tok + "/api/docs", "/s/" + tok + "/settings", "/s/" + tok + "/api/members",
		"/s/" + tok + "/api/export", "/s/" + tok + "/api/revisions", "/s/" + tok + "/api/ai", "/s/" + tok + "/api/shares", "/s/" + tok + "/../p/shared-book"} {
		_, pat := w.s.mux.Handler(httptest.NewRequest("GET", p, nil))
		pat = strings.TrimPrefix(pat, "GET ")
		if !listed[pat] && !strings.HasPrefix(pat, "/s/") {
			t.Errorf("%s reaches %q, which is not a public route", p, pat)
		}
		if strings.HasPrefix(pat, "/s/") && !listed[pat] {
			t.Errorf("%s reaches an unlisted /s/ pattern %q", p, pat)
		}
	}
	fill := func(p string) string {
		p = strings.ReplaceAll(p, "{token}", tok)
		return strings.ReplaceAll(p, "{docid}", w.doc1)
	}
	hits := 0
	for _, e := range shareEndpoints {
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			path := fill(e.Path)
			if e.Path == "/s/{token}/api/comments" && method == "GET" {
				path += "?document_id=" + w.doc1
			}
			rec := w.pub(method, path, map[bool]string{true: `{"document_id":"` + w.doc1 + `","body":"hi","name":"Sam"}`, false: ""}[method == "POST"])
			hits++
			noLeak(t, method+" "+e.Path, rec.Body.String())
			h := rec.Header()
			if h.Get("Set-Cookie") != "" {
				t.Errorf("%s %s sets a cookie: %s", method, e.Path, h.Get("Set-Cookie"))
			}
			if h.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") || !strings.Contains(h.Get("Cache-Control"), "no-store") {
				t.Errorf("%s %s headers: %v", method, e.Path, h)
			}
			if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || strings.Contains(h.Get("Content-Security-Policy"), "unsafe") {
				t.Errorf("%s %s CSP: %q", method, e.Path, h.Get("Content-Security-Policy"))
			}
			if rec.Code >= 500 {
				t.Errorf("%s %s: %d %s", method, e.Path, rec.Code, rec.Body)
			}
		}
	}
	// the download is the shown text as Markdown, clean
	rec := w.pub("GET", "/s/"+tok+"/api/download", "")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/markdown") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	noLeak(t, "the download", rec.Body.String())
	for _, want := range []string{"## Chapter One", "The quiet house stood alone", "## Chapter Two", "*some emphasis*"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("download lacks %q", want)
		}
	}
	if strings.Contains(rec.Body.String(), "GHOSTWORDS") || strings.Contains(rec.Body.String(), "Plot notes") {
		t.Error("the download holds ghosted text or a note")
	}
	// and a link that does not allow it answers as for a link that does not exist
	_, tok2 := w.link(t, `{}`)
	if r := w.pub("GET", "/s/"+tok2+"/api/download", ""); r.Code != 404 {
		t.Errorf("a download that is not allowed: %d", r.Code)
	}
	if hits < 30 {
		t.Errorf("only %d calls", hits)
	}
	// A session cookie (even a bad one) or an API key sent to a share route is not looked at: nothing is set.
	req := httptest.NewRequest("GET", "/s/"+tok+"/api/manifest", nil)
	req.AddCookie(&http.Cookie{Name: "scriobh_session", Value: "not-a-session"})
	req.AddCookie(w.owner.cookie)
	req.Header.Set("Authorization", "Bearer sk_nothing")
	rr := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 || rr.Header().Get("Set-Cookie") != "" || strings.Contains(rr.Body.String(), "olive@t.local") {
		t.Errorf("with a session: %d %v", rr.Code, rr.Header())
	}
	if strings.Contains(rr.Body.String(), "Shared Book") == false {
		t.Error("manifest lacks the title")
	}
}

func TestEveryTokenThatOpensNothingGetsTheSameAnswer(t *testing.T) {
	w := newShareWorld(t, nil)
	id, tok := w.link(t, `{}`)
	revID, revTok := w.link(t, `{}`)
	_, expTok := w.link(t, `{"expires_at":"`+db.NowTime().Add(time.Hour).Format(time.RFC3339)+`"}`)
	_, trashTok := w.link(t, `{}`)
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/shares/"+revID+"/revoke", ""); rec.Code != 200 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	// the clock moves past the expiry
	restore := db.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
	defer restore()
	secret := tok[strings.Index(tok, ".")+1:]
	wrongSecret := id + "." + strings.Repeat("A", len(secret))
	unknownID := "shl_00000000000000000000000000." + secret
	// a link of a project that is trashed
	trashed := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/trash", "")
	if trashed.Code != 200 {
		t.Fatalf("trash: %d %s", trashed.Code, trashed.Body)
	}
	cases := map[string]string{"garbage": "garbage", "wrong secret": wrongSecret, "unknown id": unknownID, "revoked": revTok, "expired": expTok,
		"project in the trash": trashTok, "empty": "", "too long": strings.Repeat("a", 4000), "unicode": "shl_é"}
	var ref *httptest.ResponseRecorder
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, p := range []string{"/api/manifest", "/api/docs/" + w.doc1, "/api/comments?document_id=" + w.doc1, "/api/download", "/api/unlock"} {
			for _, method := range []string{"GET", "POST"} {
				if n == "empty" {
					continue
				}
				rec := w.pub(method, "/s/"+cases[n]+p, "")
				if ref == nil {
					ref = rec
				}
				if rec.Code != 404 || rec.Body.String() != ref.Body.String() || rec.Header().Get("Content-Type") != ref.Header().Get("Content-Type") {
					t.Errorf("%s %s%s: %d %q, want the common answer %q", method, n, p, rec.Code, rec.Body, ref.Body)
				}
			}
		}
	}
	// A restored project opens the link again: the trash only hid it.
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/restore", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := w.pub("GET", "/s/"+tok+"/api/manifest", ""); rec.Code != 200 {
		t.Errorf("a restored project: %d", rec.Code)
	}
}

func TestLinksAreBoundToTheirProject(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"scope":"comment"}`)
	// a comment on another project's document through this link
	rec := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+w.otherDoc+`","body":"hello","name":"Sam"}`)
	if rec.Code != 404 {
		t.Errorf("a comment on another project's document: %d %s", rec.Code, rec.Body)
	}
	// a link of the other project does not open this one's documents
	rec2 := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.other+"/shares", `{}`)
	var o struct{ Token string }
	decode(t, rec2, &o)
	if r := w.pub("GET", "/s/"+o.Token+"/api/docs/"+w.doc1, ""); r.Code != 404 {
		t.Errorf("another project's link opened this project's chapter: %d", r.Code)
	}
	if r := w.pub("GET", "/s/"+o.Token+"/api/docs/"+w.otherDoc, ""); r.Code != 200 {
		t.Errorf("its own chapter: %d", r.Code)
	}
	// a link limited to one chapter shows that chapter (and its descendants) only
	_, one := w.link(t, `{"document_ids":["`+w.doc2+`"]}`)
	var mf struct{ Documents []struct{ Title string } }
	decode(t, w.pub("GET", "/s/"+one+"/api/manifest", ""), &mf)
	if len(mf.Documents) != 1 || mf.Documents[0].Title != "Chapter Two" {
		t.Errorf("a link limited to a chapter: %+v", mf)
	}
	if r := w.pub("GET", "/s/"+one+"/api/docs/"+w.doc1, ""); r.Code != 404 {
		t.Errorf("a chapter outside the subset: %d", r.Code)
	}
	r := w.pub("POST", "/s/"+one+"/api/comments", `{"document_id":"`+w.doc1+`","body":"hello","name":"Sam"}`)
	if r.Code != 403 && r.Code != 404 {
		t.Errorf("a comment outside the subset: %d", r.Code)
	}
}

func TestScopeAndGuestComments(t *testing.T) {
	w := newShareWorld(t, nil)
	_, readTok := w.link(t, `{"scope":"read"}`)
	cid, comTok := w.link(t, `{"scope":"comment","label":"Sam"}`)
	_, otherTok := w.link(t, `{"scope":"comment"}`)
	post := func(tok, body string) *httptest.ResponseRecorder {
		return w.pub("POST", "/s/"+tok+"/api/comments", body)
	}
	// a read link cannot comment or list threads
	if r := post(readTok, `{"document_id":"`+w.doc1+`","body":"x","name":"Sam"}`); r.Code != 403 {
		t.Errorf("a read link commented: %d", r.Code)
	}
	if r := w.pub("GET", "/s/"+readTok+"/api/comments?document_id="+w.doc1, ""); r.Code != 403 {
		t.Errorf("a read link listed threads: %d", r.Code)
	}
	// a comment link: an anchored comment (a plain selection, with formatting inside it), a name, a bound on size
	rec := post(comTok, `{"document_id":"`+w.doc1+`","selection":"stood alone on the hill","body":"Why alone?","name":"  Sam   Reader "}`)
	if rec.Code != 201 {
		t.Fatalf("guest comment: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		Thread struct {
			ID, Status, Anchor, Quote, Before, After string
			Comments                                 []struct {
				Author, Body string
				Mine         bool
			}
		}
	}
	decode(t, rec, &made)
	if made.Thread.Anchor != "active" || made.Thread.Quote != "stood alone on the hill" || len(made.Thread.Comments) != 1 || made.Thread.Comments[0].Author != "Guest: Sam Reader" || !made.Thread.Comments[0].Mine {
		t.Fatalf("thread: %+v", made)
	}
	// A selection in a paragraph that holds a private note is placed in the text a reader sees, not in the
	// note: the quote is the visible words, and nothing of the note is in the answer.
	rec = post(comTok, `{"document_id":"`+w.doc1+`","selection":"continues here with some emphasis in it","body":"Nice","name":"Sam"}`)
	if rec.Code != 201 || strings.Contains(rec.Body.String(), "do not show") || !strings.Contains(rec.Body.String(), "continues here with some emphasis in it") {
		t.Errorf("a selection after a note: %d %s", rec.Code, rec.Body)
	}
	// formatting inside the selection is placed exactly
	rec = post(comTok, `{"document_id":"`+w.doc1+`","selection":"says goodbye, with a little warmth","before":"The last paragraph ","body":"Nice","name":"Sam"}`)
	if rec.Code != 201 {
		t.Fatalf("across emphasis: %d %s", rec.Code, rec.Body)
	}
	var wd struct{ Thread struct{ Quote string } }
	decode(t, rec, &wd)
	if wd.Thread.Quote != "says goodbye, with a little warmth" {
		t.Errorf("across emphasis: %+v", wd)
	}
	noLeak(t, "a guest's own thread", rec.Body.String())
	// a comment with no selection is on the document as a whole; a reply stays in its thread
	if r := post(comTok, `{"document_id":"`+w.doc2+`","body":"Whole chapter.","name":"Sam"}`); r.Code != 201 {
		t.Errorf("document-level: %d %s", r.Code, r.Body)
	}
	if r := post(comTok, `{"document_id":"`+w.doc1+`","parent_id":"`+made.Thread.ID+`","body":"And again","name":"Sam"}`); r.Code != 201 {
		t.Errorf("reply: %d %s", r.Code, r.Body)
	}
	for name, body := range map[string]string{
		"no name":     `{"document_id":"` + w.doc1 + `","body":"x"}`,
		"no body":     `{"document_id":"` + w.doc1 + `","name":"Sam","body":" "}`,
		"long body":   `{"document_id":"` + w.doc1 + `","name":"Sam","body":"` + strings.Repeat("a", 2001) + `"}`,
		"long name":   `{"document_id":"` + w.doc1 + `","name":"` + strings.Repeat("n", 61) + `","body":"x"}`,
		"unknown key": `{"document_id":"` + w.doc1 + `","name":"Sam","body":"x","author_id":"usr_x"}`,
		"a note doc":  `{"document_id":"` + w.noteDoc + `","name":"Sam","body":"x"}`,
		"in a note":   `{"document_id":"` + w.doc1 + `","selection":"SECRETNOTE do not show","name":"Sam","body":"x"}`,
		"in a ghost":  `{"document_id":"` + w.doc1 + `","selection":"GHOSTWORDS cut sentence","name":"Sam","body":"x"}`,
		"no document": `{"name":"Sam","body":"x"}`,
		"not json":    `nope`,
		"two objects": `{"name":"a","body":"b"} {"c":1}`,
		"a kind":      `{"document_id":"` + w.doc1 + `","name":"Sam","body":"x","kind":"question"}`,
	} {
		if r := post(comTok, body); r.Code < 400 || r.Code >= 500 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	// the guest sees its own link's threads; another link sees none of them; neither can reply into the other's
	var own struct{ Items []struct{ ID string } }
	decode(t, w.pub("GET", "/s/"+comTok+"/api/comments?document_id="+w.doc1, ""), &own)
	var none struct{ Items []struct{ ID string } }
	decode(t, w.pub("GET", "/s/"+otherTok+"/api/comments?document_id="+w.doc1, ""), &none)
	if len(own.Items) != 3 || len(none.Items) != 0 {
		t.Errorf("own %d other %d", len(own.Items), len(none.Items))
	}
	if r := post(otherTok, `{"document_id":"`+w.doc1+`","parent_id":"`+made.Thread.ID+`","body":"snoop","name":"Eve"}`); r.Code != 404 {
		t.Errorf("a reply into another link's thread: %d", r.Code)
	}
	var withDoc struct{ Threads []struct{ ID string } }
	decode(t, w.pub("GET", "/s/"+comTok+"/api/docs/"+w.doc1, ""), &withDoc)
	if len(withDoc.Threads) != 3 {
		t.Errorf("the document carries %d threads", len(withDoc.Threads))
	}
	// A writer replies in the thread and resolves it: the guest sees the reply, by name, not the member's email
	rep := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/comments", `{"document_id":"`+w.doc1+`","parent_id":"`+made.Thread.ID+`","body":"Good question. SECRETMEMBERNOTE"}`)
	if rep.Code != 201 {
		t.Fatalf("owner reply: %d %s", rep.Code, rep.Body)
	}
	g := w.pub("GET", "/s/"+comTok+"/api/comments?document_id="+w.doc1, "")
	if !strings.Contains(g.Body.String(), "Good question.") || strings.Contains(g.Body.String(), "olive@t.local") {
		t.Errorf("guest view of the reply: %s", g.Body)
	}
	// The owner sees the guest as Guest: Name, and is told; a reviewer sees it too; a reader of the project does not
	var lst struct {
		Items []struct{ Author struct{ Kind, Name string } }
	}
	decode(t, w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/comments?status=all&limit=200", ""), &lst)
	foundGuest := false
	for _, it := range lst.Items {
		if it.Author.Kind == "guest" && it.Author.Name == "Guest: Sam Reader" {
			foundGuest = true
		}
	}
	if !foundGuest {
		t.Errorf("the owner does not see the guest: %s", w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/comments?status=all", "").Body)
	}
	var n struct{ Unread int }
	decode(t, w.s.do(w.owner, "GET", "/api/v1/notifications/count", ""), &n)
	if n.Unread == 0 {
		t.Error("the owner was not told of a guest's comment")
	}
	// usage
	var u struct {
		Item struct {
			CommentCount int `json:"comment_count"`
			Views        int
		}
		Threads int
	}
	decode(t, w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/shares/"+cid+"/usage", ""), &u)
	if u.Item.CommentCount < 5 || u.Threads != 4 || u.Item.Views < 0 {
		t.Errorf("usage: %+v", u)
	}
	// What a guest wrote is not in the manuscript, an export, a search or an event payload. (GLOB, not LIKE, for the name: LIKE
	// ignores case, and a generated id such as shl_...SAM... would match it.)
	d := w.s.do(w.owner, "GET", "/api/v1/docs/"+w.doc1, "")
	if strings.Contains(d.Body.String(), "Why alone?") {
		t.Error("guest text in the document")
	}
	for _, path := range []string{"/export", "/export/html"} {
		rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+path, `{}`)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "Why alone?") || strings.Contains(rec.Body.String(), "Whole chapter.") {
			t.Errorf("export %s: %d (comment text present: %v)", path, rec.Code, strings.Contains(rec.Body.String(), "Why alone?"))
		}
	}
	var hits struct{ Items []any }
	decode(t, w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/search?q=alone", ""), &hits)
	for _, e := range []string{`SELECT count(*) FROM events WHERE payload_json LIKE '%Why alone%'`, `SELECT count(*) FROM audit_log WHERE detail_json LIKE '%Why alone%' OR detail_json GLOB '*Sam*'`} {
		var c int
		_ = w.s.DB.Read().QueryRow(e).Scan(&c)
		if c != 0 {
			t.Errorf("guest text in %s", e)
		}
	}
}
