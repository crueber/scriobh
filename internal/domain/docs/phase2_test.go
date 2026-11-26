package docs_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

// ---- fixtures for the Phase 2 structure tests ---------------------------------

// sink records the events ops emit, in the same shape the real sink stores.
type sink struct {
	mu     sync.Mutex
	events []ops.Event
}

func (s *sink) record(e []ops.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e...)
}
func (s *sink) Flush(_ context.Context, _ string, e []ops.Event, _ []ops.AuditEntry) error {
	s.record(e)
	return nil
}
func (s *sink) FlushTx(_ *sql.Tx, _ string, e []ops.Event, _ []ops.AuditEntry) error {
	s.record(e)
	return nil
}
func (s *sink) count(typ string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// fx2 adds roles, an event sink and a controllable clock to the base fixture.
type fx2 struct {
	*fx
	sink     *sink
	editor   ops.Principal
	reader   ops.Principal
	stranger ops.Principal
	now      time.Time
}

func newFx2(t *testing.T) *fx2 {
	t.Helper()
	f := newFx(t)
	s := &sink{}
	f.reg = registry.Build(f.d, s, f.dir)
	g := &fx2{fx: f, sink: s, now: time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)}
	g.editor = ops.Principal{UserID: testutil.User(t, f.d, "writer", "editor@t.local"), Role: ops.RoleWriter}
	g.reader = ops.Principal{UserID: testutil.User(t, f.d, "reader", "reader@t.local"), Role: ops.RoleReader}
	g.stranger = ops.Principal{UserID: testutil.User(t, f.d, "writer", "stranger@t.local"), Role: ops.RoleWriter}
	for p, role := range map[string]string{g.editor.UserID: "editor", g.reader.UserID: "reader"} {
		if _, err := f.d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, f.pidA, p, role, db.Now()); err != nil {
			t.Fatal(err)
		}
	}
	restore := db.SetClock(func() time.Time { return g.now })
	t.Cleanup(restore)
	return g
}

func (g *fx2) advance(d time.Duration) { g.now = g.now.Add(d) }
func (g *fx2) at(s string) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		g.t.Fatal(err)
	}
	g.now = t
}

func (g *fx2) head(p ops.Principal, id string) docs.Doc {
	out, err := g.call(p, "doc.get", docs.DocIn{ID: id})
	if err != nil {
		g.t.Fatal(err)
	}
	return out.(docs.Item).Item
}

// save writes content as the document's next revision.
func (g *fx2) save(p ops.Principal, id, content string) docs.Doc {
	g.t.Helper()
	d := g.head(p, id)
	out, err := g.call(p, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Content: &content})
	if err != nil {
		g.t.Fatalf("save: %v", err)
	}
	return out.(docs.Item).Item
}

func (g *fx2) revs(p ops.Principal, id string) []revisions.Revision {
	g.t.Helper()
	out, err := g.call(p, "revision.list", revisions.ListIn{ID: id, Limit: 200})
	if err != nil {
		g.t.Fatalf("revision.list: %v", err)
	}
	return out.(revisions.ListOut).Items
}

func (g *fx2) count(q string, args ...any) int {
	g.t.Helper()
	var n int
	if err := g.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		g.t.Fatal(err)
	}
	return n
}

func reasons(rs []revisions.Revision) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Reason)
	}
	return strings.Join(out, ",")
}

// ---- automatic revisions --------------------------------------------------------

func TestAutosaveRevisionsAreThrottledAndStampedWithTheirOwnTime(t *testing.T) {
	g := newFx2(t)
	t0 := g.now
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "One"})

	g.save(g.ownA, d.ID, "alpha one")
	if n := len(g.revs(g.ownA, d.ID)); n != 0 {
		t.Fatalf("a first write into an empty document has no earlier text to keep: %d revisions", n)
	}
	g.advance(time.Minute)
	g.save(g.ownA, d.ID, "beta two words") // keeps alpha
	g.advance(time.Minute)
	g.save(g.ownA, d.ID, "gamma three words here") // inside the window
	g.advance(3 * time.Minute)
	g.save(g.ownA, d.ID, "delta four words here now") // t0+5m: 4 minutes after the last write
	if n := len(g.revs(g.ownA, d.ID)); n != 1 {
		t.Fatalf("at most one automatic revision per five minutes: %d", n)
	}
	g.advance(time.Minute)
	g.save(g.ownA, d.ID, "epsilon five words here now ok") // t0+6m: keeps delta

	rs := g.revs(g.ownA, d.ID)
	if len(rs) != 2 || reasons(rs) != "autosave,autosave" {
		t.Fatalf("revisions %s", reasons(rs))
	}
	newest, oldest := rs[0], rs[1]
	if newest.CreatedAt != db.Format(t0.Add(5*time.Minute)) || newest.RecordedAt != db.Format(t0.Add(6*time.Minute)) {
		t.Errorf("a late capture is shown at the time the text became the head (%s) and stored when it was written (%s)", newest.CreatedAt, newest.RecordedAt)
	}
	if oldest.CreatedAt != db.Format(t0) || oldest.Revision != 2 {
		t.Errorf("oldest %+v", oldest)
	}
	if newest.WordCount != 5 || newest.Delta != 5-2 || oldest.Delta != 0 {
		t.Errorf("words %d delta %d / %d", newest.WordCount, newest.Delta, oldest.Delta)
	}
	if newest.AuthorID != g.ownA.UserID {
		t.Errorf("author %q", newest.AuthorID)
	}
	out, _ := g.call(g.ownA, "revision.list", revisions.ListIn{ID: d.ID})
	if h := out.(revisions.ListOut).Head; h.Saved || h.Revision != 6 {
		t.Errorf("the newest text is not stored as a version yet: %+v", h)
	}

	// An identical save writes nothing, and neither does a change to metadata.
	same := "epsilon five words here now ok"
	cur := g.head(g.ownA, d.ID)
	if _, err := g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: cur.Revision, Content: &same}); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Hour)
	status := "revise"
	if _, err := g.call(g.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: cur.Revision, Status: &status}); err != nil {
		t.Fatal(err)
	}
	if n := len(g.revs(g.ownA, d.ID)); n != 2 {
		t.Errorf("metadata and idempotent saves must not write versions: %d", n)
	}

	// A named snapshot of the head, then a save: the head is already the newest
	// version, so nothing more is written however long ago that was.
	g.advance(time.Hour)
	if _, err := g.call(g.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "Draft 1"}); err != nil {
		t.Fatal(err)
	}
	g.advance(time.Hour)
	g.save(g.ownA, d.ID, "zeta six")
	if rs := g.revs(g.ownA, d.ID); reasons(rs) != "manual,autosave,autosave" {
		t.Errorf("revisions %s", reasons(rs))
	}
	if out, _ := g.call(g.ownA, "revision.list", revisions.ListIn{ID: d.ID, Limit: 1}); out.(revisions.ListOut).Next == "" {
		t.Error("a page that is not the last has a cursor")
	}
}

func TestTheEndOfAnEditingSessionIsKeptAndDaysAreListable(t *testing.T) {
	g := newFx2(t)
	g.at("2026-03-10T10:00:00Z")
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Chapter"})
	g.save(g.ownA, d.ID, "monday opening")
	g.at("2026-03-10T10:01:00Z")
	g.save(g.ownA, d.ID, "monday middle") // keeps "monday opening"
	g.at("2026-03-10T10:03:00Z")
	g.save(g.ownA, d.ID, "monday end of the session text") // inside the window: not kept yet
	g.at("2026-03-11T09:00:00Z")
	g.save(g.ownA, d.ID, "tuesday first edit") // today's first save keeps yesterday's last text

	rs := g.revs(g.ownA, d.ID)
	if len(rs) != 2 {
		t.Fatalf("revisions %d", len(rs))
	}
	if rs[0].CreatedAt != "2026-03-10T10:03:00.000Z" || rs[0].RecordedAt != "2026-03-11T09:00:00.000Z" || rs[0].WordCount != 6 {
		t.Errorf("yesterday's last text must be stamped yesterday: %+v", rs[0])
	}

	// "Restore yesterday's chapter": days, then the day's versions, then restore.
	out, err := g.call(g.ownA, "revision.days", revisions.DaysIn{ID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	days := out.(revisions.DaysOut).Days
	if len(days) != 1 || days[0].Day != "2026-03-10" || days[0].Count != 2 || days[0].LastID != rs[0].ID {
		t.Errorf("days %+v", days)
	}
	// Five hours behind UTC, the 10:00Z saves are still Monday and the 09:00Z one is Tuesday.
	out, _ = g.call(g.ownA, "revision.days", revisions.DaysIn{ID: d.ID, UTCOffset: -300})
	if d2 := out.(revisions.DaysOut).Days; len(d2) != 1 || d2[0].Day != "2026-03-10" {
		t.Errorf("offset days %+v", d2)
	}
	if _, err := g.call(g.ownA, "revision.days", revisions.DaysIn{ID: d.ID, UTCOffset: 99999}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a silly offset: %v", err)
	}
	out, err = g.call(g.ownA, "revision.list", revisions.ListIn{ID: d.ID, From: "2026-03-10", To: "2026-03-11"})
	if err != nil || len(out.(revisions.ListOut).Items) != 2 {
		t.Fatalf("one day's versions: %v %v", out, err)
	}
	out, _ = g.call(g.ownA, "revision.list", revisions.ListIn{ID: d.ID, From: "2026-03-11"})
	if n := len(out.(revisions.ListOut).Items); n != 0 {
		t.Errorf("nothing was created on Tuesday: %d", n)
	}
	if _, err := g.call(g.ownA, "revision.list", revisions.ListIn{ID: d.ID, From: "yesterday"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a bad bound: %v", err)
	}
	cur := g.head(g.ownA, d.ID)
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: days[0].LastID, BaseRevision: cur.Revision}); err != nil {
		t.Fatal(err)
	}
	if got := g.head(g.ownA, d.ID).Content; got != "monday end of the session text" {
		t.Errorf("restored %q", got)
	}
}

func TestManualSnapshotsLabelsAndDeletion(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "One", Content: "some words here"})
	other := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Two", Content: "other words"})

	out, err := g.call(g.editor, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "Draft 2", Note: "after the editor call"})
	if err != nil {
		t.Fatal(err)
	}
	snap := out.(revisions.Item).Item
	if snap.Reason != "manual" || snap.Label != "Draft 2" || snap.Note != "after the editor call" || !snap.Pinned || !snap.Current || snap.WordCount != 3 || snap.AuthorID != g.editor.UserID {
		t.Errorf("snapshot %+v", snap)
	}
	// Taking the same snapshot twice is allowed and stores the text once.
	if _, err := g.call(g.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "Again"}); err != nil {
		t.Fatal(err)
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ?`, d.ID); n != 1 {
		t.Errorf("identical text is stored once, found %d blobs", n)
	}
	if _, err := g.call(g.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: strings.Repeat("x", 121)}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a long label: %v", err)
	}

	// Reading is open to a reader; writing is not.
	if _, err := g.call(g.reader, "revision.get", revisions.GetIn{ID: d.ID, RevisionID: snap.ID}); err != nil {
		t.Errorf("a reader may read a version: %v", err)
	}
	for name, call := range map[string]func() (any, error){
		"snapshot": func() (any, error) { return g.call(g.reader, "revision.snapshot", revisions.SnapshotIn{ID: d.ID}) },
		"label": func() (any, error) {
			l := "x"
			return g.call(g.reader, "revision.label", revisions.LabelIn{ID: d.ID, RevisionID: snap.ID, Label: &l})
		},
		"delete": func() (any, error) {
			return g.call(g.reader, "revision.delete", revisions.GetIn{ID: d.ID, RevisionID: snap.ID})
		},
		"restore": func() (any, error) {
			return g.call(g.reader, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: snap.ID, BaseRevision: 1})
		},
	} {
		if _, err := call(); ops.AsError(err).Code != ops.CodeForbidden {
			t.Errorf("a reader must not %s: %v", name, err)
		}
	}
	// A stranger sees nothing, not even that the document exists.
	for _, op := range []string{"revision.list", "revision.days", "revision.get", "revision.diff", "revision.snapshot", "revision.label", "revision.delete", "revision.restore"} {
		var in any
		switch op {
		case "revision.list":
			in = revisions.ListIn{ID: d.ID}
		case "revision.days":
			in = revisions.DaysIn{ID: d.ID}
		case "revision.get", "revision.delete":
			in = revisions.GetIn{ID: d.ID, RevisionID: snap.ID}
		case "revision.diff":
			in = revisions.DiffIn{ID: d.ID, From: snap.ID}
		case "revision.snapshot":
			in = revisions.SnapshotIn{ID: d.ID}
		case "revision.label":
			l := "x"
			in = revisions.LabelIn{ID: d.ID, RevisionID: snap.ID, Label: &l}
		case "revision.restore":
			in = docs.RestoreIn{ID: d.ID, RevisionID: snap.ID, BaseRevision: 1}
		}
		if _, err := g.call(g.stranger, op, in); ops.AsError(err).Code != ops.CodeNotFound {
			t.Errorf("%s as a stranger: %v", op, err)
		}
	}

	// A revision id from another document (or project) is not found, whatever the op.
	foreign, _ := g.call(g.ownA, "revision.snapshot", revisions.SnapshotIn{ID: other.ID, Label: "Theirs"})
	fid := foreign.(revisions.Item).Item.ID
	lab := "hijack"
	for name, err := range map[string]error{
		"get":     second(g.call(g.ownA, "revision.get", revisions.GetIn{ID: d.ID, RevisionID: fid})),
		"diff":    second(g.call(g.ownA, "revision.diff", revisions.DiffIn{ID: d.ID, From: fid})),
		"label":   second(g.call(g.ownA, "revision.label", revisions.LabelIn{ID: d.ID, RevisionID: fid, Label: &lab})),
		"delete":  second(g.call(g.ownA, "revision.delete", revisions.GetIn{ID: d.ID, RevisionID: fid})),
		"restore": second(g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: fid, BaseRevision: 1})),
		"before":  second(g.call(g.ownA, "revision.list", revisions.ListIn{ID: d.ID, Before: fid})),
	} {
		if c := ops.AsError(err).Code; c != ops.CodeNotFound && !(name == "before" && c == ops.CodeInvalid) {
			t.Errorf("foreign revision id on %s: %v", name, err)
		}
	}
	bPid := g.project(g.ownB, "Elsewhere")
	bDoc := g.doc(g.ownB, bPid, docs.CreateIn{Title: "B", Content: "b words"})
	if _, err := g.call(g.ownB, "revision.get", revisions.GetIn{ID: bDoc.ID, RevisionID: snap.ID}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a revision of another project: %v", err)
	}
	// Required fields are checked before anything is looked up.
	if _, err := g.call(g.ownA, "revision.get", revisions.GetIn{ID: d.ID}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("missing revision_id: %v", err)
	}

	// Labels: relabel, annotate, clear. Any labelled version is pinned.
	cur := g.head(g.ownA, d.ID)
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "some words here and more")
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "some words here and more still") // keeps the previous text as an autosave
	var auto revisions.Revision
	for _, r := range g.revs(g.ownA, d.ID) {
		if r.Reason == "autosave" {
			auto = r
		}
	}
	if auto.ID == "" || auto.Pinned {
		t.Fatalf("expected an unpinned autosave, got %+v", auto)
	}
	if _, err := g.call(g.ownA, "revision.delete", revisions.GetIn{ID: d.ID, RevisionID: auto.ID}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("an automatic version cannot be deleted: %v", err)
	}
	if _, err := g.call(g.ownA, "revision.label", revisions.LabelIn{ID: d.ID, RevisionID: auto.ID}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a label call with nothing to set: %v", err)
	}
	name := "Before the rewrite"
	out, err = g.call(g.editor, "revision.label", revisions.LabelIn{ID: d.ID, RevisionID: auto.ID, Label: &name})
	if err != nil || !out.(revisions.Item).Item.Pinned || out.(revisions.Item).Item.Reason != "autosave" {
		t.Fatalf("label: %+v %v", out, err)
	}
	empty := ""
	out, _ = g.call(g.editor, "revision.label", revisions.LabelIn{ID: d.ID, RevisionID: auto.ID, Label: &empty})
	if out.(revisions.Item).Item.Pinned {
		t.Error("clearing the label unpins it")
	}

	// Deleting a named snapshot frees its text only when no other version holds it.
	if _, err := g.call(g.editor, "revision.delete", revisions.GetIn{ID: d.ID, RevisionID: snap.ID}); err != nil {
		t.Fatal(err)
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, cur.ContentHash); n != 1 {
		t.Error("the text is still held by the other snapshot")
	}
	for _, r := range g.revs(g.ownA, d.ID) {
		if r.Reason == "manual" {
			if _, err := g.call(g.editor, "revision.delete", revisions.GetIn{ID: d.ID, RevisionID: r.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, cur.ContentHash); n != 0 {
		t.Error("the last version holding the text is gone, so the text is freed")
	}
	if g.sink.count("revision.created") != 3 || g.sink.count("revision.deleted") != 2 {
		t.Errorf("events created=%d deleted=%d", g.sink.count("revision.created"), g.sink.count("revision.deleted"))
	}
}

func second(_ any, err error) error { return err }

// ---- storage ---------------------------------------------------------------------

func TestRevisionContentIsCompressedAndDeduplicated(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Long"})
	big := strings.Repeat("The harbour at low tide, and Máire waited. ", 400) // about 17 KB
	small := "A short one."
	g.save(g.ownA, d.ID, big)
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, small)
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, big) // back to the first text: the head (small) is kept

	rs := g.revs(g.ownA, d.ID) // newest first: small, big
	if len(rs) != 2 {
		t.Fatalf("revisions %d", len(rs))
	}
	var enc string
	var raw, stored int
	if err := g.d.Read().QueryRow(`SELECT encoding, raw_size, length(content) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash(big)).Scan(&enc, &raw, &stored); err != nil {
		t.Fatal(err)
	}
	if enc != "gzip" || raw != len(big) || stored >= raw/5 {
		t.Errorf("big text: %s, %d bytes stored of %d", enc, stored, raw)
	}
	if err := g.d.Read().QueryRow(`SELECT encoding FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash(small)).Scan(&enc); err != nil || enc != "identity" {
		t.Errorf("small text: %s %v", enc, err)
	}
	// The compressed text comes back byte for byte through the op.
	out, err := g.call(g.ownA, "revision.get", revisions.GetIn{ID: d.ID, RevisionID: rs[1].ID})
	if err != nil || out.(revisions.GetOut).Content != big || out.(revisions.GetOut).Item.Size != len(big) {
		t.Fatalf("round trip: %v", err)
	}

	// A named snapshot of the same text, and a restore to it, add rows but no second copy.
	if _, err := g.call(g.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "Same as the first"}); err != nil {
		t.Fatal(err)
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ?`, d.ID); n != 2 {
		t.Errorf("two distinct texts, %d blobs", n)
	}
	if n := g.count(`SELECT count(*) FROM document_revisions WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash(big)); n != 2 {
		t.Errorf("two versions refer to the long text, found %d", n)
	}
}

// ---- restore ---------------------------------------------------------------------

func TestRestoreWritesANewVersionAndKeepsEverythingElse(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene"})
	v1 := "First draft of the harbour scene."
	v2 := "Second draft, longer, with more words in it now."
	v3 := "Third attempt: only a lantern. See [[Lantern Lore]]."
	g.save(g.ownA, d.ID, v1)
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, v2) // keeps v1
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, v3) // keeps v2; v3 is the head and not yet a version

	before := g.revs(g.ownA, d.ID)
	if len(before) != 2 {
		t.Fatalf("versions %d", len(before))
	}
	v1ID := before[1].ID
	if links := g.docLinks(d.ID); len(links) != 1 {
		t.Fatalf("v3 links to one page, found %d", len(links))
	}
	cur := g.head(g.ownA, d.ID)

	// Stale base, trashed, wrong tab: refused, nothing written.
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: v1ID, BaseRevision: cur.Revision - 1}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("a stale base revision: %v", err)
	}
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: v1ID}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("no base revision: %v", err)
	}
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, BaseRevision: cur.Revision}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("no version named: %v", err)
	}
	if g.head(g.ownA, d.ID).Content != v3 {
		t.Fatal("a refused restore must not change the document")
	}
	g.call(g.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabA"})
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: v1ID, BaseRevision: cur.Revision, TabID: "tabB"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("another tab holds the lease: %v", err)
	}

	g.advance(time.Minute)
	out, err := g.call(g.editor, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: v1ID, BaseRevision: cur.Revision})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(docs.RestoreOut)
	if r.Unchanged || r.Item.Content != v1 || r.Item.Revision != cur.Revision+1 || r.Item.WordCount != text.WordCount(v1) || r.RevisionID == "" {
		t.Fatalf("restore result %+v", r)
	}
	// Counts and search follow the restored text, and links are rebuilt from it.
	var body string
	if err := g.d.Read().QueryRow(`SELECT body FROM doc_text WHERE document_id = ?`, d.ID).Scan(&body); err != nil || body != text.SearchBody(v1) {
		t.Errorf("search text %q %v", body, err)
	}
	if n := g.count(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'harbour'`); n != 1 {
		t.Errorf("the index finds the restored text: %d", n)
	}
	if n := g.count(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'lantern'`); n != 0 {
		t.Errorf("the index no longer finds the replaced text: %d", n)
	}
	if l := g.docLinks(d.ID); len(l) != 0 {
		t.Errorf("links of the replaced text are gone: %v", l)
	}
	if g.sink.count("revision.restored") != 1 {
		t.Error("revision.restored not emitted")
	}
	var restoredEvents int
	for _, e := range g.sink.events {
		if e.Type == "doc.updated" && e.DocumentID == d.ID && e.Payload["restored"] == true && e.Payload["content"] == true {
			restoredEvents++
		}
	}
	if restoredEvents != 1 {
		t.Errorf("a restore is announced as an ordinary content change, found %d", restoredEvents)
	}

	// History is append-only: every earlier version is still there, the replaced
	// head was kept before the restore, and the restore is a version of its own.
	after := g.revs(g.ownA, d.ID)
	ids := map[string]revisions.Revision{}
	for _, v := range after {
		ids[v.ID] = v
	}
	for _, v := range before {
		if _, ok := ids[v.ID]; !ok {
			t.Errorf("version %s vanished", v.ID)
		}
	}
	if len(after) != len(before)+2 || reasons(after) != "restore,snapshot,autosave,autosave" {
		t.Fatalf("after: %s", reasons(after))
	}
	if after[0].ID != r.RevisionID || after[0].Revision != r.Item.Revision || !after[0].Current || after[0].AuthorID != g.editor.UserID {
		t.Errorf("the restore version %+v", after[0])
	}
	if after[1].Note != "Before restore" || after[1].Label != "" || after[1].Pinned {
		// The reason is in the note: a label would pin the copy forever.
		t.Errorf("the replaced text is preserved and says why: %+v", after[1])
	}
	if got := g.content(d.ID, after[1].ID); got != v3 {
		t.Errorf("preserved text %q", got)
	}
	if n := g.count(`SELECT count(*) FROM document_revisions WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash(v1)); n != 2 {
		t.Errorf("the restore version and the original share one stored text: %d", n)
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash(v1)); n != 1 {
		t.Errorf("the original text is stored once: %d", n)
	}

	// Restoring what the document already says changes nothing.
	again, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: v1ID, BaseRevision: r.Item.Revision})
	if err != nil || !again.(docs.RestoreOut).Unchanged || len(g.revs(g.ownA, d.ID)) != len(after) {
		t.Errorf("an identical restore: %+v %v", again, err)
	}
	// A restore does not make the document restorable while it is trashed.
	g.call(g.ownA, "doc.trash", docs.DocIn{ID: d.ID})
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: before[0].ID, BaseRevision: r.Item.Revision}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("restoring into the trash: %v", err)
	}
	// History of a trashed document is still readable.
	if n := len(g.revs(g.ownA, d.ID)); n != len(after) {
		t.Errorf("the history of a trashed document: %d", n)
	}
}

func (g *fx2) content(docID, revID string) string {
	g.t.Helper()
	out, err := g.call(g.ownA, "revision.get", revisions.GetIn{ID: docID, RevisionID: revID})
	if err != nil {
		g.t.Fatal(err)
	}
	return out.(revisions.GetOut).Content
}

func TestSplitKeepsTheWholeTextFirst(t *testing.T) {
	g := newFx2(t)
	whole := "One paragraph here.\n\nAnother paragraph there.\n"
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Whole", Content: whole})
	out, err := g.call(g.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 1, Offset: len("One paragraph here.\n\n"), Title: "Tail"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(docs.SplitOut).Head.Content == whole {
		t.Fatal("the head was not cut")
	}
	rs := g.revs(g.ownA, d.ID)
	if len(rs) != 1 || rs[0].Reason != "split" || rs[0].Note != "" || rs[0].Label != "" || rs[0].Pinned {
		t.Fatalf("versions %+v", rs)
	}
	if g.content(d.ID, rs[0].ID) != whole {
		t.Error("the whole text must be kept before the cut")
	}
}

// ---- retention -------------------------------------------------------------------

func TestPruningRules(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Old", Content: "x"})
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	day := func(daysAgo int, hour int) string {
		return db.Format(time.Date(2026, 6, 1, hour, 0, 0, 0, time.UTC).AddDate(0, 0, -daysAgo))
	}
	put := func(reason, label, at, content string) string {
		var id string
		err := g.d.WithTx(g.ctx, func(tx *sql.Tx) error {
			var err error
			id, err = revisions.Record(tx, revisions.RecordIn{DocumentID: d.ID, Revision: 1, Content: content, Words: 1, Reason: reason, Label: label, At: at, Now: at})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Within 30 days: all kept.
	recent1 := put("autosave", "", day(1, 9), "recent one")
	recent2 := put("autosave", "", day(1, 10), "recent two")
	edge := put("autosave", "", day(29, 9), "edge")
	// Older: three on day 40 (keep the newest), two on day 50 (keep the newest), a lone one on day 60.
	d40a := put("autosave", "", day(40, 8), "d40 a")
	d40b := put("restore", "", day(40, 11), "d40 b")
	d40c := put("autosave", "", day(40, 15), "d40 c")
	d50a := put("autosave", "", day(50, 9), "d50 a")
	d50b := put("snapshot", "", day(50, 10), "d50 b")
	d60 := put("autosave", "", day(60, 9), "d60")
	// Pinned forever: a named snapshot and a labelled autosave, both ancient.
	manual := put("manual", "Draft 1", day(200, 9), "manual text")
	labelled := put("autosave", "keeper", day(201, 9), "labelled text")
	// An ancient version holding the same text as a kept one: the text must survive.
	shared1 := put("autosave", "", day(90, 9), "shared text")
	shared1b := put("autosave", "", day(90, 12), "the day's last word")
	shared2 := put("autosave", "", day(0, 9), "shared text") // newest overall: always kept

	exists := func(id string) bool {
		return g.count(`SELECT count(*) FROM document_revisions WHERE id = ?`, id) == 1
	}
	pol := revisions.Policy{AutosaveDays: 30, KeepDaily: true}

	dry, err := revisions.Prune(g.ctx, g.d, pol, now, revisions.Scope{DocumentID: d.ID}, true)
	if err != nil || !dry.DryRun || dry.Removed != 4 || dry.Documents != 1 {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	if !exists(d40a) {
		t.Fatal("a dry run removes nothing")
	}

	res, err := revisions.Prune(g.ctx, g.d, pol, now, revisions.Scope{ProjectID: g.pidA}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 4 || res.Blobs != 3 || res.Bytes <= 0 {
		t.Errorf("result %+v", res)
	}
	for name, id := range map[string]string{"recent1": recent1, "recent2": recent2, "edge": edge, "d40c": d40c, "d50b": d50b, "d60": d60,
		"manual": manual, "labelled": labelled, "shared2": shared2, "shared1b": shared1b} {
		if !exists(id) {
			t.Errorf("%s must be kept", name)
		}
	}
	for name, id := range map[string]string{"d40a": d40a, "d40b": d40b, "d50a": d50a, "shared1": shared1} {
		if exists(id) {
			t.Errorf("%s must be pruned", name)
		}
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash("shared text")); n != 1 {
		t.Error("text still held by a kept version must stay")
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash("d40 a")); n != 0 {
		t.Error("text only pruned versions held must be freed")
	}
	// Idempotent.
	if res, _ := revisions.Prune(g.ctx, g.d, pol, now, revisions.Scope{}, false); res.Removed != 0 {
		t.Errorf("second prune removed %d", res.Removed)
	}
	// Without a daily rule, everything automatic older than the window goes, except the newest and the pinned.
	res, _ = revisions.Prune(g.ctx, g.d, revisions.Policy{AutosaveDays: 30, KeepDaily: false}, now, revisions.Scope{}, false)
	if res.Removed != 4 || exists(d40c) || exists(d50b) || exists(d60) || exists(shared1b) {
		t.Errorf("no daily rule: %+v", res)
	}
	if !exists(manual) || !exists(labelled) || !exists(shared2) {
		t.Error("pinned versions and the newest survive any policy")
	}
	// A short window pulls recent versions into thinning too, but never the newest.
	res, _ = revisions.Prune(g.ctx, g.d, revisions.Policy{AutosaveDays: 1, KeepDaily: true}, now.AddDate(0, 0, 5), revisions.Scope{}, false)
	if !exists(shared2) {
		t.Error("the newest version is never pruned")
	}
	_ = res

	// The stored policy: defaults, then settings.
	if p := revisions.LoadPolicy(g.ctx, g.d); p != revisions.DefaultPolicy() || p.AutosaveDays != 30 || !p.KeepDaily {
		t.Errorf("default policy %+v", p)
	}
	for k, v := range map[string]string{revisions.SettingAutosaveDays: "7", revisions.SettingKeepDaily: "false"} {
		if _, err := g.d.Writer().Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?)`, k, v, db.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if p := revisions.LoadPolicy(g.ctx, g.d); p.AutosaveDays != 7 || p.KeepDaily {
		t.Errorf("stored policy %+v", p)
	}
	g.d.Writer().Exec(`UPDATE settings SET value = 'banana' WHERE key = ?`, revisions.SettingAutosaveDays)
	if p := revisions.LoadPolicy(g.ctx, g.d); p.AutosaveDays != 30 {
		t.Errorf("an unreadable setting falls back to the default: %+v", p)
	}
}

func TestRevisionPruneOpAndSweep(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Old"})
	g.at("2026-01-01T10:00:00Z")
	g.save(g.ownA, d.ID, "one")
	g.at("2026-01-01T11:00:00Z")
	g.save(g.ownA, d.ID, "two")
	g.at("2026-01-01T12:00:00Z")
	g.save(g.ownA, d.ID, "three") // three versions on one old day: only its last (and newest written) stays
	g.at("2026-04-01T12:00:00Z")
	g.save(g.ownA, d.ID, "four") // keeps "three" as a recent version
	if n := len(g.revs(g.ownA, d.ID)); n != 3 {
		t.Fatalf("versions %d", n)
	}
	if _, err := g.call(g.editor, "revision.prune", revisions.PruneIn{ProjectID: g.pidA}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("only an owner may prune a project: %v", err)
	}
	out, err := g.call(g.ownA, "revision.prune", revisions.PruneIn{ProjectID: g.pidA, DryRun: true})
	if err != nil || out.(revisions.PruneResult).Removed != 2 || len(g.revs(g.ownA, d.ID)) != 3 {
		t.Fatalf("dry run %+v %v", out, err)
	}
	// The scheduled sweep does the same with the stored policy.
	res, err := revisions.Sweep(g.ctx, g.d)
	if err != nil || res.Removed != 2 || len(g.revs(g.ownA, d.ID)) != 1 {
		t.Fatalf("sweep %+v %v", res, err)
	}
}

// ---- diff ------------------------------------------------------------------------

func TestRevisionDiffOp(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Diff"})
	g.save(g.ownA, d.ID, "The quick brown fox.\n\nA second paragraph stays.\n")
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "The quick red fox.\n\nA second paragraph stays.\n\nAnd a new one.\n")
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "The quick red fox jumps.\n")
	rs := g.revs(g.ownA, d.ID) // v2, v1
	if len(rs) != 2 {
		t.Fatal(len(rs))
	}
	// Between two versions, word by word.
	out, err := g.call(g.reader, "revision.diff", revisions.DiffIn{ID: d.ID, From: rs[1].ID, To: rs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	df := out.(revisions.DiffOut)
	if !df.Changed || df.Coarse || df.To == nil || df.From.ID != rs[1].ID || df.Stats.AddedWords != 5 || df.Stats.RemovedWords != 1 {
		t.Errorf("diff %+v", df)
	}
	if df.Segments[1].Op != "delete" || df.Segments[1].Text != "brown" || df.Segments[2].Text != "red" {
		t.Errorf("segments %+v", df.Segments)
	}
	// A version against the current text.
	out, err = g.call(g.ownA, "revision.diff", revisions.DiffIn{ID: d.ID, From: rs[0].ID, To: "current", Unit: "paragraph"})
	if err != nil {
		t.Fatal(err)
	}
	df = out.(revisions.DiffOut)
	if df.To != nil || !df.Changed {
		t.Errorf("against current %+v", df)
	}
	out, _ = g.call(g.ownA, "revision.diff", revisions.DiffIn{ID: d.ID, From: rs[0].ID, Unit: "paragraph"})
	if out.(revisions.DiffOut).Stats != df.Stats {
		t.Error("an empty 'to' means the current text")
	}
	if _, err := g.call(g.ownA, "revision.diff", revisions.DiffIn{ID: d.ID, From: rs[0].ID, Unit: "letter"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("unknown unit: %v", err)
	}
	if _, err := g.call(g.ownA, "revision.diff", revisions.DiffIn{ID: d.ID}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("missing from: %v", err)
	}
	// A version against itself is unchanged.
	out, _ = g.call(g.ownA, "revision.diff", revisions.DiffIn{ID: d.ID, From: rs[0].ID, To: rs[0].ID})
	if out.(revisions.DiffOut).Changed {
		t.Error("identical versions have no changes")
	}
}

// ---- performance -------------------------------------------------------------------

func TestSixtyThousandWordProjectWithTwentyRevisionsEach(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 60,000-word project")
	}
	g := newFx2(t)
	const ndocs, nrev = 60, 20
	words := strings.Fields(strings.Repeat("harbour lantern tide Máire waited gull cried water nothing over ", 4))
	body := func(doc, rev int) string {
		var b strings.Builder
		for p := 0; p < 10; p++ { // ten paragraphs of 100 words: 1,000 words
			for w := 0; w < 100; w++ {
				b.WriteString(words[(doc+p*7+w+rev*(1+w%3))%len(words)])
				b.WriteByte(' ')
			}
			b.WriteString("\n\n")
		}
		return b.String()
	}
	part := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Part One", Kind: "part"})
	var ids []string
	for i := 0; i < ndocs; i++ {
		ids = append(ids, g.doc(g.ownA, g.pidA, docs.CreateIn{Title: fmt.Sprintf("Chapter %d", i+1), ParentID: part.ID}).ID)
	}
	start := time.Now()
	for r := 0; r < nrev; r++ {
		for i, id := range ids {
			g.save(g.ownA, id, body(i, r))
		}
		g.advance(6 * time.Minute) // every save is past the autosave window
	}
	saveTime := time.Since(start)
	if n := g.count(`SELECT count(*) FROM document_revisions`); n != ndocs*(nrev-1) {
		t.Fatalf("revisions %d, want %d", n, ndocs*(nrev-1))
	}
	t.Run("time of a save with a revision", func(t *testing.T) {
		testutil.Timing(t)
		if per := saveTime / (ndocs * nrev); per > 40*time.Millisecond*timingFactor {
			t.Errorf("a save that also writes a revision averaged %v", per)
		}
	})

	timeit := func(name string, limit time.Duration, fn func()) {
		t.Helper()
		s := time.Now()
		fn()
		el := time.Since(s)
		t.Run("time of "+name, func(t *testing.T) {
			testutil.Timing(t)
			if el > limit*timingFactor {
				t.Errorf("%s took %v, limit %v", name, el, limit)
			}
		})
	}
	timeit("outline.get over 61 documents", 100*time.Millisecond, func() {
		out, err := g.call(g.ownA, "outline.get", docs.OutlineIn{ProjectID: g.pidA})
		if err != nil || out.(docs.OutlineOut).Totals.Words < 55_000 {
			t.Fatalf("outline %v words=%d", err, out.(docs.OutlineOut).Totals.Words)
		}
	})
	timeit("revision.list of 19 versions", 50*time.Millisecond, func() {
		if n := len(g.revs(g.ownA, ids[7])); n != nrev-1 {
			t.Fatalf("versions %d", n)
		}
	})
	rs := g.revs(g.ownA, ids[7])
	timeit("revision.get", 30*time.Millisecond, func() { g.content(ids[7], rs[3].ID) })
	timeit("revision.diff of two 1,000-word versions", 200*time.Millisecond, func() {
		if _, err := g.call(g.ownA, "revision.diff", revisions.DiffIn{ID: ids[7], From: rs[5].ID, To: rs[0].ID}); err != nil {
			t.Fatal(err)
		}
	})
	timeit("a restore", 100*time.Millisecond, func() {
		cur := g.head(g.ownA, ids[7])
		if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: ids[7], RevisionID: rs[10].ID, BaseRevision: cur.Revision}); err != nil {
			t.Fatal(err)
		}
	})
	timeit("a dry-run prune of the whole project", 500*time.Millisecond, func() {
		if _, err := g.call(g.ownA, "revision.prune", revisions.PruneIn{ProjectID: g.pidA, DryRun: true}); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("%d saves with %d revisions in %v", ndocs*nrev, ndocs*(nrev-1), saveTime)
}
