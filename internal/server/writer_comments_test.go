package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// "Only me" through the real handlers. The people: Ada owns the book, Ben edits it, Cy reviews it, Di only reads it, Eve is no
// member, an administrator is no member either, and a guest holds a comment link. Both directions are tried: Ben's note against Ada
// (the owner must not see it) and Ada's against Ben.
const wcCanary = "PRIVATE-COMMENT-CANARY"

type wcWorld struct {
	*shareWorld
	ada, ben, cy, di, eve, admin caller
}

func newWCWorld(t *testing.T) *wcWorld {
	t.Helper()
	w := &wcWorld{shareWorld: newShareWorld(t, nil)}
	w.ada, w.ben, w.cy, w.eve = w.owner, w.editor, w.reviewer, w.stranger
	w.di = w.s.newCaller(t, "reader", ops.RoleReader)
	w.s.addMember(t, w.pid, w.di.userID, ops.ProjectReader)
	w.admin = w.s.newCaller(t, "admin", ops.RoleAdmin)
	return w
}

func (w *wcWorld) base() string { return "/api/v1/projects/" + w.pid }

// write makes a comment through REST and returns its id.
func (w *wcWorld) write(t *testing.T, c caller, body string) string {
	t.Helper()
	rec := w.s.do(c, "POST", w.base()+"/comments", body)
	if rec.Code != 201 {
		t.Fatalf("%s writes a comment: %d %s", c.name, rec.Code, rec.Body)
	}
	var out struct {
		Item struct {
			ID         string
			Visibility string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Item.ID
}

func (w *wcWorld) private(t *testing.T, c caller, doc, quote, body string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"document_id": doc, "quote": quote, "body": body, "visibility": "private"})
	return w.write(t, c, string(b))
}

func TestPrivateCommentIsAnotherPersonsMissingId(t *testing.T) {
	w := newWCWorld(t)
	mine := w.private(t, w.ben, w.doc1, "quiet", wcCanary+" @Cy a note to myself")
	adas := w.private(t, w.ada, w.doc1, "last paragraph", wcCanary+" the owner's own")
	shared := w.write(t, w.cy, fmt.Sprintf(`{"document_id":%q,"quote":"warmth","body":"a shared remark"}`, w.doc1))
	// the list: nobody else's body, count or filter finds it
	for _, c := range []caller{w.ada, w.admin, w.cy} {
		for _, q := range []string{"", "?q=" + wcCanary, "?visibility=private", "?author=" + w.ben.userID, "?status=open&document_id=" + w.doc1} {
			rec := w.s.do(c, "GET", w.base()+"/comments"+q, "")
			if rec.Code != 200 {
				t.Fatalf("%s lists %s: %d %s", c.name, q, rec.Code, rec.Body)
			}
			var out review.ListOut
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			for _, it := range out.Items {
				if it.ID == mine || strings.Contains(it.Body, wcCanary) && it.ID != adas {
					t.Errorf("%s sees Ben's private comment through %q", c.name, q)
				}
			}
			if c.name == "admin" || c.name == "reviewer" {
				if strings.Contains(rec.Body.String(), wcCanary) || strings.Contains(rec.Body.String(), adas) {
					t.Errorf("%s sees a private comment through %q: %s", c.name, q, rec.Body)
				}
			}
		}
	}
	// Ada sees hers and not Ben's; Ben the other way round
	ada := w.s.do(w.ada, "GET", w.base()+"/comments", "").Body.String()
	if !strings.Contains(ada, adas) || strings.Contains(ada, mine) {
		t.Errorf("the owner's list: %s", ada)
	}
	ben := w.s.do(w.ben, "GET", w.base()+"/comments", "").Body.String()
	if !strings.Contains(ben, mine) || strings.Contains(ben, adas) || !strings.Contains(ben, shared) {
		t.Errorf("the editor's list: %s", ben)
	}
	// by id: another person's private thread is answered exactly as an id that does not exist, for every method
	for _, c := range []caller{w.ada, w.admin, w.cy, w.eve} {
		for _, call := range []struct{ method, path, body string }{
			{"GET", "", ""}, {"PATCH", "", `{"body":"x"}`}, {"DELETE", "", ""}, {"POST", "/resolve", ""}, {"POST", "/reopen", ""}, {"POST", "/reanchor", `{"detach":true}`},
		} {
			got := w.s.do(c, call.method, w.base()+"/comments/"+mine+call.path, call.body)
			want := w.s.do(c, call.method, w.base()+"/comments/cmt_00000000000000000000000000"+call.path, call.body)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("%s %s %s: %d %q differs from the missing id's %d %q", c.name, call.method, call.path, got.Code, got.Body, want.Code, want.Body)
			}
			if c.name != "stranger" && got.Code != 404 {
				t.Errorf("%s %s %s: %d, want 404", c.name, call.method, call.path, got.Code)
			}
		}
		rep := w.s.do(c, "POST", w.base()+"/comments", fmt.Sprintf(`{"parent_id":%q,"body":"x"}`, mine))
		miss := w.s.do(c, "POST", w.base()+"/comments", `{"parent_id":"cmt_00000000000000000000000000","body":"x"}`)
		if rep.Code != miss.Code || rep.Body.String() != miss.Body.String() {
			t.Errorf("%s replies under it: %d %q differs from %d %q", c.name, rep.Code, rep.Body, miss.Code, miss.Body)
		}
	}
	// untouched, and Ben still has it all
	if got := w.s.do(w.ben, "GET", w.base()+"/comments/"+mine, "").Body.String(); !strings.Contains(got, wcCanary) || strings.Contains(got, `"deleted":true`) || strings.Contains(got, `"status":"resolved"`) {
		t.Errorf("the thread was changed: %s", got)
	}
	// the owner's notifications and everybody's say nothing of it
	for _, c := range []caller{w.ada, w.cy, w.admin, w.di} {
		if body := w.s.do(c, "GET", "/api/v1/notifications", "").Body.String(); strings.Contains(body, wcCanary) || strings.Contains(body, mine) {
			t.Errorf("%s's notifications name it: %s", c.name, body)
		}
	}
	if x := w.s.do(w.cy, "GET", "/api/v1/notifications/count", "").Body.String(); strings.Contains(x, `"unread":2`) {
		t.Logf("count: %s", x)
	}
}

// The review page, its per-chapter counts and "Resolve all" are the shared review: the same set as before, whoever has notes of their own.
func TestReviewPageAndResolveAllCountOnlyWhatIsShared(t *testing.T) {
	w := newWCWorld(t)
	t1 := w.write(t, w.cy, fmt.Sprintf(`{"document_id":%q,"quote":"quiet house","body":"shared one"}`, w.doc1))
	w.private(t, w.ben, w.doc1, "last paragraph", wcCanary+" Ben's note")
	adas := w.private(t, w.ada, w.doc1, "warmth", wcCanary+" Ada's note")
	page := w.s.do(w.ada, "GET", "/p/shared-book/review", "")
	if page.Code != 200 || strings.Contains(page.Body.String(), wcCanary) || strings.Contains(page.Body.String(), adas) {
		t.Fatalf("the owner's review page: %d, holds a private comment: %v", page.Code, strings.Contains(page.Body.String(), wcCanary))
	}
	base := "/ui/projects/" + w.pid + "/review/"
	for _, c := range []caller{w.ada, w.ben} {
		rec := w.s.postForm(t, c, base+"resolve_all", url.Values{"step": {"preview"}})
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "resolves 1 comment") || strings.Contains(rec.Body.String(), wcCanary) {
			t.Errorf("%s's preview: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	// a note written between preview and confirm does not stop it
	w.private(t, w.ben, w.doc1, "goodbye", "made in between")
	if rec := w.s.postForm(t, w.ada, base+"resolve_all", url.Values{"step": {"apply"}, "expect_count": {"1"}}); rec.Code != 303 {
		t.Errorf("apply: %d %s", rec.Code, rec.Body)
	}
	got := w.s.do(w.ada, "GET", w.base()+"/comments/"+t1, "").Body.String()
	if !strings.Contains(got, `"status":"resolved"`) {
		t.Errorf("the shared thread was not resolved: %s", got)
	}
	for _, c := range []caller{w.ada, w.ben} {
		for _, st := range []string{"open"} {
			var out review.ListOut
			_ = json.Unmarshal(w.s.do(c, "GET", w.base()+"/comments?visibility=private&status="+st, "").Body.Bytes(), &out)
			if out.Total == 0 {
				t.Errorf("%s lost their own open private notes to Resolve all", c.name)
			}
		}
	}
	// the queue over REST: the default is the shared review, "all" adds the caller's own and no one else's
	var q review.QueueOut
	_ = json.Unmarshal(w.s.do(w.ada, "GET", w.base()+"/review/queue", "").Body.Bytes(), &q)
	if q.Total != 0 {
		t.Errorf("the default queue holds %d after everything shared was resolved", q.Total)
	}
	body := w.s.do(w.ada, "GET", w.base()+"/review/queue?visibility=all", "").Body.String()
	if !strings.Contains(body, adas) || strings.Contains(body, "Ben's note") {
		t.Errorf("queue, all, for the owner: %s", body)
	}
}

// A guest of a comment link sees what guests saw, whatever notes the writers keep.
func TestPrivateNeverOnAShareLink(t *testing.T) {
	w := newWCWorld(t)
	_, tok := w.link(t, `{"scope":"comment"}`)
	guest := func(path string) string {
		rec := w.pub("GET", "/s/"+tok+path, "")
		if rec.Code != 200 {
			t.Fatalf("guest %s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	if rec := w.pub("POST", "/s/"+tok+"/api/comments", fmt.Sprintf(`{"document_id":%q,"selection":"quiet","body":"a guest remark","name":"Gus"}`, w.doc1)); rec.Code != 201 {
		t.Fatalf("a guest comments: %d %s", rec.Code, rec.Body)
	}
	threads := "/api/comments?document_id=" + w.doc1
	before, manifest := guest(threads), guest("/api/manifest")
	w.private(t, w.ada, w.doc1, "quiet", wcCanary+" Ada")
	w.private(t, w.ben, w.doc1, "quiet house", wcCanary+" Ben")
	after := guest(threads)
	if before != after || manifest != guest("/api/manifest") {
		t.Errorf("a private comment changed what a guest is shown or what the link's counts say:\n%s\n%s", before, after)
	}
	if strings.Contains(after, wcCanary) || strings.Contains(guest("/api/docs/"+w.doc1), wcCanary) {
		t.Error("the canary is on the link")
	}
	// a guest cannot reply under a private thread
	id := w.private(t, w.ada, w.doc1, "last paragraph", "x")
	rep := w.pub("POST", "/s/"+tok+"/api/comments", fmt.Sprintf(`{"document_id":%q,"parent_id":%q,"body":"hello","name":"Gus"}`, w.doc1, id))
	if rep.Code != 404 {
		t.Errorf("a guest replies under a private thread: %d %s", rep.Code, rep.Body)
	}
}

func TestEveryKeyActsAsItsPersonAndSeesTheirOwnNotesOnly(t *testing.T) {
	w := newWCWorld(t)
	benKey, _ := w.s.mintKey(t, w.ben, `{"name":"ben agent","scope":"write"}`)
	adaKey, _ := w.s.mintKey(t, w.ada, `{"name":"ada agent","scope":"write"}`)
	mine := w.private(t, w.ben, w.doc1, "quiet", wcCanary+" Ben")
	adas := w.private(t, w.ada, w.doc1, "warmth", wcCanary+" Ada")
	list := func(key string) string {
		return w.s.doKey(key, "GET", w.base()+"/comments?q="+wcCanary, "").Body.String()
	}
	if got := list(benKey); !strings.Contains(got, mine) || strings.Contains(got, adas) {
		t.Errorf("Ben's key: %s", got)
	}
	if got := list(adaKey); !strings.Contains(got, adas) || strings.Contains(got, mine) {
		t.Errorf("Ada's key: %s", got)
	}
	// a key writes a private comment too, as its person
	b, _ := json.Marshal(map[string]any{"document_id": w.doc1, "quote": "last paragraph", "body": "by the agent", "visibility": "private"})
	if rec := w.s.doKey(benKey, "POST", w.base()+"/comments", string(b)); rec.Code != 201 {
		t.Errorf("a key writes a private comment: %d %s", rec.Code, rec.Body)
	}
	if got := w.s.do(w.ada, "GET", w.base()+"/comments?q=by+the+agent", "").Body.String(); strings.Contains(got, "by the agent") {
		t.Errorf("Ada sees the agent's private comment: %s", got)
	}
}

// MCP: the tools are the registry's, so they carry the new input, the new field and the same refusals.
func TestPrivateCommentsThroughMCP(t *testing.T) {
	s := newMCPServer(t, nil)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	pid, did := mcpProjectWithDoc(t, s, owner, "The quiet house stood alone on the hill.")
	s.addMember(t, pid, editor.userID, ops.ProjectEditor)
	ek, _ := s.mintKey(t, editor, `{"name":"e","scope":"write"}`)
	ok, _ := s.mintKey(t, owner, `{"name":"o","scope":"write"}`)
	r := s.modernCall(t, ek, "tools/call", map[string]any{"name": "comment_create", "arguments": map[string]any{"id": pid, "document_id": did, "quote": "stood alone", "body": wcCanary, "visibility": "private"}})
	if r.msg.Result["isError"] == true || !strings.Contains(string(r.raw), `\"visibility\":\"private\"`) && !strings.Contains(string(r.raw), `"visibility":"private"`) {
		t.Fatalf("comment_create private: %s", r.raw)
	}
	mine := func(key string) string {
		return string(s.modernCall(t, key, "tools/call", map[string]any{"name": "comment_list", "arguments": map[string]any{"id": pid}}).raw)
	}
	if got := mine(ek); !strings.Contains(got, wcCanary) {
		t.Errorf("the author's tool does not list it: %s", got)
	}
	if got := mine(ok); strings.Contains(got, wcCanary) {
		t.Errorf("the owner's tool lists it: %s", got)
	}
}

func mcpProjectWithDoc(t *testing.T, s *Server, owner caller, content string) (pid, did string) {
	t.Helper()
	call := func(op string, in any) any {
		out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	pid = call("project.create", projects.CreateIn{Title: "Private Book"}).(projects.Item).Item.ID
	did = call("doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Kind: "chapter", Content: content}).(docs.Item).Item.ID
	return
}

// Webhooks with include_text on: a private comment sends nothing, at any step of its life.
func TestPrivateCommentSendsNoWebhook(t *testing.T) {
	w := newHookWorld(t, true)
	base := "/api/v1/projects/" + w.project
	post := func(body string) string {
		var out struct{ Item struct{ ID string } }
		_ = json.Unmarshal(w.rest(t, "POST", base+"/comments", body), &out)
		return out.Item.ID
	}
	id := post(fmt.Sprintf(`{"document_id":%q,"body":%q,"visibility":"private"}`, w.doc, wcCanary+" body"))
	post(fmt.Sprintf(`{"parent_id":%q,"body":%q}`, id, wcCanary+" reply"))
	w.rest(t, "PATCH", base+"/comments/"+id, fmt.Sprintf(`{"body":%q}`, wcCanary+" reworded"))
	w.rest(t, "POST", base+"/comments/"+id+"/resolve", "")
	w.rest(t, "POST", base+"/comments/"+id+"/reopen", "")
	w.rest(t, "DELETE", base+"/comments/"+id, "")
	post(fmt.Sprintf(`{"document_id":%q,"body":"A SHARED sentinel."}`, w.doc))
	w.recv.waitFor(t, 1)
	time.Sleep(150 * time.Millisecond)
	reqs := w.recv.requests()
	if len(reqs) != 1 || strings.Contains(string(reqs[0].body), wcCanary) || !strings.Contains(string(reqs[0].body), "SHARED sentinel") {
		t.Fatalf("deliveries: %d, first: %s", len(reqs), reqs[0].body)
	}
	if tb := testutil.TablesHolding(t, w.s.DB, wcCanary, "comments"); len(tb) != 0 {
		t.Errorf("the canary is in %v", tb)
	}
}

// Live stream: a reviewer's stream hears nothing of the owner's private notes, and hears the shared one.
func TestPrivateCommentSendsNothingOnTheStream(t *testing.T) {
	f := newSSEFixture(t, nil)
	reviewer := f.s.newCaller(t, "rev", ops.RoleReader)
	f.s.addMember(t, f.pid, reviewer.userID, ops.ProjectReviewer)
	f.save(t, "Some text for a comment to hold.", 1)
	rc, code := openStream(t, f.ts.URL, reviewer, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatal(code)
	}
	rc.nextEvent() // hello
	post := func(body string) {
		t.Helper()
		if rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/comments", body); rec.Code != 201 {
			t.Fatalf("comment: %d %s", rec.Code, rec.Body)
		}
	}
	post(`{"document_id":"` + f.did + `","quote":"text for a comment","body":"STREAMPRIVATE","visibility":"private"}`)
	post(`{"document_id":"` + f.did + `","body":"a shared one"}`)
	e := rc.nextEvent()
	if e.Event != "comment.created" || strings.Contains(e.Data, "STREAMPRIVATE") {
		t.Fatalf("the first thing the reviewer hears: %+v", e)
	}
	cid := regexp.MustCompile(`"comment_id":\s*"(cmt_[A-Z0-9]+)"`).FindStringSubmatch(e.Data)
	if cid == nil {
		t.Fatalf("the event names no comment: %s", e.Data)
	}
	if body, _ := io.ReadAll(f.s.do(reviewer, "GET", "/api/v1/projects/"+f.pid+"/comments/"+cid[1], "").Body); !strings.Contains(string(body), "a shared one") {
		t.Errorf("the event named a comment the reviewer cannot read: %s", body)
	}
	rc.quiet(150 * time.Millisecond)
}

var _ = http.StatusOK

// every byte an export of the book holds, zips opened (a DOCX, an EPUB or the site are zips of text)
func wcEverything(t *testing.T, b []byte) string {
	t.Helper()
	out := string(b)
	if zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b))); err == nil {
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			x, _ := io.ReadAll(rc)
			rc.Close()
			out += "\n" + f.Name + "\n" + wcEverything(t, x)
		}
	}
	return out
}

// A private comment is not the manuscript: not in any export, the site, the search, nor any table but its own.
func TestPrivateCommentNeverReachesAnExportOrTheSearch(t *testing.T) {
	w := newWCWorld(t)
	w.private(t, w.ben, w.doc1, "quiet house", wcCanary+" Ben's note")
	w.private(t, w.ada, w.doc1, "warmth", wcCanary+" Ada's note")
	w.write(t, w.cy, fmt.Sprintf(`{"document_id":%q,"quote":"last paragraph","body":"shared words stay out of exports too"}`, w.doc1))
	base := w.base()
	var presets struct{ Items []struct{ ID, Format string } }
	_ = json.Unmarshal(w.s.do(w.ada, "GET", base+"/compile/presets", "").Body.Bytes(), &presets)
	if rec := w.s.do(w.ada, "PATCH", base+"/site/options", `{"options":{"include_drafts":true}}`); rec.Code != 200 {
		t.Fatalf("site options: %d %s", rec.Code, rec.Body)
	}
	calls := []struct{ name, method, path, body string }{
		{"markdown, single", "POST", base + "/export", `{"mode":"single"}`},
		{"markdown, bundle", "POST", base + "/export", `{"mode":"bundle"}`},
		{"html", "POST", base + "/export/html", `{}`},
		{"site", "POST", base + "/site/build", `{}`},
	}
	for _, p := range presets.Items {
		if p.Format == "docx" || p.Format == "epub" {
			calls = append(calls, struct{ name, method, path, body string }{"compile " + p.ID, "POST", base + "/export/compile", `{"preset":"` + p.ID + `"}`})
		}
	}
	got := 0
	for _, c := range calls {
		for _, who := range []caller{w.ada, w.ben} {
			rec := w.s.do(who, c.method, c.path, c.body)
			if rec.Code != 200 {
				t.Errorf("%s as %s: %d %s", c.name, who.name, rec.Code, rec.Body)
				continue
			}
			got++
			all := wcEverything(t, rec.Body.Bytes())
			if strings.Contains(all, wcCanary) || strings.Contains(all, "shared words stay out") {
				t.Errorf("%s as %s holds a comment", c.name, who.name)
			}
		}
	}
	if got < 4 {
		t.Fatalf("only %d exports were made; the test proves nothing", got)
	}
	for _, who := range []caller{w.ada, w.ben, w.admin} {
		rec := w.s.do(who, "GET", base+"/search?q="+wcCanary, "")
		if strings.Contains(rec.Body.String(), wcCanary) {
			t.Errorf("%s finds a comment through search: %s", who.name, rec.Body)
		}
	}
	if tb := testutil.TablesHolding(t, w.s.DB, wcCanary, "comments"); len(tb) != 0 {
		t.Errorf("the canary is in %v", tb)
	}
}

// A request sent with an Idempotency-Key is, for a shared comment, answered again from a stored copy of the answer. For a private comment the
// record holds only the comment's id: repeating the request reads the thread again as whoever repeats it, so one comment is written, the words are
// in no table but `comments`, and the record's fingerprint of the request does not depend on them.
func TestAPrivateCommentSentWithAnIdempotencyKeyIsReplayedFromTheTable(t *testing.T) {
	w := newWCWorld(t)
	send := func(c caller, key, vis, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", w.base()+"/comments", strings.NewReader(fmt.Sprintf(`{"document_id":%q,"quote":"quiet","body":%q,"visibility":%q}`, w.doc1, body, vis)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.AddCookie(c.cookie)
		rec := httptest.NewRecorder()
		w.s.Handler().ServeHTTP(rec, req)
		return rec
	}
	first := send(w.ben, "wc-key-1", "private", wcCanary+" replay")
	again := send(w.ben, "wc-key-1", "private", wcCanary+" replay")
	if first.Code != 201 || again.Code != 201 || again.Header().Get("Idempotent-Replay") != "true" || !strings.Contains(again.Body.String(), wcCanary+" replay") {
		t.Fatalf("first %d %s; again %d %q (replay %q)", first.Code, first.Body, again.Code, again.Body, again.Header().Get("Idempotent-Replay"))
	}
	var list review.ListOut
	_ = json.Unmarshal(w.s.do(w.ben, "GET", w.base()+"/comments?q="+wcCanary, "").Body.Bytes(), &list)
	if list.Total != 1 {
		t.Errorf("the same key wrote %d comments, want 1", list.Total)
	}
	if tb := testutil.TablesHolding(t, w.s.DB, wcCanary, "comments"); len(tb) != 0 {
		t.Errorf("the canary is in %v", tb)
	}
	// the thread is read again as it is now: after the author deletes it, the repeat says so rather than inventing an answer
	var made struct{ Item struct{ ID string } }
	_ = json.Unmarshal(first.Body.Bytes(), &made)
	w.s.do(w.ben, "DELETE", w.base()+"/comments/"+made.Item.ID, "")
	if gone := send(w.ben, "wc-key-1", "private", wcCanary+" replay"); gone.Code != 409 || !strings.Contains(gone.Body.String(), "deleted since") {
		t.Errorf("a repeat after deleting: %d %s", gone.Code, gone.Body)
	}
	// the stored fingerprint is the same for other words: not even a hash of them is kept
	send(w.ben, "wc-key-3", "private", "COMPLETELY OTHER WORDS")
	var kinds int
	if err := w.s.DB.Read().QueryRow(`SELECT count(DISTINCT request_hash) FROM idempotency_keys WHERE op = 'comment.create'`).Scan(&kinds); err != nil || kinds != 1 {
		t.Errorf("%d kinds of request fingerprint (%v): it depends on the words", kinds, err)
	}
	// a shared comment is replayed as ever
	a, b := send(w.ben, "wc-key-2", "shared", "shared words"), send(w.ben, "wc-key-2", "shared", "shared words")
	if a.Code != 201 || b.Code != 201 || a.Body.String() != b.Body.String() || b.Header().Get("Idempotent-Replay") != "true" {
		t.Errorf("a shared comment's replay: %d %d %q", a.Code, b.Code, b.Header().Get("Idempotent-Replay"))
	}
}
