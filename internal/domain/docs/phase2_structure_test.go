package docs_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func (g *fx2) docLinks(id string) []links.Link {
	g.t.Helper()
	out, err := g.call(g.ownA, "doc.links", links.LinksIn{ID: id})
	if err != nil {
		g.t.Fatal(err)
	}
	return out.(links.LinksOut).Items
}

func (g *fx2) backlinks(p ops.Principal, id string) links.BacklinksOut {
	g.t.Helper()
	out, err := g.call(p, "doc.backlinks", links.BacklinksIn{ID: id})
	if err != nil {
		g.t.Fatal(err)
	}
	return out.(links.BacklinksOut)
}

func (g *fx2) rename(id, title string) {
	g.t.Helper()
	d := g.head(g.ownA, id)
	if _, err := g.call(g.ownA, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Title: &title}); err != nil {
		g.t.Fatal(err)
	}
}

// linkState summarises a document's links as text -> target id ("" unresolved), plus ambiguity.
func linkState(ls []links.Link) map[string]string {
	m := map[string]string{}
	for _, l := range ls {
		id := ""
		if l.Target != nil {
			id = l.Target.ID
		}
		if l.Ambiguous {
			id += "!"
		}
		m[l.Text] = id
	}
	return m
}

func TestWikiLinkResolutionAcrossRenameTrashAndRestore(t *testing.T) {
	g := newFx2(t)
	elara := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Elara Vance"})
	maire := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Máire"})
	scene := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene", Content: "[[Elara Vance]] met [[MÁIRE]] and [[elara  vance|the captain]] and [[Ghost]]."})

	// Case, accents and whitespace fold; each distinct text is its own entry.
	got := linkState(g.docLinks(scene.ID))
	want := map[string]string{"Elara Vance": elara.ID, "MÁIRE": maire.ID, "elara  vance": elara.ID, "Ghost": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links %v, want %v", got, want)
	}
	ls := g.docLinks(scene.ID)
	if ls[0].Text != "Elara Vance" || ls[3].Text != "Ghost" || ls[2].Alias != "the captain" || ls[3].Resolved || !ls[0].Resolved || ls[0].Snippet == "" {
		t.Errorf("order, alias, flags: %+v", ls)
	}

	// Renaming the target breaks the links to its old title, and a new title can satisfy a waiting link.
	g.rename(elara.ID, "Elara")
	g.rename(maire.ID, "Ghost")
	got = linkState(g.docLinks(scene.ID))
	want = map[string]string{"Elara Vance": "", "MÁIRE": "", "elara  vance": "", "Ghost": maire.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after renames %v, want %v", got, want)
	}

	// A new document with the old title picks up both links at once.
	again := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "elara vance"})
	if got = linkState(g.docLinks(scene.ID)); got["Elara Vance"] != again.ID || got["elara  vance"] != again.ID {
		t.Fatalf("creating the target resolves waiting links: %v", got)
	}

	// Trash: links stop resolving (a trashed page is never a target); restore brings them back.
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: again.ID})
	if got = linkState(g.docLinks(scene.ID)); got["Elara Vance"] != "" {
		t.Fatalf("a trashed document is not a link target: %v", got)
	}
	g.save(g.ownA, scene.ID, "[[Elara Vance]] and [[Ghost]] and [[elara vance]] written while it was trashed.")
	if got = linkState(g.docLinks(scene.ID)); got["elara vance"] != "" || got["Elara Vance"] != "" {
		t.Fatalf("links written while the target is trashed do not resolve: %v", got)
	}
	g.call(g.ownA, "doc.restore", docs.DocIn{ID: again.ID})
	if got = linkState(g.docLinks(scene.ID)); got["Elara Vance"] != again.ID || got["elara vance"] != again.ID {
		t.Fatalf("restore resolves the links again: %v", got)
	}
	// Trashing a folder takes its children's titles out of play too.
	folder := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Folder", Kind: "part"})
	inside := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Inside", ParentID: folder.ID})
	g.save(g.ownA, scene.ID, "[[Inside]]")
	if got = linkState(g.docLinks(scene.ID)); got["Inside"] != inside.ID {
		t.Fatalf("%v", got)
	}
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: folder.ID})
	if got = linkState(g.docLinks(scene.ID)); got["Inside"] != "" {
		t.Fatalf("a trashed folder's children do not resolve: %v", got)
	}
	g.call(g.ownA, "doc.restore", docs.DocIn{ID: folder.ID})
	if got = linkState(g.docLinks(scene.ID)); got["Inside"] != inside.ID {
		t.Fatalf("%v", got)
	}

	// Removing the text removes the link rows; code and private notes never make links.
	g.save(g.ownA, scene.ID, "Nothing linked now.")
	if len(g.docLinks(scene.ID)) != 0 || g.count(`SELECT count(*) FROM document_links WHERE from_id = ?`, scene.ID) != 0 {
		t.Error("links must go when the text goes")
	}
	g.save(g.ownA, scene.ID, "```\n[[Code]]\n```\n\n%%note [[Private]]%% and [[Real]] and `[[Inline]]`.")
	if got = linkState(g.docLinks(scene.ID)); len(got) != 1 || got["Real"] != "" {
		t.Errorf("only the real link: %v", got)
	}
}

func TestAmbiguousTitlesResolveToTheFirstInReadingOrderAndFlag(t *testing.T) {
	g := newFx2(t)
	a := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Twin"})
	b := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "TWIN"})
	scene := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene", Content: "See [[twin]]."})
	if got := linkState(g.docLinks(scene.ID)); got["twin"] != a.ID+"!" {
		t.Fatalf("first in reading order, flagged: %v", got)
	}
	// Reordering changes which is first, without touching the linking document.
	if _, err := g.call(g.ownA, "doc.move", docs.MoveIn{ID: b.ID, After: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := linkState(g.docLinks(scene.ID)); got["twin"] != b.ID+"!" {
		t.Fatalf("after a move: %v", got)
	}
	// Nesting the new first one under a later part moves it later in reading order.
	part := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Late part", Kind: "part"})
	if _, err := g.call(g.ownA, "doc.move", docs.MoveIn{ID: b.ID, ParentID: part.ID}); err != nil {
		t.Fatal(err)
	}
	if got := linkState(g.docLinks(scene.ID)); got["twin"] != a.ID+"!" {
		t.Fatalf("after nesting: %v", got)
	}
	// Trashing one removes the ambiguity.
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: b.ID})
	if got := linkState(g.docLinks(scene.ID)); got["twin"] != a.ID {
		t.Fatalf("after trashing the twin: %v", got)
	}
	out, _ := g.call(g.ownA, "doc.resolve_link", docs.ResolveLinkIn{ProjectID: g.pidA, Title: "twin"})
	if r := out.(docs.ResolveLinkOut); !r.Found || r.Ambiguous || r.Item.ID != a.ID {
		t.Errorf("resolve %+v", r)
	}
}

func ptr(s string) *string { return &s }

func TestBacklinks(t *testing.T) {
	g := newFx2(t)
	part := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Part One", Kind: "part"})
	target := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Lantern", Content: "I link to myself: [[Lantern]]."})
	chap := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Chapter", ParentID: part.ID, After: ptr(""),
		Content: "The keeper lit the [[Lantern]] before dusk. Later the [[lantern|light]] failed, and %%secret plan%% the [[Lantern]] was lit again."})
	other := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Other", Content: "Only a short mention of [[Lantern|a lamp]]."})
	gone := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Gone", Content: "Trashed [[Lantern]]."})
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: gone.ID})

	// Another project with the same titles must not appear, and must not see ours.
	bPid := g.project(g.ownB, "Elsewhere")
	bTarget := g.doc(g.ownB, bPid, docs.CreateIn{Title: "Lantern", Content: "x"})
	bLinker := g.doc(g.ownB, bPid, docs.CreateIn{Title: "B linker", Content: "B links [[Lantern]]."})

	bl := g.backlinks(g.ownA, target.ID)
	if bl.Total != 2 || len(bl.Items) != 2 {
		t.Fatalf("backlinks %+v", bl)
	}
	first, second := bl.Items[0], bl.Items[1]
	if first.From.ID != chap.ID || second.From.ID != other.ID {
		t.Errorf("reading order: %s then %s", first.From.Title, second.From.Title)
	}
	if len(first.From.Breadcrumb) != 1 || first.From.Breadcrumb[0].ID != part.ID || first.From.Breadcrumb[0].Title != "Part One" || len(second.From.Breadcrumb) != 0 {
		t.Errorf("breadcrumbs %+v / %+v", first.From.Breadcrumb, second.From.Breadcrumb)
	}
	if first.Count != 3 || first.Text != "Lantern" || !strings.Contains(first.Snippet, "The keeper lit the Lantern before dusk") {
		t.Errorf("first %+v", first)
	}
	if strings.Contains(first.Snippet, "secret") {
		t.Error("private notes never appear in a snippet")
	}
	if second.Alias != "a lamp" || second.Count != 1 {
		t.Errorf("second %+v", second)
	}
	for _, b := range bl.Items {
		if b.From.ID == target.ID || b.From.ID == gone.ID {
			t.Errorf("self links and trashed documents are not backlinks: %s", b.From.Title)
		}
	}
	// Isolation both ways.
	bb := g.backlinks(g.ownB, bTarget.ID)
	if bb.Total != 1 || bb.Items[0].From.ID != bLinker.ID {
		t.Errorf("project B backlinks %+v", bb)
	}
	if _, err := g.call(g.ownB, "doc.backlinks", links.BacklinksIn{ID: target.ID}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("another project's document: %v", err)
	}
	if _, err := g.call(g.stranger, "doc.links", links.LinksIn{ID: chap.ID}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a stranger: %v", err)
	}
	if g.backlinks(g.reader, target.ID).Total != 2 {
		t.Error("a reader may read backlinks")
	}
	// The linking document trashed disappears; restored it returns. A trashed target has none.
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: other.ID})
	if g.backlinks(g.ownA, target.ID).Total != 1 {
		t.Error("a trashed linker is hidden")
	}
	g.call(g.ownA, "doc.restore", docs.DocIn{ID: other.ID})
	if g.backlinks(g.ownA, target.ID).Total != 2 {
		t.Error("a restored linker returns")
	}
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: target.ID})
	if g.backlinks(g.ownA, target.ID).Total != 0 {
		t.Error("nothing links to a trashed document")
	}
	g.call(g.ownA, "doc.restore", docs.DocIn{ID: target.ID})
	// Paging.
	out, err := g.call(g.ownA, "doc.backlinks", links.BacklinksIn{ID: target.ID, Limit: 1, Offset: 1})
	if got := out.(links.BacklinksOut); err != nil || got.Total != 2 || len(got.Items) != 1 || got.Items[0].From.ID != other.ID {
		t.Errorf("page %+v %v", got, err)
	}
	if _, err := g.call(g.ownA, "doc.backlinks", links.BacklinksIn{ID: target.ID, Offset: -1}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("negative offset: %v", err)
	}
}

func TestResolveLinkAndAutoCreate(t *testing.T) {
	g := newFx2(t)
	scene := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene", Content: "Beware the [[Dragon Lore]]."})
	if ls := g.docLinks(scene.ID); ls[0].Resolved {
		t.Fatal("the link waits for its page")
	}
	resolve := func(p ops.Principal, in docs.ResolveLinkIn) (docs.ResolveLinkOut, error) {
		in.ProjectID = g.pidA
		out, err := g.call(p, "doc.resolve_link", in)
		if err != nil {
			return docs.ResolveLinkOut{}, err
		}
		return out.(docs.ResolveLinkOut), nil
	}
	nDocs := func() int { return g.count(`SELECT count(*) FROM documents WHERE project_id = ?`, g.pidA) }
	before := nDocs()

	// Looking up never creates.
	r, err := resolve(g.ownA, docs.ResolveLinkIn{Title: "dragon lore"})
	if err != nil || r.Found || r.Item != nil || nDocs() != before {
		t.Fatalf("lookup %+v %v", r, err)
	}
	// Auto-create: a note titled by the link text, at the end of the tree, and the waiting link resolves.
	r, err = resolve(g.ownA, docs.ResolveLinkIn{Title: "  Dragon Lore \n", Create: true})
	if err != nil || !r.Created || !r.Found || r.Item.Kind != "note" || r.Item.Title != "Dragon Lore" || r.Item.ParentID != "" || nDocs() != before+1 {
		t.Fatalf("create %+v %v", r, err)
	}
	if ls := g.docLinks(scene.ID); !ls[0].Resolved || ls[0].Target.ID != r.Item.ID {
		t.Errorf("the waiting link resolves to the new page: %+v", ls[0])
	}
	if bl := g.backlinks(g.ownA, r.Item.ID); bl.Total != 1 || bl.Items[0].From.ID != scene.ID {
		t.Errorf("backlinks of the new page: %+v", bl)
	}
	// A second call finds it.
	again, _ := resolve(g.ownA, docs.ResolveLinkIn{Title: "DRAGON LORE", Create: true})
	if again.Created || again.Item.ID != r.Item.ID || nDocs() != before+1 {
		t.Errorf("second call %+v", again)
	}
	if g.sink.count("doc.created") < 1 {
		t.Error("doc.created not emitted")
	}

	// With a top-level Notes folder, new pages go at its end.
	notes := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "notes", Kind: "part"})
	first := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Existing note", ParentID: notes.ID})
	n, err := resolve(g.ownA, docs.ResolveLinkIn{Title: "Harbour Map", Create: true, Kind: "scene"})
	if err != nil || n.Item.ParentID != notes.ID || n.Item.Kind != "scene" || n.Item.Position <= first.Position {
		t.Fatalf("into Notes %+v %v", n, err)
	}
	// Trashed documents do not satisfy a lookup: the title is created afresh.
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: n.Item.ID})
	n2, _ := resolve(g.ownA, docs.ResolveLinkIn{Title: "Harbour Map", Create: true})
	if !n2.Created || n2.Item.ID == n.Item.ID {
		t.Errorf("a trashed page is not found: %+v", n2)
	}

	// Validation, and who may call.
	if _, err := resolve(g.ownA, docs.ResolveLinkIn{Title: "  "}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("empty title: %v", err)
	}
	if _, err := resolve(g.ownA, docs.ResolveLinkIn{Title: "x", Create: true, Kind: "nonsense"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("bad kind: %v", err)
	}
	if _, err := resolve(g.reader, docs.ResolveLinkIn{Title: "Dragon Lore"}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader follows links through doc.links, not here: %v", err)
	}
	if _, err := resolve(g.stranger, docs.ResolveLinkIn{Title: "Dragon Lore"}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a stranger: %v", err)
	}
	if n := nDocs(); n != before+5 {
		t.Errorf("documents %d", n)
	}
}

func TestSplitRewritesBothHalvesLinks(t *testing.T) {
	g := newFx2(t)
	tail := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Two"})
	whole := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Whole", Content: "First [[A]].\n\nSecond [[Two]] and [[B]].\n"})
	out, err := g.call(g.ownA, "doc.split", docs.SplitIn{ID: whole.ID, BaseRevision: 1, Offset: len("First [[A]].\n\n"), Title: "Two"})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(docs.SplitOut)
	if got := linkState(g.docLinks(o.Head.ID)); len(got) != 1 || got["A"] != "" {
		t.Errorf("head links %v", got)
	}
	got := linkState(g.docLinks(o.Tail.ID))
	if len(got) != 2 || got["B"] != "" || got["Two"] == "" {
		t.Errorf("tail links %v", got)
	}
	_ = tail
}

func TestRebuildRepairsTheLinkGraph(t *testing.T) {
	g := newFx2(t)
	a := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "A", Content: "[[B]] and [[C]]"})
	b := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "B"})
	if _, err := g.d.Writer().Exec(`DELETE FROM document_links`); err != nil {
		t.Fatal(err)
	}
	n, err := links.Rebuild(g.ctx, g.d, g.pidA)
	if err != nil || n < 2 {
		t.Fatalf("rebuild %d %v", n, err)
	}
	if got := linkState(g.docLinks(a.ID)); got["B"] != b.ID || got["C"] != "" {
		t.Errorf("after rebuild %v", got)
	}
	if n, err := links.Rebuild(g.ctx, g.d, ""); err != nil || n < 2 {
		t.Errorf("rebuild all %d %v", n, err)
	}
}

// ---- scene metadata and the outline --------------------------------------------------

func TestSceneMetadataRoundTrips(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene", Kind: "scene"})
	if d.Pov != "" || d.Location != "" || d.StoryDate != "" || d.StoryDateEnd != "" {
		t.Fatalf("new documents start blank: %+v", d.Meta)
	}
	pov, loc, sd, se := "Máire", "The lighthouse", "1921-04-02", "1921-04-03"
	out, err := g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, Pov: &pov, Location: &loc, StoryDate: &sd, StoryDateEnd: &se})
	if err != nil {
		t.Fatal(err)
	}
	u := out.(docs.Item).Item
	if u.Pov != pov || u.Location != loc || u.StoryDate != sd || u.StoryDateEnd != se || u.Revision != d.Revision+1 {
		t.Fatalf("updated %+v", u.Meta)
	}
	// doc.get and doc.list agree.
	if got := g.head(g.ownA, d.ID); got.Pov != pov || got.StoryDateEnd != se {
		t.Errorf("get %+v", got.Meta)
	}
	lo, _ := g.call(g.ownA, "doc.list", docs.ListIn{ProjectID: g.pidA})
	found := false
	for _, m := range lo.(docs.ListOut).Items {
		if m.ID == d.ID {
			found = m.Pov == pov && m.Location == loc && m.StoryDate == sd && m.StoryDateEnd == se
		}
	}
	if !found {
		t.Error("doc.list carries scene metadata")
	}
	// Saving the same values changes nothing; metadata alone writes no version.
	out, _ = g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: u.Revision, Pov: &pov, Location: &loc})
	if out.(docs.Item).Item.Revision != u.Revision {
		t.Error("an unchanged save must not bump the revision")
	}
	if n := len(g.revs(g.ownA, d.ID)); n != 0 {
		t.Errorf("versions %d", n)
	}
	// Trimmed, one line, bounded, clearable.
	messy := "  Two\nLines  "
	out, _ = g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: u.Revision, Pov: &messy})
	if out.(docs.Item).Item.Pov != "Two Lines" {
		t.Errorf("pov %q", out.(docs.Item).Item.Pov)
	}
	long := strings.Repeat("x", 201)
	if _, err := g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: u.Revision + 1, Location: &long}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("long location: %v", err)
	}
	longDate := strings.Repeat("9", 65)
	if _, err := g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: u.Revision + 1, StoryDate: &longDate}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("long date: %v", err)
	}
	empty := ""
	out, _ = g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: u.Revision + 1, Pov: &empty, StoryDate: &empty})
	if r := out.(docs.Item).Item; r.Pov != "" || r.StoryDate != "" || r.Location != loc {
		t.Errorf("cleared %+v", r.Meta)
	}
}

func TestOutlineRollUps(t *testing.T) {
	g := newFx2(t)
	pid := g.project(g.ownA, "Outline")
	mk := func(in docs.CreateIn) docs.Doc { return g.doc(g.ownA, pid, in) }
	set := func(id string, status string, target int) {
		d := g.head(g.ownA, id)
		if _, err := g.call(g.ownA, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Status: &status, TargetWords: &target}); err != nil {
			t.Fatal(err)
		}
	}
	pre := mk(docs.CreateIn{Title: "Preface", Kind: "front_matter", Content: "One."})
	p1 := mk(docs.CreateIn{Title: "Part One", Kind: "part"})
	c1 := mk(docs.CreateIn{Title: "Ch1", ParentID: p1.ID, Content: "Three words here."})
	c2 := mk(docs.CreateIn{Title: "Ch2", ParentID: p1.ID, Content: "Four words in here."})
	p2 := mk(docs.CreateIn{Title: "Part Two", Kind: "part"})
	c3 := mk(docs.CreateIn{Title: "Ch3", ParentID: p2.ID, Content: "Two words."})
	set(c1.ID, "done", 10)
	set(c2.ID, "draft", 20)
	set(c3.ID, "idea", 5)
	set(p2.ID, "outline", 100)
	synL := "A long synopsis."
	d := g.head(g.ownA, c1.ID)
	g.call(g.ownA, "doc.update", docs.UpdateIn{ID: c1.ID, BaseRevision: d.Revision, SynopsisLong: &synL, SynopsisShort: ptr("Short.")})
	pov := "Máire"
	d = g.head(g.ownA, c1.ID)
	g.call(g.ownA, "doc.update", docs.UpdateIn{ID: c1.ID, BaseRevision: d.Revision, Pov: &pov})

	out, err := g.call(g.reader2(pid), "outline.get", docs.OutlineIn{ProjectID: pid})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(docs.OutlineOut)
	by := map[string]docs.OutlineItem{}
	var titles []string
	for _, it := range o.Items {
		by[it.Title] = it
		titles = append(titles, fmt.Sprintf("%s@%d", it.Title, it.Depth))
	}
	// The seeded first page ("Chapter 1" is not created for blank projects: it is untitled) sorts first.
	wantOrder := []string{"@0", "Preface@0", "Part One@0", "Ch1@1", "Ch2@1", "Part Two@0", "Ch3@1"}
	if !reflect.DeepEqual(titles, wantOrder) {
		t.Fatalf("order %v, want %v", titles, wantOrder)
	}
	lo, _ := g.call(g.ownA, "doc.list", docs.ListIn{ProjectID: pid})
	if !reflect.DeepEqual(lo.(docs.ListOut).Order, func() []string {
		var ids []string
		for _, it := range o.Items {
			ids = append(ids, it.ID)
		}
		return ids
	}()) {
		t.Error("the outline is in doc.list's reading order")
	}
	p := by["Part One"]
	if p.ChildCount != 2 || p.WordCount != 0 || p.Rollup.Documents != 3 || p.Rollup.Words != 7 || p.Rollup.TargetWords != 30 ||
		p.Rollup.Status != (docs.StatusCounts{Draft: 2, Done: 1}) || p.Rollup.Progress != 0.233 {
		t.Errorf("Part One %+v", p)
	}
	q := by["Part Two"]
	if q.Rollup.Documents != 2 || q.Rollup.Words != 2 || q.Rollup.TargetWords != 100 || q.Rollup.Progress != 0.02 || q.TargetWords != 100 ||
		q.Rollup.Status != (docs.StatusCounts{Outline: 1, Idea: 1}) {
		t.Errorf("Part Two keeps its own target over its children's: %+v", q)
	}
	c := by["Ch1"]
	if c.WordCount != 3 || c.TargetWords != 10 || c.Progress != 0.3 || c.Rollup.Documents != 1 || c.Rollup.Words != 3 || c.Rollup.Status.Done != 1 ||
		c.SynopsisShort != "Short." || c.SynopsisLong != synL || c.Pov != pov || c.Status != "done" || !c.IncludeInCompile || c.ParentID != p1.ID {
		t.Errorf("Ch1 %+v", c)
	}
	if o.Totals.Documents != 7 || o.Totals.Words != 10 || o.Totals.TargetWords != 130 || o.Totals.Status != (docs.StatusCounts{Draft: 4, Done: 1, Outline: 1, Idea: 1}) {
		t.Errorf("totals %+v", o.Totals)
	}
	// Deterministic, and a stranger sees nothing.
	again, _ := g.call(g.ownA, "outline.get", docs.OutlineIn{ProjectID: pid})
	if !reflect.DeepEqual(again, out) {
		t.Error("two calls must give the same outline")
	}
	if _, err := g.call(g.stranger, "outline.get", docs.OutlineIn{ProjectID: pid}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("stranger: %v", err)
	}
	// Trashed documents drop out of the outline and its totals.
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: p1.ID})
	out, _ = g.call(g.ownA, "outline.get", docs.OutlineIn{ProjectID: pid})
	if t2 := out.(docs.OutlineOut).Totals; t2.Documents != 4 || t2.Words != 3 {
		t.Errorf("totals after trashing a part %+v", t2)
	}
	_, _, _ = pre, c2, c3
}

// reader2 returns a principal that can read the given project (the owner).
func (g *fx2) reader2(string) ops.Principal { return g.ownA }

func TestAPartCanBeReorderedWithoutLosingAWord(t *testing.T) {
	g := newFx2(t)
	pid := g.project(g.ownA, "Reorder")
	mk := func(in docs.CreateIn) docs.Doc { return g.doc(g.ownA, pid, in) }
	a := mk(docs.CreateIn{Title: "Part A", Kind: "part", Content: "Part intro words."})
	a1 := mk(docs.CreateIn{Title: "A1", ParentID: a.ID, Content: "alpha one two three"})
	a2 := mk(docs.CreateIn{Title: "A2", ParentID: a.ID, Content: "alpha four five"})
	a3 := mk(docs.CreateIn{Title: "A3", ParentID: a.ID, Content: "alpha six"})
	b := mk(docs.CreateIn{Title: "Part B", Kind: "part"})
	b1 := mk(docs.CreateIn{Title: "B1", ParentID: b.ID, Content: "beta one two"})
	c := mk(docs.CreateIn{Title: "Part C", Kind: "part"})

	snapshot := func() (docs.OutlineOut, map[string]string) {
		out, err := g.call(g.ownA, "outline.get", docs.OutlineIn{ProjectID: pid})
		if err != nil {
			t.Fatal(err)
		}
		content := map[string]string{}
		for _, it := range out.(docs.OutlineOut).Items {
			content[it.ID] = g.head(g.ownA, it.ID).Content
		}
		return out.(docs.OutlineOut), content
	}
	before, contentBefore := snapshot()
	titles := func(o docs.OutlineOut) string {
		var s []string
		for _, it := range o.Items {
			if it.Title != "" {
				s = append(s, it.Title)
			}
		}
		return strings.Join(s, ",")
	}
	if titles(before) != "Part A,A1,A2,A3,Part B,B1,Part C" {
		t.Fatalf("start %s", titles(before))
	}

	// Part A goes after Part C: the whole subtree travels with it.
	if _, err := g.call(g.ownA, "doc.move", docs.MoveIn{ID: a.ID, After: &c.ID}); err != nil {
		t.Fatal(err)
	}
	after, contentAfter := snapshot()
	if titles(after) != "Part B,B1,Part C,Part A,A1,A2,A3" {
		t.Fatalf("after move %s", titles(after))
	}
	if after.Totals != before.Totals || after.Totals.Words != 4+3+2+3+3 {
		t.Errorf("totals changed: %+v -> %+v", before.Totals, after.Totals)
	}
	if !reflect.DeepEqual(contentBefore, contentAfter) {
		t.Error("no text may change by reordering")
	}
	for _, id := range []string{a1.ID, a2.ID, a3.ID} {
		if g.head(g.ownA, id).ParentID != a.ID {
			t.Errorf("%s left its part", id)
		}
	}
	if g.head(g.ownA, b1.ID).ParentID != b.ID {
		t.Error("B1 left its part")
	}
	// To the front, and nested inside another part, and a part cannot go inside its own chapter.
	if _, err := g.call(g.ownA, "doc.move", docs.MoveIn{ID: a.ID, After: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if s, _ := snapshot(); titles(s) != "Part A,A1,A2,A3,Part B,B1,Part C" || s.Totals != before.Totals {
		t.Errorf("back to the front: %s %+v", titles(s), s.Totals)
	}
	if _, err := g.call(g.ownA, "doc.move", docs.MoveIn{ID: a.ID, ParentID: a2.ID}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a part inside its own chapter: %v", err)
	}
	if s, c2 := snapshot(); s.Totals != before.Totals || !reflect.DeepEqual(c2, contentBefore) {
		t.Error("a refused move changes nothing")
	}
	// Reordering chapters inside a part too.
	if _, err := g.call(g.ownA, "doc.move", docs.MoveIn{ID: a3.ID, ParentID: a.ID, After: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if s, _ := snapshot(); titles(s) != "Part A,A3,A1,A2,Part B,B1,Part C" || s.Totals != before.Totals {
		t.Errorf("chapter reorder %s", titles(s))
	}
}

func TestOutlineOfAThousandDocumentsIsFastAndStable(t *testing.T) {
	g := newFx2(t)
	pid := g.project(g.ownA, "Thousand")
	now := db.Now()
	tx, err := g.d.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for p := 0; p < 20; p++ {
		pidDoc := db.NewID("doc")
		parts = append(parts, pidDoc)
		if _, err := tx.Exec(`INSERT INTO documents(id, project_id, kind, title, position, created_at, updated_at, content_hash) VALUES (?,?,?,?,?,?,?,'')`,
			pidDoc, pid, "part", fmt.Sprintf("Part %d", p), float64(p+1)*65536, now, now); err != nil {
			t.Fatal(err)
		}
		for c := 0; c < 49; c++ {
			if _, err := tx.Exec(`INSERT INTO documents(id, project_id, parent_id, kind, title, position, word_count, target_words, synopsis_long, created_at, updated_at, content_hash)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,'')`,
				db.NewID("doc"), pid, pidDoc, "chapter", fmt.Sprintf("Chapter %d.%d", p, c), float64(c+1)*65536, 1000, 1500, strings.Repeat("synopsis ", 40), now, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var first, second docs.OutlineOut
	start := time.Now()
	out, err := g.call(g.ownA, "outline.get", docs.OutlineIn{ProjectID: pid})
	el := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	first = out.(docs.OutlineOut)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if el > 150*time.Millisecond*timingFactor {
			t.Errorf("outline of %d documents took %v", len(first.Items), el)
		}
	})
	if len(first.Items) != 1001 || first.Totals.Words != 20*49*1000 || first.Totals.TargetWords != 20*49*1500 {
		t.Errorf("items %d totals %+v", len(first.Items), first.Totals)
	}
	out, _ = g.call(g.ownA, "outline.get", docs.OutlineIn{ProjectID: pid})
	second = out.(docs.OutlineOut)
	if !reflect.DeepEqual(first, second) {
		t.Error("the outline must be deterministic")
	}
}

// ---- lifecycle -------------------------------------------------------------------------

func TestPurgingAProjectLeavesNoHistoryBehind(t *testing.T) {
	g := newFx2(t)
	pid := g.project(g.ownA, "Doomed")
	d := g.doc(g.ownA, pid, docs.CreateIn{Title: "A", Content: strings.Repeat("words ", 3000)})
	g.doc(g.ownA, pid, docs.CreateIn{Title: "B", Content: "links to [[A]]"})
	g.save(g.ownA, d.ID, strings.Repeat("other ", 3000))
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "third")
	g.call(g.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "keep"})
	if g.count(`SELECT count(*) FROM document_revisions`) == 0 || g.count(`SELECT count(*) FROM revision_blobs`) == 0 || g.count(`SELECT count(*) FROM document_links`) == 0 {
		t.Fatal("test setup wrote no history")
	}
	if _, err := g.call(g.ownA, "project.trash", projects.StateIn{ID: pid}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.call(g.ownA, "project.purge", projects.PurgeIn{ID: pid, ConfirmTitle: "Doomed"}); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"document_revisions", "revision_blobs", "document_links"} {
		// Project A (the fixture's own) has no documents with history, so any row is a leak.
		if n := g.count(`SELECT count(*) FROM ` + tbl); n != 0 {
			t.Errorf("%s kept %d rows after a purge", tbl, n)
		}
	}
}
