package timeline_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

func kinds(out timeline.CheckOut) []string {
	var k []string
	for _, f := range out.Findings {
		k = append(k, f.Kind)
	}
	sort.Strings(k)
	return k
}

func has(out timeline.CheckOut, kind string) *timeline.Finding {
	for i := range out.Findings {
		if out.Findings[i].Kind == kind {
			return &out.Findings[i]
		}
	}
	return nil
}

func TestCheckFindsNothingInACleanTimeline(t *testing.T) {
	x := newH(t)
	x.ev("A", "1998-04-12", "", x.dublin, "Elara")
	x.ev("B", "1998-04-13", "", x.cork, "Elara")
	x.scene("S1", "1998-04-12")
	x.scene("S2", "1998-04-13")
	out := x.check(x.owner)
	if len(out.Findings) != 0 || out.Errors != 0 || out.Warnings != 0 || out.Events != 2 {
		t.Errorf("a clean timeline reported %+v", out)
	}
}

func TestLocationConflictAndItsNearMisses(t *testing.T) {
	x := newH(t)
	// True positive: Máire is in Dublin all day and at an hour in Cork the same day.
	a := x.ev("Fair in Dublin", "1998-04-20T10:00", "1998-04-20T22:00", x.dublin, "Máire", "Tomas")
	b := x.ev("Dinner in Cork", "1998-04-20T19:30", "", x.cork, "Máire")
	out := x.check(x.owner)
	f := has(out, "location_conflict")
	if f == nil || f.Severity != "error" || len(f.Events) != 2 || out.Errors != 1 {
		t.Fatalf("true positive missed: %+v", out)
	}
	ids := []string{f.Events[0].ID, f.Events[1].ID}
	sort.Strings(ids)
	want := []string{a.ID, b.ID}
	sort.Strings(want)
	if ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("events %v, want %v", ids, want)
	}
	// Only Máire is in both places; Tomas is not named as being in two.
	var who []string
	for _, e := range f.Entities {
		who = append(who, e.Name)
	}
	if !strings.Contains(strings.Join(who, ","), "Máire") || strings.Contains(strings.Join(who, ","), "Tomas") {
		t.Errorf("entities %v", who)
	}
	if !strings.Contains(f.Message, "Máire") || !strings.Contains(f.Message, "Dublin") || !strings.Contains(f.Message, "Cork") {
		t.Errorf("message %q", f.Message)
	}
	// The finding id is stable.
	if again := x.check(x.owner); has(again, "location_conflict").ID != f.ID {
		t.Errorf("the finding id changed")
	}

	// Near misses: none of these may fire.
	y := newH(t)
	y.ev("Day one", "1998-04-20", "", y.dublin, "Elara")
	y.ev("Day two in Cork", "1998-04-21", "", y.cork, "Elara")                // the next day
	y.ev("Same place, same day", "1998-04-20T10:00", "", y.dublin, "Elara")   // the same place
	y.ev("No place named", "1998-04-20T11:00", "", "", "Elara")               // no location
	y.ev("Vague year", "1998", "", y.cork, "Elara")                           // a bare year says "sometime", not "all year"
	y.ev("Vague month", "1998-04", "", y.cork, "Elara")                       // so does a bare month
	y.ev("Someone else in Cork", "1998-04-20", "", y.cork, "Tomas")           // a different person
	y.ev("Cork trip", "1998-04-10", "1998-04-14", y.cork, "Elara")            // a duration, ends before the 20th
	y.ev("Two minutes apart", "1998-05-01T10:00", "", y.dublin, "Tomas")      // instants a minute apart
	y.ev("Two minutes apart, there", "1998-05-01T10:01", "", y.cork, "Tomas") //
	y.ev("Undated in Cork", "", "", y.cork, "Elara")                          // no date at all
	if out := y.check(y.owner); len(out.Findings) != 0 {
		t.Errorf("near misses fired: %+v", kinds(out))
	}
	// But two explicit ranges that overlap do fire, vague ends or not.
	y.ev("Long stay in Dublin", "1998-06-01", "1998-06-30", y.dublin, "Tomas")
	y.ev("Month in Cork", "1998-06-15", "1998-07-15", y.cork, "Tomas")
	if out := y.check(y.owner); len(out.Findings) != 1 || out.Findings[0].Kind != "location_conflict" {
		t.Errorf("overlapping ranges: %+v", kinds(out))
	}
}

func TestBirthAndDeath(t *testing.T) {
	x := newH(t)
	// Máire was born 1998-04-15.
	x.ev("Before she was born", "1998-04-12", "", "", "Máire")
	x.ev("Year before", "1997", "", "", "Máire")
	// Near misses: the day itself, the year she was born (vague), a span that reaches past her birth, no date, someone with no birth.
	x.ev("On her birthday", "1998-04-15", "", "", "Máire")
	x.ev("The year she was born", "1998", "", "", "Máire")
	x.ev("A span into her life", "1998-04-10", "1998-04-20", "", "Máire")
	x.ev("Undated", "", "", "", "Máire")
	x.ev("Elara before anyone", "1900", "", "", "Elara")
	out := x.check(x.owner)
	var titles []string
	for _, f := range out.Findings {
		if f.Kind != "before_birth" || f.Severity != "error" {
			t.Errorf("unexpected finding %+v", f)
		}
		titles = append(titles, f.Events[0].Title)
	}
	sort.Strings(titles)
	eq(t, "before_birth", titles, []string{"Before she was born", "Year before"})
	if !strings.Contains(out.Findings[0].Message, "1998-04-15") {
		t.Errorf("the message says when she was born: %q", out.Findings[0].Message)
	}

	// Death: Tomas died 1999-12-31.
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: x.tomas, Fields: map[string]any{"died": "1999-12-31"}})
	x.ev("After he died", "2000-01-01", "", "", "Tomas")
	x.ev("The day he died", "1999-12-31", "", "", "Tomas")
	x.ev("The year he died", "1999", "", "", "Tomas")
	x.ev("Earlier", "1990", "", "", "Tomas")
	dead := 0
	for _, f := range x.check(x.owner).Findings {
		if f.Kind == "after_death" {
			dead++
			if f.Events[0].Title != "After he died" {
				t.Errorf("after_death on %q", f.Events[0].Title)
			}
		}
	}
	if dead != 1 {
		t.Errorf("after_death findings: %d", dead)
	}
	// A book's override of the field applies in that book's check: here she is born in 1990.
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: x.pid, EntityID: x.maire, Fields: map[string]any{"born": "1990"}})
	for _, f := range x.check(x.owner).Findings {
		if f.Kind == "before_birth" && f.Events[0].Title == "Before she was born" {
			t.Errorf("the book's own birth year was not used")
		}
	}
	// The Compendium's own check ignores the book's override.
	cc := x.call(x.owner, "timeline.compendium.check", timeline.CompendiumCheckIn{ID: x.cid}).(timeline.CheckOut)
	if has(cc, "before_birth") == nil {
		t.Errorf("the Compendium check should use the Compendium's field values: %+v", kinds(cc))
	}
}

func TestInvalidRangeAndUnreadableDates(t *testing.T) {
	x := newH(t)
	bad := x.scene("Backwards", "1998-04-20")
	end := "1998-04-10"
	x.call(x.owner, "doc.update", docs.UpdateIn{ID: bad.ID, BaseRevision: 2, StoryDateEnd: &end})
	ok := x.scene("Forwards", "1998-04-20")
	end2 := "1998-04-20"
	x.call(x.owner, "doc.update", docs.UpdateIn{ID: ok.ID, BaseRevision: 2, StoryDateEnd: &end2})
	x.scene("Fuzzy", "sometime in spring")
	x.scene("Fuzzier", "c. 1998")
	x.scene("Undated", "")
	out := x.check(x.owner)
	if f := has(out, "invalid_range"); f == nil || len(f.Scenes) != 1 || f.Scenes[0].ID != bad.ID || f.Severity != "error" {
		t.Errorf("invalid_range: %+v", out.Findings)
	}
	f := has(out, "unreadable_date")
	if f == nil || len(f.Scenes) != 2 || f.Severity != "warning" {
		t.Fatalf("unreadable_date: %+v", out.Findings)
	}
	if v := x.view(x.owner); v.Unplaced != 3 { // the chapter the book starts with, the unreadable ones are separate, and "Undated"
		t.Logf("unplaced %d", v.Unplaced)
	}
}

func TestNarratedOutOfOrder(t *testing.T) {
	x := newH(t)
	// Chronological: nothing.
	x.scene("S1", "1990")
	x.scene("S2", "1991-05")
	x.scene("S3", "1991-05-10") // inside the month: overlaps, not earlier
	x.scene("S4", "1991-05-10") // the same day again
	if out := x.check(x.owner); has(out, "narrated_out_of_order") != nil {
		t.Fatalf("chronological scenes flagged: %+v", out.Findings)
	}
	// A flashback: told after 1991, but 1980.
	flash := x.scene("S5 flashback", "1980")
	x.scene("S6", "1992")
	out := x.check(x.owner)
	var flagged []string
	for _, f := range out.Findings {
		if f.Kind == "narrated_out_of_order" {
			if f.Severity != "warning" {
				t.Errorf("a flashback must be a warning, not an error: %+v", f)
			}
			for _, s := range f.Scenes {
				if s.ID == flash.ID {
					flagged = append(flagged, s.Title)
				}
			}
		}
	}
	if len(flagged) != 1 || out.Errors != 0 {
		t.Errorf("the flashback: flagged %v, errors %d, findings %+v", flagged, out.Errors, out.Findings)
	}
	// The later scene is not flagged for coming after the flashback.
	for _, f := range out.Findings {
		for _, s := range f.Scenes {
			if s.Title == "S6" {
				t.Errorf("S6 is in order but was flagged: %+v", f)
			}
		}
	}
	// Reordering the events by hand changes what is flagged: events told by scenes keep their place.
	e1 := x.ev("Old news", "1970", "", "")
	s := x.scene("Tells old news", "")
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: e1.ID})
	if !hasEventFlag(x.check(x.owner), e1.ID) {
		t.Errorf("an event told after later things should be flagged")
	}
}

func hasEventFlag(out timeline.CheckOut, id string) bool {
	for _, f := range out.Findings {
		if f.Kind != "narrated_out_of_order" {
			continue
		}
		for _, e := range f.Events {
			if e.ID == id {
				return true
			}
		}
	}
	return false
}

func TestSceneBeforeFirstEvent(t *testing.T) {
	x := newH(t)
	x.ev("Elara arrives", "1998-04", "", "", "Elara")
	early := x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: "Early", Kind: "scene", Content: "Elara was already there."}).(docs.Item).Item
	d := "1990-01-01"
	x.call(x.owner, "doc.update", docs.UpdateIn{ID: early.ID, BaseRevision: early.Revision, StoryDate: &d})
	// Near misses: the same period, a scene that does not mention her, an undated one, someone with no event.
	same := x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: "Same month", Kind: "scene", Content: "Elara walks."}).(docs.Item).Item
	d2 := "1998-04-20"
	x.call(x.owner, "doc.update", docs.UpdateIn{ID: same.ID, BaseRevision: same.Revision, StoryDate: &d2})
	quiet := x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: "Quiet", Kind: "scene", Content: "Nobody here."}).(docs.Item).Item
	d3 := "1980"
	x.call(x.owner, "doc.update", docs.UpdateIn{ID: quiet.ID, BaseRevision: quiet.Revision, StoryDate: &d3})
	x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: "Undated", Kind: "scene", Content: "Elara."})
	x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: "Tomas early", Kind: "scene", Content: "Tomas."})
	x.settle()
	out := x.check(x.owner)
	var hits []string
	for _, f := range out.Findings {
		if f.Kind == "scene_before_first_event" {
			hits = append(hits, f.Scenes[0].Title+"/"+f.Entities[0].Name)
			if f.Severity != "warning" {
				t.Errorf("severity %s", f.Severity)
			}
		}
	}
	eq(t, "scene_before_first_event", hits, []string{"Early/Elara"})
	if out.Indexing {
		t.Errorf("settled but indexing")
	}
	// Point of view counts as being present, with no mention in the text.
	pov := x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: "Seen through her", Kind: "scene", Content: "Rain."}).(docs.Item).Item
	d4 := "1985"
	x.call(x.owner, "doc.update", docs.UpdateIn{ID: pov.ID, BaseRevision: pov.Revision, StoryDate: &d4})
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: pov.ID, Entity: x.elara})
	found := false
	for _, f := range x.check(x.owner).Findings {
		found = found || (f.Kind == "scene_before_first_event" && f.Scenes[0].Title == "Seen through her")
	}
	if !found {
		t.Errorf("a point of view should count as present")
	}
}

func TestCheckFollowsTheBibleAccess(t *testing.T) {
	x := newH(t)
	x.ev("A", "1998-04-20", "", x.dublin, "Máire")
	x.ev("B", "1998-04-20T19:30", "", x.cork, "Máire")
	x.scene("S1", "1991")
	x.scene("S2", "1980") // a flashback: scene-only, needs no bible
	x.projectMember(x.reader, ops.ProjectReader)
	out := x.check(x.reader)
	if !out.Hidden || out.Events != 0 || has(out, "location_conflict") != nil {
		t.Errorf("a project reader who is not on the Compendium got events: %+v", out)
	}
	if has(out, "narrated_out_of_order") == nil {
		t.Errorf("scene-only findings need no bible and should still show: %+v", kinds(out))
	}
	v := x.view(x.reader)
	for _, it := range v.Items {
		if it.Kind == "event" {
			t.Errorf("the view shows an event to a non-member: %+v", it)
		}
	}
	if !v.Hidden {
		t.Errorf("hidden should say events were left out")
	}
	x.compendiumMember(x.reader, "reader")
	if out := x.check(x.reader); out.Hidden || has(out, "location_conflict") == nil {
		t.Errorf("a member sees the conflict: %+v", out)
	}
}
