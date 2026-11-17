package mentions_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/ops"
)

// A case-only rename changes what the capital guard lets through, though the
// folded key is the same: the cached matcher and the index must follow.
func TestCaseOnlyRenameMovesTheIndex(t *testing.T) {
	b := newBook(t)
	lower := b.ent(b.owner, b.cid, "character", "bran") // written in lower case: matches any case
	d := b.doc(b.owner, b.pid, "One", "Bran rode out. Later bran slept.")
	b.settle()
	if got := b.counts(b.owner, d.ID); got["bran"] != 2 {
		t.Fatalf("a lower-case name matches either case: %v", got)
	}
	b.call(b.owner, "entity.update", entities.UpdateIn{ID: lower, Name: str("Bran")}) // now the capital guard applies
	b.settle()
	if got := b.counts(b.owner, d.ID); got["Bran"] != 1 {
		t.Errorf("after the rename only the capitalised use should match: %v", got)
	}
	// And a save made before the next scan already uses the new name.
	d = b.save(b.owner, d, "Bran and bran and bran.")
	if got := b.counts(b.owner, d.ID); got["Bran"] != 1 {
		t.Errorf("a save after the rename: %v", got)
	}
}

// Anything that changes a project's scope after its documents exist makes the
// index follow, with no op calling the index.
func TestScopeChangesAreRescanTriggers(t *testing.T) {
	b := newBook(t)
	other := b.compendium(b.owner, "Elsewhere")
	bran := b.ent(b.owner, other, "character", "Bran")
	d := b.doc(b.owner, b.pid, "One", "Bran sat by Elara.")
	b.settle()
	rows := func() int {
		return b.count(`SELECT count(*) FROM entity_mentions WHERE document_id = ? AND entity_id = ?`, d.ID, bran)
	}
	if rows() != 0 {
		t.Fatalf("Bran is not in scope yet")
	}
	// A reference.
	b.call(b.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: b.pid, CompendiumID: other})
	b.settle()
	if rows() != 1 {
		t.Errorf("a new reference: %d rows", rows())
	}
	b.call(b.owner, "project.compendium.ref.remove", compendiums.RefRemoveIn{ID: b.pid, CompendiumID: other})
	b.settle()
	if rows() != 0 {
		t.Errorf("a removed reference: %d rows", rows())
	}
	// A single linked entity (a cameo).
	b.call(b.owner, "project.entity.link.add", entities.LinkIn{ID: b.pid, EntityID: bran})
	b.settle()
	if rows() != 1 {
		t.Errorf("a new link: %d rows", rows())
	}
	b.call(b.owner, "project.entity.link.remove", entities.LinkRemoveIn{ID: b.pid, EntityID: bran})
	b.settle()
	if rows() != 0 {
		t.Errorf("a removed link: %d rows", rows())
	}
	// The primary Compendium.
	if n := b.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, b.pid); n == 0 {
		t.Fatalf("the primary's entities should be indexed")
	}
	b.call(b.owner, "project.compendium.set", compendiums.SetIn{ID: b.pid, CompendiumID: ""})
	b.settle()
	if n := b.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, b.pid); n != 0 {
		t.Errorf("no primary, no entities in scope: %d rows", n)
	}
	b.call(b.owner, "project.compendium.set", compendiums.SetIn{ID: b.pid, CompendiumID: other})
	b.settle()
	if rows() != 1 {
		t.Errorf("a new primary: %d rows", rows())
	}
}

// A matcher cached before a Compendium is purged must not outlive it: the next
// save succeeds and leaves no rows for entities that are gone.
func TestSaveAfterAPurgeOfAReferencedCompendium(t *testing.T) {
	b := newBook(t)
	other := b.compendium(b.owner, "Doomed")
	b.ent(b.owner, other, "character", "Bran")
	b.call(b.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: b.pid, CompendiumID: other})
	d := b.doc(b.owner, b.pid, "One", "Bran and Elara.")
	b.settle()
	d = b.save(b.owner, d, "Bran and Elara again.") // warms the cache with the referenced entity in it
	if got := b.counts(b.owner, d.ID); got["Bran"] != 1 {
		t.Fatalf("setup: %v", got)
	}
	b.call(b.owner, "compendium.trash", compendiums.StateIn{ID: other})
	b.call(b.owner, "compendium.purge", compendiums.PurgeIn{ID: other, ConfirmName: "Doomed", DetachProjects: []string{b.pid}})
	d = b.save(b.owner, d, "Bran and Elara a third time.")
	if got := b.counts(b.owner, d.ID); len(got) != 1 || got["Elara"] != 1 {
		t.Errorf("after the purge: %v", got)
	}
	b.settle()
	if n := b.count(`SELECT count(*) FROM entity_mentions e LEFT JOIN entities n ON n.id = e.entity_id WHERE n.id IS NULL`); n != 0 {
		t.Errorf("%d rows name an entity that is gone", n)
	}
}

// Through a key pinned to a project, only that project's primary Compendium is
// readable; the book's references and linked entities, and everything in
// another Compendium, are not, whatever the person behind the key may read.
func TestPinnedKeys(t *testing.T) {
	x := newH(t)
	primary := x.compendium(x.owner, "Primary")
	shared := x.compendium(x.owner, "Shared")
	x.ent(x.owner, primary, "character", "Elara")
	x.ent(x.owner, shared, "character", "Bran")
	pid := x.project(x.owner, "Book", primary)
	other := x.project(x.owner, "Other book", primary)
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: shared})
	d := x.doc(x.owner, pid, "One", "Elara met Bran.")
	x.settle()
	owner := x.counts(x.owner, d.ID)
	if owner["Elara"] != 1 || owner["Bran"] != 1 {
		t.Fatalf("the person sees both: %v", owner)
	}
	projectKey := ops.Principal{UserID: x.owner.UserID, Role: x.owner.Role, ProjectID: pid, Scope: ops.ScopeWrite, Via: "apikey"}
	got := x.list(projectKey, d.ID, true)
	if len(got.Items) != 1 || got.Items[0].Entity.Name != "Elara" || !got.Hidden {
		t.Errorf("a project-pinned key reached the shared Compendium: %+v", got)
	}
	x.expect(projectKey, "mention.by_entity", mentions.ByEntityIn{ID: pid, Entity: "Bran"}, ops.CodeNotFound)
	fa := x.call(projectKey, "entity.first_appearance", mentions.FirstIn{ID: pid}).(mentions.FirstOut)
	if len(fa.Items) != 1 || fa.Items[0].Entity.Name != "Elara" || !fa.Hidden {
		t.Errorf("first appearance through a pin: %+v", fa)
	}
	// It reaches its own project and no other.
	x.expect(projectKey, "mention.by_entity", mentions.ByEntityIn{ID: other, Entity: "Elara"}, ops.CodeNotFound)
	// A key pinned to a Compendium reaches no project op, and sees no project through entity.appears_in.
	compKey := ops.Principal{UserID: x.owner.UserID, Role: x.owner.Role, CompendiumID: primary, Scope: ops.ScopeWrite, Via: "apikey"}
	x.expect(compKey, "mention.list", mentions.ListIn{ID: d.ID}, ops.CodeNotFound)
	elara := x.call(x.owner, "entity.list", entities.ListIn{ID: primary}).(entities.ListOut).Items[0].ID
	ap := x.call(compKey, "entity.appears_in", mentions.AppearsIn{ID: elara}).(mentions.AppearsOut)
	if len(ap.Items) != 0 || ap.HiddenProjects != 2 && ap.HiddenProjects != 1 {
		t.Errorf("a Compendium-pinned key learned about projects: %+v", ap)
	}
	if full := x.call(x.owner, "entity.appears_in", mentions.AppearsIn{ID: elara}).(mentions.AppearsOut); len(full.Items) != 1 {
		t.Errorf("the person sees the book: %+v", full)
	}
	// A project key is held to its project for appears_in as well.
	pk := x.call(projectKey, "entity.appears_in", mentions.AppearsIn{ID: elara}).(mentions.AppearsOut)
	if len(pk.Items) != 1 || pk.Items[0].ProjectID != pid {
		t.Errorf("a project-pinned key sees %+v", pk)
	}
}

// A project that cannot be scanned must not keep the worker from the others,
// or spin on itself.
func TestWorkerBacksOffFromAFailingProject(t *testing.T) {
	b := newBook(t)
	d := b.doc(b.owner, b.pid, "One", "Elara walks.")
	b.settle()
	other := b.project(b.owner, "Fine", b.cid)
	b.doc(b.owner, other, "Two", "Máire arrives.")
	// Make every write to the index of the first project fail.
	b.exec(`CREATE TRIGGER boom BEFORE INSERT ON entity_mentions WHEN NEW.project_id = '` + b.pid + `' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	b.exec(`DELETE FROM entity_mentions WHERE document_id = ?`, d.ID)
	b.exec(`UPDATE mention_state SET stamp = 'changed' WHERE project_id = ?`, b.pid)
	var mu sync.Mutex
	warns := 0
	h := countingHandler{n: &warns, mu: &mu}
	oldPoll, oldBackoff := mentions.PollInterval, mentions.FailureBackoff
	mentions.PollInterval, mentions.FailureBackoff = 5*time.Millisecond, time.Minute
	defer func() { mentions.PollInterval, mentions.FailureBackoff = oldPoll, oldBackoff }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { mentions.Run(ctx, b.d, slog.New(h)); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for b.count(`SELECT count(*) FROM mention_state WHERE project_id = ? AND stamp <> indexed_stamp`, other) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the healthy project was never scanned: the failing one starved it")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	n := warns
	mu.Unlock()
	if n < 1 || n > 3 {
		t.Errorf("the failing project was logged %d times in 300 ms of 5 ms polls: it should be tried, then left alone", n)
	}
	if b.count(`SELECT count(*) FROM mention_state WHERE project_id = ? AND stamp <> indexed_stamp`, b.pid) != 1 {
		t.Errorf("the failing project should still owe its scan")
	}
}

type countingHandler struct {
	n  *int
	mu *sync.Mutex
}

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn && strings.Contains(r.Message, "scan failed") {
		h.mu.Lock()
		*h.n++
		h.mu.Unlock()
	}
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }
