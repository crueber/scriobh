package timeline_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

func TestEventLifecycle(t *testing.T) {
	x := newH(t)
	// "1998-4-12" is not a date: the day needs two digits.
	x.expect(x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "X", StoryDate: "1998-4-12"}, ops.CodeInvalid)
	ev := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "  The   wedding ", Description: "At the **harbour**.",
		StoryDate: "1998-04-12T15:00", StoryLabel: "Midsummer", Location: "dublin",
		Participants: []timeline.Participant{{Entity: "Máire", Role: "bride"}, {Entity: x.tomas, Role: "witness"}}}).(timeline.EventItem).Item
	if ev.Title != "The wedding" || ev.StoryDate != "1998-04-12T15:00" || ev.Location == nil || ev.Location.ID != x.dublin || len(ev.Participants) != 2 ||
		ev.Participants[0].Entity.ID != x.maire || ev.Participants[0].Role != "bride" || !ev.Dated || ev.Revision != 1 {
		t.Fatalf("created: %+v", ev)
	}
	// Validation.
	for name, in := range map[string]timeline.CreateIn{
		"no title":               {ID: x.cid},
		"a title that is long":   {ID: x.cid, Title: strings.Repeat("x", 201)},
		"a date that is not":     {ID: x.cid, Title: "X", StoryDate: "last spring"},
		"an end without a start": {ID: x.cid, Title: "X", StoryDateEnd: "1999"},
		"an end before start":    {ID: x.cid, Title: "X", StoryDate: "1999-05", StoryDateEnd: "1999-04-30"},
		"an impossible day":      {ID: x.cid, Title: "X", StoryDate: "1999-02-29"},
		"an unknown person":      {ID: x.cid, Title: "X", Participants: []timeline.Participant{{Entity: "Nobody"}}},
		"a person twice":         {ID: x.cid, Title: "X", Participants: []timeline.Participant{{Entity: "Máire"}, {Entity: x.maire}}},
		"an unknown place":       {ID: x.cid, Title: "X", Location: "Atlantis"},
		"a long label":           {ID: x.cid, Title: "X", StoryLabel: strings.Repeat("l", 121)},
	} {
		if _, err := x.reg.Call(x.ctx, x.owner, "timeline.event.create", in); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A person of another Compendium is not a participant here.
	other := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Other"}).(compendiums.Item).Item.ID
	alien := x.call(x.owner, "entity.create", entities.CreateIn{ID: other, Kind: "character", Name: "Alien"}).(entities.Item).Item.ID
	x.expect(x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "X", Participants: []timeline.Participant{{Entity: alien}}}, ops.CodeInvalid)

	// Update: only what is sent changes; a stale base is refused.
	up := x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, BaseRevision: 1, Title: str("The wedding of Máire"), StoryDateEnd: str("1998-04-13")}).(timeline.EventItem).Item
	if up.Title != "The wedding of Máire" || up.StoryDateEnd != "1998-04-13" || up.Revision != 2 || up.StoryDate != "1998-04-12T15:00" || len(up.Participants) != 2 || up.Location == nil {
		t.Errorf("updated: %+v", up)
	}
	x.expect(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, BaseRevision: 1, Title: str("late")}, ops.CodeConflict)
	// Participants replace; [] clears; an empty location clears; an empty date clears the end too.
	none := []timeline.Participant{}
	up = x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, Participants: &none, Location: str("")}).(timeline.EventItem).Item
	if len(up.Participants) != 0 || up.Location != nil {
		t.Errorf("cleared: %+v", up)
	}
	up = x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, StoryDate: str("")}).(timeline.EventItem).Item
	if up.StoryDate != "" || up.StoryDateEnd != "" || up.Dated {
		t.Errorf("an empty date should clear the end: %+v", up)
	}
	x.expect(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, StoryDateEnd: str("1999")}, ops.CodeInvalid)

	// Delete moves it to the trash with everything it holds; restore brings it back exactly.
	x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, StoryDate: str("1998"), Location: str("Cork"), Participants: &[]timeline.Participant{{Entity: "Elara", Role: "guest"}}})
	x.call(x.owner, "timeline.event.delete", timeline.StateIn{ID: ev.ID})
	l := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid}).(timeline.ListOut)
	for _, it := range l.Items {
		if it.ID == ev.ID {
			t.Errorf("a trashed event is listed")
		}
	}
	tr := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Trash: true}).(timeline.ListOut)
	if len(tr.Items) != 1 || tr.Items[0].ID != ev.ID || !tr.Items[0].Trashed {
		t.Errorf("trash list: %+v", tr)
	}
	x.expect(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, Title: str("nope")}, ops.CodeConflict)
	back := x.call(x.owner, "timeline.event.restore", timeline.StateIn{ID: ev.ID}).(timeline.EventItem).Item
	if back.Trashed || back.Location == nil || back.Location.ID != x.cork || len(back.Participants) != 1 || back.Participants[0].Role != "guest" {
		t.Errorf("restored: %+v", back)
	}
	// Both are idempotent.
	x.call(x.owner, "timeline.event.restore", timeline.StateIn{ID: ev.ID})
	x.call(x.owner, "timeline.event.delete", timeline.StateIn{ID: ev.ID})
	x.call(x.owner, "timeline.event.delete", timeline.StateIn{ID: ev.ID})
	x.call(x.owner, "timeline.event.restore", timeline.StateIn{ID: ev.ID})

	// A dry run writes nothing.
	n := x.count(`SELECT count(*) FROM timeline_events`)
	if _, err := x.reg.CallDry(x.ctx, x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "Preview", StoryDate: "2000"}); err != nil {
		t.Fatal(err)
	}
	if x.count(`SELECT count(*) FROM timeline_events`) != n {
		t.Errorf("a dry run left an event behind")
	}

	// A merge moves what the timeline holds to the target: people (the target's row
	// wins where both took part, and takes a role it lacks) and the place.
	both := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "Both", Location: "Cork",
		Participants: []timeline.Participant{{Entity: "Elara", Role: "host"}, {Entity: "Tomas"}}}).(timeline.EventItem).Item
	only := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "Only", Participants: []timeline.Participant{{Entity: "Elara", Role: "guest"}}}).(timeline.EventItem).Item
	x.call(x.owner, "entity.merge", entities.MergeIn{ID: x.cork, IntoID: x.dublin})
	x.call(x.owner, "entity.merge", entities.MergeIn{ID: x.elara, IntoID: x.tomas})
	b1 := x.call(x.owner, "timeline.event.get", timeline.GetIn{ID: both.ID}).(timeline.EventItem).Item
	if len(b1.Participants) != 1 || b1.Participants[0].Entity.ID != x.tomas || b1.Participants[0].Role != "host" || b1.Location == nil || b1.Location.ID != x.dublin || b1.Revision != both.Revision+2 {
		t.Errorf("after merges: %+v", b1)
	}
	o1 := x.call(x.owner, "timeline.event.get", timeline.GetIn{ID: only.ID}).(timeline.EventItem).Item
	if len(o1.Participants) != 1 || o1.Participants[0].Entity.ID != x.tomas || o1.Participants[0].Role != "guest" {
		t.Errorf("after a merge: %+v", o1.Participants)
	}
}

func TestStoryOrderOfPartialDates(t *testing.T) {
	x := newH(t)
	for _, e := range []struct{ title, date, end string }{
		{"undated b", "", ""}, {"day", "1998-01-01", ""}, {"year", "1998", ""}, {"minute", "1998-01-01T10:00", ""}, {"month", "1998-01", ""},
		{"earlier", "1997-12-31T23:59", ""}, {"undated a", "", ""}, {"range", "1998-01-01", "1998-03"}, {"later month", "1998-02", ""}, {"bce", "-0044-03-15", ""},
	} {
		x.ev(e.title, e.date, e.end, "")
	}
	l := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid}).(timeline.ListOut)
	// A coarser date sorts at or before every finer date it contains; ties go to the shorter span, then the title; undated last.
	eq(t, "story order", titles(l.Items), []string{"bce", "earlier", "year", "month", "day", "range", "minute", "later month", "undated a", "undated b"})
	// Filters: a range overlap, and a page.
	f := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, From: "1998-02", To: "1998-02-15"}).(timeline.ListOut)
	eq(t, "overlapping Feb 1998 to the 15th", titles(f.Items), []string{"year", "range", "later month"})
	p := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Limit: 3, Offset: 2}).(timeline.ListOut)
	if len(p.Items) != 3 || p.Total != 10 || p.NextOffset != 5 || p.Items[0].Title != "year" {
		t.Errorf("page: %v total %d next %d", titles(p.Items), p.Total, p.NextOffset)
	}
	x.expect(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, From: "spring"}, ops.CodeInvalid)
}

func TestListFiltersByEntityAndProject(t *testing.T) {
	x := newH(t)
	a := x.ev("A", "1990", "", x.dublin, "Máire")
	x.ev("B", "1991", "", x.cork, "Elara")
	c := x.ev("C", "1992", "", "", "Máire", "Elara")
	got := func(in timeline.ListIn) []string {
		in.ID = x.cid
		return titles(x.call(x.owner, "timeline.event.list", in).(timeline.ListOut).Items)
	}
	eq(t, "Máire", got(timeline.ListIn{Entity: "Máire"}), []string{"A", "C"})
	eq(t, "by id", got(timeline.ListIn{Entity: x.elara}), []string{"B", "C"})
	eq(t, "the place counts", got(timeline.ListIn{Entity: "Cork"}), []string{"B"})
	eq(t, "an unknown entity", got(timeline.ListIn{Entity: "Nobody"}), []string{})
	s := x.scene("S", "")
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: a.ID})
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: c.ID})
	eq(t, "told in the project", got(timeline.ListIn{Project: x.pid}), []string{"A", "C"})
	x.expect(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Project: "prj_00000000000000000000000000"}, ops.CodeNotFound)
	// The list shows the scene to someone who can open its project, not to someone who cannot.
	x.compendiumMember(x.stranger, "reader")
	ev := x.call(x.stranger, "timeline.event.get", timeline.GetIn{ID: a.ID}).(timeline.EventItem).Item
	if len(ev.Scenes) != 0 {
		t.Errorf("a scene of a project the caller cannot open is shown: %+v", ev.Scenes)
	}
	ev = x.call(x.owner, "timeline.event.get", timeline.GetIn{ID: a.ID}).(timeline.EventItem).Item
	if len(ev.Scenes) != 1 || ev.Scenes[0].ID != s.ID || ev.Scenes[0].ProjectID != x.pid {
		t.Errorf("owner's scenes: %+v", ev.Scenes)
	}
}

func TestSceneLinking(t *testing.T) {
	x := newH(t)
	e := x.ev("A", "1990", "", "")
	s := x.scene("S", "")
	out := x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: e.ID}).(timeline.LinkOut)
	if !out.Linked || !out.Changed {
		t.Errorf("%+v", out)
	}
	if again := x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: e.ID}).(timeline.LinkOut); again.Changed {
		t.Errorf("a second link changed something")
	}
	// An event of a Compendium the book does not use cannot be linked, and looks like a missing one.
	other := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Other"}).(compendiums.Item).Item.ID
	oe := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: other, Title: "Elsewhere"}).(timeline.EventItem).Item
	real := x.expect(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: oe.ID}, ops.CodeNotFound)
	fake := x.expect(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: "tle_00000000000000000000000000"}, ops.CodeNotFound)
	if real.Message != fake.Message {
		t.Errorf("%q vs %q", real.Message, fake.Message)
	}
	// An editor who is not on the Compendium cannot see events, so cannot link them.
	x.projectMember(x.editor, ops.ProjectEditor)
	x.expect(x.editor, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: e.ID}, ops.CodeNotFound)
	x.compendiumMember(x.editor, "reader")
	x.call(x.editor, "timeline.scene.unlink", timeline.UnlinkIn{ID: s.ID, EventID: e.ID})
	if n := x.count(`SELECT count(*) FROM timeline_event_docs`); n != 0 {
		t.Errorf("unlink left %d rows", n)
	}
	// A trashed event cannot gain scenes; trashing keeps the links it has.
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: e.ID})
	x.call(x.owner, "timeline.event.delete", timeline.StateIn{ID: e.ID})
	if n := x.count(`SELECT count(*) FROM timeline_event_docs`); n != 1 {
		t.Errorf("trashing dropped the link")
	}
	x.expect(x.owner, "timeline.scene.link", timeline.LinkIn{ID: x.scene("T", "").ID, EventID: e.ID}, ops.CodeConflict)
	// Trashing the scene hides it from the event's view.
	x.call(x.owner, "timeline.event.restore", timeline.StateIn{ID: e.ID})
	x.call(x.owner, "doc.trash", docs.DocIn{ID: s.ID})
	if got := x.call(x.owner, "timeline.event.get", timeline.GetIn{ID: e.ID}).(timeline.EventItem).Item; len(got.Scenes) != 0 {
		t.Errorf("a trashed scene still tells the event: %+v", got.Scenes)
	}
}

func TestPurgeLeavesNoTimelineRows(t *testing.T) {
	x := newH(t)
	e := x.ev("A", "1990", "", x.dublin, "Máire")
	s := x.scene("S", "")
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: e.ID})
	x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{e.ID}})
	// Purging the project removes its links and its order, and keeps the event.
	x.call(x.owner, "project.trash", projects.StateIn{ID: x.pid})
	x.call(x.owner, "project.purge", projects.PurgeIn{ID: x.pid, ConfirmTitle: "Book"})
	for _, tbl := range []string{"timeline_event_docs", "timeline_narrative"} {
		if n := x.count(`SELECT count(*) FROM ` + tbl); n != 0 {
			t.Errorf("%s keeps %d rows after the project was purged", tbl, n)
		}
	}
	if n := x.count(`SELECT count(*) FROM timeline_events`); n != 1 {
		t.Errorf("the event went with the project: %d", n)
	}
	// Purging the Compendium removes the events and their people.
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: x.cid})
	x.call(x.owner, "compendium.purge", compendiums.PurgeIn{ID: x.cid, ConfirmName: "Bible"})
	for _, tbl := range []string{"timeline_events", "timeline_event_people", "timeline_event_docs", "timeline_narrative"} {
		if n := x.count(`SELECT count(*) FROM ` + tbl); n != 0 {
			t.Errorf("%s keeps %d rows after the Compendium was purged", tbl, n)
		}
	}
}
