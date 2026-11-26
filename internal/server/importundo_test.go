package server

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// Phase 4: a file the project has had is flagged on the review, and an import into a project can be taken back from the page of its
// result and from the project's import page, which names what was moved and what stayed.
func TestAnImportIntoAProjectIsFlaggedWhenRepeatedAndCanBeTakenBack(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	out, err := s.Reg.Call(t.Context(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Existing", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	stage := func() (string, string) {
		rec := s.stageForm(t, w, "?into="+pr.Slug, [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "more.docx", testDocx(t, "Extra One", "Extra Two")}}, false)
		loc := rec.Header().Get("Location")
		if rec.Code != 303 {
			t.Fatalf("staging: %d %s", rec.Code, rec.Body.String())
		}
		return loc, strings.TrimPrefix(loc, "/p/"+pr.Slug+"/import/d/")
	}
	apply := func(loc, id string) {
		_, page := s.getPage(t, w, loc)
		tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)[1]
		if rec := s.postForm(t, w, "/ui/import/d/"+id+"/apply", url.Values{"rev": {"1"}, "token": {tok}, "slug": {pr.Slug}}); rec.Code != 303 {
			t.Fatalf("apply: %d %.300s", rec.Code, rec.Body.String())
		}
	}
	loc, id := stage()
	_, page := s.getPage(t, w, loc)
	if strings.Contains(page, "imp-already") {
		t.Fatalf("a first import is flagged: %.300s", page)
	}
	apply(loc, id)

	// The result offers the undo, in a disclosure with the words and the one button; nothing is trashed by a press on the summary.
	_, done := s.getPage(t, w, loc)
	if !strings.Contains(done, "Imported 2 documents") || !strings.Contains(done, `class="imp-undo-d"`) || !strings.Contains(done, "Move 2 documents to the trash") {
		t.Fatalf("the result: %.1500s", done)
	}
	if !strings.Contains(done, "2 of 2 documents are in the project.") || !strings.Contains(done, "Undoing it would move all 2 to the trash.") {
		t.Errorf("the result does not say what an undo would do: %.1500s", done)
	}
	if !strings.Contains(done, `<summary aria-describedby="imp-ss-`) || strings.Contains(done, "<summary aria-label") {
		t.Errorf("the summary's name is its visible text: %.800s", done)
	}

	// The same file again: the review says so, with the date, and links to the earlier import.
	loc2, _ := stage()
	_, again := s.getPage(t, w, loc2)
	if !strings.Contains(again, `id="imp-already"`) || !strings.Contains(again, "was already imported into this project on") || !strings.Contains(again, "2 of its documents are still here") ||
		!strings.Contains(again, `href="/p/`+pr.Slug+`/import#imp-sources"`) {
		t.Errorf("the repeat is not flagged: %.1200s", again)
	}
	// It is a warning and no refusal: the import button is there.
	if !strings.Contains(again, "data-go") || strings.Contains(again, "data-go disabled") {
		t.Errorf("the repeat cannot be imported: %.300s", again)
	}

	// The project's import page lists it, with the same offer.
	code, hist := s.getPage(t, w, "/p/"+pr.Slug+"/import")
	if code != 200 || !strings.Contains(hist, `id="imp-sources"`) || !strings.Contains(hist, "Earlier imports") || !strings.Contains(hist, `<span class="rg-url">more</span>`) || !strings.Contains(hist, "Move 2 documents to the trash") {
		t.Fatalf("the history: %d %.1800s", code, hist[max(0, strings.Index(hist, `id="imp-sources"`)-50):])
	}
	m := regexp.MustCompile(`action="(/ui/projects/[^"]+/imports/src_[^"]+/undo)"`).FindStringSubmatch(hist)
	if m == nil {
		t.Fatalf("no undo form: %.800s", hist)
	}
	// Another person cannot take it back, and a form without the token does nothing.
	other := s.newCaller(t, "other", ops.RoleWriter)
	if rec := s.postForm(t, other, m[1], url.Values{}); rec.Code != 404 {
		t.Errorf("a stranger's undo: %d", rec.Code)
	}
	if rec := s.postForm(t, w, m[1], url.Values{"_csrf": {"nope"}}); rec.Code < 400 {
		t.Errorf("an undo without the token: %d", rec.Code)
	}
	rec := s.postForm(t, w, m[1], url.Values{})
	if loc := rec.Header().Get("Location"); rec.Code != 303 || !strings.HasPrefix(loc, "/p/"+pr.Slug+"/import?kept=0&src=src_") || !strings.HasSuffix(loc, "&undone=2#imp-undone") {
		t.Fatalf("undo: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	_, after := s.getPage(t, w, strings.TrimSuffix(rec.Header().Get("Location"), "#imp-undone"))
	if !strings.Contains(after, `id="imp-undone"`) || !strings.Contains(after, "Moved all 2 documents of that import to the trash.") || !strings.Contains(after, `role="status"`) ||
		!strings.Contains(after, "Nothing of it is left in the project.") || strings.Contains(after, `class="imp-undo-d"`) {
		t.Errorf("after the undo: %.1800s", after)
	}
	// A made-up count in the address is not believed.
	if _, p := s.getPage(t, w, "/p/"+pr.Slug+"/import?undone=zz&kept=1"); strings.Contains(p, `id="imp-undone"`) {
		t.Error("a bad count was drawn")
	}
	// With nothing of it left in the project, the same file is no repeat.
	loc3, _ := stage()
	if _, p := s.getPage(t, w, loc3); strings.Contains(p, `id="imp-already"`) {
		t.Errorf("flagged with nothing left: %.300s", p)
	}
}

// A plain text file is chosen on the chooser, staged, and reviewed like a Word file: how it was cut, the places a chapter could start with
// "Start a chapter here", and the page says so in words.
func TestAPlainTextFileHasTheReviewOfAWordFile(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	_, chooser := s.getPage(t, w, "/import")
	if !strings.Contains(chooser, `id="imp-f-text"`) || !strings.Contains(chooser, `data-fmt="text"`) || !strings.Contains(chooser, `accept=".txt,.text,text/plain"`) {
		t.Fatalf("the chooser has no text panel: %.200s", chooser)
	}
	var b strings.Builder
	for _, l := range []string{"Chapter 1", "Chapter 2", "Chapter 3"} {
		b.WriteString(l + "\n\n" + strings.Repeat("The tide came in over the grey stones and went out again.\n\n", 4))
	}
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "text"}, {"kind", "text"}, {"panel", "text"}}, []stagePart{{"file", "book.txt", []byte(b.String())}}, false)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.HasPrefix(loc, "/import/d/imd_") {
		t.Fatalf("staging a text file: %d %s %.200s", rec.Code, loc, rec.Body.String())
	}
	_, page := s.getPage(t, w, loc)
	if !strings.Contains(page, "A plain text file") || !strings.Contains(page, "Start a chapter here") || !strings.Contains(page, `id="imp-cut`) {
		t.Fatalf("the review: %.2500s", page[max(0, strings.Index(page, `id="imp"`)):])
	}
	// a file that is not text is refused in words, and nothing is kept
	rec = s.stageForm(t, w, "", [][2]string{{"fmt", "text"}, {"kind", "text"}, {"panel", "text"}}, []stagePart{{"file", "x.txt", []byte("PK\x03\x04 not text")}}, false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "That is not a plain text file") {
		t.Errorf("a zip as text: %d %.300s", rec.Code, rec.Body.String())
	}
}

// After an undo the page says which documents stayed and why, in words, in the status that takes focus, and the history lists them too;
// and the would-undo summary says the same before anything is pressed.
func TestTheUndoSaysWhichDocumentsStayedAndWhy(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	out, err := s.Reg.Call(t.Context(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Existing", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	rec := s.stageForm(t, w, "?into="+pr.Slug, [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "more.docx", testDocx(t, "Extra One", "Extra Two", "Extra Three")}}, false)
	loc := rec.Header().Get("Location")
	id := strings.TrimPrefix(loc, "/p/"+pr.Slug+"/import/d/")
	_, page := s.getPage(t, w, loc)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	if rec := s.postForm(t, w, "/ui/import/d/"+id+"/apply", url.Values{"rev": {"1"}, "token": {tok}, "slug": {pr.Slug}}); rec.Code != 303 {
		t.Fatalf("apply: %d", rec.Code)
	}
	// the writer moves one chapter to the end of the list: not a save, so not a revision
	var list struct {
		Items []struct{ ID, Title string }
	}
	_ = json.Unmarshal(s.do(w, "GET", "/api/v1/projects/"+pr.ID+"/docs", "").Body.Bytes(), &list)
	var first, last string
	for _, it := range list.Items {
		if it.Title == "Extra One" {
			first = it.ID
		}
		if it.Title == "Extra Three" {
			last = it.ID
		}
	}
	if rec := s.do(w, "POST", "/api/v1/docs/"+first+"/move", `{"parent_id":"","after":"`+last+`"}`); rec.Code >= 300 {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	const placed = "it was moved, put in another order, linked to an entity or marked after the import"
	_, hist := s.getPage(t, w, "/p/"+pr.Slug+"/import")
	for _, want := range []string{"Undoing it would move 2 to the trash and keep 1, listed below with the reason.", `class="imp-kept"`, "Extra One", placed} {
		if !strings.Contains(hist, want) {
			t.Errorf("the summary before the undo lacks %q", want)
		}
	}
	m := regexp.MustCompile(`action="(/ui/projects/[^"]+/imports/src_[^"]+/undo)"`).FindStringSubmatch(hist)
	rec = s.postForm(t, w, m[1], url.Values{})
	if rec.Code != 303 {
		t.Fatalf("undo: %d", rec.Code)
	}
	_, after := s.getPage(t, w, strings.TrimSuffix(rec.Header().Get("Location"), "#imp-undone"))
	i := strings.Index(after, `id="imp-undone"`)
	if i < 0 {
		t.Fatalf("no status after the undo")
	}
	block := after[i : i+strings.Index(after[i:], "</div>")]
	for _, want := range []string{"Moved 2 documents to the trash. 1 stayed; each is listed below with the reason.", "Extra One", placed} {
		if !strings.Contains(block, want) {
			t.Errorf("the status lacks %q:\n%s", want, block)
		}
	}
	head := after[max(0, i-120):i]
	if !strings.Contains(head+after[i:i+200], `role="status"`) || !strings.Contains(after[i:i+200], "data-focus") {
		t.Errorf("the status role or focus is gone: %.300s", after[i-60:i+260])
	}
	// a made-up source in the address is not looked up: only the project's own are
	if _, p := s.getPage(t, w, "/p/"+pr.Slug+"/import?undone=1&kept=1&src=src_nope"); strings.Contains(p, `id="imp-undone"`) && strings.Contains(p[strings.Index(p, `id="imp-undone"`):strings.Index(p, `id="imp-undone"`)+400], "imp-kept-h") {
		t.Error("a source that is not the project's listed documents")
	}
}

// A draft with no name is called for what it is, in the page's words; a text file that has no text is refused in the catalogue's words.
func TestNoRawKindNameAndTheTextRefusalsAreInTheCatalogue(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "text"}, {"kind", "text"}, {"panel", "text"}}, []stagePart{{"file", "x.txt", []byte("\n   \n\n")}}, false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "That text file has no text to import.") {
		t.Errorf("the refusal of an empty text file: %d %.300s", rec.Code, rec.Body.String())
	}
	rec = s.stageForm(t, w, "", [][2]string{{"fmt", "text"}, {"kind", "text"}, {"panel", "text"}}, []stagePart{{"file", ".txt", []byte("Some words, in a file with no name.\n")}}, false)
	if rec.Code != 303 {
		t.Fatalf("staging a file with no name: %d", rec.Code)
	}
	_, chooser := s.getPage(t, w, "/import")
	if strings.Contains(chooser, "Being reviewed (text)") || !strings.Contains(chooser, "Being reviewed (A plain text file)") {
		t.Errorf("the chooser names the draft's kind by its raw name: %.800s", chooser[max(0, strings.Index(chooser, "imp-drafts")):])
	}
}
