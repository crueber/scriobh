package server

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/editiondiff"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The Editions pages: anyone who can read the project reads the list, the download is the owners' and the editors', the
// person who imported is the owner's to know, a missing file is a sentence on a page, and another project's edition is
// not found.
func TestTheEditionsPagesFollowTheRoles(t *testing.T) {
	e := newWriteEnv(t)
	e.s.editionFixtures(t, e.pid)
	id := sweepChildren["edition_id"]
	if _, err := e.s.DB.Writer().ExecContext(t.Context(), `UPDATE editions SET imported_by = ? WHERE id = ?`, e.owner.userID, id); err != nil {
		t.Fatal(err)
	}
	editor := e.s.newCaller(t, "editor", ops.RoleWriter)
	reader := e.s.newCaller(t, "reader", ops.RoleWriter)
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	e.s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	e.s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	list, one, dl := "/p/"+e.slug+"/editions", "/p/"+e.slug+"/editions/"+id, "/p/"+e.slug+"/editions/"+id+"/download"

	if rec := e.s.do(caller{name: "anon"}, "GET", list, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous: %d", rec.Code)
	}
	// A session that has ended: the download is a redirect to sign-in (a page, not a file; a link with the download attribute
	// would save it, which is said in docs/p8-notes/desktop-go.md). Pinned so that the note stays true.
	if rec := e.s.do(caller{name: "anon"}, "GET", dl, ""); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/auth/login") {
		t.Errorf("anonymous download: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.s.do(stranger, "GET", list, ""); rec.Code != 404 {
		t.Errorf("a stranger finds the list: %d", rec.Code)
	}
	for _, c := range []caller{e.owner, editor, reader} {
		rec := e.s.do(c, "GET", list, "")
		body := rec.Body.String()
		// a reader's count leaves out a chapter that became no document at import (the op's rule)
		want := "<dd>1</dd>"
		if c.name == "reader" {
			want = "<dd>0</dd>"
		}
		if rec.Code != 200 || !strings.Contains(body, "Sweep edition") || !strings.Contains(body, want) {
			t.Errorf("%s: the list: %d", c.name, rec.Code)
		}
		canGet := c.name != "reader"
		if has := strings.Contains(body, "/download"); has != canGet {
			t.Errorf("%s: a download link is shown = %v, want %v", c.name, has, canGet)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: the list is cached", c.name)
		}
	}
	// who imported is the owner's to know
	if body := e.s.do(e.owner, "GET", one, "").Body.String(); !strings.Contains(body, "Imported by") {
		t.Error("the owner is not told who imported")
	}
	for _, c := range []caller{editor, reader} {
		if body := e.s.do(c, "GET", one, "").Body.String(); strings.Contains(body, "Imported by") {
			t.Errorf("%s is told who imported", c.name)
		}
	}
	// someone who left is said to have left only when the members were read; an editor-administrator is not told a name that is not theirs to know
	if _, err := e.s.DB.Writer().ExecContext(t.Context(), `UPDATE editions SET imported_by = ? WHERE id = ?`, stranger.userID, id); err != nil {
		t.Fatal(err)
	}
	if body := e.s.do(e.owner, "GET", one, "").Body.String(); !strings.Contains(body, "no longer on the project") {
		t.Error("an import by someone who left is not said so")
	}
	if _, err := e.s.DB.Writer().ExecContext(t.Context(), `UPDATE editions SET imported_by = ? WHERE id = ?`, e.owner.userID, id); err != nil {
		t.Fatal(err)
	}
	// the chapters, and what a chapter that has no document is called
	body := e.s.do(e.owner, "GET", one, "").Body.String()
	if !strings.Contains(body, "OEBPS/one.xhtml") || !strings.Contains(body, "Left out of the project") {
		t.Errorf("the chapter list: %.600s", body)
	}
	// the download: the file itself for an owner and an editor, a refusal for a reader, nothing for another project
	for _, c := range []caller{e.owner, editor} {
		rec := e.s.do(c, "GET", dl, "")
		if rec.Code != 200 || rec.Body.String() != "PK sweep edition, not a real book" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: the download: %d %q %v", c.name, rec.Code, rec.Body.String(), rec.Header())
		}
	}
	if rec := e.s.do(reader, "GET", dl, ""); rec.Code != 403 {
		t.Errorf("a reader downloaded: %d", rec.Code)
	}
	if rec := e.s.do(stranger, "GET", dl, ""); rec.Code != 404 {
		t.Errorf("a stranger downloaded: %d", rec.Code)
	}
	if rec := e.s.do(e.owner, "GET", "/p/"+e.slug+"/editions/edn_notmine", ""); rec.Code != 404 {
		t.Errorf("an edition that does not exist: %d", rec.Code)
	}
	// The file is gone. The pages look at the disk: no link (the download attribute would make a browser save whatever answers
	// it) and the sentence in its place, on the edition and in the list, on every reload. A download asked for anyway, by a page
	// drawn before the file went, is a 404 with the sentence as plain text and never a redirect to an HTML page.
	sum := editions.SumOf([]byte("PK sweep edition, not a real book"))
	p, _ := editions.Path(e.s.Cfg.DataDir, e.pid, sum, "epub")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	rec := e.s.do(e.owner, "GET", dl, "")
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || rec.Header().Get("Location") != "" ||
		!strings.Contains(rec.Body.String(), "not on this server") {
		t.Fatalf("a missing file: %d %q %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Location"), rec.Body.String())
	}
	for name, page := range map[string]string{"edition": one, "list": list} {
		body := e.s.do(e.owner, "GET", page, "").Body.String()
		if !strings.Contains(body, "not on this server") {
			t.Errorf("the %s page does not say the file is gone: %.400s", name, body)
		}
		if strings.Contains(body, "/download") {
			t.Errorf("the %s page still links to a file that is gone", name)
		}
		if !strings.Contains(body, "Sweep edition") {
			t.Errorf("the %s page lost the record", name)
		}
	}
}

func TestTheEditionsListSaysWhenThereAreNone(t *testing.T) {
	e := newWriteEnv(t)
	body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/editions", "").Body.String()
	if !strings.Contains(body, "No imported books yet") {
		t.Errorf("the empty state: %.400s", body)
	}
}

// editionsFixture is the Editions pages for the template lint and the structure checks: the list, an empty list, a reader's list, one edition.
func editionsFixture(kind string) editionsData {
	proj := ownerFixture(fixtureProjects()[0])
	d := editionsData{Project: proj, Slug: "the-salt-road", CSRF: "tok", CanGet: true, IsOwner: true, Max: editions.MaxPerProject, MaxFileMB: editions.MaxFileBytes >> 20}
	row := editionRow{Edition: editions.Edition{ID: "edn_1", Kind: "epub", Label: "First printing", Identifier: "urn:isbn:9780000000099", SHA256: "4cfca5a3555a0000000000000000000000000000000000000000000000000000", Size: 14000, HasFile: true, Chapters: 3},
		KindWord: "EPUB book", Source: "marsh", Short: "4cfca5a3555a", SizeText: "14 KB", When: "13 Nov 2026, 09:00", ByName: "Wren Writer"}
	switch kind {
	case "empty":
	case "reader":
		d.CanGet, d.IsOwner = false, false
		row.ByName = ""
		d.Items = []editionRow{row}
	case "one":
		d.One = &row
		d.Chapters = []editionChapter{{Chapter: editions.Chapter{Ord: 1, Kind: "part", Title: "Part One", SourcePath: "OEBPS/text/part1.xhtml", Words: 0, DocumentID: "doc_1"}},
			{Chapter: editions.Chapter{Ord: 2, ParentOrd: 1, Kind: "chapter", Title: "The Road", SourcePath: "OEBPS/text/ch01.xhtml", Words: 172, DocumentID: "doc_2"}, Depth: 1},
			{Chapter: editions.Chapter{Ord: 3, ParentOrd: 1, Kind: "chapter", Title: "The Wall", SourcePath: "OEBPS/text/ch02.xhtml", Words: 1}, Depth: 1, Left: true}}
		d.Hidden = 2
		d.Notice = "A note."
	default:
		d.Items = []editionRow{row}
		d.Error = "The original file is not on this server."
	}
	return d
}

// diffFixture is the diff screens for the template lint and the structure checks: the list of a comparison with a guess, a
// chapter's differences with hostile text, a comparison of two editions, the picker and a refusal.
func diffFixture(kind string) diffData {
	proj := ownerFixture(fixtureProjects()[0])
	d := diffData{Project: proj, Slug: "the-salt-road", CSRF: "tok", EditionID: "edn_1", EditionName: "First printing", CanText: true}
	res := &editiondiff.Result{Mode: "project", Base: editiondiff.Side{Kind: "edition", Label: "First printing", Words: 900, Source: "record"}, Head: editiondiff.Side{Kind: "project", Project: "The Salt Road", Words: 950, Source: "project"},
		Summary: editiondiff.Summary{Steps: 3, Edited: 1, Added: 1, Unchanged: 1, WordsAdded: 40, WordsRemoved: 12}}
	edited := editiondiff.Step{Index: 1, Key: "b2:d1", Status: "edited", Base: []editiondiff.Ref{{Ord: 2, Title: "The Road", Words: 300}}, Head: []editiondiff.Ref{{DocumentID: "doc_1", Title: "The Long Road", Words: 330}},
		Match: &editiondiff.Match{Method: "title", Score: 0.71, Similarity: 0.6, Guess: true}, Renamed: true, Words: editiondiff.Words{Base: 300, Head: 330, Added: 40, Removed: 12}, Compared: "full", Changes: 2,
		Offer: &editiondiff.Offer{Suggest: true, Take: true, Confirm: true}}
	edited.Hunks = []editiondiff.Hunk{
		{ID: "hunk1", Index: 1, Kind: "replace", Base: "The <script>alert(1)</script> boat %%private%% came [[in]] {{.}} ‮at dawn.", Head: "The boat came in at first light.", Words: editiondiff.Words{Added: 3, Removed: 2},
			Segments: []editiondiff.Segment{{Op: "equal", Text: "The boat came in at "}, {Op: "delete", Text: "dawn"}, {Op: "insert", Text: "first light"}}, Before: "A paragraph before."},
		{ID: "hunk2", Index: 2, Kind: "removed", Base: "A paragraph only the edition has.", Words: editiondiff.Words{Removed: 6}, Offer: "A heading cannot be offered."},
	}
	res.Steps = []editiondiff.Step{edited,
		{Index: 2, Key: "h:d2", Status: "added", Head: []editiondiff.Ref{{DocumentID: "doc_2", Title: "A new chapter", Words: 80}}, Words: editiondiff.Words{Head: 80, Added: 80}, Compared: "none"},
		{Index: 3, Key: "b3:d3", Status: "unchanged", Base: []editiondiff.Ref{{Ord: 3, Title: "The Wall"}}, Head: []editiondiff.Ref{{DocumentID: "doc_3", Title: "The Wall"}}, Match: &editiondiff.Match{Method: "recorded", Score: 1}, Compared: "equal"}}
	d.Res = res
	switch kind {
	case "step":
		d.selectStep(res, "b2:d1", 1)
		d.Revision = 4
		d.Back = d.listURL()
	case "compare":
		d.Compare, d.EditionName = true, ""
		d.FirstLabel = "First printing"
		d.Query = url.Values{"edition": {"edn_1"}, "with_edition": {"edn_2"}}
		res.Mode = "editions"
		d.decorate(res, "edn_1")
	case "picker":
		d.Compare, d.Res, d.EditionName, d.FirstLabel = true, nil, "", "First printing"
		d.Picker = &diffPicker{With: proj.ID, Projects: []projects.Project{proj}, Editions: []editionRow{{Edition: editions.Edition{ID: "edn_2", Label: "Second printing"}, When: "13 Nov 2026"}}}
	case "busy":
		d.Compare, d.Res, d.EditionName = true, nil, ""
		d.Error, d.Retry, d.RetryURL = "Another comparison of yours is still running.", 5, "/p/the-salt-road/editions/compare?edition=edn_1"
	case "reader":
		d.CanText = false
		d.Error = "The text of an edition is for the project's editors."
		d.decorate(res, "edn_1")
	default:
		d.decorate(res, "edn_1")
		d.Notice = "Suggestions made: 2."
		d.Lines = []string{"Not offered: a heading."}
	}
	return d
}

// A book's own text is data: tags, markers, wiki links, template braces and a bidirectional override are written as text,
// each run in a block that isolates its direction, and nothing of it is markup.
func TestTheDiffScreenWritesABooksTextAsText(t *testing.T) {
	s := newWebServer(t)
	pg := Page{Title: "Changes", Path: "/p/p-a/editions/edn_1/diff", User: fixtureUser("writer"), CSRF: "tok_writer", Data: diffFixture("step")}
	doc := renderPage(t, s, fixture{name: "editiondiff", page: pg})
	for _, bad := range []string{"<script>alert(1)</script>", "<script>alert"} {
		if strings.Contains(doc, bad) {
			t.Errorf("the text of a book is markup: %q", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "%%private%%", "[[in]]", "{{.}}", `dir="auto"`, `<del>`, `<ins>`, "Suggest this change", "A guess.", `name="confirm_guess"`, "saved first as a version"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the chapter's page does not have %q", want)
		}
	}
	if strings.Contains(doc, "Chapter 1</h2>") || strings.Contains(doc, "net ") {
		t.Error("a net figure or a stray heading")
	}
	pg.Data = diffFixture("reader")
	rd := renderPage(t, s, fixture{name: "editiondiff", page: pg})
	if strings.Contains(rd, "Review the differences") || !strings.Contains(rd, "for the project&#39;s editors") {
		t.Error("a reader is given the step links or not told why")
	}
}

// The diff pages and the two actions follow the roles: a reader reads the list and is refused the text and the writes; a
// stranger finds nothing; a chapter's key that is not in the comparison sends back to the list with a sentence.
func TestTheDiffPagesFollowTheRoles(t *testing.T) {
	e := newWriteEnv(t)
	e.s.editionFixtures(t, e.pid)
	id := sweepChildren["edition_id"]
	reader := e.s.newCaller(t, "reader", ops.RoleWriter)
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	e.s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	page := "/p/" + e.slug + "/editions/" + id + "/diff"
	if rec := e.s.do(caller{name: "anon"}, "GET", page, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := e.s.do(stranger, "GET", page, ""); rec.Code != 404 {
		t.Errorf("a stranger: %d", rec.Code)
	}
	for _, c := range []caller{e.owner, reader} {
		rec := e.s.do(c, "GET", page, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Changes since this edition") || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: the list: %d", c.name, rec.Code)
		}
	}
	// a reader who asks for a chapter's text is told, and gets the list
	rd := e.s.do(reader, "GET", page+"?chapter=b1:x", "")
	if rd.Code != 200 || !strings.Contains(rd.Body.String(), "for the project&#39;s editors") || strings.Contains(rd.Body.String(), `class="dfh"`) {
		t.Errorf("a reader's chapter request: %d", rd.Code)
	}
	// an owner's request for a key that is not there
	og := e.s.do(e.owner, "GET", page+"?chapter=nope", "")
	if og.Code != 200 || !strings.Contains(og.Body.String(), "not in this comparison") {
		t.Errorf("a chapter that is not in the comparison: %d", og.Code)
	}
	// the writes: a reader is refused, an owner's refusal is a redirect with a sentence, once
	v := url.Values{"chapter": {"b1:x"}, "base_revision": {"1"}}
	for _, a := range []string{"suggest", "take"} { // the op refuses; the screen answers with the sentence, on a page, once
		rec := e.s.post(t, reader, "/ui/projects/"+e.pid+"/editions/"+id+"/"+a, v, "", false)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("a reader's %s: %d", a, rec.Code)
			continue
		}
		if body := followAs(t, e.s, reader, rec).Body.String(); !strings.Contains(body, "for the project&#39;s editors") || strings.Contains(body, "Suggestions made") {
			t.Errorf("a reader's %s is not refused in words: %.300s", a, body)
		}
	}
	if rec := e.s.post(t, stranger, "/ui/projects/"+e.pid+"/editions/"+id+"/take", v, "", false); rec.Code != 404 {
		t.Errorf("a stranger took: %d", rec.Code)
	}
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/editions/"+id+"/take", v, "", false)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), page) {
		t.Fatalf("a refused take is not a redirect to the page: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	shown := followAs(t, e.s, e.owner, rec).Body.String()
	if !strings.Contains(shown, `role="alert"`) {
		t.Errorf("the refusal is not on the page: %.300s", shown)
	}
	// comparing two editions is for editors of both: a reader sees why, and nothing is computed
	cm := e.s.do(reader, "GET", "/p/"+e.slug+"/editions/compare?edition="+id+"&with_edition="+id, "")
	if cm.Code != 200 || !strings.Contains(cm.Body.String(), "for the project&#39;s editors") {
		t.Errorf("a reader's comparison: %d", cm.Code)
	}
}

// The sentence an action leaves is for the project and the edition it was made on: another project's page does not take it, the
// page it was meant for still has it, and a reload does not say it again.
func TestADiffFlashIsShownOnlyWhereItBelongs(t *testing.T) {
	e := newWriteEnv(t)
	e.s.editionFixtures(t, e.pid)
	idA := sweepChildren["edition_id"]
	e.s.editionFixtures(t, e.pidOth)
	idB := sweepChildren["edition_id"]
	slugB := "other-book"
	v := url.Values{"chapter": {"b1:x"}, "base_revision": {"1"}}
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/editions/"+idA+"/take", v, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("take: %d", rec.Code)
	}
	if b := e.s.do(e.owner, "GET", "/p/"+slugB+"/editions/"+idB+"/diff", "").Body.String(); strings.Contains(b, `data-result`) {
		t.Error("another project's page shows the sentence of an action made on this one")
	}
	if b := e.s.do(e.owner, "GET", "/p/"+e.slug+"/editions", "").Body.String(); strings.Contains(b, `data-result`) {
		t.Error("the editions list takes a diff page's sentence")
	}
	if a := followAs(t, e.s, e.owner, rec).Body.String(); !strings.Contains(a, `data-result`) {
		t.Error("the page it was meant for lost the sentence")
	}
	if a := e.s.do(e.owner, "GET", "/p/"+e.slug+"/editions/"+idA+"/diff", "").Body.String(); strings.Contains(a, `data-result`) {
		t.Error("the sentence comes back on a reload")
	}
}

// An edition id that is not shaped like one is refused before it is written into the address the answer goes to.
func TestAnActionRefusesAnEditionIdThatIsNotAnId(t *testing.T) {
	e := newWriteEnv(t)
	for _, edn := range []string{"..%2F..%2Fevil", "x%23frag", "a%3Fb"} {
		rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/editions/"+edn+"/take", url.Values{"chapter": {"b1:x"}, "base_revision": {"1"}}, "", false)
		if rec.Code != 404 {
			t.Errorf("%s: %d (Location %q), want 404", edn, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// A chapter counted by whole paragraph says so in the totals and on its row (a size limit's replacement is not the real change).
func TestACoarseCountIsSaidInTheTotalsAndOnTheRow(t *testing.T) {
	s := newWebServer(t)
	d := diffFixture("list")
	d.Res.Steps[0].Compared = "coarse"
	d.decorate(d.Res, "edn_1")
	doc := renderPage(t, s, fixture{name: "editiondiff", page: Page{Title: "Changes", Path: "/p/p-a/editions/edn_1/diff", User: fixtureUser("writer"), CSRF: "tok_writer", Data: d}})
	if !strings.Contains(doc, "Chapters counted by whole paragraph: 1") || !strings.Contains(doc, "Counted by whole paragraph here") {
		t.Errorf("the caveat is missing from the totals or the row")
	}
}
