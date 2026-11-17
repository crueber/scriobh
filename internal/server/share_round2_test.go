package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// pubFrom is pub from a chosen client address.
func (w *shareWorld) pubFrom(addr, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = addr
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

// mkDoc creates a chapter with a content as the owner, and a comment link limited to it.
func (w *shareWorld) mkDoc(t *testing.T, title, content string) (string, string) {
	t.Helper()
	rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/docs", `{"title":`+jsonString(title)+`,"kind":"chapter","content":`+jsonString(content)+`}`)
	var d struct{ Item struct{ ID string } }
	decode(t, rec, &d)
	if d.Item.ID == "" {
		t.Fatalf("doc: %d %s", rec.Code, rec.Body)
	}
	_, tok := w.link(t, `{"scope":"comment","document_ids":["`+d.Item.ID+`"]}`)
	return d.Item.ID, tok
}

// Item 1. What a guest can find out from the answer to a selection does not depend on text a reader cannot see.
func TestAGuestsAnswerDoesNotDependOnHiddenText(t *testing.T) {
	w := newShareWorld(t, nil)
	visible := "Chapter text. The lantern was lit at dusk, see [[%WIKI%|the office]] now.\n\nAnother paragraph.\n"
	templates := map[string]struct{ tpl, wiki string }{
		"front matter":                     {"---\nsecret: %s\n---\n" + visible, "Zed"},
		"a footnote only a note refers to": {visible + "\nPrivate %%x[^9]%% ref.\n\n[^9]: %s\n", "Zed"},
		"a private note":                   {visible + "\nA %%%s%% note.\n", "Zed"},
		"an html comment":                  {visible + "\n<!-- %s -->\n", "Zed"},
		"a wiki link's target":             {visible, ""}, // handled below
	}
	ask := func(content, sel string) string {
		id, tok := w.mkDoc(t, "T", content)
		rec := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+id+`","selection":`+jsonString(sel)+`,"body":"x","name":"Sam"}`)
		var out struct {
			Thread struct{ Quote, Anchor string }
		}
		decode(t, rec, &out)
		return fmt.Sprintf("%d|%s|%s", rec.Code, out.Thread.Quote, out.Thread.Anchor)
	}
	for name, c := range templates {
		if name == "a wiki link's target" {
			continue
		}
		for _, sel := range []string{"lantern was lit", "Marlowe"} {
			fill := func(v string) string {
				return strings.ReplaceAll(strings.Replace(c.tpl, "%s", v, 1), "%WIKI%", "Zed")
			}
			with, without := ask(fill(sel), sel), ask(fill("zzz"), sel)
			if with != without {
				t.Errorf("%s, selecting %q: %q with the phrase hidden there, %q without", name, sel, with, without)
			}
		}
	}
	// a wiki link shows its label: its target is not text a reader can select
	with := ask(strings.ReplaceAll(visible, "%WIKI%", "Marlowe"), "Marlowe")
	without := ask(strings.ReplaceAll(visible, "%WIKI%", "Zed"), "Marlowe")
	if with != without || !strings.HasPrefix(with, "422") {
		t.Errorf("a wiki target: %q against %q", with, without)
	}
	if got := ask(strings.ReplaceAll(visible, "%WIKI%", "Zed"), "lantern was lit"); !strings.HasPrefix(got, "201|lantern was lit|active") {
		t.Errorf("the visible phrase is placed: %q", got)
	}
}

// Item 2. The download is built from the reading view's own reading of the text.
func TestTheDownloadLeaksNothingTheReadingViewHides(t *testing.T) {
	w := newShareWorld(t, nil)
	content := "Open text %%a note[^1]%% goes on.\n\n[^1]: HIDDENFOOTNOTE body\n\n```\nfenced %%kept%% and <!-- kept too -->\n```\n\nInline `%%code%% span`.\n"
	rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/docs", `{"title":"Leaky","kind":"chapter","content":`+jsonString(content)+`}`)
	var d struct{ Item struct{ ID string } }
	decode(t, rec, &d)
	_, tok := w.link(t, `{"allow_download":true,"document_ids":["`+d.Item.ID+`"]}`)
	dl := w.pub("GET", "/s/"+tok+"/api/download", "")
	if dl.Code != 200 {
		t.Fatalf("download: %d %s", dl.Code, dl.Body)
	}
	body := dl.Body.String()
	if strings.Contains(body, "HIDDENFOOTNOTE") {
		t.Errorf("a hidden footnote leaked: %s", body)
	}
	for _, kept := range []string{"fenced %%kept%% and <!-- kept too -->", "`%%code%% span`"} {
		if !strings.Contains(body, kept) {
			t.Errorf("%q was cut out of code: %s", kept, body)
		}
	}
	// the reading view and the download agree on what a reader may see
	page := w.pub("GET", "/s/"+tok+"/api/docs/"+d.Item.ID, "").Body.String()
	if strings.Contains(page, "HIDDENFOOTNOTE") {
		t.Errorf("the reading view shows it: %s", page)
	}
}

// Item 5. Every id a guest names that is not a thread of its own link is the same 404.
func TestAGuestCannotTellWhichCommentIdsExist(t *testing.T) {
	w := newShareWorld(t, nil)
	_, a := w.link(t, `{"scope":"comment"}`)
	_, b := w.link(t, `{"scope":"comment"}`)
	mk := func(tok, body string) (thread, id string) {
		rec := w.pub("POST", "/s/"+tok+"/api/comments", body)
		var out struct {
			Thread struct {
				ID       string
				Comments []struct{ ID string }
			}
		}
		decode(t, rec, &out)
		if rec.Code != 201 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out.Thread.ID, out.Thread.Comments[len(out.Thread.Comments)-1].ID
	}
	root, _ := mk(a, `{"document_id":"`+w.doc2+`","body":"first","name":"Ann"}`)
	_, guestReply := mk(a, `{"document_id":"`+w.doc2+`","parent_id":"`+root+`","body":"second","name":"Ann"}`)
	rr := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/comments", `{"document_id":"`+w.doc2+`","parent_id":"`+root+`","body":"the writer answers"}`)
	var wr struct {
		Item struct{ Replies []struct{ ID string } }
	}
	decode(t, rr, &wr)
	memberReply := wr.Item.Replies[len(wr.Item.Replies)-1].ID
	other := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.other+"/comments", `{"body":"on another project"}`)
	var oc struct{ Item struct{ ID string } }
	decode(t, other, &oc)
	var ref *httptest.ResponseRecorder
	for name, parent := range map[string]string{"another link's thread": root, "another link's reply": guestReply, "a writer's reply": memberReply,
		"another project's comment": oc.Item.ID, "an id that does not exist": "cmt_00000000000000000000000000", "nonsense": "x"} {
		rec := w.pub("POST", "/s/"+b+"/api/comments", `{"document_id":"`+w.doc2+`","parent_id":"`+parent+`","body":"snoop","name":"Eve"}`)
		if ref == nil {
			ref = rec
		}
		if rec.Code != 404 || rec.Body.String() != ref.Body.String() {
			t.Errorf("%s: %d %s (the others: %s)", name, rec.Code, rec.Body, ref.Body)
		}
	}
}

// Item 6. A document the writer left out of the compile is not shown by a link, nor anything beneath it; ai_exclude is
// about AI only.
func TestLinksDoNotShowWhatIsNotCompiled(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"scope":"comment","allow_download":true}`)
	mk := func(title, parent string) string {
		body := `{"title":` + jsonString(title) + `,"kind":"chapter","content":` + jsonString(title+" body text.") + `,"parent_id":` + jsonString(parent) + `}`
		rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/docs", body)
		var d struct{ Item struct{ ID string } }
		decode(t, rec, &d)
		return d.Item.ID
	}
	draft := mk("SCRATCHDRAFT", "")
	child := mk("SCRATCHCHILD", draft)
	aiOnly := mk("AIEXCLUDEDCHAPTER", "")
	for q, id := range map[string]string{`UPDATE documents SET include_in_compile = 0 WHERE id = ?`: draft, `UPDATE documents SET ai_exclude = 1 WHERE id = ?`: aiOnly} {
		if _, err := w.s.DB.Writer().Exec(q, id); err != nil {
			t.Fatal(err)
		}
	}
	mf := w.pub("GET", "/s/"+tok+"/api/manifest", "").Body.String()
	if strings.Contains(mf, "SCRATCH") || !strings.Contains(mf, "AIEXCLUDEDCHAPTER") {
		t.Errorf("manifest: %s", mf)
	}
	for _, id := range []string{draft, child} {
		if r := w.pub("GET", "/s/"+tok+"/api/docs/"+id, ""); r.Code != 404 {
			t.Errorf("a not-compiled document: %d", r.Code)
		}
		if r := w.pub("GET", "/s/"+tok+"/api/comments?document_id="+id, ""); r.Code != 404 {
			t.Errorf("its comments: %d", r.Code)
		}
		if r := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+id+`","body":"x","name":"Sam"}`); r.Code != 404 {
			t.Errorf("a comment on it: %d", r.Code)
		}
	}
	if dl := w.pub("GET", "/s/"+tok+"/api/download", "").Body.String(); strings.Contains(dl, "SCRATCH") || !strings.Contains(dl, "AIEXCLUDEDCHAPTER body text") {
		t.Errorf("download: %s", dl)
	}
	if r := w.pub("GET", "/s/"+tok+"/api/docs/"+aiOnly, ""); r.Code != 200 {
		t.Errorf("an ai_exclude document: %d", r.Code)
	}
}

// Item 7. Wrong passphrases are counted per link and per client address, with an escalating wait, so a stranger
// locks only themselves out; an IPv6 /64 is one client; the owner sees it.
func TestUnlockFailuresAreCountedPerClientWithAnEscalatingWait(t *testing.T) {
	w := newShareWorld(t, nil)
	id, tok := w.link(t, `{"passphrase":"correct horse battery"}`)
	now := time.Now()
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	unlock := func(addr, pass string) *httptest.ResponseRecorder {
		w.s.limiter.now = func() time.Time { return now }
		return w.pubFrom(addr, "POST", "/s/"+tok+"/api/unlock", `{"passphrase":"`+pass+`"}`)
	}
	const bad, good = "wrong wrong wrong", "correct horse battery"
	A, B := "198.51.100.7:4000", "203.0.113.9:4000"
	for i := 0; i < 3; i++ {
		if r := unlock(A, bad); r.Code != 401 {
			t.Fatalf("free try %d: %d", i, r.Code)
		}
	}
	if r := unlock(A, bad); r.Code != 401 { // the fourth is wrong too and earns a wait
		t.Fatalf("fourth: %d", r.Code)
	}
	if r := unlock(A, good); r.Code != 429 || r.Header().Get("Retry-After") == "" {
		t.Errorf("the right passphrase while waiting: %d %v", r.Code, r.Header())
	}
	// another address is not held up by it
	if r := unlock(B, good); r.Code != 200 {
		t.Errorf("another client: %d %s", r.Code, r.Body)
	}
	// the wait ends, the next wrong try doubles it
	now = now.Add(31 * time.Second)
	if r := unlock(A, bad); r.Code != 401 {
		t.Fatalf("after the wait: %d", r.Code)
	}
	r := unlock(A, good)
	if r.Code != 429 {
		t.Fatalf("second wait: %d", r.Code)
	}
	var retry int
	fmt.Sscan(r.Header().Get("Retry-After"), &retry)
	if retry < 55 || retry > 61 {
		t.Errorf("the second wait is %ds, want about a minute", retry)
	}
	now = now.Add(61 * time.Second)
	if r := unlock(A, good); r.Code != 200 {
		t.Errorf("after the second wait the right passphrase works: %d", r.Code)
	}
	// an IPv6 /64 is one client; another /64 is another
	V1, V2, V3 := "[2001:db8:1:2::1]:4000", "[2001:db8:1:2:ffff::5]:4000", "[2001:db8:1:3::1]:4000"
	for i := 0; i < 4; i++ {
		unlock(V1, bad)
	}
	if r := unlock(V2, good); r.Code != 429 {
		t.Errorf("the same /64: %d", r.Code)
	}
	if r := unlock(V3, good); r.Code != 200 {
		t.Errorf("another /64: %d", r.Code)
	}
	// the owner sees the failed tries and who is waiting
	var u struct {
		Failed int `json:"failed_unlocks"`
		Locked bool
		Count  int `json:"locked_clients"`
	}
	decode(t, w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/shares/"+id+"/usage", ""), &u)
	if u.Failed != 9 || !u.Locked || u.Count != 1 {
		t.Errorf("usage: %+v", u)
	}
	// a new passphrase starts everyone again
	if r := w.s.do(w.owner, "PATCH", "/api/v1/projects/"+w.pid+"/shares/"+id, `{"passphrase":"a brand new phrase"}`); r.Code != 200 {
		t.Fatal(r.Body)
	}
	if r := w.pubFrom(V1, "POST", "/s/"+tok+"/api/unlock", `{"passphrase":"a brand new phrase"}`); r.Code != 200 {
		t.Errorf("after a new passphrase: %d", r.Code)
	}
}

// Item 7. Callers who do not hold the proof spend their own address's allowance, never the link's.
func TestOnlyProofHoldersSpendTheLinksAllowance(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_SHARE_LINK_RATE": "12", "SCRIOBH_SHARE_RATE_LIMIT": "0"})
	_, tok := w.link(t, `{"passphrase":"correct horse battery"}`)
	for i := 0; i < 40; i++ { // a stranger with the token, hammering without a proof
		if r := w.pubFrom("198.51.100.7:1", "GET", "/s/"+tok+"/api/manifest", ""); r.Code != 401 {
			t.Fatalf("request %d: %d", i, r.Code)
		}
	}
	for i := 0; i < 12; i++ { // wrong passphrases are not charged to the link either
		w.pubFrom("198.51.100.7:1", "POST", "/s/"+tok+"/api/unlock", `{"passphrase":"nope nope nope"}`)
	}
	var u struct{ Proof string }
	decode(t, w.pubFrom("203.0.113.9:1", "POST", "/s/"+tok+"/api/unlock", `{"passphrase":"correct horse battery"}`), &u)
	if u.Proof == "" {
		t.Fatal("no proof")
	}
	for i := 0; i < 8; i++ {
		if r := w.pubFrom("203.0.113.9:1", "GET", "/s/"+tok+"/api/manifest", "", "X-Share-Unlock", u.Proof); r.Code != 200 {
			t.Fatalf("the reader's request %d: %d (the stranger used up the link's allowance)", i, r.Code)
		}
	}
	// the IPv6 /64 is one client for the per-address limit too
	w2 := newShareWorld(t, map[string]string{"SCRIOBH_SHARE_RATE_LIMIT": "40"})
	_, tok2 := w2.link(t, `{}`)
	codes := map[int]int{}
	for i := 0; i < 24; i++ {
		codes[w2.pubFrom(fmt.Sprintf("[2001:db8:5:6:%x::1]:1", i+1), "GET", "/s/garbage/api/manifest", "").Code]++
	}
	if codes[429] == 0 {
		t.Errorf("rotating the low half of an IPv6 address got round the limit: %v", codes)
	}
	_ = tok2
}

// Item 10. Every refused guest attempt counts against the link's hour.
func TestRefusedGuestAttemptsSpendTheHour(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_SHARE_COMMENTS_PER_HOUR": "4"})
	_, tok := w.link(t, `{"scope":"comment"}`)
	now := time.Now()
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	try := func(sel string) int {
		w.s.limiter.now = func() time.Time { return now }
		return w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+w.doc1+`","selection":`+jsonString(sel)+`,"body":"guess","name":"Eve"}`).Code
	}
	for i := 0; i < 4; i++ {
		if c := try(fmt.Sprint("a guess that is not there ", i)); c != 422 {
			t.Fatalf("guess %d: %d", i, c)
		}
	}
	if c := try("stood alone on the hill"); c != 429 {
		t.Errorf("a valid comment after four refused: %d", c)
	}
	now = now.Add(61 * time.Minute)
	if c := try("stood alone on the hill"); c != 201 {
		t.Errorf("an hour later: %d", c)
	}
}

// Item 10. The download is cached for a minute by what it was built from.
func TestDownloadIsBuiltOnceForTheSameText(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"allow_download":true}`)
	before := w.s.shr.downloads.Builds.Load()
	a := w.pub("GET", "/s/"+tok+"/api/download", "")
	b := w.pub("GET", "/s/"+tok+"/api/download", "")
	if a.Code != 200 || b.Body.String() != a.Body.String() || w.s.shr.downloads.Builds.Load() != before+1 {
		t.Errorf("builds %d", w.s.shr.downloads.Builds.Load()-before)
	}
}

// Item 12. The same comment sent twice within a minute is one comment.
func TestADoubleSubmitIsAnswerWithTheFirstComment(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"scope":"comment"}`)
	now := time.Now()
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	body := `{"document_id":"` + w.doc1 + `","selection":"stood alone on the hill","body":"Why alone?","name":"Sam"}`
	first := w.pub("POST", "/s/"+tok+"/api/comments", body)
	now = now.Add(5 * time.Second)
	second := w.pub("POST", "/s/"+tok+"/api/comments", body)
	if first.Code != 201 || second.Code != 200 || !strings.Contains(second.Body.String(), `"duplicate":true`) {
		t.Fatalf("%d %d %s", first.Code, second.Code, second.Body)
	}
	var f, s struct{ Thread struct{ ID string } }
	decode(t, first, &f)
	decode(t, second, &s)
	if f.Thread.ID != s.Thread.ID || countRows(t, w.s, `SELECT count(*) FROM comments WHERE share_id IS NOT NULL`) != 1 {
		t.Errorf("a double submit wrote twice")
	}
	// a different place, name or text is a different comment, and so is the same one after a minute
	if r := w.pub("POST", "/s/"+tok+"/api/comments", strings.Replace(body, "Sam", "Sue", 1)); r.Code != 201 {
		t.Errorf("another name: %d", r.Code)
	}
	now = now.Add(2 * time.Minute)
	if r := w.pub("POST", "/s/"+tok+"/api/comments", body); r.Code != 201 {
		t.Errorf("after a minute: %d", r.Code)
	}
}

// Item 12. The guest route and comment.create are one code path (review.InsertComment): the same cases through both
// give the same outcomes where the rules are the same.
func TestTheGuestRouteAndCommentCreateAgreeOnTheRules(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"scope":"comment"}`)
	member := func(body string) int {
		return w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/comments", body).Code
	}
	guest := func(body string) int {
		return w.pub("POST", "/s/"+tok+"/api/comments", body).Code
	}
	type c struct {
		name          string
		member, guest string // request bodies; the guest's carry a name
		want          int
	}
	d := w.doc2
	cases := []c{
		{"white space only", `{"document_id":"` + d + `","body":"   "}`, `{"document_id":"` + d + `","body":"   ","name":"Sam"}`, 422},
		{"a control character", `{"document_id":"` + d + `","body":"a\u0001b"}`, `{"document_id":"` + d + `","body":"a\u0001b","name":"Sam"}`, 422},
		{"an unknown document", `{"document_id":"doc_00000000000000000000000000","body":"x"}`, `{"document_id":"doc_00000000000000000000000000","body":"x","name":"Sam"}`, 404},
		{"another project's document", `{"document_id":"` + w.otherDoc + `","body":"x"}`, `{"document_id":"` + w.otherDoc + `","body":"x","name":"Sam"}`, 404},
		{"a selection in a private note", `{"document_id":"` + w.doc1 + `","quote":"SECRETNOTE do not show","body":"x"}`, `{"document_id":"` + w.doc1 + `","selection":"SECRETNOTE do not show","body":"x","name":"Sam"}`, 422},
		{"a selection in ghosted text", `{"document_id":"` + w.doc1 + `","quote":"GHOSTWORDS cut sentence","body":"x"}`, `{"document_id":"` + w.doc1 + `","selection":"GHOSTWORDS cut sentence","body":"x","name":"Sam"}`, 422},
		{"invisible characters", `{"document_id":"` + d + `","body":"he​llo"}`, `{"document_id":"` + d + `","body":"he​llo","name":"Sam"}`, 201},
		{"an anchored comment", `{"document_id":"` + w.doc1 + `","quote":"stood alone","body":"fine"}`, `{"document_id":"` + w.doc1 + `","selection":"stood alone","body":"fine","name":"Sam"}`, 201},
	}
	for _, k := range cases {
		m, g := member(k.member), guest(k.guest)
		if m != k.want || g != k.want {
			t.Errorf("%s: comment.create %d, guest route %d, want %d for both", k.name, m, g, k.want)
		}
	}
	// the text that was stored is the same
	var bodies []string
	rows, err := w.s.DB.Read().Query(`SELECT body FROM comments WHERE body = 'hello'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var b string
		_ = rows.Scan(&b)
		bodies = append(bodies, b)
	}
	rows.Close()
	if len(bodies) != 2 {
		t.Errorf("%d comments say hello after the invisible characters were removed", len(bodies))
	}
	// the project cap: live comments only, and 409 for both
	fill := func() {
		tx, err := w.s.DB.Writer().Begin()
		if err != nil {
			t.Fatal(err)
		}
		now := db.Now()
		for i := 0; i < 20000; i++ {
			if _, err := tx.Exec(`INSERT INTO comments(id, project_id, kind, body, status, anchor_state, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
				db.NewID("cmt"), w.pid, "comment", "bulk", "open", "none", now, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	fill()
	if m, g := member(cases[0].member[:len(cases[0].member)-len(`"   "}`)]+`"capped"}`), guest(`{"document_id":"`+d+`","body":"capped","name":"Sam"}`); m != 409 || g != 409 {
		t.Errorf("the project cap: comment.create %d, guest route %d, want 409 for both", m, g)
	}
	if _, err := w.s.DB.Writer().Exec(`DELETE FROM comments WHERE body = 'bulk'`); err != nil {
		t.Fatal(err)
	}
	// the reply cap, for a thread of each
	var mt, gt struct{ Item struct{ ID string } }
	decode(t, w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/comments", `{"document_id":"`+d+`","body":"a member thread"}`), &mt)
	var gtt struct{ Thread struct{ ID string } }
	decode(t, w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+d+`","body":"a guest thread","name":"Sam"}`), &gtt)
	_ = gt
	for _, th := range []string{mt.Item.ID, gtt.Thread.ID} {
		tx, err := w.s.DB.Writer().Begin()
		if err != nil {
			t.Fatal(err)
		}
		now := db.Now()
		for i := 0; i < 500; i++ {
			if _, err := tx.Exec(`INSERT INTO comments(id, project_id, document_id, parent_id, kind, body, status, anchor_state, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				db.NewID("cmt"), w.pid, d, th, "comment", "reply", "open", "none", now, now); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if m := member(`{"document_id":"` + d + `","parent_id":"` + mt.Item.ID + `","body":"one too many"}`); m != 409 {
		t.Errorf("the reply cap, comment.create: %d", m)
	}
	w.s.limiter.now = func() time.Time { return time.Now().Add(5 * time.Minute) } // the link's per-minute allowance has refilled
	if g := guest(`{"document_id":"` + d + `","parent_id":"` + gtt.Thread.ID + `","body":"one too many","name":"Sam"}`); g != 409 {
		t.Errorf("the reply cap, guest route: %d", g)
	}
}
