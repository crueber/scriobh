package mirror_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/timeline"
)

// A project's compendium/ files are carried forward while its projection hash
// is unchanged, so what timeline.md shows of the book's documents must be part
// of that hash. Document changes alone (no event, no link, no order) must still
// reach the file; a chapter save must not render it.
func TestProjectTimelineFileFollowsTheBooksDocuments(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Book")
	cid := e.compendium("World")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	file := func() string { return read(t, filepath.Join(e.root(pid), "compendium", "timeline.md")) }
	setDate := func(d docs.Doc, date string) docs.Doc {
		cur := e.call(e.owner, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
		return e.call(e.owner, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: cur.Revision, StoryDate: &date}).(docs.Item).Item
	}
	// A project with no events at all: a dated scene is on the timeline.
	rain := setDate(e.doc(pid, "Rain", "scene", "It rained.\n"), "1992-06")
	e.settleAll()
	if got := file(); !strings.Contains(got, "## 1992-06: Rain") {
		t.Fatalf("a dated scene with no events is not on the timeline:\n%s", got)
	}
	// Re-dating an unlinked scene rewrites the file.
	rain = setDate(rain, "1993-01-05")
	e.settleAll()
	if got := file(); !strings.Contains(got, "## 1993-01-05: Rain") || strings.Contains(got, "1992-06") {
		t.Errorf("re-dating a scene did not reach timeline.md:\n%s", got)
	}
	// A linked scene: renaming it rewrites the "told in" line.
	ev := e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: cid, Title: "The wedding", StoryDate: "1998-04-12"}).(timeline.EventItem).Item
	opening := e.doc(pid, "Opening", "scene", "Words.\n")
	e.call(e.owner, "timeline.scene.link", timeline.LinkIn{ID: opening.ID, EventID: ev.ID})
	e.settleAll()
	if got := file(); !strings.Contains(got, "- told in: Opening") {
		t.Fatalf("the link is not in timeline.md:\n%s", got)
	}
	title := "Arrival"
	cur := e.call(e.owner, "doc.get", docs.DocIn{ID: opening.ID}).(docs.Item).Item
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: opening.ID, BaseRevision: cur.Revision, Title: &title})
	e.settleAll()
	if got := file(); !strings.Contains(got, "- told in: Arrival") || strings.Contains(got, "told in: Opening") {
		t.Errorf("renaming a scene did not reach timeline.md:\n%s", got)
	}
	// Moving a scene changes the order the book tells things in.
	before := file()
	e.call(e.owner, "doc.move", docs.MoveIn{ID: opening.ID, After: strp("")})
	e.settleAll()
	if after := file(); after == before {
		t.Errorf("moving a scene did not reach the book's narrative order in timeline.md")
	}
	// Trashing the linked scene drops it from the event.
	e.call(e.owner, "doc.trash", docs.DocIn{ID: opening.ID})
	e.settleAll()
	if got := file(); strings.Contains(got, "told in") {
		t.Errorf("a trashed scene still tells the event:\n%s", got)
	}
	// Saving the text of a chapter renders nothing of the compendium/ folder.
	e.fs.reset()
	e.setContent(rain, "It rained all night.\n")
	e.settleAll()
	if n := e.fs.writesUnder(filepath.Join(e.root(pid), "compendium")); n != 0 {
		t.Errorf("a chapter save rewrote %d files of compendium/", n)
	}
}

func strp(s string) *string { return &s }
