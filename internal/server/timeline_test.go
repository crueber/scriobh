package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// ---- the plain-words rendering of a check -----------------------------------------------------------------------------

func TestTimelineFindingSentences(t *testing.T) {
	l := tlLinker{ProjectSlug: "salt", EntityBase: "/p/salt/timeline"}
	ev := []timeline.EventRef{{ID: "tle_1", Title: "Wedding", StoryDate: "1998-04-12"}}
	sc := []timeline.SceneRef{{ID: "doc_1", Title: "The arrival", StoryDate: "1998", Breadcrumb: []timeline.Crumb{{ID: "doc_0", Title: "Part one"}, {ID: "doc_1", Title: "The arrival"}}}}
	en := []vocab.Brief{{ID: "ent_1", Name: "Máire"}}
	cases := []struct {
		kind, sev, msg string
		events         []timeline.EventRef
		scenes         []timeline.SceneRef
		entities       []vocab.Brief
		label          string
		explain        string // a phrase the explanation must carry
		wantMsg        string
		hrefs          []string
	}{
		{"location_conflict", "error", "Máire cannot be at A and at B.", ev, nil, en, "Two places at once", "overlapping, definite times", "Máire cannot be at A and at B.", []string{"/timeline/events/tle_1", "/p/salt/timeline?entity=ent_1#tl-events"}},
		{"before_birth", "error", "Máire takes part before their birth.", ev, nil, en, "Before a birth", "birth date", "Máire takes part before their birth.", []string{"/timeline/events/tle_1", "/p/salt/timeline?entity=ent_1#tl-events"}},
		{"after_death", "error", "Máire takes part after their death.", ev, nil, en, "After a death", "death date", "Máire takes part after their death.", []string{"/timeline/events/tle_1", "/p/salt/timeline?entity=ent_1#tl-events"}},
		{"invalid_range", "error", "The scene \"X\" ends before it starts.", nil, sc, nil, "A scene ends before it starts", "Correct the dates", "The scene \"X\" ends before it starts.", []string{"/p/salt/d/doc_1"}},
		{"narrated_out_of_order", "warning", "\"A\" is told after \"B\".", ev, sc, nil, "Told out of order", "flashback you meant", "\"A\" is told after \"B\".", []string{"/timeline/events/tle_1", "/p/salt/d/doc_1"}},
		{"scene_before_first_event", "warning", "The scene features Máire early.", ev, sc, en, "Before a first appearance", "first recorded event", "The scene features Máire early.", []string{"/timeline/events/tle_1", "/p/salt/d/doc_1", "/p/salt/timeline?entity=ent_1#tl-events"}},
		{"unreadable_date", "warning", "3 scene(s) have a story date the timeline cannot read, so they are left off it.", nil, sc, nil, "Dates the timeline cannot read", "such as 1998, 1998-04 or 1998-04-12", "3 scenes have a story date the timeline cannot read, so they are left off the timeline.", []string{"/p/salt/d/doc_1"}},
		{"unreadable_date", "warning", "1 scene(s) have a story date the timeline cannot read, so they are left off it.", nil, sc, nil, "Dates the timeline cannot read", "left off it", "1 scene has a story date the timeline cannot read, so it is left off the timeline.", []string{"/p/salt/d/doc_1"}},
		{"something_new", "warning", "A kind this page has never heard of.", nil, nil, nil, "Something new", "", "A kind this page has never heard of.", nil},
	}
	for _, c := range cases {
		out := timeline.CheckOut{Events: 4, Scenes: 5, Findings: []timeline.Finding{{ID: "f", Kind: c.kind, Severity: c.sev, Message: c.msg, Events: c.events, Scenes: c.scenes, Entities: c.entities}}}
		if c.sev == "error" {
			out.Errors = 1
		} else {
			out.Warnings = 1
		}
		ck := buildCheck(out, l, true)
		if len(ck.Findings) != 1 {
			t.Fatalf("%s: %d findings", c.kind, len(ck.Findings))
		}
		f := ck.Findings[0]
		if f.Label != c.label || f.Message != c.wantMsg || !strings.Contains(f.Explain, c.explain) {
			t.Errorf("%s: label %q message %q explain %q", c.kind, f.Label, f.Message, f.Explain)
		}
		if (c.sev == "error") != (f.SevText == "Error") || (c.sev == "warning") != (f.SevText == "Warning") {
			t.Errorf("%s: severity words %q for %s", c.kind, f.SevText, c.sev)
		}
		var hrefs []string
		for _, ln := range f.Links {
			hrefs = append(hrefs, ln.Href)
		}
		if strings.Join(hrefs, " ") != strings.Join(c.hrefs, " ") {
			t.Errorf("%s: links %v, want %v", c.kind, hrefs, c.hrefs)
		}
		s := newWebServer(t)
		html := renderFragment(t, s, fragmentFixture{"timeline-parts", "tl-check", ck})
		lint(t, c.kind, html)
		if !strings.Contains(html, f.SevText) || !strings.Contains(html, f.Label) || strings.Contains(html, "!") {
			t.Errorf("%s: the panel does not say %q and %q in words: %s", c.kind, f.SevText, f.Label, html)
		}
	}
	// Errors come before warnings, under their own headings, and a flashback is explained.
	ck := tlFixtureCheck(true)
	if len(ck.Errs) != 2 || len(ck.Warns) != 2 || ck.Findings[0].Severity != "error" {
		t.Errorf("errors and warnings: %d %d", len(ck.Errs), len(ck.Warns))
	}
	html := renderFragment(t, newWebServer(t), fragmentFixture{"timeline-parts", "tl-check", ck})
	if i, j := strings.Index(html, ">Errors<"), strings.Index(html, ">Warnings<"); i < 0 || j < i {
		t.Error("the Errors heading must come before the Warnings heading")
	}
	if !strings.Contains(html, "flashback") {
		t.Error("a flashback is not explained")
	}
}

func TestTimelineCheckStates(t *testing.T) {
	s := newWebServer(t)
	l := tlLinker{ProjectSlug: "salt", EntityBase: "/p/salt/timeline"}
	render := func(c tlCheck) string { return renderFragment(t, s, fragmentFixture{"timeline-parts", "tl-check", c}) }
	// An empty result says what was looked at and never claims the story is consistent.
	for _, project := range []bool{true, false} {
		html := render(buildCheck(timeline.CheckOut{Events: 3, Scenes: 2}, l, project))
		txt := visibleText(html)
		if !strings.Contains(txt, "found nothing to flag") || !strings.Contains(txt, "They cannot tell whether the story is consistent") {
			t.Errorf("project=%v: the empty result: %s", project, txt)
		}
		if strings.Contains(strings.ToLower(txt), "no contradictions") || strings.Contains(strings.ToLower(txt), "is consistent.") {
			t.Errorf("project=%v: the empty result claims too much: %s", project, txt)
		}
	}
	if !strings.Contains(visibleText(render(buildCheck(timeline.CheckOut{}, l, false))), "Scenes belong to books") {
		t.Error("a Compendium's check should say that scenes are checked in a book")
	}
	// Indexing and hidden are said calmly, in a status region, not as an error.
	html := render(buildCheck(timeline.CheckOut{Indexing: true, Hidden: true}, l, true))
	if !strings.Contains(html, "still catching up") || !strings.Contains(html, "not open to you") || strings.Contains(html, `role="alert"`) {
		t.Errorf("indexing and hidden: %s", html)
	}
}

// ---- the strip --------------------------------------------------------------------------------------------------------

func TestTimelineStrip(t *testing.T) {
	href := func(from, to string) string { return from + ".." + to }
	pts := func(dates ...string) []tlPoint {
		var out []tlPoint
		for i, d := range dates {
			out = append(out, tlPoint{Date: d, Title: "T" + strconv.Itoa(i)})
		}
		return out
	}
	sum := func(s tlStrip) (e, sc int) {
		for _, b := range s.Buckets {
			e += b.Events
			sc += b.Scenes
		}
		return
	}
	// Nothing, or nothing dated: no blocks and a plain sentence.
	if s := buildStrip(nil, href); len(s.Buckets) != 0 || !strings.Contains(s.Summary, "nothing") && !strings.Contains(s.Summary, "Nothing") {
		t.Errorf("empty: %+v", s)
	}
	if s := buildStrip(pts("", "soon"), href); len(s.Buckets) != 0 || s.Undated != 2 || !strings.Contains(s.Summary, "no date") {
		t.Errorf("undated only: %+v", s)
	}
	// One date is one block that lists exactly it; a year alone stays a year.
	s := buildStrip(pts("1998"), href)
	if len(s.Buckets) != 1 || s.Buckets[0].Label != "1998" || s.Buckets[0].Href != "1998..1998" || s.Unit != "years" {
		t.Errorf("one year: %+v", s)
	}
	s = buildStrip(pts("1998-04-12"), href)
	if len(s.Buckets) != 1 || s.Unit != "days" || s.Buckets[0].From != "1998-04-12" || s.Buckets[0].To != "1998-04-12" {
		t.Errorf("one day: %+v", s)
	}
	// Months when the span is under four years; blocks are contiguous and cover the span.
	s = buildStrip(pts("1998-01", "1998-06-30", "1999-03", "2000-12-31"), href)
	if s.Unit != "months" || len(s.Buckets) > tlBuckets || s.First != "1998-01" || s.Last != "2000-12" {
		t.Errorf("months: unit %s, %d blocks, %s..%s", s.Unit, len(s.Buckets), s.First, s.Last)
	}
	// Years over a long span, negative years included, and each point lands in exactly one block.
	s = buildStrip(append(pts("-0044", "-0001", "0000", "1998", "2004-02", "2011"), tlPoint{Date: "", Title: "u"}, tlPoint{Date: "1999", Scene: true}), href)
	if e, sc := sum(s); s.Unit != "years" || e != 6 || sc != 1 || s.Undated != 1 || len(s.Buckets) > tlBuckets || s.First != "-0044" {
		t.Errorf("years: unit %s, %d/%d placed, undated %d, %d blocks, first %s", s.Unit, e, sc, s.Undated, len(s.Buckets), s.First)
	}
	if !s.HasScenes {
		t.Error("the strip should know it holds scenes")
	}
	// Blocks do not overlap: each From is the day after the last To, at the unit's precision.
	for i := 1; i < len(s.Buckets); i++ {
		if s.Buckets[i].From <= s.Buckets[i-1].To {
			t.Errorf("blocks %d and %d overlap: %s..%s then %s..%s", i-1, i, s.Buckets[i-1].From, s.Buckets[i-1].To, s.Buckets[i].From, s.Buckets[i].To)
		}
	}
	// Five hundred events become at most twelve blocks, every one counted, and the tallest is full height.
	var many []tlPoint
	for i := 0; i < 500; i++ {
		many = append(many, tlPoint{Date: strconv.Itoa(1000 + i*3), Title: "E"})
	}
	s = buildStrip(many, href)
	if e, _ := sum(s); e != 500 || len(s.Buckets) > tlBuckets {
		t.Errorf("500 events: %d counted in %d blocks", e, len(s.Buckets))
	}
	top := 0
	for _, b := range s.Buckets {
		top = max(top, b.LevE)
		if b.Events > 0 && (b.LevE < 1 || b.LevE > tlLevels || b.Href == "" || b.Text == "") {
			t.Errorf("block %+v", b)
		}
		if b.Events == 0 && (b.Href != "" || b.LevE != 0) {
			t.Errorf("an empty block must not be a link: %+v", b)
		}
	}
	if top != tlLevels {
		t.Errorf("the tallest block is level %d, want %d", top, tlLevels)
	}
	// A block's accessible sentence names its stretch, its count and its first titles.
	s = buildStrip(pts("1998-04", "1998-04", "1998-04", "1998-04", "1999-04"), href)
	if txt := s.Buckets[0].Text; !strings.Contains(txt, "4 events start") || !strings.Contains(txt, "T0, T1, T2 and 1 more") {
		t.Errorf("block sentence: %q", txt)
	}
}

func TestTimelineRangeKeepsTheOtherFilters(t *testing.T) {
	got := tlRange(url.Values{"entity": {"ent_1"}, "from": {"1990"}, "offset": {"50"}, "order": {"narrative"}}, "1998", "1999")
	if !strings.HasPrefix(got, "?") || !strings.HasSuffix(got, "#tl-events") || strings.Contains(got, "offset") || strings.Contains(got, "1990") ||
		!strings.Contains(got, "entity=ent_1") || !strings.Contains(got, "from=1998") || !strings.Contains(got, "to=1999") || !strings.Contains(got, "order=narrative") {
		t.Errorf("range link %q", got)
	}
}

// ---- the pages, against a real server ---------------------------------------------------------------------------------

type tlEnv struct {
	s                               *Server
	owner, editor, reader, projOnly caller
	stranger, admin, anon           caller
	cid, cslug, pid, pslug          string
	ana, ben, harbour, hill         string
	e1, e2, e3                      string
	arrival, departure              string
}

func newTLEnv(t *testing.T) tlEnv {
	t.Helper()
	s := newAuthzServer(t)
	e := tlEnv{s: s, anon: caller{name: "anon"}}
	e.owner = s.newCaller(t, "owner", ops.RoleWriter)
	e.editor = s.newCaller(t, "editor", ops.RoleWriter)
	e.reader = s.newCaller(t, "reader", ops.RoleWriter)
	e.projOnly = s.newCaller(t, "projonly", ops.RoleWriter)
	e.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	e.admin = s.newCaller(t, "admin", ops.RoleAdmin)
	ctx := context.Background()
	op := ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}
	call := func(name string, in any) any {
		t.Helper()
		out, err := s.Reg.Call(ctx, op, name, in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}
	c := call("compendium.create", compendiums.CreateIn{Name: "Salt Bible"}).(compendiums.Item).Item
	e.cid, e.cslug = c.ID, c.Slug
	pr := call("project.create", projects.CreateIn{Title: "The Salt Road", Kind: "novel"}).(projects.Item).Item
	e.pid, e.pslug = pr.ID, pr.Slug
	call("project.compendium.set", compendiums.SetIn{ID: pr.ID, CompendiumID: c.ID})
	s.addCompendiumMember(t, c.ID, e.editor.userID, ops.ProjectEditor)
	s.addCompendiumMember(t, c.ID, e.reader.userID, ops.ProjectReader)
	s.addMember(t, pr.ID, e.editor.userID, ops.ProjectEditor)
	s.addMember(t, pr.ID, e.reader.userID, ops.ProjectReader)
	s.addMember(t, pr.ID, e.projOnly.userID, ops.ProjectEditor)
	ent := func(kind, name string) string {
		return call("entity.create", entities.CreateIn{ID: c.ID, Kind: kind, Name: name}).(entities.Item).Item.ID
	}
	e.ana, e.ben, e.harbour, e.hill = ent("character", "Ana"), ent("character", "Ben"), ent("place", "The Harbour"), ent("place", "The Hill")
	ev := func(title, date, end, label, place string, who ...string) string {
		var ps []timeline.Participant
		for _, w := range who {
			ps = append(ps, timeline.Participant{Entity: w, Role: "there"})
		}
		return call("timeline.event.create", timeline.CreateIn{ID: c.ID, Title: title, StoryDate: date, StoryDateEnd: end, StoryLabel: label, Location: place, Participants: ps}).(timeline.EventItem).Item.ID
	}
	e.e1 = ev("Wedding", "1998-04-12", "", "Midsummer, Year 312", e.harbour, e.ana)
	e.e2 = ev("Funeral", "1998-04-12", "", "", e.hill, e.ana)
	e.e3 = ev("Voyage", "1999", "2000-02", "", "", e.ben)
	doc := func(title, date string) string {
		d := call("doc.create", docs.CreateIn{ProjectID: pr.ID, Title: title, Kind: "chapter"}).(docs.Item).Item
		call("doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, StoryDate: &date})
		return d.ID
	}
	e.arrival, e.departure = doc("Arrival", "1998-04-12"), doc("Departure", "1998-03-01")
	call("timeline.scene.link", timeline.LinkIn{ID: e.arrival, EventID: e.e1})
	return e
}

func (e tlEnv) get(t *testing.T, c caller, path string) (int, string) {
	t.Helper()
	rec := e.s.do(c, "GET", path, "")
	return rec.Code, rec.Body.String()
}

func (e tlEnv) page(t *testing.T, c caller, path string) string {
	t.Helper()
	code, body := e.get(t, c, path)
	if code != http.StatusOK {
		t.Fatalf("GET %s as %s: %d: %s", path, c.name, code, visibleText(body))
	}
	lint(t, path, body)
	structure(t, path, body)
	return body
}

func TestTimelineCompendiumPageAccess(t *testing.T) {
	e := newTLEnv(t)
	base := "/c/" + e.cslug + "/timeline"
	want := map[string]int{"anon": 303, "stranger": 404, "projonly": 404, "admin": 404, "reader": 200, "editor": 200, "owner": 200}
	for _, c := range []caller{e.anon, e.stranger, e.projOnly, e.admin, e.reader, e.editor, e.owner} {
		for _, p := range []string{base, base + "/events/" + e.e1} {
			if code, _ := e.get(t, c, p); code != want[c.name] {
				t.Errorf("GET %s as %s: %d, want %d", p, c.name, code, want[c.name])
			}
		}
	}
	// The new-event form is for editors: a reader is told so; a stranger learns nothing.
	for name, code := range map[string]int{"owner": 200, "editor": 200, "reader": 403, "stranger": 404, "projonly": 404, "anon": 303} {
		c := map[string]caller{"owner": e.owner, "editor": e.editor, "reader": e.reader, "stranger": e.stranger, "projonly": e.projOnly, "anon": e.anon}[name]
		if got, _ := e.get(t, c, base+"/events/new"); got != code {
			t.Errorf("new event as %s: %d, want %d", name, got, code)
		}
	}
	// The id route sends a member to the event's page, and a stranger to a 404 that matches a missing id.
	rec := e.s.do(e.reader, "GET", "/timeline/events/"+e.e1, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != base+"/events/"+e.e1 {
		t.Errorf("event id route: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	a, b := e.s.do(e.stranger, "GET", "/timeline/events/"+e.e1, ""), e.s.do(e.stranger, "GET", "/timeline/events/tle_00000000000000000000000000", "")
	if a.Code != 404 || b.Code != 404 || a.Body.String() != b.Body.String() {
		t.Errorf("a stranger tells an existing event from a missing one: %d %d", a.Code, b.Code)
	}
	if rec := e.s.do(e.anon, "GET", "/timeline/events/"+e.e1, ""); rec.Code != 303 {
		t.Errorf("anonymous event id route: %d", rec.Code)
	}
	// An event of another Compendium's slug is not found through this one.
	if code, _ := e.get(t, e.owner, "/c/not-there/timeline/events/"+e.e1); code != 404 {
		t.Errorf("an event under the wrong slug: %d", code)
	}
}

func TestTimelineCompendiumPageContent(t *testing.T) {
	e := newTLEnv(t)
	base := "/c/" + e.cslug + "/timeline"
	body := e.page(t, e.owner, base)
	txt := visibleText(body)
	for _, w := range []string{"Wedding", "Funeral", "Voyage", "Midsummer, Year 312", "1998-04-12", "1999 to 2000-02", "Add an event", "Check timeline", "Over story time", "Arrival"} {
		if !strings.Contains(txt, w) {
			t.Errorf("the Compendium page does not show %q", w)
		}
	}
	// The label sits beside the date; the date column is the sortable machine date.
	if !regexp.MustCompile(`(?s)tl-date">1998-04-12</span><span class="tl-lab">Midsummer, Year 312`).MatchString(body) {
		t.Error("the story label should sit beside the date")
	}
	// A reader reads and cannot add or edit.
	rb := e.page(t, e.reader, base)
	if strings.Contains(rb, "Add an event") || !strings.Contains(visibleText(rb), "You can read this timeline") {
		t.Error("a reader should see the timeline without the add button, and be told why")
	}
	// Filters: by entity, by date range, by book; the strip's block links use the same filter.
	f := e.page(t, e.owner, base+"?entity="+e.ben)
	if !strings.Contains(f, "Voyage") || strings.Contains(f, `id="e-`+e.e1+`"`) {
		t.Error("the entity filter should keep Voyage and drop the wedding")
	}
	f = e.page(t, e.owner, base+"?from=1998&to=1998-12")
	if !strings.Contains(f, `id="e-`+e.e1+`"`) || strings.Contains(f, `id="e-`+e.e3+`"`) {
		t.Error("the date filter should keep 1998 and drop the voyage")
	}
	f = e.page(t, e.owner, base+"?project="+e.pid)
	if !strings.Contains(f, `id="e-`+e.e1+`"`) || strings.Contains(f, `id="e-`+e.e2+`"`) {
		t.Error("the book filter should keep what Arrival tells and drop the rest")
	}
	if !strings.Contains(body, "?from=") || !strings.Contains(body, "#tl-events") {
		t.Error("the strip's blocks should link to a date filter of the table")
	}
	// A bad date is explained beside the filter, with the table still there.
	code, bad := e.get(t, e.owner, base+"?from=1998-4")
	if code != http.StatusUnprocessableEntity || !strings.Contains(visibleText(bad), "Use a date such as 1998, 1998-04 or 1998-04-12") || !strings.Contains(bad, `aria-invalid="true"`) || !strings.Contains(bad, "Wedding") {
		t.Errorf("a bad filter date: %d", code)
	}
	// Honest empty states.
	f = e.page(t, e.owner, base+"?entity="+e.hill+"&from=1999")
	if !strings.Contains(visibleText(f), "No event matches these filters") {
		t.Error("an empty filter result should say so")
	}
	f = e.page(t, e.owner, base+"?trash=1")
	if !strings.Contains(visibleText(f), "Nothing is in the trash") {
		t.Error("an empty trash should say so")
	}
	other := e.s.newCaller(t, "other-owner", ops.RoleWriter)
	c2 := e.s.mustCall(t, other, "compendium.create", compendiums.CreateIn{Name: "Empty One"}).(compendiums.Item).Item
	f = e.page(t, other, "/c/"+c2.Slug+"/timeline")
	if !strings.Contains(visibleText(f), "No events yet") || !strings.Contains(visibleText(f), "Nothing on this timeline has a date yet") {
		t.Errorf("an empty Compendium: %s", visibleText(f))
	}
}

func (s *Server) mustCall(t *testing.T, c caller, name string, in any) any {
	t.Helper()
	out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: c.userID, Role: ops.RoleWriter}, name, in)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func TestTimelineEventFormsAndOps(t *testing.T) {
	e := newTLEnv(t)
	s := e.s
	ctx := context.Background()
	base := "/c/" + e.cslug + "/timeline"
	form := func(kv ...string) url.Values {
		v := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			v.Add(kv[i], kv[i+1])
		}
		return v
	}
	// Create: the person's text is kept beside a plain-words refusal, then a good one redirects to the event.
	rec := s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", form("title", "Storm", "story_date", "1998-02-30"), "", false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Use a date such as 1998") || !strings.Contains(rec.Body.String(), `value="Storm"`) || !strings.Contains(rec.Body.String(), `aria-invalid="true"`) {
		t.Errorf("a bad date: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
	rec = s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", form("title", "Storm", "story_date", "1998-04-10", "story_date_end", "1998-04-01"), "", false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "The end is before the start") {
		t.Errorf("end before start: %d", rec.Code)
	}
	rec = s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", form("title", "Storm", "person_name", "Nobody", "person_role", "witness"), "", false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "No entity in this Compendium is called Nobody") || !strings.Contains(rec.Body.String(), `value="Nobody"`) {
		t.Errorf("an unknown person: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
	rec = s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", form("title", "Storm", "person_name", "", "person_role", "witness"), "", false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "Choose who took part for each role you typed") {
		t.Errorf("a role without a person: %d", rec.Code)
	}
	rec = s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", form("title", "", "story_date", "1998"), "", false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "An event needs a title") {
		t.Errorf("no title: %d", rec.Code)
	}
	rec = s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", form("title", "Storm", "story_date", "1998-04-10", "location", "ben", "person_name", "ana", "person_role", "caught in it", "person_name", "Ben", "person_role", ""), "", false)
	if rec.Code != 422 {
		// "ben" is a character but a valid place reference: the op accepts any entity, so this saves.
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("create: %d %s", rec.Code, visibleText(rec.Body.String()))
		}
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, base+"/events/") || !strings.HasSuffix(loc, "?saved=created") {
		t.Fatalf("create redirect %q", loc)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(loc, base+"/events/"), "?saved=created")
	body := e.page(t, e.editor, strings.TrimSuffix(loc, "?saved=created")+"?saved=created")
	if !strings.Contains(visibleText(body), "Event added.") || !strings.Contains(body, `value="Storm"`) || !strings.Contains(body, `value="caught in it"`) {
		t.Error("the new event's page should confirm and show its people")
	}
	// Update keeps the participants when they did not change, even one whose entity is in the trash (not shown).
	ev := s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: id}).(timeline.EventItem).Item
	rec = s.post(t, e.editor, "/ui/timeline/events/"+id, form("title", "Storm at sea", "story_date", "1998-04-10", "base_revision", strconv.Itoa(ev.Revision), "location", "ben", "person_name", "Ana", "person_role", "caught in it", "person_name", "Ben", "person_role", ""), "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
	now := s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: id}).(timeline.EventItem).Item
	if now.Title != "Storm at sea" || len(now.Participants) != 2 {
		t.Errorf("update: %+v", now)
	}
	// A participant whose entity is in the trash is not shown, so a save that did not touch the people must not drop them.
	ownerP := ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}
	if _, err := s.Reg.Call(ctx, ownerP, "entity.trash", entities.StateIn{ID: e.ben}); err != nil {
		t.Fatalf("entity.trash: %v", err)
	}
	ev = s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: id}).(timeline.EventItem).Item
	if len(ev.Participants) != 1 {
		t.Fatalf("a trashed entity should not be listed: %+v", ev.Participants)
	}
	rec = s.post(t, e.editor, "/ui/timeline/events/"+id, form("title", "Storm again", "story_date", "1998-04-10", "base_revision", strconv.Itoa(ev.Revision), "person_name", "Ana", "person_role", "caught in it"), "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update with a trashed participant: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
	if _, err := s.Reg.Call(ctx, ownerP, "entity.restore", entities.StateIn{ID: e.ben}); err != nil {
		t.Fatalf("entity.restore: %v", err)
	}
	if ev = s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: id}).(timeline.EventItem).Item; len(ev.Participants) != 2 || ev.Title != "Storm again" {
		t.Errorf("the trashed participant was dropped by a save that did not touch the people: %+v", ev.Participants)
	}
	// A stale revision is refused in plain words with what was typed kept.
	rec = s.post(t, e.editor, "/ui/timeline/events/"+id, form("title", "Stale", "base_revision", strconv.Itoa(ev.Revision-1)), "", false)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "changed somewhere else") || !strings.Contains(rec.Body.String(), `value="Stale"`) {
		t.Errorf("a stale save: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
	// A reader cannot change anything; the refusal is a 403, and a stranger gets the 404 of a missing event.
	for _, path := range []string{"/ui/timeline/events/" + id, "/ui/timeline/events/" + id + "/trash", "/ui/timeline/events/" + id + "/restore", "/ui/compendiums/" + e.cid + "/timeline/events"} {
		if rec := s.post(t, e.reader, path, form("title", "x"), "", false); rec.Code != 403 {
			t.Errorf("POST %s as a reader: %d", path, rec.Code)
		}
		if rec := s.post(t, e.stranger, path, form("title", "x"), "", false); rec.Code != 404 {
			t.Errorf("POST %s as a stranger: %d", path, rec.Code)
		}
		if rec := s.post(t, e.projOnly, path, form("title", "x"), "", false); rec.Code != 404 {
			t.Errorf("POST %s as someone on the book but not the Compendium: %d", path, rec.Code)
		}
		if rec := s.post(t, e.editor, path, form("title", "x"), "-", false); rec.Code != 403 {
			t.Errorf("POST %s without a token: %d", path, rec.Code)
		}
		if rec := s.post(t, e.anon, path, form("title", "x"), "-", false); rec.Code != 303 && rec.Code != 401 {
			t.Errorf("POST %s anonymous: %d", path, rec.Code)
		}
	}
	// Trash and restore, with the way back offered on the list.
	rec = s.post(t, e.editor, "/ui/timeline/events/"+id+"/trash", nil, "", false)
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "saved=trashed&event="+id) {
		t.Fatalf("trash: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	page := e.page(t, e.editor, base+"?saved=trashed&event="+id)
	if !strings.Contains(page, "/ui/timeline/events/"+id+"/restore") || !strings.Contains(visibleText(page), "Event moved to the trash.") || strings.Contains(page, `id="e-`+id+`"`) {
		t.Error("the list should confirm the move and offer Undo, and no longer list the event")
	}
	if tr := e.page(t, e.editor, base+"?trash=1"); !strings.Contains(tr, `id="e-`+id+`"`) {
		t.Error("the trash should list the event")
	}
	ep := e.page(t, e.editor, base+"/events/"+id)
	if !strings.Contains(visibleText(ep), "in the trash") || strings.Contains(ep, `id="tl-form"`) || !strings.Contains(ep, "Restore this event") {
		t.Error("a trashed event is read-only with a Restore button")
	}
	if rec := s.post(t, e.editor, "/ui/timeline/events/"+id+"/restore", nil, "", false); rec.Code != 303 {
		t.Errorf("restore: %d", rec.Code)
	}
	if !strings.Contains(e.page(t, e.editor, base), `id="e-`+id+`"`) {
		t.Error("a restored event is back on the list")
	}
}

func TestTimelineSceneLinks(t *testing.T) {
	e := newTLEnv(t)
	s := e.s
	base := "/c/" + e.cslug + "/timeline/events/" + e.e1
	body := e.page(t, e.editor, base)
	if !strings.Contains(body, "/p/"+e.pslug+"/d/"+e.arrival) || !strings.Contains(body, "Unlink") {
		t.Error("a linked scene is shown as a link to the chapter, with Unlink for an editor")
	}
	// The project the scene is in decides: choosing the book lists the scenes still free to link.
	opt := e.page(t, e.editor, base+"?book="+e.pid)
	if !strings.Contains(opt, `<option value="`+e.departure+`"`) || strings.Contains(opt, `<option value="`+e.arrival+`"`) {
		t.Error("the scene picker should offer Departure and not the scene that is already linked")
	}
	f := url.Values{"doc": {e.departure}, "book": {e.pid}}
	if rec := s.post(t, e.editor, "/ui/timeline/events/"+e.e1+"/scenes", f, "", false); rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "saved=linked") {
		t.Fatalf("link: %d", rec.Code)
	}
	if !strings.Contains(e.page(t, e.reader, base), "/p/"+e.pslug+"/d/"+e.departure) {
		t.Error("the link should show for a reader too")
	}
	if rec := s.post(t, e.editor, "/ui/timeline/events/"+e.e1+"/scenes/unlink", f, "", false); rec.Code != 303 {
		t.Errorf("unlink: %d", rec.Code)
	}
	// A Compendium editor who cannot edit the book is told so in words; a missing choice is explained.
	e2 := e.s.newCaller(t, "comp-only", ops.RoleWriter)
	e.s.addCompendiumMember(t, e.cid, e2.userID, ops.ProjectEditor)
	rec := s.post(t, e2, "/ui/timeline/events/"+e.e1+"/scenes", f, "", false)
	if rec.Code != 422 || !strings.Contains(visibleText(rec.Body.String()), "not the book that scene is in") {
		t.Errorf("a scene in a book the person cannot edit: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
	rec = s.post(t, e.editor, "/ui/timeline/events/"+e.e1+"/scenes", url.Values{"book": {e.pid}}, "", false)
	if rec.Code != 422 || !strings.Contains(visibleText(rec.Body.String()), "Choose a scene first") {
		t.Errorf("no scene chosen: %d", rec.Code)
	}
}

func TestTimelineProjectPage(t *testing.T) {
	e := newTLEnv(t)
	base := "/p/" + e.pslug + "/timeline"
	want := map[string]int{"anon": 303, "stranger": 404, "admin": 404, "reader": 200, "editor": 200, "owner": 200, "projonly": 200}
	for _, c := range []caller{e.anon, e.stranger, e.admin, e.reader, e.editor, e.owner, e.projOnly} {
		if code, _ := e.get(t, c, base); code != want[c.name] {
			t.Errorf("GET %s as %s: %d, want %d", base, c.name, code, want[c.name])
		}
	}
	story := e.page(t, e.owner, base)
	txt := visibleText(story)
	for _, w := range []string{"Wedding", "Funeral", "Voyage", "Departure", "Told in", "Check timeline"} {
		if !strings.Contains(txt, w) {
			t.Errorf("the story-order view does not show %q", w)
		}
	}
	// Story order: the dated scene before the wedding, events by date, the 1999 voyage last.
	if !(strings.Index(story, "Departure") < strings.Index(story, "Wedding") && strings.Index(story, "Funeral") < strings.Index(story, "Voyage")) {
		t.Error("story order is not by date")
	}
	if strings.Contains(story, `class="tl-mf"`) {
		t.Error("the story-order view has no move buttons")
	}
	nar := e.page(t, e.owner, base+"?order=narrative")
	if strings.Count(nar, `class="tl-mf"`) != 3 || !strings.Contains(nar, `value="earlier"`) || !strings.Contains(visibleText(nar), "Scenes never move here") {
		t.Errorf("the narrative view: %d move forms", strings.Count(nar, `class="tl-mf"`))
	}
	if rd := e.page(t, e.reader, base+"?order=narrative"); strings.Contains(rd, `class="tl-mf"`) {
		t.Error("a reader sees no move buttons")
	}
	if fl := e.page(t, e.owner, base+"?order=narrative&entity="+e.ben); strings.Contains(fl, `class="tl-mf"`) || !strings.Contains(visibleText(fl), "Moving is off while the list is filtered") {
		t.Error("moving is off while filtered, and says why")
	}
	// A person on the book who is not on the Compendium sees the dated scenes and none of the events.
	po := e.page(t, e.projOnly, base)
	if strings.Contains(po, "Wedding") || !strings.Contains(po, "Departure") || !strings.Contains(visibleText(po), "not open to you") {
		t.Error("someone not on the Compendium must not see its events, and is told some are left out")
	}
	// An undated scene counts toward the outline link.
	d := e.s.mustCall(t, e.owner, "doc.create", docs.CreateIn{ProjectID: e.pid, Title: "Loose", Kind: "scene"}).(docs.Item).Item
	_ = d
	if p := e.page(t, e.owner, base); !strings.Contains(p, "/p/"+e.pslug+"/outline") || !strings.Contains(visibleText(p), "neither a story date nor an event") {
		t.Error("scenes with no date should be counted with a link to the outline")
	}
	// A bad range is explained.
	if code, bad := e.get(t, e.owner, base+"?from=19x8"); code != 422 || !strings.Contains(visibleText(bad), "for the start of the range") {
		t.Errorf("a bad range: %d", code)
	}
	if f := e.page(t, e.owner, base+"?from=1999&to=2000"); !strings.Contains(f, `id="tl-row-`+e.e3+`"`) || strings.Contains(f, `id="tl-row-`+e.e1+`"`) {
		t.Error("the range keeps what overlaps it")
	}
	// A book with nothing on its timeline says so.
	pr := e.s.mustCall(t, e.owner, "project.create", projects.CreateIn{Title: "Blank", Kind: "novel"}).(projects.Item).Item
	if b := e.page(t, e.owner, "/p/"+pr.Slug+"/timeline"); !strings.Contains(visibleText(b), "Nothing is on this timeline yet") {
		t.Error("an empty book timeline should say so")
	}
}

func TestTimelineOrderForms(t *testing.T) {
	e := newTLEnv(t)
	s := e.s
	path := "/ui/projects/" + e.pid + "/timeline/order"
	order := func() []string {
		return s.mustCall(t, e.owner, "timeline.order.get", timeline.OrderIn{ID: e.pid}).(timeline.OrderOut).Events
	}
	start := order()
	if len(start) != 3 || start[0] != e.e1 {
		t.Fatalf("derived order %v", start)
	}
	// Move the first event later: it swaps with the next event, and the order becomes the book's own.
	rec := s.post(t, e.editor, path, url.Values{"event": {e.e1}, "dir": {"later"}, "offset": {"0"}}, "", false)
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "order=narrative") || !strings.Contains(rec.Header().Get("Location"), "saved=moved") || !strings.HasSuffix(rec.Header().Get("Location"), "#tl-row-"+e.e1) {
		t.Fatalf("move: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	got := order()
	if got[0] != start[1] || got[1] != e.e1 || got[2] != start[2] {
		t.Errorf("after a move later: %v from %v", got, start)
	}
	// The same move from the script is answered with no content; the server works out the swap from its own order.
	if rec := s.post(t, e.editor, path, url.Values{"event": {e.e1}, "dir": {"earlier"}}, "", true); rec.Code != http.StatusNoContent {
		t.Errorf("fetch move: %d", rec.Code)
	}
	if got := order(); got[0] != e.e1 {
		t.Errorf("after moving it back: %v", got)
	}
	// The first cannot go earlier: nothing is saved and nothing is refused.
	if rec := s.post(t, e.editor, path, url.Values{"event": {e.e1}, "dir": {"earlier"}}, "", false); rec.Code != 303 || strings.Contains(rec.Header().Get("Location"), "saved=") {
		t.Errorf("move the first earlier: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := s.post(t, e.editor, path, url.Values{"event": {"tle_nothing"}, "dir": {"later"}}, "", true); rec.Code != 422 {
		t.Errorf("an event that is not on the book: %d", rec.Code)
	}
	if rec := s.post(t, e.editor, path, url.Values{"event": {e.e1}, "dir": {"sideways"}}, "", true); rec.Code != 422 {
		t.Errorf("a direction that is not one: %d", rec.Code)
	}
	// The page shows the book's own order, and Reset goes back to the scenes'.
	s.post(t, e.editor, path, url.Values{"event": {e.e1}, "dir": {"later"}}, "", false)
	nar := e.page(t, e.owner, "/p/"+e.pslug+"/timeline?order=narrative")
	if !strings.Contains(visibleText(nar), "This book has its own order of events") || !strings.Contains(nar, `name="reset"`) {
		t.Error("the page should say the book has its own order and offer a reset")
	}
	if rec := s.post(t, e.editor, path, url.Values{"reset": {"1"}}, "", false); rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "saved=reset") {
		t.Errorf("reset: %d", rec.Code)
	}
	if got := order(); got[0] != e.e1 {
		t.Errorf("after reset: %v", got)
	}
	// Access.
	for name, c := range map[string]caller{"reader": e.reader, "stranger": e.stranger, "projonly": e.projOnly} {
		want := map[string]int{"reader": 403, "stranger": 404, "projonly": 422}[name]
		if rec := s.post(t, c, path, url.Values{"event": {e.e1}, "dir": {"later"}}, "", false); rec.Code != want {
			t.Errorf("order post as %s: %d, want %d", name, rec.Code, want)
		}
	}
	if rec := s.post(t, e.editor, path, url.Values{"event": {e.e1}, "dir": {"later"}}, "-", false); rec.Code != 403 {
		t.Errorf("order post without a token: %d", rec.Code)
	}
	if rec := s.post(t, e.anon, path, url.Values{"event": {e.e1}, "dir": {"later"}}, "-", false); rec.Code != 303 && rec.Code != 401 {
		t.Errorf("order post anonymous: %d", rec.Code)
	}
}

func TestTimelineCheckPanels(t *testing.T) {
	e := newTLEnv(t)
	// The Compendium's check finds the same person at two places, with links to both events and to the person.
	body := e.page(t, e.reader, "/c/"+e.cslug+"/timeline?check=1")
	txt := visibleText(body)
	if !strings.Contains(txt, "Two places at once") || !strings.Contains(txt, "Ana cannot be at") || !strings.Contains(body, "/timeline/events/"+e.e1) || !strings.Contains(body, "/timeline/events/"+e.e2) ||
		!strings.Contains(body, "?entity="+e.ana) {
		t.Errorf("the Compendium's check: %s", txt)
	}
	if code, frag := e.get(t, e.reader, "/ui/compendiums/"+e.cid+"/timeline/check"); code != 200 || !strings.Contains(frag, "Two places at once") || strings.Contains(frag, "<html") {
		t.Errorf("the check fragment: %d", code)
	}
	for _, c := range []caller{e.stranger, e.projOnly} {
		if code, _ := e.get(t, c, "/ui/compendiums/"+e.cid+"/timeline/check"); code != 404 {
			t.Errorf("the check fragment for %s: %d", c.name, code)
		}
	}
	if code, _ := e.get(t, e.anon, "/ui/compendiums/"+e.cid+"/timeline/check"); code != 303 && code != 401 {
		t.Errorf("anonymous check: %d", code)
	}
	// A book's check adds what its scenes say: Departure is told after the wedding but happened before it.
	pb := e.page(t, e.reader, "/p/"+e.pslug+"/timeline?check=1")
	ptxt := visibleText(pb)
	if !strings.Contains(ptxt, "Told out of order") || !strings.Contains(ptxt, "flashback") || !strings.Contains(pb, "/p/"+e.pslug+"/d/"+e.departure) {
		t.Errorf("the book's check: %s", ptxt)
	}
	if code, frag := e.get(t, e.projOnly, "/ui/projects/"+e.pid+"/timeline/check"); code != 200 || strings.Contains(frag, "Two places at once") || !strings.Contains(frag, "not open to you") {
		t.Errorf("a book member not on the Compendium: %d", code)
	}
	if code, _ := e.get(t, e.stranger, "/ui/projects/"+e.pid+"/timeline/check"); code != 404 {
		t.Errorf("a stranger's check: %d", code)
	}
}

// ---- five hundred events ----------------------------------------------------------------------------------------------

func TestTimelineAtFiveHundredEvents(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 500 events")
	}
	e := newTLEnv(t)
	s := e.s
	for i := 0; i < 500; i++ {
		date := strconv.Itoa(1900+i/5) + "-" + pad2(1+i%12)
		s.mustCall(t, e.owner, "timeline.event.create", timeline.CreateIn{ID: e.cid, Title: "Event " + strconv.Itoa(i), StoryDate: date,
			Description: "The harbour at low tide, number " + strconv.Itoa(i) + ".", Location: "The Harbour", Participants: []timeline.Participant{{Entity: "Ana", Role: "there"}, {Entity: "Ben"}}})
	}
	measure := func(label, path string) {
		var best time.Duration = time.Hour
		var body string
		for i := 0; i < 3; i++ {
			start := time.Now()
			rec := s.do(e.owner, "GET", path, "")
			d := time.Since(start)
			if rec.Code != 200 {
				t.Fatalf("%s: %d", path, rec.Code)
			}
			body = rec.Body.String()
			best = min(best, d)
		}
		elems := len(regexp.MustCompile(`<[a-zA-Z]`).FindAllString(body, -1))
		t.Logf("%-34s %4d ms  %7d bytes  %5d elements", label, best.Milliseconds(), len(body), elems)
		t.Run("time "+label, func(t *testing.T) {
			testutil.Timing(t)
			if best > 3*time.Second {
				t.Errorf("%s took %v", label, best)
			}
		})
		if elems > 6000 {
			t.Errorf("%s has %d elements", label, elems)
		}
	}
	measure("Compendium timeline, first page", "/c/"+e.cslug+"/timeline")
	measure("Compendium timeline, last page", "/c/"+e.cslug+"/timeline?offset=450")
	measure("Compendium timeline, one block", "/c/"+e.cslug+"/timeline?from=1950&to=1959")
	measure("Compendium timeline with the check", "/c/"+e.cslug+"/timeline?check=1")
	measure("Book timeline, story order", "/p/"+e.pslug+"/timeline")
	measure("Book timeline, as told", "/p/"+e.pslug+"/timeline?order=narrative")
	measure("Book timeline with the check", "/p/"+e.pslug+"/timeline?check=1")
	body := s.do(e.owner, "GET", "/c/"+e.cslug+"/timeline", "").Body.String()
	if n := strings.Count(body, `class="tl-blk`); n > tlBuckets+1 {
		t.Errorf("the strip has %d blocks for 500 events", n)
	}
	if n := strings.Count(body, `<tr id="e-`); n != tlPageSize {
		t.Errorf("the table has %d rows, want one page of %d", n, tlPageSize)
	}
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// ---- the holes the first pass left: a place in the trash, the id routes for every caller, a trashed or archived Compendium

func TestTimelineAPlaceInTheTrashIsNotClearedBySavingTheTitle(t *testing.T) {
	e := newTLEnv(t)
	s := e.s
	owner := ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}
	if _, err := s.Reg.Call(context.Background(), owner, "entity.trash", entities.StateIn{ID: e.harbour}); err != nil {
		t.Fatal(err)
	}
	ev := s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: e.e1}).(timeline.EventItem).Item
	if ev.Location != nil {
		t.Fatalf("a trashed place should not be listed: %+v", ev.Location)
	}
	form := url.Values{"title": {"Wedding, renamed"}, "story_date": {"1998-04-12"}, "base_revision": {strconv.Itoa(ev.Revision)}, "person_name": {"Ana"}, "person_role": {"there"}}
	if rec := s.post(t, e.editor, "/ui/timeline/events/"+e.e1, form, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
	if _, err := s.Reg.Call(context.Background(), owner, "entity.restore", entities.StateIn{ID: e.harbour}); err != nil {
		t.Fatal(err)
	}
	ev = s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: e.e1}).(timeline.EventItem).Item
	if ev.Title != "Wedding, renamed" || ev.Location == nil || ev.Location.ID != e.harbour {
		t.Errorf("the place was cleared by a save that did not touch it: %+v", ev.Location)
	}
	// Changing it on purpose still works, and so does clearing it.
	form.Set("base_revision", strconv.Itoa(ev.Revision))
	form.Set("location", "The Hill")
	if rec := s.post(t, e.editor, "/ui/timeline/events/"+e.e1, form, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("change the place: %d", rec.Code)
	}
	if ev = s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: e.e1}).(timeline.EventItem).Item; ev.Location == nil || ev.Location.ID != e.hill {
		t.Errorf("the place was not changed: %+v", ev.Location)
	}
	form.Set("base_revision", strconv.Itoa(ev.Revision))
	form.Set("location", "")
	s.post(t, e.editor, "/ui/timeline/events/"+e.e1, form, "", false)
	if ev = s.mustCall(t, e.owner, "timeline.event.get", timeline.GetIn{ID: e.e1}).(timeline.EventItem).Item; ev.Location != nil {
		t.Errorf("the place was not cleared on purpose: %+v", ev.Location)
	}
}

func TestTimelineIDRoutesForEveryCaller(t *testing.T) {
	e := newTLEnv(t)
	s := e.s
	type route struct {
		name, method, path string
		form               url.Values
		editor             bool // a POST that needs an editor
	}
	ev := func(suffix string) string { return "/ui/timeline/events/" + e.e3 + suffix }
	routes := []route{
		{"create", "POST", "/ui/compendiums/" + e.cid + "/timeline/events", url.Values{"title": {"By id"}}, true},
		{"update", "POST", ev(""), url.Values{"title": {"Renamed"}}, true},
		{"link", "POST", ev("/scenes"), url.Values{"doc": {e.departure}, "book": {e.pid}}, true},
		{"unlink", "POST", ev("/scenes/unlink"), url.Values{"doc": {e.departure}, "book": {e.pid}}, true},
		{"trash", "POST", ev("/trash"), nil, true},
		{"restore", "POST", ev("/restore"), nil, true},
		{"order", "POST", "/ui/projects/" + e.pid + "/timeline/order", url.Values{"event": {e.e1}, "dir": {"later"}}, true},
		{"event id route", "GET", "/timeline/events/" + e.e1, nil, false},
		{"compendium check", "GET", "/ui/compendiums/" + e.cid + "/timeline/check", nil, false},
		{"book check", "GET", "/ui/projects/" + e.pid + "/timeline/check", nil, false},
		{"rows", "GET", "/ui/projects/" + e.pid + "/timeline/rows?order=narrative", nil, false},
	}
	do := func(c caller, r route, token string) int {
		if r.method == "GET" {
			return s.do(c, "GET", r.path, "").Code
		}
		return s.post(t, c, r.path, r.form, token, false).Code
	}
	for _, r := range routes {
		onCompendium := !strings.Contains(r.path, "/projects/")
		// Anonymous: a sign-in redirect (or 401); a POST without the token: 403.
		if got := do(e.anon, r, "-"); got != 303 && got != 401 {
			t.Errorf("%s anonymous: %d", r.name, got)
		}
		if r.method == "POST" {
			if got := do(e.editor, r, "-"); got != 403 {
				t.Errorf("%s without a token: %d", r.name, got)
			}
		}
		// A stranger learns nothing.
		if got := do(e.stranger, r, ""); got != 404 {
			t.Errorf("%s as a stranger: %d, want 404", r.name, got)
		}
		// A reader of the Compendium and of the book reads, and cannot change.
		want := 200
		if r.method == "GET" && r.name == "event id route" {
			want = 303
		}
		if r.editor {
			want = 403
		}
		if got := do(e.reader, r, ""); got != want {
			t.Errorf("%s as a reader: %d, want %d", r.name, got, want)
		}
		// Someone on the book and not the Compendium: the Compendium's routes are 404, the book's open without its events.
		if onCompendium && r.name != "link" && r.name != "unlink" {
			if got := do(e.projOnly, r, ""); got != 404 {
				t.Errorf("%s as someone not on the Compendium: %d, want 404", r.name, got)
			}
		}
	}
	// An administrator who is not a member is a stranger on the pages: the id route answers 404 as the slug pages do.
	rec := s.post(t, e.admin, "/ui/compendiums/"+e.cid+"/timeline/events", url.Values{"title": {"By an administrator"}}, "", false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("administrator create: %d, want 404", rec.Code)
	}
	for _, r := range routes {
		if r.name == "create" || r.name == "link" || r.name == "unlink" || r.name == "trash" || r.name == "restore" {
			continue
		}
		if got := do(e.admin, r, ""); got != http.StatusNotFound {
			t.Errorf("%s as an administrator who is not a member: %d, want 404", r.name, got)
		}
	}
	// A reader can neither link nor unlink, whatever the book says.
	for _, r := range routes {
		if r.name == "link" || r.name == "unlink" {
			if got := do(e.reader, r, ""); got != 403 {
				t.Errorf("%s as a reader: %d", r.name, got)
			}
		}
	}
}

func TestTimelineTrashedAndArchivedCompendium(t *testing.T) {
	e := newTLEnv(t)
	s := e.s
	call := func(op string) {
		t.Helper()
		if _, err := s.Reg.Call(context.Background(), ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}, op, compendiums.StateIn{ID: e.cid}); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
	base := "/c/" + e.cslug + "/timeline"
	for _, step := range []struct{ op, note string }{{"compendium.archive", "is archived"}, {"compendium.unarchive", ""}, {"compendium.trash", "is in the trash"}} {
		call(step.op)
		body := e.page(t, e.owner, base)
		if step.note == "" {
			continue
		}
		txt := visibleText(body)
		if !strings.Contains(txt, step.note) || strings.Contains(body, "Add an event") || !strings.Contains(txt, "Wedding") {
			t.Errorf("%s: the page should read, say why it is read-only and offer no add button: %s", step.op, txt[:min(len(txt), 400)])
		}
		ep := e.page(t, e.owner, base+"/events/"+e.e1)
		if strings.Contains(ep, `id="tl-form"`) {
			t.Errorf("%s: the event page offers a form", step.op)
		}
		if code, _ := e.get(t, e.owner, base+"/events/new"); code != http.StatusForbidden {
			t.Errorf("%s: the new-event form: %d, want 403", step.op, code)
		}
		if code, _ := e.get(t, e.owner, "/ui/compendiums/"+e.cid+"/timeline/check"); code != 200 {
			t.Errorf("%s: the check fragment: %d", step.op, code)
		}
		if code, _ := e.get(t, e.owner, "/p/"+e.pslug+"/timeline"); code != 200 {
			t.Errorf("%s: the book's page: %d", step.op, code)
		}
	}
	// Writing by id to a trashed Compendium is the ops' to decide: the page shows what they answer, never a 500.
	rec := s.post(t, e.owner, "/ui/timeline/events/"+e.e3, url.Values{"title": {"Renamed in the trash"}}, "", false)
	t.Logf("a save to an event of a trashed Compendium answers %d", rec.Code)
	if rec.Code >= 500 {
		t.Errorf("a save to a trashed Compendium: %d", rec.Code)
	}
	call("compendium.restore")
	call("compendium.archive")
	rec = s.post(t, e.owner, "/ui/timeline/events/"+e.e3, url.Values{"title": {"Renamed while archived"}}, "", false)
	t.Logf("a save to an event of an archived Compendium answers %d", rec.Code)
	if rec.Code >= 500 {
		t.Errorf("a save to an archived Compendium: %d", rec.Code)
	}
}
