package docs_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
)

// The copies a restore or a split keeps are safety nets, not pins: they age
// out under the ordinary retention rules, unless a person labels one.
func TestSystemSnapshotsAgeOutButALabelPinsThem(t *testing.T) {
	g := newFx2(t)
	g.at("2026-01-10T10:00:00Z")
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Scene"})
	for i, body := range []string{"first draft words", "second draft words here", "third draft"} {
		g.save(g.ownA, d.ID, body)
		g.advance(10 * time.Minute)
		_ = i
	}
	rs := g.revs(g.ownA, d.ID) // second, first
	cur := g.head(g.ownA, d.ID)
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: rs[1].ID, BaseRevision: cur.Revision}); err != nil {
		t.Fatal(err)
	}
	var snap string
	for _, r := range g.revs(g.ownA, d.ID) {
		if r.Reason == "snapshot" {
			snap = r.ID
			if r.Pinned || r.Label != "" || r.Note != "Before restore" {
				t.Fatalf("a system snapshot is not pinned: %+v", r)
			}
		}
	}
	if snap == "" {
		t.Fatal("no snapshot was kept")
	}
	// A second document whose snapshot a person labels.
	e := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Other"})
	g.save(g.ownA, e.ID, "alpha one")
	g.advance(10 * time.Minute)
	g.save(g.ownA, e.ID, "beta two three")
	ec := g.head(g.ownA, e.ID)
	g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: e.ID, RevisionID: g.revs(g.ownA, e.ID)[0].ID, BaseRevision: ec.Revision})
	var kept string
	for _, r := range g.revs(g.ownA, e.ID) {
		if r.Reason == "snapshot" {
			kept = r.ID
		}
	}
	name := "Before the big rewrite"
	if out, err := g.call(g.ownA, "revision.label", revisions.LabelIn{ID: e.ID, RevisionID: kept, Label: &name}); err != nil || !out.(revisions.Item).Item.Pinned {
		t.Fatalf("a person's label pins a snapshot: %v", err)
	}

	// Seventy days on (long past the 30 days kept in full) the system copy is
	// thinned like any automatic version; the labelled one stays.
	g.advance(70 * 24 * time.Hour)
	if _, err := revisions.Prune(g.ctx, g.d, revisions.DefaultPolicy(), g.now, revisions.Scope{ProjectID: g.pidA}, false); err != nil {
		t.Fatal(err)
	}
	has := func(id string) bool { return g.count(`SELECT count(*) FROM document_revisions WHERE id = ?`, id) == 1 }
	if has(snap) {
		t.Error("an unlabelled system snapshot must age out")
	}
	if !has(kept) {
		t.Error("a snapshot a person labelled is kept forever")
	}
}

// Pruning and the autosave throttle must agree on which revision is the
// newest: the one most recently written, not the one with the latest shown
// time. A late capture (stamped yesterday, written today) is that revision.
func TestPruneNeverDropsTheNewestWrittenRevision(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Old", Content: "x"})
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	at := func(daysAgo, hour int) string {
		return db.Format(time.Date(2026, 6, 1, hour, 0, 0, 0, time.UTC).AddDate(0, 0, -daysAgo))
	}
	put := func(shown, written, content string) string {
		var id string
		err := g.d.WithTx(g.ctx, func(tx *sql.Tx) error {
			var err error
			id, err = revisions.Record(tx, revisions.RecordIn{DocumentID: d.ID, Revision: 1, Content: content, Words: 1,
				Reason: "autosave", At: shown, Now: written})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Shown latest: today 08:00. Last of its day (day 40): 15:00. The late
	// capture: shown 09:00 on day 40, written today at 10:00, so it is neither
	// the latest shown nor the last of its day, but it is the newest written.
	put(at(0, 8), at(0, 8), "today early")
	late := put(at(40, 9), at(0, 10), "late capture")
	last := put(at(40, 15), at(40, 15), "end of that day")

	if _, err := revisions.Prune(g.ctx, g.d, revisions.DefaultPolicy(), now, revisions.Scope{DocumentID: d.ID}, false); err != nil {
		t.Fatal(err)
	}
	has := func(id string) bool { return g.count(`SELECT count(*) FROM document_revisions WHERE id = ?`, id) == 1 }
	if !has(late) {
		t.Error("the newest written revision was pruned, and the throttle would now read an older row and write a duplicate")
	}
	if !has(last) {
		t.Error("the last version of an old day stays")
	}
}
