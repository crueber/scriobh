package mentions_test

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
)

// A small book: one Compendium, three entities, one project.
type book struct {
	*h
	cid, pid               string
	maire, elara, saltRoad string
}

func newBook(t *testing.T) *book {
	x := newH(t)
	b := &book{h: x}
	b.cid = x.compendium(x.owner, "The Salt Road Bible")
	b.maire = x.ent(x.owner, b.cid, "character", "Máire Ní Bhriain", "Máire")
	b.elara = x.ent(x.owner, b.cid, "character", "Elara")
	b.saltRoad = x.ent(x.owner, b.cid, "place", "Salt Road")
	b.pid = x.project(x.owner, "Salt", b.cid)
	return b
}

func TestMentionsFollowEveryDocumentOp(t *testing.T) {
	b := newBook(t)
	d := b.doc(b.owner, b.pid, "One", "Máire walked the Salt Road. Elara followed Máire's boat.")
	// Created: indexed at once, in the same transaction.
	got := b.counts(b.owner, d.ID)
	if want := map[string]int{"Máire Ní Bhriain": 2, "Elara": 1, "Salt Road": 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after create: %v, want %v", got, want)
	}
	// The vocabulary changed before the first scan: until it runs, reads say so.
	if !b.list(b.owner, d.ID, false).Indexing {
		t.Errorf("a scan is owed before the worker has run")
	}
	b.settle()
	if b.list(b.owner, d.ID, false).Indexing {
		t.Errorf("settled, yet still indexing")
	}

	// Updated.
	d = b.save(b.owner, d, "Elara alone on the Salt Road, then Elara again.")
	if got, want := b.counts(b.owner, d.ID), map[string]int{"Elara": 2, "Salt Road": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("after update: %v, want %v", got, want)
	}
	// An update that changes no text leaves the rows alone.
	if got, want := b.count(`SELECT count(*) FROM entity_mentions WHERE document_id = ?`, d.ID), 2; got != want {
		t.Errorf("rows %d, want %d", got, want)
	}

	// Split: both halves are indexed, the head loses what it gave away.
	whole := b.save(b.owner, d, "Máire opens the door.\n\nElara waits outside by the Salt Road.")
	cut := len(utf16.Encode([]rune("Máire opens the door.\n\n")))
	sp := b.call(b.owner, "doc.split", docs.SplitIn{ID: whole.ID, BaseRevision: whole.Revision, Offset: cut, Title: "Two"}).(docs.SplitOut)
	if got, want := b.counts(b.owner, sp.Head.ID), map[string]int{"Máire Ní Bhriain": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("head after split: %v, want %v", got, want)
	}
	if got, want := b.counts(b.owner, sp.Tail.ID), map[string]int{"Elara": 1, "Salt Road": 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("tail after split: %v, want %v", got, want)
	}

	// Restoring an older version re-indexes.
	hist := b.call(b.owner, "revision.list", revisions.ListIn{ID: sp.Head.ID}).(revisions.ListOut)
	var oldID string
	for _, v := range hist.Items {
		oldID = v.ID
	}
	head := b.call(b.owner, "doc.get", docs.DocIn{ID: sp.Head.ID}).(docs.Item).Item
	if oldID != "" {
		b.call(b.owner, "revision.restore", docs.RestoreIn{ID: sp.Head.ID, RevisionID: oldID, BaseRevision: head.Revision})
		want := b.counts(b.owner, sp.Head.ID)
		var n int
		b.d.Read().QueryRow(`SELECT count(*) FROM entity_mentions WHERE document_id = ?`, sp.Head.ID).Scan(&n)
		if n != len(want) {
			t.Errorf("rows %d for %v", n, want)
		}
	}

	// Trash keeps the rows (and the text); the reads leave the document out; restore needs no work.
	before := b.call(b.owner, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara"}).(mentions.ByEntityOut)
	b.call(b.owner, "doc.trash", docs.DocIn{ID: sp.Tail.ID})
	by := b.call(b.owner, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara"}).(mentions.ByEntityOut)
	if by.Documents != before.Documents-1 {
		t.Errorf("a trashed document appears: %+v (was %d)", by, before.Documents)
	}
	if n := b.count(`SELECT count(*) FROM entity_mentions WHERE document_id = ?`, sp.Tail.ID); n == 0 {
		t.Errorf("trashing dropped the rows")
	}
	b.call(b.owner, "doc.restore", docs.DocIn{ID: sp.Tail.ID})
	by = b.call(b.owner, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara"}).(mentions.ByEntityOut)
	if by.Documents != before.Documents {
		t.Errorf("a restored document is missing: %+v", by)
	}

	// Purging the project leaves no rows behind.
	b.call(b.owner, "project.trash", projects.StateIn{ID: b.pid})
	b.call(b.owner, "project.purge", projects.PurgeIn{ID: b.pid, ConfirmTitle: "Salt"})
	for _, tbl := range []string{"entity_mentions", "mention_state"} {
		if n := b.count(`SELECT count(*) FROM ` + tbl); n != 0 {
			t.Errorf("%s keeps %d rows after the project was purged", tbl, n)
		}
	}
}

func TestVocabularyChangesTriggerARescan(t *testing.T) {
	b := newBook(t)
	d1 := b.doc(b.owner, b.pid, "One", "Brannoch rode out at dawn. Later the Marshal spoke.")
	d2 := b.doc(b.owner, b.pid, "Two", "Nothing here.")
	b.settle()
	if got := b.counts(b.owner, d1.ID); len(got) != 0 {
		t.Fatalf("nothing is mentioned yet: %v", got)
	}

	// A new entity: found without touching the documents.
	bran := b.ent(b.owner, b.cid, "character", "Brannoch")
	if !b.list(b.owner, d1.ID, false).Indexing {
		t.Errorf("a new entity should owe a scan")
	}
	st := b.settle()
	if st.Documents == 0 {
		t.Errorf("no documents were scanned: %+v", st)
	}
	if got := b.counts(b.owner, d1.ID); got["Brannoch"] != 1 {
		t.Errorf("new entity: %v", got)
	}

	// An alias.
	b.call(b.owner, "entity.alias.add", entities.AliasAddIn{ID: bran, Alias: "the Marshal"})
	b.settle()
	if got := b.counts(b.owner, d1.ID); got["Brannoch"] != 2 {
		t.Errorf("after alias: %v", got)
	}

	// A rename: the old name stops matching, the new one starts.
	b.call(b.owner, "entity.update", entities.UpdateIn{ID: bran, Name: str("Nothing")})
	b.settle()
	if got := b.counts(b.owner, d2.ID); got["Nothing"] != 1 {
		t.Errorf("after rename: %v", got)
	}
	if got := b.counts(b.owner, d1.ID); got["Nothing"] != 1 {
		t.Errorf("the alias still matches: %v", got)
	}

	// Trash, restore.
	b.call(b.owner, "entity.trash", entities.StateIn{ID: bran})
	b.settle()
	if got := b.counts(b.owner, d2.ID); len(got) != 0 {
		t.Errorf("a trashed entity is still mentioned: %v", got)
	}
	b.call(b.owner, "entity.restore", entities.StateIn{ID: bran})
	b.settle()
	if got := b.counts(b.owner, d2.ID); got["Nothing"] != 1 {
		t.Errorf("a restored entity is missing: %v", got)
	}

	// A book's override renames it for this book only.
	b.call(b.owner, "project.entity.override.set", entities.OverrideSetIn{ID: b.pid, EntityID: bran, Name: str("Rider")})
	d3 := b.doc(b.owner, b.pid, "Three", "The Rider came back.")
	b.settle()
	if got := b.counts(b.owner, d3.ID); got["Rider"] != 1 {
		t.Errorf("override name: %v", got)
	}
	b.call(b.owner, "project.entity.override.clear", entities.OverrideClearIn{ID: b.pid, EntityID: bran})
	b.settle()
	if got := b.counts(b.owner, d3.ID); len(got) != 0 {
		t.Errorf("a cleared override still matches: %v", got)
	}

	// Merge: the source's rows move to the target; counts add where a document mentions both.
	d4 := b.doc(b.owner, b.pid, "Four", "Elara met the Salt Road's keeper. Máire spoke.")
	b.settle()
	b.call(b.owner, "entity.merge", entities.MergeIn{ID: b.maire, IntoID: b.elara, DryRun: false, Summary: "target"})
	got := b.counts(b.owner, d4.ID)
	if got["Elara"] != 2 || got["Máire Ní Bhriain"] != 0 {
		t.Errorf("right after a merge, before the rescan: %v", got)
	}
	b.settle()
	if got := b.counts(b.owner, d4.ID); got["Elara"] != 2 {
		t.Errorf("after the merge rescan: %v (the source's name is an alias of the target now)", got)
	}

	// Deleting the Compendium takes the vocabulary away.
	b.call(b.owner, "compendium.trash", compendiums.StateIn{ID: b.cid})
	b.settle()
	if n := b.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, b.pid); n != 0 {
		t.Errorf("a trashed Compendium leaves %d mention rows", n)
	}
	b.call(b.owner, "compendium.restore", compendiums.StateIn{ID: b.cid})
	b.settle()
	if n := b.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, b.pid); n == 0 {
		t.Errorf("a restored Compendium brings the mentions back")
	}
}

func TestExplicitLinksAndShownText(t *testing.T) {
	b := newBook(t)
	d := b.doc(b.owner, b.pid, "One", "[[Máire Ní Bhriain]] came in, and [[Máire Ní Bhriain|the captain]] sat. [[Máire|Máire]] waved.")
	b.settle()
	var m mentions.Mention
	for _, it := range b.list(b.owner, d.ID, false).Items {
		if it.Entity.ID == b.maire {
			m = it
		}
	}
	if m.Explicit != 3 {
		t.Errorf("explicit %d, want 3", m.Explicit)
	}
	// Prose finds the shown text of the first and third links (as the name and
	// as an alias); the second shows other words, so only the link can see it
	// and it is counted as explicit alone.
	if m.Count != 2 || m.Names != 1 || m.Aliases != 1 {
		t.Errorf("count %d names %d aliases %d, want 2, 1, 1 (%+v)", m.Count, m.Names, m.Aliases, m)
	}
	// A document that only links an entity still has a row.
	d2 := b.doc(b.owner, b.pid, "Two", "She was [[Elara|the girl]].")
	got := b.list(b.owner, d2.ID, false).Items
	if len(got) != 1 || got[0].Count != 0 || got[0].Explicit != 1 || got[0].Snippet == "" {
		t.Errorf("explicit-only: %+v", got)
	}
	by := b.call(b.owner, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara"}).(mentions.ByEntityOut)
	if by.Documents != 1 || by.Mentions != 1 {
		t.Errorf("by entity counts a linked document: %+v", by)
	}
}

func TestPrivateNotesAndCodeAreNotMentions(t *testing.T) {
	b := newBook(t)
	body := "Visible Elara.\n\n%%Máire is a secret in a private note%%\n\n```\nSalt Road in code\n```\n\n    Salt Road indented\n\nUse `Salt Road` inline."
	d := b.doc(b.owner, b.pid, "One", body)
	got := b.counts(b.owner, d.ID)
	if want := map[string]int{"Elara": 1, "Salt Road": 1}; !reflect.DeepEqual(got, want) { // the inline code counts as prose
		t.Errorf("%v, want %v", got, want)
	}
}

func TestSpansMatchTheCounts(t *testing.T) {
	b := newBook(t)
	body := "---\ntitle: x\n---\n\n😀 Máire met Elara.\r\n\r\nThen Máire’s boat, the Salt Road, and Elara."
	d := b.doc(b.owner, b.pid, "One", body)
	out := b.list(b.owner, d.ID, true)
	if !out.SpansComplete {
		t.Errorf("spans incomplete")
	}
	norm := strings.ReplaceAll(body, "\r\n", "\n")
	u := utf16.Encode([]rune(norm))
	for _, m := range out.Items {
		if len(m.Spans) != m.Count {
			t.Errorf("%s: %d spans for count %d", m.Entity.Name, len(m.Spans), m.Count)
		}
		for _, s := range m.Spans {
			word := string(utf16.Decode(u[s.Start:s.End]))
			if !strings.EqualFold(word, "Máire") && !strings.EqualFold(word, "Elara") && word != "Salt Road" {
				t.Errorf("%s: span covers %q", m.Entity.Name, word)
			}
		}
	}
}

func TestAmbiguousWordsAreFlaggedNeverGuessed(t *testing.T) {
	x := newH(t)
	primary := x.compendium(x.owner, "Primary")
	other := x.compendium(x.owner, "Other")
	a := x.ent(x.owner, primary, "character", "Bran")
	bb := x.ent(x.owner, other, "character", "Bran the Elder", "Bran")
	pid := x.project(x.owner, "Book", primary)
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: other})
	d := x.doc(x.owner, pid, "One", "Bran sat by the fire.")
	x.settle()
	out := x.list(x.owner, d.ID, false)
	if len(out.Items) != 1 || out.Items[0].Entity.ID != a {
		t.Fatalf("the primary Compendium has precedence: %+v", out.Items)
	}
	m := out.Items[0]
	if !m.Ambiguous || len(m.Also) != 1 || m.Also[0].ID != bb {
		t.Errorf("not flagged: %+v", m)
	}
	// A reader who may see only the primary is not told another meaning exists.
	x.projectMember(pid, x.editor, ops.ProjectEditor)
	x.member(primary, x.editor, "reader")
	got := x.list(x.editor, d.ID, false)
	if len(got.Items) != 1 || got.Items[0].Ambiguous || len(got.Items[0].Also) != 0 {
		t.Errorf("a hidden candidate leaked: %+v", got.Items)
	}
	if !got.Hidden && len(got.Items) == 1 {
		// nothing hidden in this document: the other Compendium's entity is not mentioned by the index
		t.Logf("hidden=%v", got.Hidden)
	}
}

func TestProjectReadersWithoutTheCompendiumSeeNothing(t *testing.T) {
	b := newBook(t)
	d := b.doc(b.owner, b.pid, "One", "Máire and Elara on the Salt Road.")
	b.settle()
	b.projectMember(b.pid, b.reader, ops.ProjectReader)
	out := b.list(b.reader, d.ID, true)
	if len(out.Items) != 0 || !out.Hidden {
		t.Errorf("a project reader got the bible: %+v", out)
	}
	if e := b.expect(b.reader, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara"}, ops.CodeNotFound); e == nil {
		t.Fatal("no error")
	}
	// The same 404 as for a name that does not exist.
	real := b.expect(b.reader, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara"}, ops.CodeNotFound)
	fake := b.expect(b.reader, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Nobody"}, ops.CodeNotFound)
	if real.Message != fake.Message {
		t.Errorf("an unreadable entity and a missing one answer differently: %q vs %q", real.Message, fake.Message)
	}
	fa := b.call(b.reader, "entity.first_appearance", mentions.FirstIn{ID: b.pid}).(mentions.FirstOut)
	if len(fa.Items) != 0 || !fa.Hidden {
		t.Errorf("first appearance leaks: %+v", fa)
	}
	// Once they join the Compendium, they see it.
	b.member(b.cid, b.reader, "reader")
	out = b.list(b.reader, d.ID, false)
	if len(out.Items) != 3 || out.Hidden {
		t.Errorf("a member sees %+v", out)
	}
}

func TestFirstAppearanceIsPerProjectOfASharedCompendium(t *testing.T) {
	b := newBook(t)
	other := b.project(b.owner, "Sequel", b.cid)
	// Book one: Elara first appears in chapter 2, after a chapter that does not mention her.
	b.doc(b.owner, b.pid, "Ch 1", "Only Máire here.")
	c2 := b.doc(b.owner, b.pid, "Ch 2", "Elara arrives.")
	b.doc(b.owner, b.pid, "Ch 3", "Elara again.")
	// Book two: she is in its very first scene.
	s1 := b.doc(b.owner, other, "Opening", "Elara, older now.")
	b.settle()
	f1 := b.call(b.owner, "entity.first_appearance", mentions.FirstIn{ID: b.pid, Entity: b.elara}).(mentions.FirstOut)
	f2 := b.call(b.owner, "entity.first_appearance", mentions.FirstIn{ID: other, Entity: "Elara"}).(mentions.FirstOut)
	if len(f1.Items) != 1 || f1.Items[0].Document == nil || f1.Items[0].Document.ID != c2.ID || f1.Items[0].Documents != 2 {
		t.Errorf("book one: %+v", f1.Items)
	}
	if len(f2.Items) != 1 || f2.Items[0].Document == nil || f2.Items[0].Document.ID != s1.ID {
		t.Errorf("book two: %+v", f2.Items)
	}
	// Moving a chapter changes "first" for this book only.
	c1 := b.call(b.owner, "doc.list", docs.ListIn{ProjectID: b.pid}).(docs.ListOut).Items[0]
	b.call(b.owner, "doc.move", docs.MoveIn{ID: c2.ID, After: str("")})
	_ = c1
	f1 = b.call(b.owner, "entity.first_appearance", mentions.FirstIn{ID: b.pid, Entity: b.elara}).(mentions.FirstOut)
	if f1.Items[0].Document.ID != c2.ID {
		t.Errorf("after the move: %+v", f1.Items[0].Document)
	}
	// Elara's page in the Compendium: both books, each with its own first.
	ap := b.call(b.owner, "entity.appears_in", mentions.AppearsIn{ID: b.elara}).(mentions.AppearsOut)
	if len(ap.Items) != 2 || ap.HiddenProjects != 0 {
		t.Fatalf("appears in: %+v", ap)
	}
	// Someone who can read the Compendium but not the sequel sees one book and a count.
	b.member(b.cid, b.editor, "reader")
	b.projectMember(b.pid, b.editor, ops.ProjectReader)
	ap = b.call(b.editor, "entity.appears_in", mentions.AppearsIn{ID: b.elara}).(mentions.AppearsOut)
	if len(ap.Items) != 1 || ap.Items[0].ProjectID != b.pid || ap.HiddenProjects != 1 {
		t.Errorf("a reader of one book: %+v", ap)
	}
}

func TestByEntityIsInReadingOrderAndPaged(t *testing.T) {
	b := newBook(t)
	var ids []string
	for i := 0; i < 5; i++ {
		d := b.doc(b.owner, b.pid, "Scene", "Elara in scene.")
		ids = append(ids, d.ID)
	}
	b.doc(b.owner, b.pid, "Quiet", "No one.")
	b.settle()
	// Move the last scene to the front.
	b.call(b.owner, "doc.move", docs.MoveIn{ID: ids[4], After: str("")})
	page := b.call(b.owner, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara", Limit: 2}).(mentions.ByEntityOut)
	if page.Documents != 5 || page.Mentions != 5 || len(page.Items) != 2 || page.NextOffset != 2 || page.Items[0].Document.ID != ids[4] {
		t.Errorf("page one: %+v", page)
	}
	page = b.call(b.owner, "mention.by_entity", mentions.ByEntityIn{ID: b.pid, Entity: "Elara", Limit: 2, Offset: 4}).(mentions.ByEntityOut)
	if len(page.Items) != 1 || page.NextOffset != 0 {
		t.Errorf("last page: %+v", page)
	}
	if page.Items[0].Snippet == "" {
		t.Errorf("no snippet")
	}
}

func TestTheWorkerRepairsOnceAndThenKeepsUp(t *testing.T) {
	b := newBook(t)
	d := b.doc(b.owner, b.pid, "One", "Elara met Máire.")
	b.settle()
	// An upgraded database: no index, no marker.
	b.exec(`DELETE FROM entity_mentions`)
	b.exec(`DELETE FROM settings WHERE key = ?`, mentions.IndexKey)
	old := mentions.PollInterval
	mentions.PollInterval = 10 * time.Millisecond
	defer func() { mentions.PollInterval = old }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mentions.Run(ctx, b.d, slog.New(slog.NewTextHandler(io.Discard, nil))); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 500; i++ {
			if ok() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the repair", func() bool {
		var v string
		b.d.Read().QueryRow(`SELECT value FROM settings WHERE key = ?`, mentions.IndexKey).Scan(&v)
		return v == mentions.IndexVersion
	})
	if n := b.count(`SELECT count(*) FROM entity_mentions WHERE document_id = ?`, d.ID); n != 2 {
		t.Errorf("repaired to %d rows, want 2", n)
	}
	// A new entity is picked up with no one calling Settle.
	b.doc(b.owner, b.pid, "Two", "Tomas arrived.")
	b.ent(b.owner, b.cid, "character", "Tomas")
	waitFor("the worker to catch up", func() bool { return b.count(`SELECT count(*) FROM mention_state WHERE stamp <> indexed_stamp`) == 0 })
	if n := b.count(`SELECT count(*) FROM entity_mentions e JOIN entities n ON n.id = e.entity_id WHERE n.name = 'Tomas'`); n != 1 {
		t.Errorf("the worker did not index the new entity: %d", n)
	}
}

func TestRebuildOpWithDryRun(t *testing.T) {
	b := newBook(t)
	b.doc(b.owner, b.pid, "One", "Elara.")
	b.settle()
	b.exec(`DELETE FROM entity_mentions`)
	dry := b.call(b.owner, "mention.rebuild", mentions.RebuildIn{ID: b.pid, DryRun: true}).(mentions.RebuildOut)
	all := b.count(`SELECT count(*) FROM documents WHERE project_id = ? AND kind <> 'research'`, b.pid)
	if !dry.DryRun || dry.Documents != all || b.count(`SELECT count(*) FROM entity_mentions`) != 0 {
		t.Errorf("a dry run wrote or miscounted: %+v", dry)
	}
	out := b.call(b.owner, "mention.rebuild", mentions.RebuildIn{ID: b.pid}).(mentions.RebuildOut)
	if out.Documents != all || out.Written != 1 || b.count(`SELECT count(*) FROM entity_mentions`) != 1 {
		t.Errorf("rebuild: %+v", out)
	}
	// Only the owner may rebuild.
	b.projectMember(b.pid, b.editor, ops.ProjectEditor)
	b.expect(b.editor, "mention.rebuild", mentions.RebuildIn{ID: b.pid}, ops.CodeForbidden)
}
