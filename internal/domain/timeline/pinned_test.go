package timeline_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// A key pinned to a project reaches the project and its primary Compendium's
// timeline, not the Compendiums the book merely references; a key pinned to a
// Compendium reaches that Compendium's events and no book.
func TestPinnedKeysAndTheTimeline(t *testing.T) {
	x := newH(t)
	mine := x.ev("Mine", "2000", "", "")
	world := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Shared world"}).(compendiums.Item).Item.ID
	theirs := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: world, Title: "Long ago", StoryDate: "-0100"}).(timeline.EventItem).Item
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: x.pid, CompendiumID: world})
	s := x.scene("S", "")
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: mine.ID})

	projectKey := ops.Principal{UserID: x.owner.UserID, Role: x.owner.Role, ProjectID: x.pid, Scope: ops.ScopeWrite, Via: "apikey"}
	v := x.view(projectKey)
	eq(t, "what a project-pinned key sees", keys(v, v.Story), []string{"Mine"})
	if !v.Hidden {
		t.Errorf("hidden should say the referenced Compendium was left out")
	}
	if out := x.check(projectKey); !out.Hidden || out.Events != 1 {
		t.Errorf("check through a project pin: %+v", out)
	}
	// It cannot order, or link, events of a Compendium it cannot read.
	x.expect(projectKey, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{theirs.ID}}, ops.CodeNotFound)
	x.expect(projectKey, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: theirs.ID}, ops.CodeNotFound)
	x.call(projectKey, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{mine.ID}})

	// A Compendium-pinned key: its own events, no books.
	compKey := ops.Principal{UserID: x.owner.UserID, Role: x.owner.Role, CompendiumID: x.cid, Scope: ops.ScopeWrite, Via: "apikey"}
	list := x.call(compKey, "timeline.event.list", timeline.ListIn{ID: x.cid}).(timeline.ListOut)
	if len(list.Items) != 1 || len(list.Items[0].Scenes) != 0 {
		t.Errorf("a Compendium-pinned key sees the book's scenes: %+v", list.Items)
	}
	x.expect(compKey, "timeline.event.list", timeline.ListIn{ID: world}, ops.CodeNotFound)
	x.expect(compKey, "timeline.project.view", timeline.ViewIn{ID: x.pid}, ops.CodeNotFound)
	x.expect(compKey, "timeline.event.get", timeline.GetIn{ID: theirs.ID}, ops.CodeNotFound)
	// The same key's own project filter is refused: it cannot ask about a book.
	x.expect(compKey, "timeline.event.list", timeline.ListIn{ID: x.cid, Project: x.pid}, ops.CodeNotFound)
}
