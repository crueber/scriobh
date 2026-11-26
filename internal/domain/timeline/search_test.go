package timeline_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// The search of timeline.event.list: each word of q must match the title, the story label, the story date, the place's name or a
// participant's name (folded: case-blind for every letter, Irish fadas included). The matching is done by the database.
func TestListSearchMatchesEveryField(t *testing.T) {
	x := newH(t)
	list := func(in timeline.ListIn) timeline.ListOut {
		in.ID = x.cid
		return x.call(x.owner, "timeline.event.list", in).(timeline.ListOut)
	}
	search := func(q string) []string { return titles(list(timeline.ListIn{Q: q}).Items) }
	x.ev("Quay meeting", "1998-04-15", "", x.dublin, "Máire")
	label := x.ev("Midsummer", "312", "", x.cork)
	x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: label.ID, StoryLabel: str("Midsummer, Year 312")})
	x.ev("Harbour", "1990", "", "", "Elara")
	x.ev("Éire at dawn", "1991", "", "")

	eq(t, "the title", search("quay"), []string{"Quay meeting"})
	eq(t, "the place", search("dublin"), []string{"Quay meeting"})
	eq(t, "a person", search("elara"), []string{"Harbour"})
	eq(t, "the story label", search("midsummer"), []string{"Midsummer"})
	eq(t, "the story date", search("1998-04"), []string{"Quay meeting"})
	eq(t, "a non-ASCII name, in lower case", search("máire"), []string{"Quay meeting"})
	eq(t, "a non-ASCII name, in capitals", search("MÁIRE"), []string{"Quay meeting"})
	eq(t, "a non-ASCII title, in capitals", search("ÉIRE"), []string{"Éire at dawn"})
	eq(t, "every word, each in any field", search("dublin máire"), []string{"Quay meeting"})
	eq(t, "every word must match", search("dublin elara"), []string{})
	eq(t, "nothing matches", search("zzz"), []string{})
	eq(t, "no words, no filter", search("   "), []string{"Midsummer", "Harbour", "Éire at dawn", "Quay meeting"})

	// LIKE's wildcards are the words' own characters, not patterns.
	eq(t, "a percent sign is not a wildcard", search("%"), []string{})
	eq(t, "an underscore is not a wildcard", search("_"), []string{})
	x.ev("100% true", "1995", "", "")
	eq(t, "a percent sign in a title", search("100%"), []string{"100% true"})

	// A trashed name no longer matches: the place and the person are gone from the book's view of the event.
	x.call(x.owner, "entity.trash", entities.StateIn{ID: x.maire})
	eq(t, "a trashed person does not match", search("máire"), []string{})
	eq(t, "the event itself is still there", search("quay"), []string{"Quay meeting"})
}

// Search results are the same story order and paging as the whole list, and Total counts the matches, not the page.
func TestListSearchPagesInStoryOrder(t *testing.T) {
	x := newH(t)
	x.ev("Ash day 3", "1992", "", "")
	x.ev("Ash day 1", "1990", "", "")
	x.ev("Ash day 2", "1991", "", "")
	x.ev("Oak", "1991", "", "")
	out := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Q: "ash", Limit: 2}).(timeline.ListOut)
	eq(t, "the first page", titles(out.Items), []string{"Ash day 1", "Ash day 2"})
	if out.Total != 3 || out.NextOffset != 2 {
		t.Errorf("total %d, next %d; want 3 and 2", out.Total, out.NextOffset)
	}
	out = x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Q: "ash", Limit: 2, Offset: 2}).(timeline.ListOut)
	eq(t, "the last page", titles(out.Items), []string{"Ash day 3"})
	if out.NextOffset != 0 {
		t.Errorf("a last page has next %d", out.NextOffset)
	}
}

// Scene narrows the list to the events one scene tells; a scene of another book tells none of them here.
func TestListSceneFilter(t *testing.T) {
	x := newH(t)
	a := x.ev("A", "1990", "", "")
	x.ev("B", "1991", "", "")
	c := x.ev("C", "1992", "", "")
	s := x.scene("S", "")
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: a.ID})
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: c.ID})
	out := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Scene: s.ID}).(timeline.ListOut)
	eq(t, "the events the scene tells", titles(out.Items), []string{"A", "C"})
	if out.Total != 2 {
		t.Errorf("total %d, want 2", out.Total)
	}
	out = x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Scene: s.ID, Q: "c"}).(timeline.ListOut)
	eq(t, "a scene with a search", titles(out.Items), []string{"C"})
}

// A word that names an entity of another Compendium does not match here; a search never reads another Compendium's events.
func TestListSearchStaysInItsCompendium(t *testing.T) {
	x := newH(t)
	x.ev("Quay", "1990", "", "")
	other := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Other"}).(compendiums.Item).Item.ID
	x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: other, Title: "Quay elsewhere"})
	out := x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Q: "quay"}).(timeline.ListOut)
	eq(t, "this Compendium only", titles(out.Items), []string{"Quay"})
	x.expect(x.owner, "timeline.event.list", timeline.ListIn{ID: "cmp_00000000000000000000000000", Q: "quay"}, ops.CodeNotFound)
}
