package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// outlineFixture is a small book: a front matter page, two parts with chapters and a scene, a back matter page.
func outlineFixture(proj projects.Project, canEdit bool, empty bool) outlineView {
	if empty {
		return buildOutline(proj, docs.OutlineOut{}, canEdit, "tok_writer")
	}
	mk := func(id, parent, kind, title string, depth, kids, words, target int, status, syn string) docs.OutlineItem {
		it := docs.OutlineItem{ID: id, ParentID: parent, Kind: kind, Title: title, Depth: depth, ChildCount: kids, Status: status, SynopsisShort: syn,
			WordCount: words, TargetWords: target, Revision: 1}
		it.Rollup = docs.Rollup{Documents: 1, Words: words, TargetWords: target}
		return it
	}
	items := []docs.OutlineItem{
		mk("doc_f", "", "front_matter", "Dedication", 0, 0, 40, 0, "done", ""),
		mk("doc_p1", "", "part", "Part One", 0, 2, 0, 0, "draft", "The harbour, before the storm."),
		mk("doc_c1", "doc_p1", "chapter", "Low Water", 1, 1, 5200, 6000, "draft", "Mara counts the boats that did not come in."),
		mk("doc_s1", "doc_c1", "scene", "The ledger", 2, 0, 800, 0, "outline", "A <b>careful</b> reckoning."),
		mk("doc_c2", "doc_p1", "chapter", "", 1, 0, 7400, 6000, "revise", "Over target."),
		mk("doc_p2", "", "part", "Part Two", 0, 0, 0, 0, "idea", ""),
		mk("doc_b", "", "back_matter", "Acknowledgements", 0, 0, 120, 0, "idea", ""),
	}
	items[1].Rollup = docs.Rollup{Documents: 5, Words: 13400, TargetWords: 12000, Progress: 1.117, Status: docs.StatusCounts{Draft: 2, Outline: 1, Revise: 1}}
	items[2].Rollup = docs.Rollup{Documents: 2, Words: 6000, TargetWords: 6000, Progress: 1, Status: docs.StatusCounts{Draft: 1, Outline: 1}}
	return buildOutline(proj, docs.OutlineOut{Items: items, Totals: docs.Rollup{Documents: 7, Words: 13560, TargetWords: 12000, Status: docs.StatusCounts{Idea: 2, Done: 1, Draft: 2, Outline: 1, Revise: 1}}}, canEdit, "tok_writer")
}

func TestOutlinePageAndFragment(t *testing.T) {
	e := newWriteEnv(t)
	part := e.create(t, "Part One", "part", "", "")
	c1 := e.create(t, "Low Water", "chapter", part, "one two three four five")
	e.create(t, "High Water", "chapter", part, "six seven")
	front := e.create(t, "Dedication", "front_matter", "", "for you")
	_ = front
	body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/outline", "").Body.String()
	for _, want := range []string{`id="o-list"`, "Part One", "Low Water", "Front matter", `src="/static/js/outline.`, "Chapter 1", "Chapter 2"} {
		if !strings.Contains(body, want) {
			t.Errorf("outline page lacks %q", want)
		}
	}
	// The first (seeded) chapter of the project is numbered too: front matter and parts are not.
	if strings.Contains(body, "Front matter 1") || strings.Contains(body, "Part 1") {
		t.Error("front matter or a part got a chapter number")
	}
	frag := e.s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/outline", "")
	if frag.Code != 200 || !strings.Contains(frag.Body.String(), `id="o-summary"`) || strings.Contains(frag.Body.String(), "<html") {
		t.Errorf("fragment: %d %.80s", frag.Code, frag.Body.String())
	}
	_ = c1
}

func TestOutlineAccess(t *testing.T) {
	e := newWriteEnv(t)
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	if rec := e.s.do(stranger, "GET", "/p/"+e.slug+"/outline", ""); rec.Code != 404 {
		t.Errorf("outline page, stranger: %d", rec.Code)
	}
	if rec := e.s.do(stranger, "GET", "/ui/projects/"+e.pid+"/outline", ""); rec.Code != 404 {
		t.Errorf("outline fragment, stranger: %d", rec.Code)
	}
	if rec := e.s.do(caller{name: "anon"}, "GET", "/p/"+e.slug+"/outline", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("outline page, anonymous: %d", rec.Code)
	}
	if rec := e.s.do(caller{name: "anon"}, "GET", "/ui/projects/"+e.pid+"/outline", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("outline fragment, anonymous: %d", rec.Code)
	}
}

// A reader sees the outline without the editing script or the add form.
func TestOutlineForAReader(t *testing.T) {
	v := outlineFixture(fixtureProjects()[0], false, false)
	s := newWebServer(t)
	page := renderPage(t, s, fixture{"outline", Page{Title: "Outline", Path: "/p/the-salt-road/outline", User: fixtureUser("reader"), CSRF: "tok", Data: v}})
	if strings.Contains(page, "/static/js/outline.") || strings.Contains(page, `id="o-add"`) {
		t.Error("a reader's outline carries the editing script or the add form")
	}
	if !strings.Contains(page, "Over target") || !strings.Contains(page, "over the target") {
		t.Error("the over-target row does not say so in words")
	}
}

// The outline numbers chapters exactly as the writing view's tree does, after every reorder.
func TestOutlineNumberingMatchesTheTree(t *testing.T) {
	e := newWriteEnv(t)
	front := e.create(t, "Dedication", "front_matter", "", "")
	p1 := e.create(t, "Part One", "part", "", "")
	a := e.create(t, "A", "chapter", p1, "")
	b := e.create(t, "B", "chapter", p1, "")
	p2 := e.create(t, "Part Two", "part", "", "")
	c := e.create(t, "C", "chapter", p2, "")
	e.create(t, "Notes", "back_matter", "", "")
	check := func(when string) {
		t.Helper()
		oo := e.call(t, "outline.get", docs.OutlineIn{ProjectID: e.pid}).(docs.OutlineOut)
		pr := e.call(t, "project.get", projects.GetIn{ID: e.pid}).(projects.Item).Item
		ov := buildOutline(pr, oo, true, "")
		lo := e.call(t, "doc.list", docs.ListIn{ProjectID: e.pid}).(docs.ListOut)
		tv := buildTree(lo.Items, lo.Order, pr, "", true, "")
		if len(ov.Rows) != len(tv.Rows) {
			t.Fatalf("%s: %d outline rows, %d tree rows", when, len(ov.Rows), len(tv.Rows))
		}
		for i := range ov.Rows {
			if ov.Rows[i].ID != tv.Rows[i].ID || ov.Rows[i].Number != tv.Rows[i].Number {
				t.Errorf("%s: row %d is %s %q in the outline and %s %q in the tree", when, i, ov.Rows[i].ID, ov.Rows[i].Number, tv.Rows[i].ID, tv.Rows[i].Number)
			}
			if k := ov.Rows[i].Kind; k != "chapter" && ov.Rows[i].Number != "" {
				t.Errorf("%s: a %s is numbered %q", when, k, ov.Rows[i].Number)
			}
		}
	}
	check("as built")
	after := ""
	e.call(t, "doc.move", docs.MoveIn{ID: p2, ParentID: "", After: &after}) // Part Two to the front
	check("part moved first")
	e.call(t, "doc.move", docs.MoveIn{ID: b, ParentID: p2, After: &after}) // B to the front of Part Two
	check("chapter moved across parts")
	e.call(t, "doc.move", docs.MoveIn{ID: front, ParentID: p1})
	check("front matter moved inside a part")
	_ = a
	_ = c
}

// Reordering a whole part keeps every word, every child and every revision.
func TestOutlineReorderingAPartLosesNothing(t *testing.T) {
	e := newWriteEnv(t)
	p1 := e.create(t, "Part One", "part", "", "")
	p2 := e.create(t, "Part Two", "part", "", "")
	text := func(n int, w string) string { return strings.TrimSpace(strings.Repeat(w+" ", n)) }
	ids := map[string]string{}
	for _, c := range []struct {
		title, parent, kind string
		n                   int
	}{{"One A", p1, "chapter", 120}, {"One B", p1, "chapter", 340}, {"Two A", p2, "chapter", 77}, {"Two B", p2, "chapter", 510}} {
		ids[c.title] = e.create(t, c.title, c.kind, c.parent, text(c.n, strings.ToLower(strings.ReplaceAll(c.title, " ", ""))))
	}
	// A scene inside a chapter moves with it too.
	scene := e.create(t, "A scene", "scene", ids["One B"], text(33, "scene"))
	before := e.call(t, "outline.get", docs.OutlineIn{ProjectID: e.pid}).(docs.OutlineOut)
	snap := func(o docs.OutlineOut) map[string]docs.OutlineItem {
		m := map[string]docs.OutlineItem{}
		for _, it := range o.Items {
			m[it.ID] = it
		}
		return m
	}
	was := snap(before)
	order := func(o docs.OutlineOut) string {
		var ids []string
		for _, it := range o.Items {
			ids = append(ids, it.Title)
		}
		return strings.Join(ids, ",")
	}
	empty := ""
	e.call(t, "doc.move", docs.MoveIn{ID: p2, ParentID: "", After: &empty}) // Part Two before Part One
	after := e.call(t, "outline.get", docs.OutlineIn{ProjectID: e.pid}).(docs.OutlineOut)
	now := snap(after)
	if got, want := order(after), "Part Two,Two A,Two B,Chapter 1,Part One,One A,One B,A scene"; !strings.Contains(got, want) {
		t.Errorf("reading order after the move: %s, want ...%s", got, want)
	}
	if after.Totals.Words != before.Totals.Words || after.Totals.Documents != before.Totals.Documents {
		t.Errorf("totals changed: %+v then %+v", before.Totals, after.Totals)
	}
	for id, w := range was {
		n, ok := now[id]
		if !ok {
			t.Errorf("%s (%s) vanished", w.Title, id)
			continue
		}
		if n.WordCount != w.WordCount || n.ParentID != w.ParentID || n.Revision != w.Revision || n.Rollup.Words != w.Rollup.Words {
			t.Errorf("%s changed: words %d -> %d, parent %q -> %q, revision %d -> %d, rollup %d -> %d", w.Title, w.WordCount, n.WordCount, w.ParentID, n.ParentID, w.Revision, n.Revision, w.Rollup.Words, n.Rollup.Words)
		}
	}
	// The text itself is untouched.
	d := e.call(t, "doc.get", docs.DocIn{ID: scene}).(docs.Item).Item
	if d.Content != text(33, "scene")+"\n" && d.Content != text(33, "scene") {
		t.Errorf("a scene's text changed: %.40q", d.Content)
	}
	// Indent, outdent and move down are the same op in different positions: a part dropped into another part, and out again.
	e.call(t, "doc.move", docs.MoveIn{ID: p2, ParentID: p1})
	e.call(t, "doc.move", docs.MoveIn{ID: p2, ParentID: "", After: &p1})
	final := e.call(t, "outline.get", docs.OutlineIn{ProjectID: e.pid}).(docs.OutlineOut)
	if final.Totals.Words != before.Totals.Words {
		t.Errorf("totals after indent and outdent: %d, want %d", final.Totals.Words, before.Totals.Words)
	}
	for id, w := range was {
		if n := snap(final)[id]; n.WordCount != w.WordCount {
			t.Errorf("%s: %d words, want %d", w.Title, n.WordCount, w.WordCount)
		}
	}
}

// The fragment the outline script merges, and the REST ops it calls, respect the reader role.
func TestOutlineEditsNeedAnEditor(t *testing.T) {
	e := newWriteEnv(t)
	reader := e.s.newCaller(t, "reader", ops.RoleWriter)
	e.s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	rec := e.s.do(reader, "GET", "/p/"+e.slug+"/outline", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `data-can-edit="true"`) || strings.Contains(rec.Body.String(), "outline.js") {
		t.Errorf("a reader's outline: %d, editing markup present: %v", rec.Code, strings.Contains(rec.Body.String(), "outline.js"))
	}
	body, _ := json.Marshal(map[string]any{"base_revision": 1, "status": "done"})
	if rec := e.s.do(reader, "PATCH", "/api/v1/docs/"+e.chap, string(body)); rec.Code != 403 && rec.Code != 404 {
		t.Errorf("a reader saved a document: %d", rec.Code)
	}
}

// The timeline and point-of-view orders: scenes by story date (undated last), and scenes grouped by whose eyes they
// are told through; both as the page and as the fragment, with a mark for the current order, and with no move tools
// to draw (the list says moving is only possible in manuscript order).
func TestOutlineSortedOrders(t *testing.T) {
	e := newWriteEnv(t)
	part := e.create(t, "Part One", "part", "", "")
	ch := e.create(t, "Low Water", "chapter", part, "")
	set := func(id, pov, date string) {
		cur := e.call(t, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
		e.call(t, "doc.update", docs.UpdateIn{ID: id, BaseRevision: cur.Revision, Pov: &pov, StoryDate: &date})
	}
	late := e.create(t, "The late scene", "scene", ch, "late")
	early := e.create(t, "The early scene", "scene", ch, "early")
	none := e.create(t, "The undated scene", "scene", ch, "none")
	set(late, "Mara", "1999-05-01")
	set(early, "Joss", "1998-04-12")
	set(none, "Mara", "")
	order := func(body string, titles ...string) {
		t.Helper()
		last := -1
		for _, ti := range titles {
			i := strings.Index(body, ">"+ti+"</a>")
			if i < 0 || i < last {
				t.Errorf("%q is missing or out of order in the outline", ti)
			}
			last = i
		}
	}
	tl := e.s.do(e.owner, "GET", "/p/"+e.slug+"/outline?sort=timeline", "").Body.String()
	order(tl, "The early scene", "The late scene", "The undated scene")
	for _, want := range []string{`data-sort="timeline"`, `data-sort-link="timeline" data-frag="/ui/projects/` + e.pid + `/outline?sort=timeline" aria-current="true"`, "Part One / Low Water", "1998-04-12", "Undated", "come last", "Moving is only possible in manuscript order"} {
		if !strings.Contains(tl, want) {
			t.Errorf("timeline order lacks %q", want)
		}
	}
	if strings.Contains(tl, `class="o-sort"`) && strings.Count(tl, `aria-current="true"`) != 1 {
		t.Error("more than one order is marked current")
	}
	pov := e.s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/outline?sort=pov", "")
	if pov.Code != 200 || strings.Contains(pov.Body.String(), "<html") {
		t.Fatalf("pov fragment: %d", pov.Code)
	}
	pb := pov.Body.String()
	for _, want := range []string{`class="o-groups"`, `data-sort="pov"`, ">Mara <", ">Joss <", "2 scenes", "1 scene"} {
		if !strings.Contains(pb, want) {
			t.Errorf("point of view order lacks %q", want)
		}
	}
	// The manuscript order is the default, an unknown order is the same as none, and nothing is a tree in the others.
	for _, q := range []string{"", "?sort=sideways"} {
		b := e.s.do(e.owner, "GET", "/p/"+e.slug+"/outline"+q, "").Body.String()
		if !strings.Contains(b, `data-sort="order"`) || !strings.Contains(b, "Low Water") || strings.Contains(b, `class="o-groups"`) {
			t.Errorf("outline%s is not the manuscript order", q)
		}
	}
	if strings.Contains(tl, "o-d1") || strings.Contains(tl, ">Part One</a>") {
		t.Error("the timeline order is not a flat list of scenes")
	}
}
