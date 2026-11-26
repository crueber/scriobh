package server

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// U1: the review's changes are plain POSTs when there is no script, and the import page's edits go through once.

// With no script, a change to a row's title is a plain POST that redirects back to the review, and a second POST with the revision it
// already used is refused in words on the page it lands on.
func TestTheNoScriptPostToAnUpdateRedirectsBackToTheReview(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One", "Two", "Three")
	_, page := s.getPage(t, w, "/import/d/"+id)
	key := regexp.MustCompile(`<li class="imp-node d0" data-kind="[a-z_]+" data-key="([^"]+)"`).FindStringSubmatch(page)
	rev := regexp.MustCompile(`name="rev" value="(\d+)"`).FindStringSubmatch(page)
	if key == nil || rev == nil {
		t.Fatalf("no row or revision on the review: %.600s", page)
	}
	vals := url.Values{"act": {"title"}, "key": {key[1]}, "edit": {key[1]}, "rev": {rev[1]}, "title": {"Renamed"}}
	rec := s.postForm(t, w, "/ui/import/d/"+id+"/update", vals)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.HasPrefix(loc, "/import/d/"+id+"?") || !strings.Contains(loc, "#imp-r-") {
		t.Fatalf("no-script change: %d %q", rec.Code, loc)
	}
	_, after := s.getPage(t, w, strings.SplitN(loc, "#", 2)[0])
	if !strings.Contains(after, `value="Renamed"`) || !strings.Contains(after, `id="imp-h"`) {
		t.Fatalf("the redirect does not show the review with the change: %.800s", after)
	}
	// The same revision again is not made: the review is drawn at once, with the words that say so.
	rec = s.postForm(t, w, "/ui/import/d/"+id+"/update", vals)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Your change was not made.") || !strings.Contains(rec.Body.String(), `id="imp-h"`) {
		t.Errorf("a stale no-script change: %d %.300s", rec.Code, rec.Body.String())
	}
}

// U1 3(a): the result of an import into a new project offers Undo, and the undo trashes what the import added.
func TestAnImportAsANewProjectOffersUndoOnItsResult(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "new.docx", testDocx(t, "Alpha", "Beta")}}, false)
	if rec.Code != 303 {
		t.Fatalf("staging: %d %.300s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	id := strings.TrimPrefix(loc, "/import/d/")
	_, page := s.getPage(t, w, loc)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)
	if tok == nil {
		t.Fatalf("no token: %.400s", page)
	}
	if rec := s.postForm(t, w, "/ui/import/d/"+id+"/apply", url.Values{"rev": {"1"}, "token": {tok[1]}, "title": {"Fresh Book"}}); rec.Code != 303 {
		t.Fatalf("apply: %d %.300s", rec.Code, rec.Body.String())
	}
	_, done := s.getPage(t, w, loc)
	if !strings.Contains(done, "Imported 2 documents") {
		t.Fatalf("the result: %.1200s", done)
	}
	m := regexp.MustCompile(`action="(/ui/projects/[^"]+/imports/src_[^"]+/undo)"`).FindStringSubmatch(done)
	if m == nil {
		t.Fatalf("a new project's result offers no undo: %.1500s", done)
	}
	if !strings.Contains(done, "Undo this import") {
		t.Errorf("the undo is not named as an undo: %.1500s", done)
	}
	rec = s.postForm(t, w, m[1], url.Values{})
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "&undone=2#imp-undone") {
		t.Fatalf("undo: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

// U1 3(c): a new project's import is warned of an earlier copy of the same file in a project the caller can read, and of no other: a
// second user, who cannot read that project, is told nothing about it.
func TestTheDuplicateWarningOnANewProjectNamesOnlyAProjectTheCallerCanRead(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)
	out, err := s.Reg.Call(t.Context(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Quarry Tales", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	file := []stagePart{{"file", "tales.docx", testDocx(t, "Gamma", "Delta")}}
	fields := [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}

	// The writer adds the file to Quarry Tales, then starts it again as a new project.
	rec := s.stageForm(t, w, "?into="+pr.Slug, fields, file, false)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 {
		t.Fatalf("staging into: %d %.300s", rec.Code, rec.Body.String())
	}
	_, page := s.getPage(t, w, loc)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	if rec := s.postForm(t, w, "/ui/import/d/"+strings.TrimPrefix(loc, "/p/"+pr.Slug+"/import/d/")+"/apply", url.Values{"rev": {"1"}, "token": {tok}, "slug": {pr.Slug}}); rec.Code != 303 {
		t.Fatalf("apply: %d %.300s", rec.Code, rec.Body.String())
	}

	rec = s.stageForm(t, w, "", fields, file, false)
	if rec.Code != 303 {
		t.Fatalf("new-project staging: %d %.300s", rec.Code, rec.Body.String())
	}
	_, again := s.getPage(t, w, rec.Header().Get("Location"))
	if !strings.Contains(again, `id="imp-already"`) || !strings.Contains(again, "Quarry Tales") || !strings.Contains(again, `href="/p/`+pr.Slug+`/import#imp-sources"`) {
		t.Fatalf("the writer is not warned of the earlier import into a project of theirs: %.1000s", again)
	}

	// The second user cannot read Quarry Tales: the same file is not flagged, and the project is neither named nor linked.
	rec = s.stageForm(t, other, "", fields, file, false)
	if rec.Code != 303 {
		t.Fatalf("the second user's staging: %d %.300s", rec.Code, rec.Body.String())
	}
	_, theirs := s.getPage(t, other, rec.Header().Get("Location"))
	if strings.Contains(theirs, `id="imp-already"`) || strings.Contains(theirs, "Quarry Tales") || strings.Contains(theirs, pr.Slug) {
		t.Fatalf("a stranger is told of a project they cannot read: %.1200s", theirs)
	}
}

// U1 3(b): a finished import is removed from the list, not discarded: its button says so, and its help says the project stays. A draft
// still being reviewed keeps Discard.
func TestAFinishedImportIsRemovedFromTheListAndAnOpenOneIsDiscarded(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	open := s.stageDocx(t, w, "open.docx", "Open One")
	done := s.stageDocx(t, w, "done.docx", "Done One", "Done Two")
	_, page := s.getPage(t, w, "/import/d/"+done)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)
	if tok == nil {
		t.Fatalf("no token: %.300s", page)
	}
	if rec := s.postForm(t, w, "/ui/import/d/"+done+"/apply", url.Values{"rev": {"1"}, "token": {tok[1]}, "title": {"Kept Book"}}); rec.Code != 303 {
		t.Fatalf("apply: %d %.300s", rec.Code, rec.Body.String())
	}
	code, list := s.getPage(t, w, "/import")
	if code != 200 {
		t.Fatalf("the chooser: %d", code)
	}
	if !strings.Contains(list, ">Remove from this list</button>") || !strings.Contains(list, `aria-describedby="imp-done-`+done+`"`) ||
		!strings.Contains(list, "The project and its documents stay as they are.") {
		t.Errorf("a finished import is not offered as a removal with its help: %.1500s", list)
	}
	if !strings.Contains(list, ">Discard this import</button>") || !strings.Contains(list, "/ui/import/d/"+open+"/discard") {
		t.Errorf("an open draft lost its Discard: %.800s", list)
	}
	own := regexp.MustCompile(`(?s)<form method="post" action="/ui/import/d/` + done + `/discard">.*?</form>`).FindString(list)
	if own == "" || strings.Contains(own, "Discard this import") {
		t.Errorf("a finished import still says Discard: %.400s", own)
	}
}

// U1 2 with no script: Enter in Project title is an implicit submission of the apply form, which a browser sends with its first submit
// button in tree order. That button saves the project fields and imports nothing.
func TestTheProjectTitleEnterWithNoScriptSavesAndCreatesNoProject(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "enter.docx", testDocx(t, "Epsilon", "Zeta")}}, false)
	if rec.Code != 303 {
		t.Fatalf("staging: %d %.300s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	_, page := s.getPage(t, w, loc)
	btn := regexp.MustCompile(`<button([^>]*name="act"[^>]*value="project"[^>]*)>`).FindStringSubmatch(page)
	if btn == nil || !strings.Contains(btn[1], `form="imp-applyform"`) {
		t.Fatalf("no save button for the apply form: %.300s", page)
	}
	// It must be the first submit button of that form in tree order, the one a browser presses for Enter.
	if strings.Index(page, `name="act" value="project"`) > strings.Index(page, "data-go") {
		t.Fatal("the save button comes after the Import button in tree order")
	}
	action := regexp.MustCompile(`formaction="([^"]+)"`).FindStringSubmatch(btn[1])
	token := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)
	rev := regexp.MustCompile(`name="rev" value="(\d+)"`).FindStringSubmatch(page)
	rows := regexp.MustCompile(`name="rows" value="(\d*)"`).FindStringSubmatch(page)
	kind := regexp.MustCompile(`<option value="([^"]+)" selected`).FindStringSubmatch(page)
	if action == nil || token == nil || rev == nil || rows == nil || kind == nil {
		t.Fatalf("the form's fields are not all there: %v %v %v %v", action, token != nil, rev, rows)
	}
	// The fields a browser sends for an implicit submission: the apply form's own hidden fields, its token, the associated fields and the button.
	vals := url.Values{"_csrf": {s.Sess.CSRFToken(sessionOf(t, s, w))}, "rev": {rev[1]}, "rows": {rows[1]}, "token": {token[1]},
		"title": {"Typed Title"}, "kind": {kind[1]}, "author": {""}, "act": {"project"}}
	rec = s.postForm(t, w, strings.ReplaceAll(action[1], "&amp;", "&"), vals)
	if rec.Code != 303 || rec.Header().Get("Location") != loc {
		t.Fatalf("the save did not return to the review: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	out, err := s.Reg.Call(t.Context(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.list", projects.ListIn{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range out.(projects.ListOut).Items {
		if p.Title == "Typed Title" {
			t.Fatal("Enter in the title made a project")
		}
	}
	if _, p := s.getPage(t, w, loc); !strings.Contains(p, `value="Typed Title"`) {
		t.Errorf("the draft does not keep the title: %.300s", p)
	}
}
