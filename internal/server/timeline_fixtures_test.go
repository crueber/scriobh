package server

import (
	"net/url"
	"strconv"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/domain/vocab"
)

// Fixtures for the timeline pages, added to the template lint in web_test.go: every state a page can be in, with
// awkward text (markup, long words, a hundred participants' worth of names) so the lint sees the worst of it.

func tlFixtureComp(role string) compendiums.Compendium {
	return compendiums.Compendium{ID: "cmp_a", Slug: "the-salt-bible", Name: "The Salt Road Bible <i>", Role: role}
}

func tlFixtureEvents(n int) []timeline.Event {
	people := []timeline.PersonOut{
		{Entity: vocab.Brief{ID: "ent_a", Name: "Máire Nolan", Kind: "character"}, Role: "host"},
		{Entity: vocab.Brief{ID: "ent_b", Name: "Ciara Ní Bhriain", Kind: "character"}},
	}
	var out []timeline.Event
	for i := 0; i < n; i++ {
		e := timeline.Event{ID: "tle_" + strconv.Itoa(i), CompendiumID: "cmp_a", Title: "The harbour opens <b>" + strconv.Itoa(i) + "</b>", Description: "# Heading\nA long first line. " + "word ",
			StoryDate: "1998-04-12", StoryLabel: "Midsummer, Year 312", Dated: true, Participants: people, Revision: 1,
			Location: &vocab.Brief{ID: "ent_p", Name: "The Harbour", Kind: "place"},
			Scenes:   []timeline.SceneLink{{ID: "doc_1", Title: "The arrival", Kind: "scene", ProjectID: "prj_a", ProjectTitle: "The Salt Road"}}}
		switch i % 3 {
		case 1:
			e.StoryDate, e.StoryDateEnd, e.StoryLabel = "1998", "1999-02", ""
		case 2:
			e.StoryDate, e.Dated, e.Location, e.Scenes, e.Participants = "", false, nil, nil, nil
		}
		out = append(out, e)
	}
	return out
}

func tlFixturePoints() []tlPoint {
	var pts []tlPoint
	for i, d := range []string{"1990", "1998-04", "1998-06-01", "2004", "2004-02", "2011", "", "1999", "-0044"} {
		pts = append(pts, tlPoint{Date: d, Title: "Thing " + strconv.Itoa(i), Scene: i%4 == 3})
	}
	return pts
}

func tlFixtureCheck(project bool) tlCheck {
	out := timeline.CheckOut{Errors: 2, Warnings: 2, Events: 12, Scenes: 9, Findings: []timeline.Finding{
		{ID: "f1", Kind: "location_conflict", Severity: "error", Message: "Máire Nolan cannot be at The Harbour for \"Wedding\" (1998-04-12) and at The Hill for \"Funeral\" (1998-04-12): the two overlap.",
			Events:   []timeline.EventRef{{ID: "tle_1", Title: "Wedding", StoryDate: "1998-04-12"}, {ID: "tle_2", Title: "Funeral", StoryDate: "1998-04-12"}},
			Entities: []vocab.Brief{{ID: "ent_a", Name: "Máire Nolan"}}},
		{ID: "f2", Kind: "before_birth", Severity: "error", Message: "Ciara takes part in \"Wedding\" (1998-04-12), before their birth (2001).",
			Events: []timeline.EventRef{{ID: "tle_1", Title: "Wedding", StoryDate: "1998-04-12"}}, Entities: []vocab.Brief{{ID: "ent_b", Name: "Ciara"}}},
		{ID: "f3", Kind: "narrated_out_of_order", Severity: "warning", Message: "\"Funeral\" (1998) is told after \"Wedding\" (1999), which happens later. A flashback is fine; check that it is meant.",
			Events: []timeline.EventRef{{ID: "tle_2", Title: "Funeral"}},
			Scenes: []timeline.SceneRef{{ID: "doc_1", Title: "The arrival", Breadcrumb: []timeline.Crumb{{ID: "doc_0", Title: "Part one"}, {ID: "doc_1", Title: "The arrival"}}}}},
		{ID: "f4", Kind: "unreadable_date", Severity: "warning", Message: "3 scene(s) have a story date the timeline cannot read, so they are left off it.",
			Scenes: []timeline.SceneRef{{ID: "doc_2", Title: "", StoryDate: "last spring"}}},
	}}
	if !project {
		out.Scenes = 0
		out.Findings = out.Findings[:2]
		out.Warnings = 0
	}
	return buildCheck(out, tlLinker{ProjectSlug: "p-a", EntityBase: "/p/p-a/timeline"}, project)
}

func tlFixtureComp1(role string, rows int, f tlFilter) tlCompData {
	c := tlFixtureComp(role)
	can, why := tlCanEdit(c)
	d := tlCompData{C: c, Slug: c.Slug, CSRF: "tok_writer", CanEdit: can, Why: why, Filter: f, Filtered: f.active(),
		Entities: []entities.Entry{{ID: "ent_a", Name: "Máire Nolan", Kind: "character"}, {ID: "ent_p", Name: "The Harbour", Kind: "place"}},
		Projects: []compendiums.ProjectUse{{ID: "prj_a", Title: "The Salt Road", Relation: "primary"}}}
	d.Rows = tlRows(tlFixtureEvents(rows), c.Slug, map[string]tlBook{"prj_a": {ID: "prj_a", Slug: "p-a", Title: "The Salt Road", Editable: true}})
	d.Total = rows
	if rows > 0 {
		d.First, d.Last = 1, rows
	}
	d.Strip = buildStrip(tlFixturePoints(), func(from, to string) string { return tlRange(url.Values{}, from, to) })
	return d
}

func tlFixtureProj(order string, movable bool, rows int) tlProjData {
	d := tlProjData{Project: fixtureProjects()[0], Slug: "p-a", ID: "prj_a", CSRF: "tok_writer", CanEdit: true, Order: order, Movable: movable, All: rows, Total: rows,
		Entities: []tlEntityOpt{{ID: "ent_a", Name: "Máire Nolan"}}}
	for i := 0; i < rows; i++ {
		r := tlPRow{Key: "event:tle_" + strconv.Itoa(i), ID: "tle_" + strconv.Itoa(i), Kind: "event", IsEvent: true, Title: "The harbour opens <b>" + strconv.Itoa(i) + "</b>", When: "1998-04-12", Dated: true, Pos: i + 1,
			Href: "/timeline/events/tle_" + strconv.Itoa(i), Label: "Midsummer", Place: &vocab.Brief{ID: "ent_p", Name: "The Harbour"},
			People: []timeline.PersonOut{{Entity: vocab.Brief{ID: "ent_a", Name: "Máire Nolan"}, Role: "host"}}, PeopleMore: 2,
			Told: []tlTold{{Title: "The arrival", Href: "/p/p-a/d/doc_1", Where: "Part one"}}, CanUp: i > 0, CanDown: i < rows-1}
		if i%4 == 3 {
			r.Key, r.Kind, r.IsEvent, r.Told, r.Place, r.People, r.PeopleMore, r.Pov = "scene:doc_"+strconv.Itoa(i), "scene", false, nil, nil, nil, 0, &vocab.Brief{ID: "ent_a", Name: "Máire Nolan"}
			r.Href = "/p/p-a/d/doc_" + strconv.Itoa(i)
		}
		if i%5 == 4 {
			r.Dated, r.When, r.Label = false, "Undated", ""
		}
		d.Rows = append(d.Rows, r)
	}
	if rows > 0 {
		d.First, d.Last = 1, rows
	}
	d.Strip = buildStrip(tlFixturePoints(), func(from, to string) string { return tlRange(url.Values{"order": {order}}, from, to) })
	return d
}

func tlFixtureEvent(role string, mode string) tlEventData {
	c := tlFixtureComp(role)
	d := tlEventBaseFor(c, "tok_writer")
	e := tlFixtureEvents(1)[0]
	e.Scenes = append(e.Scenes, timeline.SceneLink{ID: "doc_9", Title: "", Kind: "chapter", ProjectID: "prj_x", ProjectTitle: "Someone else's book"})
	switch mode {
	case "new":
		d.New = true
	default:
		d.E, d.V = e, tlFormOf(e)
	}
	if mode == "trashed" {
		d.CanRestore, d.CanEdit, d.Why = d.CanEdit, false, "This event is in the trash. Restore it to edit it."
	}
	d.Rows = append(d.Rows, d.V.People...)
	for i := 0; i < tlBlankRows; i++ {
		d.Rows = append(d.Rows, tlPerson{})
	}
	if mode == "error" {
		d.Error, d.Field = "The end is before the start.", "story_date_end"
		d.V.Date, d.V.End = "1998-04-12", "1998-04-01"
	}
	if mode == "linking" {
		d.Books = []tlBook{{ID: "prj_a", Slug: "p-a", Title: "The Salt Road", Editable: true}}
		d.Linkable, d.Book = true, "prj_a"
		d.Options = []tlSceneOpt{{ID: "doc_3", Title: "Low tide", Where: "Part one"}, {ID: "doc_4", Title: "High tide"}}
		d.LinkErr = "You can change this Compendium, but not the book that scene is in, so the link was not changed."
	}
	if d.CanEdit && !d.New {
		d.Scenes = tlRows([]timeline.Event{e}, c.Slug, map[string]tlBook{"prj_a": {ID: "prj_a", Slug: "p-a", Title: "The Salt Road", Editable: true}})[0].SceneRows
	} else if !d.New {
		d.Scenes = tlRows([]timeline.Event{e}, c.Slug, map[string]tlBook{})[0].SceneRows
	}
	return d
}

// pageFixtures and fragmentFixtures are the base lists of web_test.go with the timeline's added.
func pageFixtures() []fixture {
	f := append(append(append(append(append(append(basePageFixtures(), tlPageFixtures()...), p5PageFixtures()...), p6PageFixtures()...), p7MailPageFixtures()...), p7PageFixtures()...), desktopPageFixtures()...)
	return append(append(append(f, studioPageFixtures()...), coverPageFixtures()...), importPageFixtures()...)
}
func fragmentFixtures() []fragmentFixture {
	f := append(append(append(baseFragmentFixtures(), tlFragmentFixtures()...), p7MailFragmentFixtures()...), p7FragmentFixtures()...)
	return append(append(append(f, studioFragmentFixtures()...), lookupFragmentFixtures()...), importFragmentFixtures()...)
}

func tlPageFixtures() []fixture {
	writer := Page{Title: "Timeline", Path: "/c/the-salt-bible/timeline", User: fixtureUser("writer"), CSRF: "tok_writer"}
	with := func(title, path string, data any) Page {
		p := writer
		p.Title, p.Path, p.Data = title, path, data
		return p
	}
	empty := tlFixtureComp1("owner", 0, tlFilter{})
	empty.Strip = buildStrip(nil, nil)
	filtered := tlFixtureComp1("editor", 0, tlFilter{Entity: "ent_a", From: "1998", To: "1999"})
	bad := tlFixtureComp1("editor", 3, tlFilter{})
	bad.Error, bad.Field = "Use a date such as 1998, 1998-04 or 1998-04-12.", "from"
	checked := tlFixtureComp1("owner", 3, tlFilter{})
	ck := tlFixtureCheck(false)
	checked.Check = &ck
	checked.Saved, checked.UndoID = tlSaved("trashed"), "tle_1"
	trash := tlFixtureComp1("owner", 2, tlFilter{Trash: true})
	paged := tlFixtureComp1("owner", 50, tlFilter{})
	paged.Total, paged.Prev, paged.Next = 120, "?#tl-events", "?offset=50#tl-events"
	emptyProj := tlFixtureProj("story", false, 0)
	emptyProj.Strip = buildStrip(nil, nil)
	emptyProj.Unplaced, emptyProj.Hidden = 3, true
	pc := tlFixtureProj("narrative", true, 6)
	pck := tlFixtureCheck(true)
	pc.Check, pc.Explicit, pc.Saved = &pck, true, tlSaved("moved")
	roProj := tlFixtureProj("narrative", false, 4)
	roProj.CanEdit = false
	filteredProj := tlFixtureProj("story", false, 0)
	filteredProj.Filter, filteredProj.Filtered, filteredProj.Total, filteredProj.All = tlFilter{Entity: "ent_a", From: "1990x"}, true, 0, 5
	filteredProj.Error, filteredProj.Field = "Use a date such as 1998, 1998-04 or 1998-04-12 for the start of the range.", "from"
	return []fixture{
		{"timeline", with("Timeline of The Salt Road Bible", "/c/the-salt-bible/timeline", tlFixtureComp1("owner", 5, tlFilter{}))},
		{"timeline", with("Timeline", "/c/the-salt-bible/timeline", tlFixtureComp1("reader", 3, tlFilter{}))},
		{"timeline", with("Timeline", "/c/the-salt-bible/timeline", empty)},
		{"timeline", with("Timeline", "/c/the-salt-bible/timeline", filtered)},
		{"timeline", with("Timeline", "/c/the-salt-bible/timeline", bad)},
		{"timeline", with("Timeline", "/c/the-salt-bible/timeline", checked)},
		{"timeline", with("Timeline", "/c/the-salt-bible/timeline", trash)},
		{"timeline", with("Timeline", "/c/the-salt-bible/timeline", paged)},
		{"timeline-event", with("New event", "/c/the-salt-bible/timeline/events/new", tlFixtureEvent("owner", "new"))},
		{"timeline-event", with("Event", "/c/the-salt-bible/timeline/events/tle_0", tlFixtureEvent("editor", "edit"))},
		{"timeline-event", with("Event", "/c/the-salt-bible/timeline/events/tle_0", tlFixtureEvent("editor", "error"))},
		{"timeline-event", with("Event", "/c/the-salt-bible/timeline/events/tle_0", tlFixtureEvent("editor", "linking"))},
		{"timeline-event", with("Event", "/c/the-salt-bible/timeline/events/tle_0", tlFixtureEvent("reader", "edit"))},
		{"timeline-event", with("Event", "/c/the-salt-bible/timeline/events/tle_0", tlFixtureEvent("owner", "trashed"))},
		{"project-timeline", with("Timeline of The Salt Road", "/p/p-a/timeline", tlFixtureProj("story", false, 6))},
		{"project-timeline", with("Timeline of The Salt Road", "/p/p-a/timeline", pc)},
		{"project-timeline", with("Timeline of The Salt Road", "/p/p-a/timeline", roProj)},
		{"project-timeline", with("Timeline of The Salt Road", "/p/p-a/timeline", emptyProj)},
		{"project-timeline", with("Timeline of The Salt Road", "/p/p-a/timeline", filteredProj)},
	}
}

func tlFragmentFixtures() []fragmentFixture {
	empty := tlFixtureCheck(true)
	empty.Findings, empty.Errs, empty.Warns, empty.Errors, empty.Warnings = nil, nil, nil, 0, 0
	calm := empty
	calm.Indexing, calm.Hidden = true, true
	cempty := tlFixtureCheck(false)
	cempty.Findings, cempty.Errs, cempty.Warns, cempty.Errors, cempty.Warnings = nil, nil, nil, 0, 0
	return []fragmentFixture{
		{"timeline-parts", "tl-check", tlFixtureCheck(true)},
		{"timeline-parts", "tl-check", tlFixtureCheck(false)},
		{"timeline-parts", "tl-check", empty},
		{"timeline-parts", "tl-check", calm},
		{"timeline-parts", "tl-check", cempty},
		{"timeline-parts", "tl-strip", tlFixtureComp1("owner", 3, tlFilter{}).Strip},
		{"timeline-parts", "tl-rows", tlFixtureComp1("owner", 4, tlFilter{})},
		{"timeline-parts", "tl-ptable", tlFixtureProj("narrative", true, 5)},
		{"timeline-parts", "tl-ptable", tlFixtureProj("story", false, 5)},
	}
}
