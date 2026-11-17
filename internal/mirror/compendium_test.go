package mirror_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/testutil"
)

func (e *env) compendium(name string) string {
	e.t.Helper()
	return e.call(e.owner, "compendium.create", compendiums.CreateIn{Name: name}).(compendiums.Item).Item.ID
}

func (e *env) entity(cid, kind, name string) string {
	e.t.Helper()
	return e.call(e.owner, "entity.create", entities.CreateIn{ID: cid, Kind: kind, Name: name}).(entities.Item).Item.ID
}

// cmpRoot is a standalone Compendium's folder in the mirror.
func (e *env) cmpRoot(cid string) string {
	e.t.Helper()
	dir := e.m.Status(cid).Directory
	if dir == "" {
		e.t.Fatal("the Compendium has no mirror folder")
	}
	return filepath.Join(e.dir, "mirror", dir)
}

func (e *env) settleAll() {
	e.t.Helper()
	e.caughtUp()
	e.flush()
}

// A Compendium with no project has a folder of its own, with a manifest, a
// README, a timeline placeholder and one Markdown file per entity.
func TestStandaloneCompendiumMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	cid := e.compendium("The Salt Road")
	maire := e.call(e.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Máire Nolan", Summary: "Keeps the house.",
		Body: "She walks the **harbour** at dawn.\n", Aliases: []string{"Maire", "Mrs Nolan"}, Fields: map[string]any{"age": 34, "wants": "to keep the house"}}).(entities.Item).Item
	sorcha := e.entity(cid, "character", "Sorcha Nolan")
	e.call(e.owner, "entity.relation.add", entities.RelationAddIn{ID: maire.ID, ToEntityID: sorcha, Kind: "family", Label: "mother of", InverseLabel: "daughter of"})
	e.entity(cid, "place", "The Harbour")
	e.settleAll()

	root := e.cmpRoot(cid)
	if !strings.HasPrefix(filepath.Base(root), "compendium-the-salt-road--") {
		t.Errorf("folder %q", filepath.Base(root))
	}
	files := tree(t, root)
	var mFile string
	for _, f := range files {
		if strings.HasPrefix(f, "entities/characters/maire-nolan--") {
			mFile = f
		}
	}
	for _, want := range []string{"compendium.json", "README.md", "timeline.md"} {
		if !has(files, want) {
			t.Errorf("missing %s in %v", want, files)
		}
	}
	if mFile == "" || len(files) != 6 {
		t.Fatalf("files: %v", files)
	}
	got := read(t, filepath.Join(root, mFile))
	want := "---\nid: \"" + maire.ID + "\"\nname: \"Máire Nolan\"\nkind: character\naliases:\n  - \"Maire\"\n  - \"Mrs Nolan\"\nsummary: \"Keeps the house.\"\n" +
		"fields:\n  age: \"34\"\n  wants: \"to keep the house\"\nrelations:\n  - \"mother of: Sorcha Nolan\"\n---\n\nShe walks the **harbour** at dawn.\n"
	if got != want {
		t.Errorf("entity file:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(read(t, filepath.Join(root, "compendium.json")), `"format": "scriobh-compendium"`) {
		t.Error("manifest has no format")
	}

	// A rebuild changes nothing, down to the modification times.
	before := mtimes(t, root)
	e.fs.reset()
	if _, err := e.m.Rebuild(e.ctx, cid); err != nil {
		t.Fatal(err)
	}
	if n := e.fs.writes(); n != 0 {
		t.Errorf("a rebuild of unchanged data wrote %d files", n)
	}
	for p, mt := range mtimes(t, root) {
		if !before[p].Equal(mt) {
			t.Errorf("%s was touched", p)
		}
	}
	// An edit rewrites one entity file and the manifest, nothing else.
	e.fs.reset()
	e.call(e.owner, "entity.update", entities.UpdateIn{ID: sorcha, Summary: strptr("Her daughter.")})
	e.settleAll()
	if n := e.fs.writesUnder(root); n != 2 { // Sorcha's file, and the manifest that carries its hash
		t.Errorf("editing one entity's summary wrote %d files, want 2", n)
	}
	if strings.Contains(strings.Join(tree(t, root), " "), "tmp") {
		t.Error("a temporary file was left in the folder")
	}
}

func strptr(s string) *string { return &s }

// A project's compendium/ folder shows its primary Compendium, the ones it
// references and the entities it links, with the book's overrides applied. A
// Compendium that becomes a project's primary loses its folder of its own, and
// gets it back when the project lets go.
func TestProjectProjectionAndStandaloneHandover(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Book One")
	cid := e.compendium("World")
	other := e.compendium("Cosmology")
	ada := e.call(e.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Ada", Summary: "A cartographer.", Fields: map[string]any{"age": 30}}).(entities.Item).Item
	moon := e.entity(other, "place", "The Moon")
	cameo := e.entity(e.compendium("Elsewhere"), "character", "Stranger")
	e.settleAll()
	standalone := e.cmpRoot(cid)
	if _, err := os.Stat(standalone); err != nil {
		t.Fatal(err)
	}

	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	e.call(e.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: other})
	e.call(e.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: cameo})
	age := any("31")
	name := "Ada Quill"
	e.call(e.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: ada.ID, Name: &name, Fields: map[string]any{"age": age}, Notes: strptr("Older in book one.")})
	e.settleAll()

	if _, err := os.Stat(standalone); !os.IsNotExist(err) {
		t.Errorf("the standalone folder of a project's primary Compendium remains (%v)", err)
	}
	root := e.root(pid)
	files := tree(t, root)
	var adaFile, moonFile string
	for _, f := range files {
		switch {
		case strings.HasPrefix(f, "compendium/entities/characters/ada-quill--"):
			adaFile = f
		case strings.HasPrefix(f, "compendium/entities/places/the-moon--"):
			moonFile = f
		}
	}
	for _, want := range []string{"compendium/README.md", "compendium/index.json", "compendium/timeline.md"} {
		if !has(files, want) {
			t.Errorf("missing %s in %v", want, files)
		}
	}
	if adaFile == "" || moonFile == "" || len(files) != 9 {
		t.Fatalf("files: %v", files)
	}
	got := read(t, filepath.Join(root, adaFile))
	for _, want := range []string{"name: \"Ada Quill\"", "compendium: \"World\"", "age: \"31\"", "overridden: true", "book_notes: \"Older in book one.\"", "summary: \"A cartographer.\""} {
		if !strings.Contains(got, want) {
			t.Errorf("the overridden entity lacks %q:\n%s", want, got)
		}
	}
	if m := read(t, filepath.Join(root, moonFile)); !strings.Contains(m, "reference: true") || !strings.Contains(m, "compendium: \"Cosmology\"") {
		t.Errorf("a referenced entity is not marked:\n%s", m)
	}
	// The Compendium's own record is untouched by the override.
	got2 := e.call(e.owner, "entity.get", entities.GetIn{ID: ada.ID}).(entities.DetailItem).Item
	if got2.Name != "Ada" || got2.Fields["age"] != "30" || got2.Revision != ada.Revision {
		t.Errorf("the override changed the Compendium record: %+v", got2.Entity)
	}
	_ = moon

	// A chapter save does not render or write the projection again.
	e.fs.reset()
	pr := e.doc(pid, "Chapter", "chapter", "text\n")
	e.settleAll()
	e.setContent(pr, "more text\n")
	e.settleAll()
	if n := e.fs.wrote(".md"); n == 0 {
		t.Error("the chapter was not written")
	}
	for _, r := range e.fs.renames {
		if strings.Contains(r, "/compendium/") {
			t.Errorf("a chapter save rewrote %s", r)
		}
	}

	// Detaching the Compendium removes the projection and brings the folder back.
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: ""})
	e.call(e.owner, "project.compendium.ref.remove", compendiums.RefRemoveIn{ID: pid, CompendiumID: other})
	e.call(e.owner, "project.entity.link.remove", entities.LinkRemoveIn{ID: pid, EntityID: cameo})
	e.settleAll()
	for _, f := range tree(t, root) {
		if strings.HasPrefix(f, "compendium/") {
			t.Errorf("%s remains after the project let go", f)
		}
	}
	if _, err := os.Stat(e.cmpRoot(cid)); err != nil {
		t.Errorf("the standalone folder did not come back: %v", err)
	}
}

// A trashed entity and a trashed Compendium leave the projection; restoring
// brings them back.
func TestTrashedMaterialLeavesTheProjection(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Book")
	cid := e.compendium("World")
	ada := e.entity(cid, "character", "Ada")
	e.entity(cid, "character", "Bea")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	e.settleAll()
	count := func() int {
		n := 0
		for _, f := range tree(t, e.root(pid)) {
			if strings.HasPrefix(f, "compendium/entities/") {
				n++
			}
		}
		return n
	}
	if count() != 2 {
		t.Fatalf("%d entity files", count())
	}
	e.call(e.owner, "entity.trash", entities.StateIn{ID: ada})
	e.settleAll()
	if count() != 1 {
		t.Errorf("after trashing an entity: %d files", count())
	}
	e.call(e.owner, "compendium.trash", compendiums.StateIn{ID: cid})
	e.settleAll()
	if count() != 0 {
		t.Errorf("after trashing the Compendium: %d files", count())
	}
	e.call(e.owner, "compendium.restore", compendiums.StateIn{ID: cid})
	e.call(e.owner, "entity.restore", entities.StateIn{ID: ada})
	e.settleAll()
	if count() != 2 {
		t.Errorf("after restoring: %d files", count())
	}
}

// Purging a Compendium removes its standalone folder, and the projection of
// every project that used it, hidden ones included.
func TestPurgeCompendiumRemovesFolders(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Book")
	cid := e.compendium("World")
	e.entity(cid, "character", "Ada")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	solo := e.compendium("Solo")
	e.entity(solo, "item", "Key")
	e.settleAll()
	soloRoot := e.cmpRoot(solo)
	if _, err := os.Stat(soloRoot); err != nil {
		t.Fatal(err)
	}
	e.call(e.owner, "compendium.trash", compendiums.StateIn{ID: solo})
	e.call(e.owner, "compendium.purge", compendiums.PurgeIn{ID: solo, ConfirmName: "Solo"})
	e.call(e.owner, "compendium.trash", compendiums.StateIn{ID: cid})
	e.call(e.owner, "compendium.purge", compendiums.PurgeIn{ID: cid, ConfirmName: "World", DetachProjects: []string{pid}})
	e.settleAll()
	if _, err := os.Stat(soloRoot); !os.IsNotExist(err) {
		t.Errorf("the purged Compendium's folder remains (%v)", err)
	}
	for _, f := range tree(t, e.root(pid)) {
		if strings.HasPrefix(f, "compendium/") {
			t.Errorf("%s remains in the project after its Compendium was purged", f)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(e.dir, "mirror"))
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), "compendium-") {
			t.Errorf("a Compendium folder %s remains", en.Name())
		}
	}
	if st := e.m.Status(solo); st.Directory != "" {
		t.Errorf("state remains for a purged Compendium: %+v", st)
	}
}

// At start-up a Compendium that changed while the server was down is found
// from its fingerprint and rewritten, one that was purged has its folder
// removed, a project's projection follows a change to its Compendium, and
// everything current is left untouched.
func TestStartupReconcilesCompendiums(t *testing.T) {
	e := newEnv(t)
	solo := e.compendium("Solo")
	soloEnt := e.entity(solo, "character", "Ada")
	gone := e.compendium("Gone")
	e.entity(gone, "item", "Key")
	shared := e.compendium("Shared")
	e.entity(shared, "place", "Harbour")
	pid := e.project("Book")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: shared})

	run := func() (*mirror.Mirror, *countFS, func()) {
		fs := &countFS{}
		m := mirror.New(mirror.Options{DataDir: e.dir, DB: e.d, Notifier: e.ev, FS: fs, Debounce: 10 * time.Millisecond})
		ctx, cancel := context.WithCancel(context.Background())
		m.Start(ctx)
		return m, fs, func() { cancel(); m.Wait() }
	}
	settle := func(m *mirror.Mirror) {
		time.Sleep(30 * time.Millisecond)
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		if err := m.WaitIdle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	m1, _, stop1 := run()
	settle(m1)
	// The start-up reconciliation can still be running when WaitIdle returns:
	// under the race detector the project's folder had not appeared yet and the
	// test read the mirror root (run #534). Wait for it, and for the file.
	dirDeadline := time.Now().Add(10 * time.Second)
	for m1.Status(pid).Directory == "" || m1.Status(solo).Directory == "" || m1.Status(gone).Directory == "" {
		if time.Now().After(dirDeadline) {
			t.Fatal("the mirror folders of the project and its two standalone Compendiums never all appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	soloRoot := filepath.Join(e.dir, "mirror", m1.Status(solo).Directory)
	goneRoot := filepath.Join(e.dir, "mirror", m1.Status(gone).Directory)
	projRoot := filepath.Join(e.dir, "mirror", m1.Status(pid).Directory)
	fileDeadline := time.Now().Add(10 * time.Second)
	for !has(tree(t, projRoot), "compendium/index.json") {
		if time.Now().After(fileDeadline) {
			t.Fatalf("project files: %v", tree(t, projRoot))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if m1.Status(shared).Directory != "" {
		t.Errorf("a project's primary Compendium has a folder of its own: %q", m1.Status(shared).Directory)
	}
	stop1()

	// Nothing changed: not one file is written.
	m2, fs2, stop2 := run()
	settle(m2)
	if n := fs2.writes(); n != 0 {
		t.Errorf("current mirrors were rewritten at start-up: %v", fs2.renames)
	}
	stop2()

	// Changes made while no mirror was running.
	e.call(e.owner, "entity.update", entities.UpdateIn{ID: soloEnt, Summary: strptr("Changed offline.")})
	e.entity(shared, "place", "Quay")
	e.call(e.owner, "compendium.trash", compendiums.StateIn{ID: gone})
	e.call(e.owner, "compendium.purge", compendiums.PurgeIn{ID: gone, ConfirmName: "Gone"})
	m3, fs3, stop3 := run()
	settle(m3)
	defer stop3()
	// As at the first start, WaitIdle can return before the start-up reconciliation has run (the race build on the CI
	// runner): wait until the reconciliation's last effects are there, then check each of them.
	places := func() int {
		n := 0
		for _, f := range tree(t, projRoot) {
			if strings.HasPrefix(f, "compendium/entities/places/") {
				n++
			}
		}
		return n
	}
	reconciled := time.Now().Add(15 * time.Second)
	for time.Now().Before(reconciled) {
		_, err := os.Stat(goneRoot)
		if os.IsNotExist(err) && fs3.wrote("ada--"+idTail(soloEnt)+".md") >= 1 && places() == 2 && m3.Status(solo).InSync {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(goneRoot); !os.IsNotExist(err) {
		t.Errorf("the purged Compendium's folder remains: %v", err)
	}
	if fs3.wrote("ada--"+idTail(soloEnt)+".md") != 1 || !strings.Contains(readGlob(t, soloRoot, "entities/characters/ada--"), "Changed offline.") {
		t.Errorf("the changed Compendium was not rewritten: %v", fs3.renames)
	}
	if n := places(); n != 2 {
		t.Errorf("the project's projection has %d places after a change to its Compendium, want 2", n)
	}
	if st := m3.Status(solo); !st.InSync {
		t.Errorf("%+v", st)
	}
}

func idTail(id string) string { return strings.ToLower(id[len(id)-6:]) }

func readGlob(t *testing.T, root, prefix string) string {
	t.Helper()
	for _, f := range tree(t, root) {
		if strings.HasPrefix(f, prefix) {
			return read(t, filepath.Join(root, f))
		}
	}
	t.Fatalf("no file under %s%s", root, prefix)
	return ""
}

// The coverage list for Compendium ops: every mutating op whose name starts
// compendium., entity. or mirror.compendium. is either mirrored (and the
// action proves the mirror wakes and ends in sync) or exempt with a reason.
func TestEveryMutatingCompendiumOpReachesTheMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	cid := e.compendium("Coverage")
	other := e.compendium("Other")
	ada := e.entity(cid, "character", "Ada")
	bea := e.entity(cid, "character", "Bea")
	cal := e.entity(cid, "character", "Cal")
	var aliasID, relID string
	watch := cid
	// Research, tags and an attachment a note links to.
	att := e.cupload(cid, "portrait.png", 5)
	note := e.call(e.owner, "compendium.research.create", research.CreateIn{ID: cid, Title: "Bible note", Body: "![p](" + att.URL + ")", Tags: []string{"one"}}).(research.Item).Item
	folder := e.call(e.owner, "compendium.research.create", research.CreateIn{ID: cid, Title: "Bible folder", Kind: "folder"}).(research.Item).Item
	tag1 := e.call(e.owner, "compendium.tag.create", research.TagCreateIn{ID: cid, Name: "alpha"}).(research.TagItem).Item
	tag2 := e.call(e.owner, "compendium.tag.create", research.TagCreateIn{ID: cid, Name: "beta"}).(research.TagItem).Item

	mirrored := map[string]func(){
		"compendium.create": func() { watch = e.compendium("Created") },
		"compendium.update": func() {
			e.call(e.owner, "compendium.update", compendiums.UpdateIn{ID: cid, Description: strptr("Described.")})
		},
		"compendium.archive":   func() { e.call(e.owner, "compendium.archive", compendiums.StateIn{ID: cid}) },
		"compendium.unarchive": func() { e.call(e.owner, "compendium.unarchive", compendiums.StateIn{ID: cid}) },
		"compendium.trash":     func() { e.call(e.owner, "compendium.trash", compendiums.StateIn{ID: cid}) },
		"compendium.restore":   func() { e.call(e.owner, "compendium.restore", compendiums.StateIn{ID: cid}) },
		"compendium.ref.add": func() {
			e.call(e.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: cid, TargetID: other})
		},
		"compendium.ref.remove": func() {
			e.call(e.owner, "compendium.ref.remove", compendiums.CompendiumRefRemoveIn{ID: cid, TargetID: other})
		},
		"compendium.purge": func() {
			doomed := e.compendium("Doomed")
			e.settleAll()
			e.call(e.owner, "compendium.trash", compendiums.StateIn{ID: doomed})
			e.call(e.owner, "compendium.purge", compendiums.PurgeIn{ID: doomed, ConfirmName: "Doomed"})
			watch = doomed
		},
		"entity.create": func() { e.entity(cid, "item", "Key") },
		"entity.update": func() {
			e.call(e.owner, "entity.update", entities.UpdateIn{ID: ada, Summary: strptr("Updated.")})
		},
		"entity.trash":   func() { e.call(e.owner, "entity.trash", entities.StateIn{ID: bea}) },
		"entity.restore": func() { e.call(e.owner, "entity.restore", entities.StateIn{ID: bea}) },
		"entity.alias.add": func() {
			aliasID = e.call(e.owner, "entity.alias.add", entities.AliasAddIn{ID: ada, Alias: "Addie"}).(entities.AliasItem).Alias.ID
		},
		"entity.alias.remove": func() {
			e.call(e.owner, "entity.alias.remove", entities.AliasRemoveIn{ID: ada, AliasID: aliasID})
		},
		"entity.relation.add": func() {
			relID = e.call(e.owner, "entity.relation.add", entities.RelationAddIn{ID: ada, ToEntityID: bea, Kind: "ally"}).(entities.RelationItem).Item.ID
		},
		"entity.relation.remove": func() {
			e.call(e.owner, "entity.relation.remove", entities.RelationRemoveIn{ID: ada, RelationID: relID})
		},
		"entity.field.set": func() {
			e.call(e.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "weight", Type: "number"})
		},
		"entity.field.remove": func() {
			e.call(e.owner, "entity.field.remove", entities.FieldRemoveIn{ID: cid, Kind: "item", Key: "weight"})
		},
		"entity.merge": func() {
			e.call(e.owner, "entity.merge", entities.MergeIn{ID: cal, IntoID: ada})
		},
		"compendium.merge": func() {
			src, dst := e.compendium("Merge source"), e.compendium("Merge target")
			e.entity(src, "character", "Mover")
			e.settleAll()
			in := compendiums.MergeIn{ID: src, IntoID: dst}
			in.Fingerprint = e.call(e.owner, "compendium.merge.preview", in).(compendiums.PlanOut).Plan.Fingerprint
			e.call(e.owner, "compendium.merge", in)
			watch = dst
		},
		"compendium.split": func() {
			src := e.compendium("Split source")
			mover := e.entity(src, "character", "Splitter")
			e.settleAll()
			in := compendiums.SplitIn{ID: src, Name: "Split off"}
			in.EntityIDs = []string{mover}
			in.Fingerprint = e.call(e.owner, "compendium.split.preview", in).(compendiums.PlanOut).Plan.Fingerprint
			watch = e.call(e.owner, "compendium.split", in).(compendiums.ChangeOut).TargetID
		},
		"compendium.primary.set": func() {
			bible, book := e.compendium("Primary bible"), e.project("Primary book")
			e.settleAll()
			in := compendiums.PrimaryIn{ID: book, CompendiumID: bible}
			in.Fingerprint = e.call(e.owner, "compendium.primary.preview", in).(compendiums.PrimaryOut).Plan.Fingerprint
			e.call(e.owner, "compendium.primary.set", in)
			watch = book
		},
		"compendium.research.create": func() {
			e.call(e.owner, "compendium.research.create", research.CreateIn{ID: cid, Title: "Another bible note", Body: "More."})
		},
		"compendium.research.update": func() {
			cur := e.call(e.owner, "compendium.research.get", research.NoteIn{ID: cid, NoteID: note.ID}).(research.Item).Item
			b := "Changed words."
			e.call(e.owner, "compendium.research.update", research.UpdateIn{ID: cid, NoteID: note.ID, BaseRevision: cur.Revision, Body: &b})
		},
		"compendium.research.move": func() {
			p := folder.ID
			e.call(e.owner, "compendium.research.move", research.MoveIn{ID: cid, NoteID: note.ID, ParentID: &p})
		},
		"compendium.research.trash": func() {
			e.call(e.owner, "compendium.research.trash", research.NoteIn{ID: cid, NoteID: note.ID})
		},
		"compendium.research.restore": func() {
			e.call(e.owner, "compendium.research.restore", research.NoteIn{ID: cid, NoteID: note.ID})
		},
		"compendium.tag.update": func() {
			n := "alpha2"
			e.call(e.owner, "compendium.tag.update", research.TagUpdateIn{ID: cid, TagID: tag1.ID, Name: &n})
		},
		"compendium.tag.merge": func() {
			e.call(e.owner, "compendium.tag.merge", research.TagMergeIn{ID: cid, TagID: tag2.ID, IntoID: tag1.ID})
		},
		"compendium.tag.delete": func() {
			e.call(e.owner, "compendium.tag.delete", research.TagIn{ID: cid, TagID: tag1.ID})
		},
		"compendium.attachment.update": func() {
			a := "A new alt"
			e.call(e.owner, "compendium.attachment.update", attachments.UpdateIn{ID: cid, AttID: att.ID, Alt: &a})
		},
		"compendium.attachment.delete": func() {
			e.call(e.owner, "compendium.attachment.delete", attachments.DeleteIn{ID: cid, AttID: att.ID, Force: true})
		},
	}
	exempt := map[string]string{
		"compendium.member.add": "access, not content", "compendium.member.update": "access, not content", "compendium.member.remove": "access, not content",
		"compendium.export": "reads", "mirror.compendium.rebuild": "is the mirror",
		"compendium.merge.preview": "reads", "compendium.split.preview": "reads", "compendium.primary.preview": "reads",
		"compendium.tag.create":        "a tag no note carries is not in a file; carrying one is compendium.research.update",
		"compendium.scratch.set":       "a private scratchpad is never mirrored (TestScratchpadIsNotMirrored)",
		"compendium.attachment.upload": "an attachment nothing links to is not mirrored; the note that links it is (compendium.research.update)",
	}
	for _, en := range e.reg.Entries() {
		n := en.Op.Name
		if !(strings.HasPrefix(n, "compendium.") || strings.HasPrefix(n, "entity.") || strings.HasPrefix(n, "mirror.compendium.")) || en.Op.Method == "GET" {
			continue
		}
		if mirrored[n] == nil && exempt[n] == "" {
			t.Errorf("%s changes data but is neither covered nor exempt in this test; does the mirror need to see it?", n)
		}
	}
	e.settleAll()
	order := []string{"compendium.research.create", "compendium.research.update", "compendium.research.move", "compendium.research.trash", "compendium.research.restore",
		"compendium.tag.update", "compendium.tag.merge", "compendium.tag.delete", "compendium.attachment.update", "compendium.attachment.delete",
		"compendium.update", "entity.create", "entity.update", "entity.alias.add", "entity.alias.remove", "entity.relation.add",
		"entity.relation.remove", "entity.field.set", "entity.field.remove", "entity.trash", "entity.restore", "entity.merge",
		"compendium.merge", "compendium.split", "compendium.primary.set",
		"compendium.ref.add", "compendium.ref.remove", "compendium.archive", "compendium.unarchive", "compendium.trash", "compendium.restore",
		"compendium.create", "compendium.purge"}
	for _, n := range order {
		act := mirrored[n]
		if act == nil {
			t.Fatalf("no action for %s", n)
		}
		watch = cid
		act()
		if n != "compendium.purge" {
			deadline := time.Now().Add(5 * time.Second)
			for !e.m.Status(watch).Pending {
				if time.Now().After(deadline) {
					t.Fatalf("%s did not wake the mirror", n)
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
		e.flush()
		if n == "compendium.purge" {
			if st := e.m.Status(watch); st.Directory != "" {
				t.Errorf("compendium.purge left state: %+v", st)
			}
			continue
		}
		if st := e.m.Status(watch); !st.InSync {
			t.Errorf("%s: mirror not in sync afterwards: %+v", n, st)
		}
	}
}

// 5,000 entities: the first write, a quiet rewrite, one edit, and a chapter
// save in a project that uses the Compendium. The bounds are generous; they are
// there to catch a change that makes the mirror scale badly.
func TestFiveThousandEntitiesMirror(t *testing.T) {
	if testing.Short() || timingFactor > 1 {
		t.Skip("writes 5,000 files; skipped under -short and under the race detector")
	}
	// The push gate (SCRIOBH_TIMING=off) keeps every behaviour check of this test (the file counts, the quiet
	// rebuild, the two files of one edit, the chapter save that leaves the projection alone) at a tenth of the size
	// for fixture cost; the budgets are asserted, at full size, only when timing tests run.
	big, proj := 5000, 1000
	if testutil.TimingOff() {
		big, proj = 200, 50
	}
	e := newEnv(t)
	load := func(name string, n int) string {
		cid := e.compendium(name)
		now := db.Now()
		err := e.d.WithTx(e.ctx, func(tx *sql.Tx) error {
			for i := 0; i < n; i++ {
				kind := []string{"character", "place", "item", "org", "concept"}[i%5]
				if _, err := entities.Insert(e.ctx, tx, entities.CreateIn{ID: cid, Kind: kind, Name: fmt.Sprintf("Entity %05d", i),
					Summary: "A summary.", Body: fmt.Sprintf("Body %d.\n\nMore text here.\n", i)}, now); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return cid
	}
	cid := load("Big bible", big)
	pid := e.project("Book")
	e.start()
	type measure struct {
		name  string
		limit time.Duration
		d     time.Duration
	}
	var measures []measure
	timed := func(name string, limit time.Duration, f func()) {
		start := time.Now()
		f()
		d := time.Since(start)
		t.Logf("%s: %v (limit %v)", name, d.Round(time.Millisecond), limit)
		measures = append(measures, measure{name, limit, d})
	}
	timed("first write of a standalone Compendium of 5,000 entities", 120*time.Second, func() {
		// The start-up check finds the missing mirror and queues it.
		deadline := time.Now().Add(5 * time.Minute)
		for !e.m.Status(cid).InSync {
			if time.Now().After(deadline) {
				t.Fatal("the Compendium was never written")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	if n := len(tree(t, e.cmpRoot(cid))); n != big+3 {
		t.Fatalf("%d files, want %d", n, big+3)
	}
	root := e.cmpRoot(cid)
	e.fs.reset()
	timed("rebuild with nothing changed", 10*time.Second, func() {
		if _, err := e.m.Rebuild(e.ctx, cid); err != nil {
			t.Fatal(err)
		}
	})
	if n := e.fs.writesUnder(root); n != 0 {
		t.Errorf("a quiet rebuild wrote %d files", n)
	}
	first := e.call(e.owner, "entity.list", entities.ListIn{ID: cid, Limit: 1}).(entities.ListOut).Items[0]
	e.fs.reset()
	timed("one edit", 5*time.Second, func() {
		e.call(e.owner, "entity.update", entities.UpdateIn{ID: first.ID, Summary: strptr("Edited.")})
		e.settleAll()
	})
	if n := e.fs.writesUnder(root); n != 2 { // the entity's file, and the manifest that carries its hash
		t.Errorf("one edit wrote %d files, want 2", n)
	}

	// A project that uses a Compendium: the projection is written once, then a chapter save leaves it alone.
	projectBible := load("Book bible", proj)
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: projectBible})
	timed("first write of the project's projection of 1,000 entities", 120*time.Second, func() {
		e.caughtUp()
		deadline := time.Now().Add(5 * time.Minute)
		for {
			e.clk.Advance(mirror.DefaultDebounce)
			if st := e.m.Status(pid); st.Files >= proj+3 && st.InSync {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the projection was never written")
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	if n := len(tree(t, e.root(pid))); n < proj+3 {
		t.Fatalf("%d project files", n)
	}
	e.fs.reset()
	ch := e.doc(pid, "Chapter", "chapter", "text\n")
	e.settleAll()
	timed("a chapter save in a project with a 1,000-entity bible", 3*time.Second, func() {
		e.setContent(ch, "more text\n")
		e.settleAll()
	})
	for _, r := range e.fs.renames {
		if strings.Contains(r, "/compendium/") {
			t.Fatalf("a chapter save rewrote %s", r)
		}
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		for _, m := range measures {
			if m.d > m.limit*timingFactor {
				t.Errorf("%s took %v, over %v", m.name, m.d, m.limit*timingFactor)
			}
		}
	})
}

// writesUnder counts files written into one folder.
func (f *countFS) writesUnder(dir string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.renames {
		if strings.HasPrefix(r, dir+string(filepath.Separator)) {
			n++
		}
	}
	return n
}

// The compendium/ files a project's flush carries forward are not rendered, but
// they are still checked against the disk: delete them, or the whole folder, and
// the next write of the project puts them back.
func TestCarriedProjectionIsCheckedAgainstTheDisk(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Book")
	cid := e.compendium("World")
	e.entity(cid, "character", "Ada")
	e.entity(cid, "place", "Quay")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	ch := e.doc(pid, "Chapter", "chapter", "text\n")
	e.settleAll()
	root := e.root(pid)
	count := func() int {
		n := 0
		for _, f := range tree(t, root) {
			if strings.HasPrefix(f, "compendium/") {
				n++
			}
		}
		return n
	}
	if count() != 5 {
		t.Fatalf("%d projection files: %v", count(), tree(t, root))
	}
	// One file deleted by hand: a chapter save restores it.
	var victim string
	for _, f := range tree(t, root) {
		if strings.HasPrefix(f, "compendium/entities/places/") {
			victim = f
		}
	}
	if err := os.Remove(filepath.Join(root, victim)); err != nil {
		t.Fatal(err)
	}
	ch = e.setContent(ch, "more\n")
	e.settleAll()
	if _, err := os.Stat(filepath.Join(root, victim)); err != nil {
		t.Errorf("a deleted projection file was not restored by the next write: %v", err)
	}
	// The whole project folder deleted, its state record left behind: everything comes back.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	e.setContent(ch, "more again\n")
	e.settleAll()
	if count() != 5 {
		t.Errorf("after deleting the folder: %d projection files: %v", count(), tree(t, root))
	}
	if !has(tree(t, root), "manuscript/01-chapter-1.md") && len(tree(t, root)) < 8 {
		t.Errorf("the manuscript did not come back: %v", tree(t, root))
	}
}

// A Compendium that references another names it in its manifest, so renaming or
// deleting the target rewrites the referencing Compendium's folder too.
func TestReferencingCompendiumFollowsItsTarget(t *testing.T) {
	e := newEnv(t)
	e.start()
	a, b := e.compendium("Main"), e.compendium("Cosmology")
	e.entity(a, "character", "Ada")
	e.call(e.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: a, TargetID: b})
	e.settleAll()
	manifest := func() string { return read(t, filepath.Join(e.cmpRoot(a), "compendium.json")) }
	if !strings.Contains(manifest(), `"name": "Cosmology"`) {
		t.Fatalf("the manifest does not name the reference:\n%s", manifest())
	}
	e.call(e.owner, "compendium.update", compendiums.UpdateIn{ID: b, Name: strptr("Cosmos")})
	e.settleAll()
	if !strings.Contains(manifest(), `"name": "Cosmos"`) {
		t.Errorf("a rename of the target did not reach the referencing folder:\n%s", manifest())
	}
	e.call(e.owner, "compendium.trash", compendiums.StateIn{ID: b})
	e.call(e.owner, "compendium.purge", compendiums.PurgeIn{ID: b, ConfirmName: "Cosmos"})
	e.settleAll()
	if strings.Contains(manifest(), "Cosmos") {
		t.Errorf("a purged target is still named:\n%s", manifest())
	}
}
