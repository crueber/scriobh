package server

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// ---- fixtures for web_test.go ----------------------------------------------------------------

func cardsFixture(proj projects.Project, canEdit bool, filter string, empty bool) cardsView {
	return buildCards(outlineFixture(proj, canEdit, empty), filter)
}

func boardFixture(proj projects.Project, canEdit bool, empty bool) boardView {
	return buildBoard(outlineFixture(proj, canEdit, empty))
}

func readFixture(proj projects.Project, canEdit bool, empty bool) readData {
	d := readData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CanEdit: canEdit, Prefs: defaultPrefs()}
	if empty {
		d.Empty = true
		return d
	}
	d.Count = 3
	d.Position = 2
	d.Toc = []readTocItem{
		{ID: "doc_f", Label: "Dedication", Words: 40},
		{ID: "doc_c1", Label: "Chapter 1: Low Water", Part: "Part One", Current: true, Words: 6000},
		{ID: "doc_c2", Label: "Chapter 2: <b>High</b> Water", Words: 7400},
	}
	d.Unit = &readUnit{ID: "doc_c1", Title: "Low Water", Kind: "chapter", Number: "1", Kicker: "Part One", Words: 6000}
	d.Prev, d.Next = &d.Toc[0], &d.Toc[2]
	d.WriteURL = "/p/" + proj.Slug + "/d/doc_c1"
	d.Article = readArticle{ID: "u-doc_c1", Doc: "doc_c1", Title: "Low Water", Label: "Chapter 1", Kicker: "Part One", Next: "doc_c2", Body: []readSection{
		{ID: "d-doc_c1", HTML: template.HTML("<p>Mara counted the boats that did not come in.</p><blockquote><p>None.</p></blockquote>")},
		{ID: "d-doc_s1", Title: "The ledger", Scene: true, HTML: template.HTML("<p>A careful reckoning.</p>")},
	}}
	return d
}

// ---- helpers ------------------------------------------------------------------------------------

func (e writeEnv) words(n int) string {
	return strings.TrimSpace(strings.Repeat("harbour ", n))
}

func (e writeEnv) status(t *testing.T, id, status string) {
	t.Helper()
	d := e.call(t, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
	st := status
	e.call(t, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Status: &st})
}

type boardsRoles struct {
	e                                      writeEnv
	editor, reader, stranger, admin, other caller
}

func newBoardsRoles(t *testing.T) boardsRoles {
	t.Helper()
	e := newWriteEnv(t)
	r := boardsRoles{e: e}
	r.editor = e.s.newCaller(t, "editor", ops.RoleWriter)
	r.reader = e.s.newCaller(t, "reader", ops.RoleReader)
	r.stranger = e.s.newCaller(t, "stranger", ops.RoleWriter)
	r.admin = e.s.newCaller(t, "admin", ops.RoleAdmin)
	e.s.addMember(t, e.pid, r.editor.userID, ops.ProjectEditor)
	e.s.addMember(t, e.pid, r.reader.userID, ops.ProjectReader)
	return r
}

// ---- access ---------------------------------------------------------------------------------------

func TestBoardsAccess(t *testing.T) {
	r := newBoardsRoles(t)
	e := r.e
	paths := []string{
		"/p/" + e.slug + "/cards", "/p/" + e.slug + "/board", "/p/" + e.slug + "/read", "/p/" + e.slug + "/read/" + e.chap,
		"/ui/projects/" + e.pid + "/cards", "/ui/projects/" + e.pid + "/board", "/ui/projects/" + e.pid + "/read/" + e.chap,
	}
	for _, p := range paths {
		if rec := e.s.do(caller{name: "anon"}, "GET", p, ""); rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d", p, rec.Code)
		}
		if rec := e.s.do(r.stranger, "GET", p, ""); rec.Code != 404 {
			t.Errorf("%s stranger: %d, want 404", p, rec.Code)
		}
		for _, c := range []caller{e.owner, r.editor, r.reader} {
			rec := e.s.do(c, "GET", p, "")
			want := 200
			if strings.HasSuffix(p, "/read") {
				want = http.StatusSeeOther // to the first page
			}
			if rec.Code != want {
				t.Errorf("%s as %s: %d, want %d", p, c.name, rec.Code, want)
			}
		}
		// An instance administrator who is not a member is a stranger on the pages, slug or id.
		if rec := e.s.do(r.admin, "GET", p, ""); rec.Code != 404 {
			t.Errorf("%s instance admin who is not a member: %d, want 404", p, rec.Code)
		}
	}
	if rec := e.s.do(r.stranger, "GET", "/ui/projects/"+e.pidOth+"/read/"+e.docOth, ""); rec.Code != 404 {
		t.Errorf("someone else's document, stranger: %d", rec.Code)
	}
	if rec := e.s.do(e.owner, "GET", "/p/"+e.slug+"/read/"+e.docOth, ""); rec.Code != 404 {
		t.Errorf("a document of another project through this project's reader: %d", rec.Code)
	}
	if rec := e.s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/read/"+e.docOth, ""); rec.Code != 404 {
		t.Errorf("a document of another project through this project's fragment: %d", rec.Code)
	}
}

// A reader gets the pages without the script that changes anything; an editor and the owner get it.
func TestBoardsReaderIsReadOnly(t *testing.T) {
	r := newBoardsRoles(t)
	e := r.e
	for _, p := range []string{"/p/" + e.slug + "/cards", "/p/" + e.slug + "/board"} {
		for c, want := range map[string]bool{"owner": true, "editor": true, "reader": false} {
			cl := e.owner
			switch c {
			case "editor":
				cl = r.editor
			case "reader":
				cl = r.reader
			}
			body := e.s.do(cl, "GET", p, "").Body.String()
			if got := strings.Contains(body, "/static/js/boards."); got != want {
				t.Errorf("%s as %s: boards script present = %v", p, c, got)
			}
			if got := strings.Contains(body, `data-can-edit="true"`); got != want {
				t.Errorf("%s as %s: data-can-edit true = %v", p, c, got)
			}
		}
	}
	// Reading mode never loads the writing scripts.
	body := e.s.do(r.reader, "GET", "/p/"+e.slug+"/read/"+e.chap, "").Body.String()
	for _, bad := range []string{"/static/js/boards.", "/static/js/doc.", "/static/js/tree."} {
		if strings.Contains(body, bad) {
			t.Errorf("the reading page loads %s", bad)
		}
	}
}

// ---- the corkboard --------------------------------------------------------------------------------

func TestCorkboardNumberingMatchesTheTree(t *testing.T) {
	e := newWriteEnv(t)
	e.create(t, "Dedication", "front_matter", "", "")
	p1 := e.create(t, "Part One", "part", "", "")
	e.create(t, "A", "chapter", p1, "")
	b := e.create(t, "B", "chapter", p1, "")
	p2 := e.create(t, "Part Two", "part", "", "")
	e.create(t, "C", "chapter", p2, "")
	check := func(when string) {
		t.Helper()
		oo := e.call(t, "outline.get", docs.OutlineIn{ProjectID: e.pid}).(docs.OutlineOut)
		pr := e.call(t, "project.get", projects.GetIn{ID: e.pid}).(projects.Item).Item
		cv := buildCards(buildOutline(pr, oo, true, ""), "")
		lo := e.call(t, "doc.list", docs.ListIn{ProjectID: e.pid}).(docs.ListOut)
		tv := buildTree(lo.Items, lo.Order, pr, "", true, "")
		want := map[string]string{}
		for _, row := range tv.Rows {
			want[row.ID] = row.Number
		}
		n := 0
		for _, l := range cv.Lanes {
			for _, c := range l.Cards {
				n++
				if c.Number != want[c.ID] {
					t.Errorf("%s: card %s is numbered %q on the corkboard and %q in the tree", when, c.Title, c.Number, want[c.ID])
				}
			}
		}
		if n != cv.Cards || n == 0 {
			t.Errorf("%s: %d cards in lanes, %d counted", when, n, cv.Cards)
		}
	}
	check("as built")
	// Earlier and Later are doc.move with the neighbours the card carries: apply them the way the script does.
	cards := func() map[string]cardRow {
		oo := e.call(t, "outline.get", docs.OutlineIn{ProjectID: e.pid}).(docs.OutlineOut)
		pr := e.call(t, "project.get", projects.GetIn{ID: e.pid}).(projects.Item).Item
		m := map[string]cardRow{}
		for _, l := range buildCards(buildOutline(pr, oo, true, ""), "").Lanes {
			for _, c := range l.Cards {
				m[c.ID] = c
			}
		}
		return m
	}
	cb := cards()[b]
	if cb.Prev == "" || cb.Next != "" {
		t.Fatalf("B should have a neighbour before and none after: %+v", cb)
	}
	first := cb.Prev2
	e.call(t, "doc.move", docs.MoveIn{ID: b, ParentID: cb.ParentID, After: &first}) // Earlier
	check("a chapter moved earlier")
	if got := cards()[b]; got.Prev != "" || got.Next == "" {
		t.Errorf("after Earlier, B is first in its part: %+v", got)
	}
	nx := cards()[b].Next
	e.call(t, "doc.move", docs.MoveIn{ID: b, ParentID: cb.ParentID, After: &nx}) // Later
	check("and later again")
	if got := cards()[b]; got.Next != "" || got.Prev == "" {
		t.Errorf("after Later, B is last in its part: %+v", got)
	}
	after := ""
	e.call(t, "doc.move", docs.MoveIn{ID: p2, ParentID: "", After: &after}) // a whole part to the front
	check("a part moved first")
	e.call(t, "doc.move", docs.MoveIn{ID: b, ParentID: p2}) // Move to another part
	check("a chapter moved to another part")
}

func TestCorkboardPage(t *testing.T) {
	r := newBoardsRoles(t)
	e := r.e
	p1 := e.create(t, "Part One", "part", "", "")
	c1 := e.create(t, "Low Water", "chapter", p1, e.words(120))
	c2 := e.create(t, "High Water", "chapter", p1, e.words(40))
	e.status(t, c1, "draft")
	e.status(t, c2, "done")
	body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/cards", "").Body.String()
	for _, want := range []string{`id="cards"`, "Part One", "Low Water", "High Water", "Status: </span>Draft", "Status: </span>Done", `data-targets=`, `id="b-live"`, "120 words", "40 words", `id="c-` + c1 + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("corkboard lacks %q", want)
		}
	}
	// The filter keeps every neighbour in the data but hides the cards of other statuses, and says so in words.
	body = e.s.do(e.owner, "GET", "/p/"+e.slug+"/cards?status=done", "").Body.String()
	if !strings.Contains(body, `id="c-`+c2+`"`) || strings.Contains(body, `id="c-`+c1+`"`) {
		t.Error("the done filter shows the wrong cards")
	}
	if !strings.Contains(body, "Clear the filter to reorder cards") {
		t.Error("the filter does not say why moving is off")
	}
	// Anything that is not a status is ignored rather than trusted.
	body = e.s.do(e.owner, "GET", "/p/"+e.slug+"/cards?status=%3Cscript%3E", "").Body.String()
	if strings.Contains(body, "<script>") || !strings.Contains(body, `id="c-`+c1+`"`) {
		t.Error("an unknown filter changed the page")
	}
	frag := e.s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/cards", "")
	if frag.Code != 200 || strings.Contains(frag.Body.String(), "<html") || !strings.Contains(frag.Body.String(), `class="lane"`) {
		t.Errorf("fragment: %d %.100s", frag.Code, frag.Body.String())
	}
}

func TestBoardPage(t *testing.T) {
	r := newBoardsRoles(t)
	e := r.e
	a := e.create(t, "Alpha", "chapter", "", e.words(30))
	b := e.create(t, "Beta", "chapter", "", e.words(70))
	e.status(t, a, "revise")
	e.status(t, b, "revise")
	body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/board", "").Body.String()
	for _, want := range []string{`id="board"`, `data-statuses="idea:Idea,outline:Outline,draft:Draft,revise:Revise,done:Done"`, `id="bc-revise"`, "100 words", "Alpha", "Beta"} {
		if !strings.Contains(body, want) {
			t.Errorf("board lacks %q", want)
		}
	}
	for _, s := range []string{"idea", "outline", "draft", "revise", "done"} {
		if !strings.Contains(body, `data-status="`+s+`" aria-labelledby="bc-`+s+`"`) {
			t.Errorf("no column for %s", s)
		}
	}
}

// ---- reading mode -----------------------------------------------------------------------------------

func TestReadingEscapesWhatItShows(t *testing.T) {
	r := newBoardsRoles(t)
	e := r.e
	hostile := "<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n\n<div style=\"x\" onclick=\"alert(3)\">hi</div>\n\n[click](javascript:alert(4))\n\n" +
		"![Remote picture](https://evil.example/pixel.png)\n\n![Local picture](/files/att_0123456789abcdefghij)\n\n![Bad](/files/../etc/passwd)\n"
	id := e.create(t, `Evil <script>alert(5)</script> & "quotes"`, "chapter", "", hostile)
	body := e.s.do(r.reader, "GET", "/p/"+e.slug+"/read/"+id, "").Body.String()
	for _, bad := range []string{"<script>", "<img src=x", "<div style", ` onclick="`, `href="javascript`, `src="https://evil`, `src="/files/../`} {
		if strings.Contains(body, bad) {
			t.Errorf("the reading page contains %q", bad)
		}
	}
	for _, want := range []string{"&lt;script&gt;alert(1)", "[image: Remote picture (not included)]", `src="/files/att_0123456789abcdefghij"`, `alt="Local picture"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the reading page lacks %q", want)
		}
	}
	if strings.Contains(body, `<img src="https://`) || strings.Contains(body, "<img src=\"//") {
		t.Error("a remote image became a request")
	}
	frag := e.s.do(r.reader, "GET", "/ui/projects/"+e.pid+"/read/"+id, "").Body.String()
	if strings.Contains(frag, "<script>") || strings.Contains(frag, "<img src=x") || strings.Contains(frag, "<div style") {
		t.Error("the fragment is not inert")
	}
}

func TestReadingNavigation(t *testing.T) {
	r := newBoardsRoles(t)
	e := r.e
	p1 := e.create(t, "Part One", "part", "", "")
	a := e.create(t, "Low Water", "chapter", p1, "First chapter text.")
	s1 := e.create(t, "The ledger", "scene", a, "A ledger scene.")
	b := e.create(t, "High Water", "chapter", p1, "Second chapter text.")
	note := e.create(t, "Private jotting", "note", "", "do not show this")
	_ = note
	body := e.s.do(r.reader, "GET", "/p/"+e.slug+"/read/"+a, "").Body.String()
	for _, want := range []string{"First chapter text.", "A ledger scene.", "The ledger", "Part One", `rel="next" href="/p/` + e.slug + `/read/` + b + `"`, `id="read-toc"`, "Back to writing", `data-doc="` + a + `"`, "Page "} {
		if !strings.Contains(body, want) {
			t.Errorf("chapter page lacks %q", want)
		}
	}
	if strings.Contains(body, "Second chapter text.") {
		t.Error("another chapter is on this page")
	}
	if strings.Contains(body, "Private jotting") || strings.Contains(body, "do not show this") {
		t.Error("a note is in the reading order")
	}
	if !strings.Contains(body, `href="/p/`+e.slug+`/d/`+a+`"`) {
		t.Error("the way back to writing does not land in this chapter")
	}
	// A scene's own address lands in its chapter, at the scene.
	rec := e.s.do(r.reader, "GET", "/p/"+e.slug+"/read/"+s1, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/p/"+e.slug+"/read/"+a+"#d-"+s1 {
		t.Errorf("scene address: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.s.do(r.reader, "GET", "/p/"+e.slug+"/read/doc_nope", ""); rec.Code != 404 {
		t.Errorf("unknown page: %d", rec.Code)
	}
	// The landing goes to the first page in reading order.
	land := e.s.do(r.reader, "GET", "/p/"+e.slug+"/read", "")
	if land.Code != http.StatusSeeOther || !strings.HasPrefix(land.Header().Get("Location"), "/p/"+e.slug+"/read/") {
		t.Errorf("landing: %d %q", land.Code, land.Header().Get("Location"))
	}
	// Everything the reader may see is there: no compile filter.
	frag := e.s.do(r.reader, "GET", "/ui/projects/"+e.pid+"/read/"+b, "").Body.String()
	if !strings.Contains(frag, "Second chapter text.") || strings.Contains(frag, "<html") {
		t.Errorf("fragment: %.100s", frag)
	}
}

// The numbering on the reading pages is the numbering of the tree, after reorders.
func TestReadingNumberingMatchesTheTree(t *testing.T) {
	e := newWriteEnv(t)
	p1 := e.create(t, "Part One", "part", "", "")
	a := e.create(t, "Alpha", "chapter", p1, "a")
	b := e.create(t, "Beta", "chapter", p1, "b")
	p2 := e.create(t, "Part Two", "part", "", "")
	c := e.create(t, "Gamma", "chapter", p2, "c")
	check := func(when string) {
		t.Helper()
		pr := e.call(t, "project.get", projects.GetIn{ID: e.pid}).(projects.Item).Item
		lo := e.call(t, "doc.list", docs.ListIn{ProjectID: e.pid}).(docs.ListOut)
		tv := buildTree(lo.Items, lo.Order, pr, "", true, "")
		for _, row := range tv.Rows {
			if row.Kind != "chapter" {
				continue
			}
			body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/read/"+row.ID, "").Body.String()
			if want := `<p class="r-label">Chapter ` + row.Number + `</p>`; !strings.Contains(body, want) {
				t.Errorf("%s: %s should read as Chapter %s", when, row.Title, row.Number)
			}
			if want := "Chapter " + row.Number + ": " + row.Title; !strings.Contains(body, want) {
				t.Errorf("%s: the contents lack %q", when, want)
			}
		}
	}
	check("as built")
	after := ""
	e.call(t, "doc.move", docs.MoveIn{ID: p2, ParentID: "", After: &after})
	check("part moved")
	e.call(t, "doc.move", docs.MoveIn{ID: b, ParentID: p2, After: &after})
	check("chapter moved across parts")
	_, _ = a, c
}

func TestReadingAnEmptyProject(t *testing.T) {
	e := newWriteEnv(t)
	e.call(t, "doc.trash", docs.DocIn{ID: e.chap}) // nothing left in the reading order
	landing := e.s.do(e.owner, "GET", "/p/"+e.slug+"/read", "")
	if landing.Code != 200 || !strings.Contains(landing.Body.String(), "nothing to read yet") || strings.Contains(landing.Body.String(), `id="reader"`) {
		t.Errorf("the empty reading page: %d", landing.Code)
	}
	if rec := e.s.do(e.owner, "GET", "/p/"+e.slug+"/read/"+e.chap, ""); rec.Code != 404 {
		t.Errorf("a page of an empty project: %d", rec.Code)
	}
}

// One chapter of a 60,000 word project is built and rendered well inside the budget.
func TestReadingOneChapterIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 60,000 word project")
	}
	r := newBoardsRoles(t)
	e := r.e
	p1 := e.create(t, "Part One", "part", "", "")
	para := strings.TrimSpace(strings.Repeat("The harbour was quiet and *the boats* did not come in, said **Mara**, and [[Low Water]] was far. ", 12))
	var ids []string
	for i := 0; i < 12; i++ {
		var sb strings.Builder
		for w := 0; w < 5000/(12*18); w++ {
			sb.WriteString(para + "\n\n")
		}
		ids = append(ids, e.create(t, fmt.Sprintf("Chapter title %d", i+1), "chapter", p1, sb.String()))
	}
	var best, worst time.Duration
	best = time.Hour
	var all []time.Duration
	for i := 0; i < 7; i++ {
		start := time.Now()
		rec := e.s.do(r.reader, "GET", "/p/"+e.slug+"/read/"+ids[5], "")
		d := time.Since(start)
		if rec.Code != 200 {
			t.Fatalf("page: %d", rec.Code)
		}
		all = append(all, d)
		// timing: not a budget: tracks the best and the worst of the runs, for the log
		if d < best {
			best = d
		}
		// timing: not a budget: tracks the worst run, for the log
		if d > worst {
			worst = d
		}
	}
	// timing: not a budget: sorts the samples for their median
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	t.Logf("one chapter (about 5,000 of 60,000 words, 13 pages): best %v, median %v, worst %v", best, all[len(all)/2], worst)
	median := all[len(all)/2]
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if median > 150*time.Millisecond {
			t.Errorf("median %v is over the 150 ms budget", median)
		}
	})
}
