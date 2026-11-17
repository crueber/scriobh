package mirror_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/domain/timeline"
)

// timeline.md is written in a project's compendium/ folder, and in a
// standalone Compendium's folder: deterministic, rewritten when an event, a
// scene link or the book's order changes, and quiet when nothing did.
func TestTimelineFileFollowsTheTimeline(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Book")
	cid := e.compendium("World")
	maire := e.entity(cid, "character", "Máire")
	harbour := e.entity(cid, "place", "Port Salt")
	// A standalone Compendium first: it holds the file under its own folder.
	ev := e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: cid, Title: "The wedding", StoryDate: "1998-04-12", Description: "At the **harbour**.",
		Location: harbour, Participants: []timeline.Participant{{Entity: maire, Role: "bride"}}}).(timeline.EventItem).Item
	e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: cid, Title: "Long before", StoryDate: "1990"})
	e.settleAll()
	standalone := filepath.Join(e.dir, "mirror")
	var root string
	for _, p := range globDirs(t, standalone, "compendium-world--*") {
		root = p
	}
	if root == "" {
		t.Fatal("no standalone folder")
	}
	got := read(t, filepath.Join(root, "timeline.md"))
	for _, want := range []string{"# Timeline", "## 1990: Long before", "## 1998-04-12: The wedding", "- where: Port Salt", "- who: Máire (bride)", "At the **harbour**."} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline.md lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "1990: Long before") > strings.Index(got, "1998-04-12: The wedding") {
		t.Errorf("story order: %s", got)
	}
	if strings.Contains(got, "not written here yet") {
		t.Errorf("the placeholder is still there")
	}

	// A project that uses the Compendium takes the file under compendium/, with its scenes and order.
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	d := e.doc(pid, "Opening scene", "scene", "She came ashore.\n")
	e.call(e.owner, "timeline.scene.link", timeline.LinkIn{ID: d.ID, EventID: ev.ID})
	e.settleAll()
	pt := read(t, filepath.Join(e.root(pid), "compendium", "timeline.md"))
	if !strings.Contains(pt, "- told in: Opening scene") || !strings.Contains(pt, "## 1998-04-12: The wedding") || !strings.Contains(pt, "## The order this book tells it") {
		t.Errorf("project timeline.md:\n%s", pt)
	}
	// Setting the book's own order rewrites it; a rebuild of unchanged data writes nothing.
	e.call(e.owner, "timeline.order.set", timeline.OrderSetIn{ID: pid, EventIDs: []string{ev.ID}})
	e.settleAll()
	if got := read(t, filepath.Join(e.root(pid), "compendium", "timeline.md")); !strings.Contains(got, "set by hand for this book") {
		t.Errorf("the order change did not reach timeline.md:\n%s", got)
	}
	e.fs.reset()
	if _, err := e.m.Rebuild(e.ctx, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Rebuild(e.ctx, cid); err != nil {
		t.Logf("rebuild of the Compendium: %v", err)
	}
	if n := e.fs.writes(); n != 0 {
		t.Errorf("a rebuild of unchanged data wrote %d files", n)
	}
	// A renamed scene (a document change) and a renamed entity both reach it.
	e.call(e.owner, "entity.update", entities.UpdateIn{ID: maire, Name: strptr("Máire Ní Nolan")})
	e.settleAll()
	if got := read(t, filepath.Join(e.root(pid), "compendium", "timeline.md")); !strings.Contains(got, "Máire Ní Nolan (bride)") {
		t.Errorf("a renamed participant did not reach timeline.md:\n%s", got)
	}
}

func globDirs(t *testing.T, dir, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Every op of the timeline and scenes that changes what a mirror shows wakes
// it, and leaves it in sync. A new op fails this test until it is classified.
func TestEveryMutatingTimelineAndSceneOpReachesTheMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Coverage")
	cid := e.compendium("Coverage world")
	maire := e.entity(cid, "character", "Máire")
	harbour := e.entity(cid, "place", "Port Salt")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	sc := e.outlineDoc(pid)
	ev := e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: cid, Title: "First", StoryDate: "2000"}).(timeline.EventItem).Item
	e.settleAll()

	mirrored := map[string]func(){
		"scene.set_pov":      func() { e.call(e.owner, "scene.set_pov", scenes.SetIn{ID: sc.ID, Entity: maire}) },
		"scene.set_location": func() { e.call(e.owner, "scene.set_location", scenes.SetIn{ID: sc.ID, Entity: harbour}) },
		"scene.link_entities": func() {
			// Clear the link (the text stays: "Máire"), then let the repair restore it.
			e.call(e.owner, "scene.set_pov", scenes.SetIn{ID: sc.ID, Entity: ""})
			e.call(e.owner, "scene.link_entities", scenes.LinkIn{ID: pid})
		},
		"timeline.event.create": func() {
			e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: cid, Title: "Second", StoryDate: "2001"})
		},
		"timeline.event.update": func() {
			e.call(e.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, Title: strptr("First, renamed")})
		},
		"timeline.event.delete":  func() { e.call(e.owner, "timeline.event.delete", timeline.StateIn{ID: ev.ID}) },
		"timeline.event.restore": func() { e.call(e.owner, "timeline.event.restore", timeline.StateIn{ID: ev.ID}) },
		"timeline.order.set": func() {
			e.call(e.owner, "timeline.order.set", timeline.OrderSetIn{ID: pid, EventIDs: []string{ev.ID}})
		},
		"timeline.scene.link": func() {
			e.call(e.owner, "timeline.scene.link", timeline.LinkIn{ID: sc.ID, EventID: ev.ID})
		},
		"timeline.scene.unlink": func() {
			e.call(e.owner, "timeline.scene.unlink", timeline.UnlinkIn{ID: sc.ID, EventID: ev.ID})
		},
	}
	exempt := map[string]string{"mention.rebuild": "a derived index, not shown in the mirror"}
	for _, en := range e.reg.Entries() {
		n := en.Op.Name
		if !(strings.HasPrefix(n, "timeline.") || strings.HasPrefix(n, "scene.") || strings.HasPrefix(n, "mention.")) || en.Op.Method == "GET" {
			continue
		}
		if mirrored[n] == nil && exempt[n] == "" {
			t.Errorf("%s changes data but is neither covered nor exempt in this test; does the mirror need to see it?", n)
		}
	}
	order := []string{"scene.set_pov", "scene.set_location", "scene.link_entities", "timeline.event.create", "timeline.event.update", "timeline.event.delete",
		"timeline.event.restore", "timeline.scene.link", "timeline.order.set", "timeline.scene.unlink"}
	for _, n := range order {
		act := mirrored[n]
		if act == nil {
			t.Fatalf("no action for %s", n)
		}
		act()
		deadline := time.Now().Add(5 * time.Second)
		for !e.m.Status(pid).Pending {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not wake the mirror", n)
			}
			time.Sleep(2 * time.Millisecond)
		}
		e.flush()
		if st := e.m.Status(pid); !st.InSync {
			t.Errorf("%s: mirror not in sync afterwards: %+v", n, st)
		}
	}
}
