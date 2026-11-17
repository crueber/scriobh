package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// ---- presence --------------------------------------------------------------------------------------------------

func (f *presFixture) beatKeyed(c caller, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/v1/projects/"+f.pid+"/presence", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(rec, req)
	return rec
}

// FAILS TODAY. A beat is a write, so the REST layer honours an Idempotency-Key on it: the answer (names, and for an
// owner or editor the titles of the chapters others have open) is written to idempotency_keys for 24 hours and a repeat
// of the same request is answered from that row, after the caller's role, presence.share or membership has changed.
// Presence is documented as memory only: "nothing in a backup, export, mirror, event or Git".
func TestReviewBeatWithAnIdempotencyKeyIsNeitherStoredNorReplayed(t *testing.T) {
	f := newPresFixture(t)
	f.beat(t, f.owner, f.docB) // the owner is on the chapter whose title is the canary
	rec := f.beatKeyed(f.editor, "k-edgar-1", "{}")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), presTitleB) {
		t.Fatalf("setup: the editor's beat should name the owner's chapter: %d %s", rec.Code, rec.Body)
	}

	// (1) the answer is now in the database (and so in every backup)
	var n int
	if err := f.s.DB.Read().QueryRow(`SELECT count(*) FROM idempotency_keys WHERE response_json LIKE '%' || ? || '%'`, presTitleB).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a presence answer (a chapter title and a person's name) is stored in idempotency_keys: %d row(s)", n)
	}

	// (2) the editor is made a reader; the same request is replayed and still carries the names and the title
	if _, err := f.s.DB.Writer().Exec(`UPDATE project_members SET role = 'reader' WHERE project_id = ? AND user_id = ?`, f.pid, f.editor.userID); err != nil {
		t.Fatal(err)
	}
	fresh := f.beat(t, f.editor, "") // the control: an unkeyed beat tells a reader no one
	if strings.Contains(fresh.Body.String(), "Owner") || strings.Contains(fresh.Body.String(), presTitleB) {
		t.Fatalf("control: an unkeyed beat told a reader about people: %s", fresh.Body)
	}
	rep := f.beatKeyed(f.editor, "k-edgar-1", "{}")
	if strings.Contains(rep.Body.String(), presTitleB) || strings.Contains(rep.Body.String(), `"Owner"`) {
		t.Errorf("a replayed beat told a person who has since become a reader who is here and on which chapter (Idempotent-Replay=%q): %s",
			rep.Header().Get("Idempotent-Replay"), rep.Body)
	}

	// (3) and the same after switching presence.share off ("off means neither sends nor sees")
	f.beatKeyed(f.owner, "k-owner-1", "{}")
	f.setShare(t, f.owner, "false")
	rep = f.beatKeyed(f.owner, "k-owner-1", "{}")
	if strings.Contains(rep.Body.String(), `"enabled":true`) || strings.Contains(rep.Body.String(), "Edgar") {
		t.Errorf("a replayed beat showed who is here to a person who switched presence.share off: %s", rep.Body)
	}
}

// Guards (these pass): which requests reach the passive path, and that a beating page does not keep a session alive.
func TestReviewOnlyHeartbeatsLetTheSessionIdleOut(t *testing.T) {
	f := newPresFixture(t)
	// SCRIOBH_SESSION_IDLE_MINUTES defaults to 480. A page that does nothing but beat must be signed out on schedule.
	var code int
	for i := 0; i < 400; i++ {
		f.advance(2 * time.Minute)
		code = f.beat(t, f.editor, "").Code
		if code == http.StatusUnauthorized {
			if elapsed := time.Duration(i+1) * 2 * time.Minute; elapsed < 470*time.Minute || elapsed > 490*time.Minute {
				t.Fatalf("signed out after %v of beating, want about 8 hours", elapsed)
			}
			return
		}
		if code != 200 {
			t.Fatalf("beat %d: %d", i, code)
		}
	}
	t.Fatal("a page that only beats kept its session alive for more than 13 hours")
}

func TestReviewPresencePathsAndMethods(t *testing.T) {
	f := newPresFixture(t)
	f.beat(t, f.editor, f.docB)
	base := "/api/v1/projects/" + f.pid
	for _, tc := range []struct {
		method, path string
		not200       bool
	}{
		{"GET", base + "/presence/", true},        // no such route
		{"GET", base + "/presence/../docs", true}, // cleaned and redirected, then an ordinary call
		{"GET", base + "/presence%2Fx", true},     // not a route
		{"DELETE", base + "/presence", true},      // wrong method
		{"PUT", base + "/presence", true},         // wrong method
		{"POST", base + "/presence/leave/", true}, // trailing slash: no route
		{"GET", base + "/presence/leave", true},   // wrong method
		{"POST", base + "/presence?document_id=x", false},
	} {
		rec := f.s.do(f.reviewer, tc.method, tc.path, "")
		if !tc.not200 && rec.Code != 200 {
			t.Errorf("%s %s: %d", tc.method, tc.path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), presTitleB) {
			t.Errorf("%s %s leaked a title to a reviewer: %s", tc.method, tc.path, rec.Body)
		}
	}
	// an anonymous caller, an administrator who is not a member, a stranger and a key
	anon := f.s.do(caller{}, "GET", base+"/presence", "")
	if anon.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", anon.Code)
	}
	admin := f.s.newCaller(t, "boss", ops.RoleAdmin)
	f.setShare(t, admin, "true")
	for _, m := range [][2]string{{"GET", "/presence"}, {"POST", "/presence"}, {"POST", "/presence/leave"}} {
		if rec := f.s.do(f.stranger, m[0], base+m[1], "{}"); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as a stranger: %d %s", m[0], m[1], rec.Code, rec.Body)
		}
	}
	// the administrator is an owner for the API's purposes (projects.RoleFunc) but not a member: shown no one, recorded nowhere
	f.beat(t, f.owner, f.docB)
	for _, m := range [][2]string{{"GET", "/presence"}, {"POST", "/presence"}} {
		rec := f.s.do(admin, m[0], base+m[1], "{}")
		if rec.Code != 200 || strings.Contains(rec.Body.String(), presTitleB) || strings.Contains(rec.Body.String(), "Owner") || !strings.Contains(rec.Body.String(), `"not_member"`) {
			t.Errorf("%s as a non-member administrator: %d %s", m[0], rec.Code, rec.Body)
		}
	}
	for _, it := range parsePres(t, f.s.do(f.owner, "GET", base+"/presence", "").Body.String()).Items {
		if it.Name == "Boss" || it.UserID == admin.userID {
			t.Errorf("the administrator was recorded: %+v", it)
		}
	}
	// another project's chapter id in a beat is ignored without a trace and answers like an unknown id
	other := idOf(t, f.s.do(f.stranger, "POST", "/api/v1/projects", `{"title":"Elsewhere"}`).Body.Bytes())
	otherDoc := idOf(t, f.s.do(f.stranger, "POST", "/api/v1/projects/"+other+"/docs", `{"title":"Secret Chapter"}`).Body.Bytes())
	a := f.beat(t, f.editor, otherDoc)
	b := f.beat(t, f.editor, "doc_00000000000000000000000000")
	if a.Code != b.Code || strings.Contains(a.Body.String(), "Secret Chapter") {
		t.Errorf("a foreign document id is told apart from an unknown one: %d %s / %d %s", a.Code, a.Body, b.Code, b.Body)
	}
	if strings.Contains(f.s.do(f.owner, "GET", base+"/presence", "").Body.String(), "Secret Chapter") {
		t.Error("a foreign chapter title reached the owner's list")
	}
}

// Guard (passes): an editor demoted to reader while a stream is open is told no one from the next frame on.
func TestReviewStreamFollowsARoleChange(t *testing.T) {
	f := newPresFixture(t)
	cl, code := openStream(t, f.ts.URL, f.editor, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatalf("stream: %d", code)
	}
	cl.nextEvent()
	nextPresence(t, cl)
	if _, err := f.s.DB.Writer().Exec(`UPDATE project_members SET role = 'reader' WHERE project_id = ? AND user_id = ?`, f.pid, f.editor.userID); err != nil {
		t.Fatal(err)
	}
	f.beat(t, f.owner, f.docB)
	f.advance(6 * time.Second)
	f.tick()
	fr := nextPresence(t, cl)
	if strings.Contains(fr.Data, presTitleB) || strings.Contains(fr.Data, "Owner") {
		t.Fatalf("a demoted editor was sent names or titles: %s", fr.Data)
	}
}

// FAILS TODAY (low). pageGate and the registry ask the gate passively for the presence routes whoever calls them, and a
// passive check is not counted, so a key's refused attempts on a protected project that refuses keys are counted when it
// calls any other route but not when it calls a presence route.
func TestReviewKeyRefusedOnPresenceIsCountedLikeAnyOtherRefusal(t *testing.T) {
	f := newPresFixture(t)
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/passphrase", `{"passphrase":"a long enough one"}`); rec.Code != 200 {
		t.Fatalf("passphrase: %d %s", rec.Code, rec.Body)
	}
	count := func(keyID string) int {
		var n int
		for i := 0; i < 40; i++ { // written from a goroutine
			_ = f.s.DB.Read().QueryRow(`SELECT COALESCE(sum(refused),0) FROM project_gate_key_use WHERE key_id = ?`, keyID).Scan(&n)
			if n > 0 {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		return n
	}
	call := func(secret, path string) int {
		req := typoReq("GET", path, "")
		req.Header.Set("Authorization", "Bearer "+secret)
		return typoServe(f.s, req).Code
	}
	docsKey, docsID := f.s.mintKey(t, f.owner, `{"name":"d","scope":"write"}`)
	presKey, presID := f.s.mintKey(t, f.owner, `{"name":"p","scope":"write"}`)
	if c := call(docsKey, "/api/v1/projects/"+f.pid+"/docs"); c != http.StatusForbidden {
		t.Fatalf("control: a key on a protected project: %d", c)
	}
	if count(docsID) == 0 {
		t.Fatal("control: the refusal on the docs route was not counted")
	}
	if c := call(presKey, "/api/v1/projects/"+f.pid+"/presence"); c != http.StatusForbidden {
		t.Fatalf("a key on the presence route: %d", c)
	}
	if count(presID) == 0 {
		t.Error("the same refusal on the presence route was not counted")
	}
}

// ---- typography ------------------------------------------------------------------------------------------------

// FAILS TODAY. A person who has chosen a taller line height, a narrower measure or another typeface in Settings, Editor
// (a stored non-default value) must not have it taken away by a project's typography: an editor can set 1.3 over a
// reader's 190. The person's own typography override (account.typography.set) still wins over everything.
func TestReviewAProjectCannotOverrideAPersonsExplicitEditorSettings(t *testing.T) {
	w := newTypoWorld(t)
	if rec := w.s.do(w.reader, "PUT", "/api/v1/me/prefs", `{"items":{"editor_line_height":"1.9","editor_measure":"full","editor_font":"sans"}}`); rec.Code != 200 {
		t.Fatalf("prefs: %d %s", rec.Code, rec.Body)
	}
	if rec := w.put(w.owner, `{"line_height":"1.3","measure":80,"font":"mono"}`); rec.Code != 200 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{w.writeURL, w.readURL} {
		// the control: a person who chose nothing follows the project
		if tag := w.paperTags(t, w.editor, path)[0]; !strings.Contains(tag, `data-tlh="130"`) || !strings.Contains(tag, `data-font="mono"`) {
			t.Fatalf("control: %s", tag)
		}
		tag := w.paperTags(t, w.reader, path)[0]
		if strings.Contains(tag, `data-tlh="130"`) {
			t.Errorf("%s: the project's 1.3 line height replaced the reader's own 1.9: %s", path, tag)
		}
		if strings.Contains(tag, ` data-tm=`) {
			t.Errorf("%s: the project's measure replaced the reader's own full width: %s", path, tag)
		}
		if strings.Contains(tag, `data-font="mono"`) {
			t.Errorf("%s: the project's typeface replaced the reader's own: %s", path, tag)
		}
	}
	// the person's typography override is the deliberate way to follow neither
	if rec := w.s.do(w.reader, "PUT", "/api/v1/me/typography", `{"line_height":"1.3"}`); rec.Code != 200 {
		t.Fatalf("override: %d %s", rec.Code, rec.Body)
	}
	if tag := w.paperTags(t, w.reader, w.writeURL)[0]; !strings.Contains(tag, `data-tlh="130"`) {
		t.Errorf("the person's own override no longer wins: %s", tag)
	}
}

// FAILS TODAY (low). The writing view draws the document in <pre id="doc-plain"> until (and unless) the editor mounts, and
// typography.css scales only .scriobh-prose and .chapter, so the text changes size and indent when the editor takes over,
// and for the no-script fallback it never applies.
func TestReviewTypographySizeRuleNamesEveryProseContainerOfTheWritingView(t *testing.T) {
	css, err := os.ReadFile("../../web/static/css/typography.css")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := os.ReadFile("../../web/templates/write.html")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`id="doc-plain"`).Match(tpl) {
		t.Skip("the plain-text stand-in is gone")
	}
	rule := regexp.MustCompile(`\.paper\[data-ts\][^{]*\{[^}]*font-size`).FindString(string(css))
	if !strings.Contains(rule, "doc-plain") {
		t.Errorf("the size rule does not reach .doc-plain: %q", rule)
	}
}

// Guard (passes): odd JSON never stores anything that is not on the lists, and what is stored never reaches the page as
// anything but the fixed attributes.
func TestReviewTypographyOddInputs(t *testing.T) {
	w := newTypoWorld(t)
	bad := []string{
		`{"size":"105"}`, `{"size":105.0}`, `{"size":1.05e2}`, `{"size":1e400}`, `{"size":99999999999999999999}`, `{"size":-0.0}`, `{"size":NaN}`,
		`{"size":104}`, `{"size":-105}`, `{"size":"１０５"}`, `{"line_height":1.3}`, `{"line_height":"1.30"}`, `{"line_height":"１.３"}`,
		`{"line_height":"1.3\u0000"}`, `{"measure":"60"}`, `{"measure":61}`, `{"font":"SERIF"}`, `{"font":"serif "}`, `{"font":"Newsreader"}`,
		`{"font":["serif"]}`, `{"font":null,"unknown":1}`, `{"paragraphs":"Indent"}`, `{"scene_break":"url(x)"}`, `{"font":"serif","font":"x"}`,
		`{"font":"system-serif;}</style><script>alert(1)</script>"}`, `[]`, `"x"`,
	}
	for _, body := range bad {
		rec := w.put(w.owner, body)
		if rec.Code < 400 || rec.Code >= 500 {
			// `{"font":null,"unknown":1}` and the duplicate font key are both refused; a duplicate whose last value is valid is not tested here
			t.Errorf("%s accepted: %d %s", body, rec.Code, rec.Body)
		}
	}
	var out struct{ Project struct{ Set bool } }
	_ = json.Unmarshal(w.s.do(w.owner, "GET", strings.Replace(typoBase, "%s", w.pid, 1), "").Body.Bytes(), &out)
	if out.Project.Set {
		t.Fatal("something was stored by a refused request")
	}
	// case variants of a field name are the standard library's: they land in the same field and are validated the same way
	if rec := w.put(w.owner, `{"Size":110,"LINE_HEIGHT":"1.5"}`); rec.Code != 200 {
		t.Logf("case-variant field names: %d", rec.Code)
	}
	if tag := w.paperTags(t, w.owner, w.writeURL)[0]; !regexp.MustCompile(`^<article class="paper" id="paper" lang="en" dir="ltr" data-size="19" data-measure="66" data-lh="165" data-font="serif"( data-t[a-z]+="[a-z0-9-]+")* aria-labelledby="doc-title-h">$`).MatchString(tag) {
		t.Errorf("unexpected paper tag: %s", tag)
	}
}

// FAILS TODAY (informational, low). answer() always loads the caller's own override, so a key pinned to a project that
// reads project.typography.get is handed the owner's personal override (`mine`), though the branch's own test says such a
// key cannot reach /me/typography.
func TestReviewPinnedKeyDoesNotLearnThePersonalOverride(t *testing.T) {
	w := newTypoWorld(t)
	if rec := w.s.do(w.owner, "PUT", "/api/v1/me/typography", `{"line_height":"1.3","font":"system-mono"}`); rec.Code != 200 {
		t.Fatalf("override: %d %s", rec.Code, rec.Body)
	}
	rk, _ := w.s.mintKey(t, w.owner, `{"name":"r","scope":"read","project_id":"`+w.pid+`"}`)
	req := typoReq("GET", strings.Replace(typoBase, "%s", w.pid, 1), "")
	req.Header.Set("Authorization", "Bearer "+rk)
	rec := typoServe(w.s, req)
	if rec.Code != 200 {
		t.Fatalf("pinned key reading the project's typography: %d", rec.Code)
	}
	var out struct{ Mine struct{ Set bool } }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Mine.Set {
		t.Errorf("a project-pinned key was told the person's own typography override: %s", rec.Body)
	}
}
