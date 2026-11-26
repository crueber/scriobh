package server

import (
	"bytes"
	"context"
	"database/sql"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

func (s *Server) htmxPost(t *testing.T, c caller, path string, vals url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-CSRF-Token", s.Sess.CSRFToken(sessionOf(t, s, c)))
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func (s *Server) makeProject(t *testing.T, w caller, title string) projects.Project {
	t.Helper()
	out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: title, Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	return out.(projects.Item).Item
}

// A review that adds to a project stays one through every fragment and form: the row, the rest of the rows, the progress poll and the update
// are drawn from the draft's own project, and the apply that follows an edit is an addition, not a new project.
func TestAnAdditionStaysAnAdditionThroughARowAnUpdateAndTheApply(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	pr := s.makeProject(t, w, "Existing")
	rec := s.stageForm(t, w, "?into="+pr.Slug, [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "more.docx", testDocx(t, "Extra One", "Extra Two")}}, false)
	loc := rec.Header().Get("Location")
	id := strings.TrimPrefix(loc, "/p/"+pr.Slug+"/import/d/")
	_, page := s.getPage(t, w, loc)
	key := regexp.MustCompile(`data-key="([^"]+)"`).FindStringSubmatch(page)[1]
	row := s.do(w, "GET", "/ui/import/d/"+id+"/row?key="+url.QueryEscape(key), "")
	if row.Code != 200 || strings.Contains(row.Body.String(), `name="slug"`) {
		t.Fatalf("the row: %d", row.Code)
	}
	if prog := s.do(w, "GET", "/ui/import/d/"+id+"/progress", ""); !strings.Contains(prog.Body.String(), "What would be added") || strings.Contains(prog.Body.String(), `id="imp-t"`) {
		t.Errorf("the progress fragment of an addition: %.200s", prog.Body.String())
	}
	if more := s.do(w, "GET", "/ui/import/d/"+id+"/more?after=0", ""); more.Code != 200 {
		t.Errorf("more: %d", more.Code)
	}
	// the rename the panel posts carries no slug and no hint of the project
	up := s.htmxPost(t, w, "/ui/import/d/"+id+"/update", url.Values{"rev": {revOf(t, row.Body.String())}, "key": {key}, "act": {"title"}, "title": {"Renamed"}, "edit": {key}})
	body := up.Body.String()
	if up.Code != 200 || !strings.Contains(body, "What would be added") || strings.Contains(body, `id="imp-t"`) || !strings.Contains(body, "Renamed") {
		t.Fatalf("the update of an addition: %d %.300s", up.Code, body)
	}
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(body)[1]
	ap := s.postForm(t, w, "/ui/import/d/"+id+"/apply", url.Values{"rev": {revOf(t, body)}, "token": {tok}})
	if ap.Code != 303 || !strings.HasPrefix(ap.Header().Get("Location"), "/p/"+pr.Slug+"/d/doc_") {
		t.Fatalf("the apply after an edit: %d %s", ap.Code, ap.Header().Get("Location"))
	}
	lo, err := s.Reg.Call(context.Background(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "doc.list", docs.ListIn{ProjectID: pr.ID})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, m := range lo.(docs.ListOut).Items {
		titles = append(titles, m.Title)
	}
	if !strings.Contains(strings.Join(titles, "|"), "Renamed") {
		t.Errorf("the project's documents: %v", titles)
	}
	// the result page names the project's pages, and a draft is not read from a slug that is not its own
	if _, done := s.getPage(t, w, "/p/"+pr.Slug+"/import/d/"+id); !strings.Contains(done, `href="/p/`+pr.Slug+`/d/doc_`) {
		t.Errorf("the result of an addition has no link to the book")
	}
	other := s.makeProject(t, w, "Other")
	if code, _ := s.getPage(t, w, "/p/"+other.Slug+"/import/d/"+id); code != 303 && code != 404 {
		t.Errorf("a draft under another project's address: %d", code)
	}
	// a form that names another project cannot turn the apply into one for it: the slug field is not read
	id2 := strings.TrimPrefix(s.stageForm(t, w, "?into="+pr.Slug, [][2]string{{"kind", "docx"}}, []stagePart{{"file", "x.docx", testDocx(t, "Third")}}, false).Header().Get("Location"), "/p/"+pr.Slug+"/import/d/")
	_, p2 := s.getPage(t, w, "/p/"+pr.Slug+"/import/d/"+id2)
	tok2 := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(p2)[1]
	ap2 := s.postForm(t, w, "/ui/import/d/"+id2+"/apply", url.Values{"rev": {revOf(t, p2)}, "token": {tok2}, "slug": {other.Slug}})
	if !strings.HasPrefix(ap2.Header().Get("Location"), "/p/"+pr.Slug+"/") {
		t.Errorf("a posted slug redirected the apply: %s", ap2.Header().Get("Location"))
	}
}

// A form dropped half-way leaves nothing in data/tmp, and a file a dead request left there is swept once it is old.
func TestAStageFormThatDiesLeavesNothingInTmp(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("_csrf", s.Sess.CSRFToken(sessionOf(t, s, w)))
	_ = mw.WriteField("kind", "docx")
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="a.docx"`)
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write(testDocx(t, "One"))
	_ = mw.WriteField("panel", "docx")
	raw := body.Bytes()
	raw = raw[:bytes.LastIndex(raw, []byte(`Content-Disposition: form-data; name="panel"`))+12] // a second part, cut off
	req := httptest.NewRequest("POST", "/ui/import/stage", bytes.NewReader(raw))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(w.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == 303 || rec.Code == 204 {
		t.Fatalf("a cut form was staged: %d", rec.Code)
	}
	ents, _ := os.ReadDir(filepath.Join(s.Cfg.DataDir, "tmp"))
	for _, e := range ents {
		t.Errorf("left in data/tmp: %s", e.Name())
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM import_drafts`).Scan(&n)
	if n != 0 {
		t.Errorf("%d drafts", n)
	}
	// the sweep
	dir := filepath.Join(s.Cfg.DataDir, "tmp")
	_ = os.MkdirAll(dir, 0o700)
	old, young, other := filepath.Join(dir, "upload-old"), filepath.Join(dir, "folder-young"), filepath.Join(dir, "up-attachment")
	for _, f := range []string{old, young, other} {
		_ = os.WriteFile(f, []byte("x"), 0o600)
	}
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, past, past)
	_ = os.Chtimes(other, past, past)
	if _, err := imports.SweepDrafts(context.Background(), s.DB, s.Cfg.DataDir, imports.StageLimits{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("an old upload file stayed")
	}
	if _, err := os.Stat(young); err != nil {
		t.Error("a young one (maybe in use) was removed")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a file that is not the stage form's was removed")
	}
}

// The room is looked at before the bytes: a declared size past the share is refused without a file, and a stream of no declared size is cut at the
// room and refused as a quota, not as too large.
func TestQuotaIsCheckedBeforeTheBytesAreKept(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	big := bytes.Repeat([]byte("PK\x03\x04"), (3<<20)/4)
	// The server's quota is the default (256 MiB): shrink it by holding drafts of a size the quota counts.
	if _, err := s.DB.Writer().Exec(`INSERT INTO import_drafts(id, owner_id, source_kind, kind, name, bytes, status, created_at, updated_at) VALUES ('imd_01HOLDHOLDHOLDHOLDHOLDHOLD', ?, 'upload', 'docx', 'held', ?, 'open', ?, ?)`,
		w.userID, int64(255<<20)+(512<<10), db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/imports/stage?kind=docx", bytes.NewReader(big))
	req.AddCookie(w.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "user_quota") {
		t.Errorf("a declared size past the share: %d %.200s", rec.Code, rec.Body.String())
	}
	if ents, _ := os.ReadDir(filepath.Join(s.Cfg.DataDir, "imports", "stage")); len(ents) != 0 {
		t.Errorf("%d staged folders were made", len(ents))
	}
}

// A draft the apply of which committed, and one that did not, are both put right by the sweep after a stop.
func TestApplyingLimboIsResolvedByTheSweep(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	ctx := context.Background()
	p := ops.Principal{UserID: w.userID, Role: ops.RoleWriter}
	stage := func(name string) string {
		return s.stageDocx(t, w, name+".docx", name, name+" two")
	}
	// committed: a real apply, then the row put back to "applying" as a stop between the commit and the mark would leave it
	a := stage("Committed")
	_, page := s.getPage(t, w, "/import/d/"+a)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	ap := s.postForm(t, w, "/ui/import/d/"+a+"/apply", url.Values{"rev": {revOf(t, page)}, "token": {tok}})
	if ap.Code != 303 {
		t.Fatalf("apply: %d", ap.Code)
	}
	if _, err := s.DB.Writer().Exec(`UPDATE import_drafts SET status = 'applying', result_json = '{}', updated_at = ? WHERE id = ?`, db.Format(time.Now().Add(-time.Hour)), a); err != nil {
		t.Fatal(err)
	}
	// not committed: a draft claimed and then lost
	b := stage("Never")
	if _, err := s.DB.Writer().Exec(`UPDATE import_drafts SET status = 'applying', updated_at = ? WHERE id = ?`, db.Format(time.Now().Add(-time.Hour)), b); err != nil {
		t.Fatal(err)
	}
	// one claimed a moment ago is an apply in progress: left alone
	c := stage("Running")
	if _, err := s.DB.Writer().Exec(`UPDATE import_drafts SET status = 'applying', updated_at = ? WHERE id = ?`, db.Now(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := imports.SweepDrafts(ctx, s.DB, s.Cfg.DataDir, imports.StageLimits{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	status := func(id string) (st, res string) {
		_ = s.DB.Read().QueryRow(`SELECT status, result_json FROM import_drafts WHERE id = ?`, id).Scan(&st, &res)
		return
	}
	if st, res := status(a); st != "done" || !strings.Contains(res, `"project_id":"prj_`) || !strings.Contains(res, `"slug":"committed"`) {
		t.Errorf("the committed apply: %s %s", st, res)
	}
	if _, err := os.Stat(imports.StageDir(s.Cfg.DataDir, a)); err == nil {
		t.Error("the staged file of a committed apply stayed")
	}
	if st, _ := status(b); st != "open" {
		t.Errorf("the apply that never committed: %s", st)
	}
	if _, err := os.Stat(imports.StageDir(s.Cfg.DataDir, b)); err != nil {
		t.Error("its file went")
	}
	if st, _ := status(c); st != "applying" {
		t.Errorf("an apply in progress was touched: %s", st)
	}
	_ = p
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM projects WHERE title = 'Committed'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d projects", n)
	}
	_ = sql.ErrNoRows
}

// The same bytes with other options are another draft; the same bytes with the same options are the one open.
func TestTheSameBytesWithOtherOptionsAreAnotherDraft(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	stage := func(headings string) string {
		rec := s.stageForm(t, w, "", [][2]string{{"kind", "docx"}, {"opt.docx_headings", headings}}, []stagePart{{"file", "a.docx", testDocx(t, "One", "Two")}}, false)
		return strings.TrimPrefix(rec.Header().Get("Location"), "/import/d/")
	}
	a, b, c := stage("chapters"), stage("parts"), stage("chapters")
	if a == b {
		t.Error("another choice of options reused the draft")
	}
	if a != c {
		t.Error("the same choice made a second draft")
	}
}

// A draft for a project its owner can no longer reach is listed, flagged, and can be discarded; it does not sit hidden against the open limit.
func TestADraftOfAnUnreachableProjectIsListedAndCanBeDiscarded(t *testing.T) {
	s := newAuthzServer(t)
	own := s.newCaller(t, "own", ops.RoleWriter)
	ed := s.newCaller(t, "ed", ops.RoleWriter)
	pr := s.makeProject(t, own, "P")
	s.addMember(t, pr.ID, ed.userID, ops.ProjectEditor)
	rec := s.stageForm(t, ed, "?into="+pr.Slug, [][2]string{{"kind", "docx"}}, []stagePart{{"file", "a.docx", testDocx(t, "One")}}, false)
	id := strings.TrimPrefix(rec.Header().Get("Location"), "/p/"+pr.Slug+"/import/d/")
	if err := s.DB.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM project_members WHERE project_id=? AND user_id=?`, pr.ID, ed.userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if code, _ := s.getPage(t, ed, "/import/d/"+id); code != 404 {
		t.Errorf("the page of a draft whose project is out of reach: %d", code)
	}
	_, o := restJSON(t, s, ed, "GET", "/api/v1/imports/drafts", "")
	items, _ := o["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["unreachable"] != true {
		t.Fatalf("the list: %v", o)
	}
	if _, chooser := s.getPage(t, ed, "/import"); !strings.Contains(chooser, "not open to you any more") || !strings.Contains(chooser, "/ui/import/d/"+id+"/discard") || strings.Contains(chooser, `href="/import/d/`+id+`"`) {
		t.Error("the chooser does not list it for discarding only")
	}
	if rec := s.postForm(t, ed, "/ui/import/d/"+id+"/discard", url.Values{}); rec.Code != 303 {
		t.Errorf("discard: %d", rec.Code)
	}
	if _, o := restJSON(t, s, ed, "GET", "/api/v1/imports/drafts", ""); len(o["items"].([]any)) != 0 {
		t.Error("it is still listed")
	}
}

// A project that locks after a file was staged for it: the draft is not read, changed or applied by a session that has not unlocked it.
func TestAProjectThatLocksAfterStagingHidesItsDraftFromALockedSession(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	req := httptest.NewRequest("POST", "/api/v1/imports/stage?kind=docx&project="+w.pid, bytes.NewReader(testDocx(t, "One")))
	req.AddCookie(w.owner.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("stage: %d %.200s", rec.Code, rec.Body.String())
	}
	id := regexp.MustCompile(`imd_[A-Za-z0-9]+`).FindString(rec.Body.String())
	for _, c := range []struct{ m, p, b string }{
		{"GET", "/api/v1/imports/drafts/" + id + "?preview=true", ""},
		{"POST", "/api/v1/imports/drafts/" + id, `{"base_revision":1,"undo":true}`},
		{"POST", "/api/v1/imports/preview", `{"stage_id":"` + id + `"}`},
		{"POST", "/api/v1/projects/" + w.pid + "/import", `{"stage_id":"` + id + `","rev":1,"token":"x"}`},
	} {
		r := s.do(w.owner2, c.m, c.p, c.b)
		if strings.Contains(c.p, "/projects/") { // a project's own op: the gate answers a locked session with its passphrase notice
			if r.Code != 403 || !strings.Contains(r.Body.String(), "passphrase") {
				t.Errorf("a locked session, %s %s: %d %.120s, want the passphrase refusal", c.m, c.p, r.Code, r.Body.String())
			}
			continue
		}
		if r.Code != 404 {
			t.Errorf("a locked session, %s %s: %d %.120s, want 404", c.m, c.p, r.Code, r.Body.String())
		}
	}
	if r := s.do(w.owner2, "GET", "/p/"+w.slug+"/import/d/"+id, ""); r.Code == 200 {
		t.Errorf("a locked session read the review page: %d", r.Code)
	}
	// listed as out of reach (so it can be discarded), never described
	if r := s.do(w.owner2, "GET", "/api/v1/imports/drafts", ""); !strings.Contains(r.Body.String(), `"unreachable":true`) || strings.Contains(r.Body.String(), "One") {
		t.Errorf("the locked session's list: %.300s", r.Body.String())
	}
}

// A reader that is busy (every place for a conversion is taken) is said in words at stage time, with nothing kept, and on a review that exists
// it is the review's own page with the way to try again, never the bare error page.
func TestABusyReaderIsSaidAtStageTimeAndOnAReview(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One", "Two")
	defer imports.LimitPlacesForTest(1, 30*time.Millisecond)()
	// another person's reader holds the only place
	release, err := imports.HoldPlaceForTest(other.userID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rec := s.stageForm(t, w, "", [][2]string{{"fmt", "docx"}, {"kind", "docx"}}, []stagePart{{"file", "b.docx", testDocx(t, "Other book")}}, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "busy") || !strings.Contains(rec.Body.String(), `id="imp-stage-err"`) || strings.Contains(rec.Body.String(), "Something went wrong") {
		t.Errorf("busy at stage time: %d %.300s", rec.Code, rec.Body.String())
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM import_drafts WHERE owner_id = ?`, w.userID).Scan(&n)
	if n != 1 {
		t.Errorf("%d drafts, want only the first", n)
	}
	page := s.do(w, "GET", "/import/d/"+id, "")
	b := page.Body.String()
	if page.Code != 429 || page.Header().Get("Retry-After") == "" || !strings.Contains(b, "busy") || !strings.Contains(b, "Try again") || !strings.Contains(b, "Discard this import") || strings.Contains(b, "Something went wrong") {
		t.Errorf("busy on a review: %d %.300s", page.Code, b)
	}
}

// Every reason an import op gives, and every sentence the page says for one, is in the catalogue.
func TestEveryImportReasonIsSaidFromTheCatalogue(t *testing.T) {
	for _, k := range []string{"not_docx", "not_zip", "bad_zip", "not_epub", "not_pdf", "not_export", "empty", "disk_full", "title_empty", "nothing_to_undo", "cannot_move",
		"open_drafts", "user_quota", "total_quota"} {
		if !i18n.Default.Has("import.err_" + k) {
			t.Errorf("import.err_%s is not in the catalogue", k)
		}
	}
	for _, k := range []string{"notice_revision", "notice_applying", "notice_done", "notice_gone", "stays", "no_join", "err_read", "too_big", "too_big_34", "pdf_busy", "busy", "err_busy_you",
		"page_title", "page_title_into", "page_title_git", "crumb_import", "crumb_review", "crumb_git", "unreachable", "retry"} {
		if !i18n.Default.Has("import." + k) {
			t.Errorf("import.%s is not in the catalogue", k)
		}
	}
}

// The page says an op's refusal in its own language, not the op's English.
func TestARefusalIsSaidInThePagesLanguage(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	if rec := s.do(w, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	rec := s.stageForm(t, w, "", [][2]string{{"kind", "docx"}}, []stagePart{{"file", "n.docx", []byte("only words")}}, false)
	body := rec.Body.String()
	if rec.Code != 422 || !strings.Contains(body, "⟦") || strings.Contains(body, "That is not a Word document (.docx). A .docx is a zip") {
		t.Errorf("the refusal in the pseudo-locale: %d %.400s", rec.Code, body)
	}
}

// Leave out and Join hand focus to the Put back button of what left, so the keyboard goes on from where the row was.
func TestFocusGoesToPutBackAfterLeaveOutAndJoin(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "One", "Two", "Three")
	_, page := s.getPage(t, w, "/import/d/"+id)
	var keys []string
	for _, m := range regexp.MustCompile(`<li class="imp-node d0" data-kind="[a-z_]+" data-key="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		keys = append(keys, m[1])
	}
	focused := regexp.MustCompile(`id="imp-c-r[0-9a-f]+-back"[^>]* data-focus`)
	rec := s.update(t, w, id, true, url.Values{"rev": {revOf(t, page)}, "act": {"skip"}, "key": {keys[2]}})
	if !focused.MatchString(rec.Body.String()) {
		t.Errorf("after Leave out nothing is marked to take focus on Put back: %.200s", rec.Body.String())
	}
	rec = s.update(t, w, id, true, url.Values{"rev": {revOf(t, rec.Body.String())}, "act": {"merge"}, "key": {keys[1]}, "to": {keys[0]}})
	if !focused.MatchString(rec.Body.String()) {
		t.Errorf("after Join nothing is marked to take focus on Put back")
	}
}

// Nothing a person typed or a chapter's title goes in an address: a refused change on a draft is drawn as a page, not redirected with its words.
func TestARefusalWithNoScriptCarriesNoWordsInTheAddress(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	id := s.stageDocx(t, w, "a.docx", "Secret Title One", "Secret Title Two")
	_, page := s.getPage(t, w, "/import/d/"+id)
	var keys []string
	for _, m := range regexp.MustCompile(`<li class="imp-node d0" data-kind="[a-z_]+" data-key="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		keys = append(keys, m[1])
	}
	rev := revOf(t, page)
	r1 := s.update(t, w, id, true, url.Values{"rev": {rev}, "act": {"merge"}, "key": {keys[1]}, "to": {keys[0]}})
	rev = revOf(t, r1.Body.String())
	rec := s.update(t, w, id, false, url.Values{"rev": {rev}, "act": {"skip"}, "key": {keys[0]}}) // Leave out what holds a join: refused, with titles in its words
	if rec.Header().Get("Location") != "" || rec.Code != 422 || !strings.Contains(rec.Body.String(), "Secret Title") {
		t.Errorf("the refusal: %d location=%q", rec.Code, rec.Header().Get("Location"))
	}
}
