package server

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// The AI screens are shells that the script fills from the ai.* ops, so what the server decides is who sees which shell and
// which affordances the writing view carries: none of the writing partner unless AI is on for the project and the person
// edits it, the Lab (local checks, no model) for every editor, nothing at all for a reader.

func (e writeEnv) setAIPolicy(t *testing.T, c caller, mode string) *http.Response {
	t.Helper()
	rec := e.s.do(c, "PUT", "/api/v1/projects/"+e.pid+"/ai/policy", `{"mode":"`+mode+`"}`)
	return &http.Response{StatusCode: rec.Code}
}

func (e writeEnv) writeBody(t *testing.T, c caller) (int, string) {
	t.Helper()
	rec := e.s.do(c, "GET", "/p/"+e.slug+"/d/"+e.chap, "")
	return rec.Code, rec.Body.String()
}

func TestAuthzAISettingsPage(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	if rec := s.do(caller{name: "anonymous"}, "GET", "/settings/ai", ""); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusSeeOther && rec.Code != http.StatusFound {
		t.Errorf("anonymous: %d, want a sign-in", rec.Code)
	}
	for _, c := range []struct {
		who          caller
		admin, write string
	}{{admin, "true", "true"}, {e.owner, "false", "true"}, {reader, "false", "false"}} {
		rec := s.do(c.who, "GET", "/settings/ai", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", c.who.name, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `data-admin="`+c.admin+`" data-writer="`+c.write+`"`) {
			t.Errorf("%s: the shell does not say who they are", c.who.name)
		}
		for _, want := range []string{`id="ai-root"`, `js/aiadmin`, `css/ai`, "Keys are write-only"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: lacks %q", c.who.name, want)
			}
		}
		if strings.Contains(body, "<script>") || regexp.MustCompile(`\son[a-z]+="`).MatchString(body) {
			t.Errorf("%s: an inline script or handler", c.who.name)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: the page may be cached", c.who.name)
		}
	}
}

func TestAuthzAIProjectPage(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	reader := s.newCaller(t, "reader", ops.RoleReader)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	path := "/p/" + e.slug + "/ai"
	rec := s.do(e.owner, "GET", path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="ai-project"`, `data-owner="true"`, `data-edit="true"`, `data-state="off"`, `aria-current="page"`} {
		if !strings.Contains(body, want) {
			t.Errorf("owner: lacks %q", want)
		}
	}
	rec = s.do(reader, "GET", path, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-owner="false" data-edit="false"`) {
		t.Errorf("reader: %d, and the shell must say they may only look", rec.Code)
	}
	if rec := s.do(outsider, "GET", path, ""); rec.Code != http.StatusNotFound {
		t.Errorf("outsider: %d, want 404 so existence does not leak", rec.Code)
	}
	if rec := s.do(caller{name: "anonymous"}, "GET", path, ""); rec.Code == http.StatusOK {
		t.Error("anonymous opened the page")
	}
	// The AI page is in the project's tabs.
	if code, w := e.writeBody(t, e.owner); code != http.StatusOK || !strings.Contains(w, `href="/p/`+e.slug+`/ai"`) {
		t.Errorf("the project's tabs do not lead to the AI page (%d)", code)
	}
}

func TestWritingViewAffordancesFollowThePolicy(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	reader := s.newCaller(t, "reader", ops.RoleReader)
	s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	// A fresh project: AI is off. The Lab is there (it needs no model), the writing partner is not.
	code, off := e.writeBody(t, e.owner)
	if code != http.StatusOK {
		t.Fatalf("write: %d", code)
	}
	for _, want := range []string{`data-ai="off"`, `id="itab-lab"`, `data-lab-src=`, `AI off`} {
		if !strings.Contains(off, want) {
			t.Errorf("AI off: lacks %q", want)
		}
	}
	for _, not := range []string{`id="itab-ai"`, `data-cmd="ai"`, "js/aiboot", "js/ai.js", "js/aipanel", "js/aichat", "js/aireviews", "data-ai-src", "data-aicore-src"} {
		if strings.Contains(off, not) {
			t.Errorf("AI off: the page carries %q", not)
		}
	}
	// A reader gets neither.
	_, rd := e.writeBody(t, reader)
	for _, not := range []string{`id="itab-lab"`, `id="itab-ai"`, "data-lab-src", "js/aiboot", `data-cmd="ai"`} {
		if strings.Contains(rd, not) {
			t.Errorf("reader: the page carries %q", not)
		}
	}
	// Only the owner changes the policy.
	if r := e.setAIPolicy(t, reader, "ask"); r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusNotFound {
		t.Errorf("a reader set the policy: %d", r.StatusCode)
	}
	if r := e.setAIPolicy(t, e.owner, "ask"); r.StatusCode != http.StatusOK {
		t.Fatalf("policy ask: %d", r.StatusCode)
	}
	_, on := e.writeBody(t, e.owner)
	for _, want := range []string{`data-ai="on"`, `id="itab-ai"`, `data-cmd="ai"`, "js/aiboot", "data-aicore-src", "data-aichat-src", "data-aireviews-src", "ask first"} {
		if !strings.Contains(on, want) {
			t.Errorf("AI on: lacks %q", want)
		}
	}
	// Only the small boot file is on the page; the rest is fetched when an affordance is opened.
	if !regexp.MustCompile(`data-ext="[^"]*js/aiboot[^"]*"`).MatchString(on) || regexp.MustCompile(`data-ext="[^"]*js/ai\.[^"]*"`).MatchString(on) {
		t.Error("only aiboot.js may be loaded with the page")
	}
	_, rdOn := e.writeBody(t, reader)
	if strings.Contains(rdOn, "js/aiboot") || strings.Contains(rdOn, `id="itab-ai"`) {
		t.Error("AI on: a reader's page carries the writing partner")
	}
	if r := e.setAIPolicy(t, e.owner, "local_only"); r.StatusCode != http.StatusOK {
		t.Fatalf("policy local_only: %d", r.StatusCode)
	}
	if _, lo := e.writeBody(t, e.owner); !strings.Contains(lo, "local only") {
		t.Error("local only: the header does not say so")
	}
	if r := e.setAIPolicy(t, e.owner, "off"); r.StatusCode != http.StatusOK {
		t.Fatalf("policy off: %d", r.StatusCode)
	}
	if _, again := e.writeBody(t, e.owner); strings.Contains(again, `id="itab-ai"`) || strings.Contains(again, "js/aiboot") || !strings.Contains(again, `data-ai="off"`) {
		t.Error("turned off again: the affordances stayed")
	}
	// The state helper agrees with the page.
	if st := e.s.aiStateOf(t.Context(), ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}, e.pid); st.State != "off" {
		t.Errorf("aiStateOf: %+v", st)
	}
}

// Fixtures for the template lint and the render tests: the writing view in each AI state, and the AI project page.
func writeAIFixture(role, state, mode string) writeData {
	d := writeFixture(fixtureProjects()[0], role, true)
	d.AI = aiState{State: state, Mode: mode}
	return d
}

func aiProjectFixture(owner, edit bool, state string) aiProjectData {
	p := ownerFixture(fixtureProjects()[0])
	return aiProjectData{Project: p, Slug: p.Slug, CSRF: "tok_writer", IsOwner: owner, CanEdit: edit, State: aiState{State: state}}
}

// The page shows the policy controls exactly when the op would allow the change: the project's owner, and an instance
// administrator (who acts as the owner through the ops); an editor sees them read-only.
func TestAuthzAIPolicyControlsFollowTheOp(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	s.addMember(t, e.pid, admin.userID, ops.ProjectEditor)
	s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	path := "/p/" + e.slug + "/ai"
	for _, c := range []struct {
		who   caller
		owner string
		want  int
	}{{e.owner, "true", http.StatusOK}, {admin, "true", http.StatusOK}, {editor, "false", http.StatusForbidden}} {
		body := s.do(c.who, "GET", path, "").Body.String()
		if !strings.Contains(body, `data-owner="`+c.owner+`"`) {
			t.Errorf("%s: the page does not say data-owner=%s", c.who.name, c.owner)
		}
		rec := s.do(c.who, "PUT", "/api/v1/projects/"+e.pid+"/ai/policy", `{"mode":"ask"}`)
		if rec.Code != c.want && !(c.want == http.StatusForbidden && rec.Code == http.StatusNotFound) {
			t.Errorf("%s: the op answered %d, and the page shows owner=%s", c.who.name, rec.Code, c.owner)
		}
	}
}

// A reviewer comments and suggests; the project's AI page is not one of their tabs, and the Review page does not offer it.
func TestReviewerIsNotOfferedTheAIPage(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	rv := s.newCaller(t, "reviewer", ops.RoleReader)
	s.addMember(t, e.pid, rv.userID, ops.ProjectReviewer)
	for _, page := range []string{"/review", "/people"} {
		rec := s.do(rv, "GET", "/p/"+e.slug+page, "")
		if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), `href="/p/`+e.slug+`/ai"`) {
			i := strings.Index(rec.Body.String(), `/ai"`)
			t.Errorf("reviewer on %s is offered the AI page: %q", page, rec.Body.String()[max(0, i-200):i+40])
		}
	}
	rec := s.do(e.owner, "GET", "/p/"+e.slug+"/review", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/p/`+e.slug+`/ai"`) {
		t.Errorf("owner on the Review page: %d, and the AI link must be there", rec.Code)
	}
}

// With the server's switch off the project's AI page is still the shell the script fills: the script draws the banner,
// the never-send list, the use and the kept chats from ops that send nothing, and every one of them answers. The writing
// view carries no AI affordance at all.
func TestAIProjectPageWithAIOff(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	if r := e.setAIPolicy(t, e.owner, "ask"); r.StatusCode != http.StatusOK {
		t.Fatalf("policy ask: %d", r.StatusCode)
	}
	if rec := s.do(admin, "PUT", "/api/v1/ai/settings", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("switch off: %d %s", rec.Code, rec.Body)
	}
	rec := s.do(e.owner, "GET", "/p/"+e.slug+"/ai", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("page: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`id="ai-project"`, `id="ai-root"`, `data-state="unavailable"`, `data-edit="true"`, `id="ai-announce" role="status" aria-live="polite"`, "js/aiadmin", "css/ai"} {
		if !strings.Contains(body, want) {
			t.Errorf("AI off: the page lacks %q", want)
		}
	}
	base := "/api/v1/projects/" + e.pid
	for _, p := range []string{"/api/v1/ai/status", base + "/ai/exclude", base + "/docs", base + "/ai/usage", "/api/v1/ai/usage", "/api/v1/ai/budget?project_id=" + e.pid, base + "/ai/runs?limit=15", base + "/ai/chats"} {
		if rec := s.do(e.owner, "GET", p, ""); rec.Code != http.StatusOK {
			t.Errorf("AI off: the page's %s answered %d %s", p, rec.Code, rec.Body)
		}
	}
	for _, p := range []string{base + "/ai/policy"} {
		if rec := s.do(e.owner, "GET", p, ""); rec.Code != http.StatusConflict {
			t.Errorf("AI off: %s answered %d, want 409 (the page does not ask for it)", p, rec.Code)
		}
	}
	_, w := e.writeBody(t, e.owner)
	for _, not := range []string{`id="itab-ai"`, `data-cmd="ai"`, "js/aiboot", "js/aichat", "data-ai-src", "data-aicore-src"} {
		if strings.Contains(w, not) {
			t.Errorf("AI off: the writing view carries %q", not)
		}
	}
}
