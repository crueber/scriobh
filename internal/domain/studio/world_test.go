package studio_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// world is a database, a registry and a clock the test moves by hand.
type world struct {
	t   testing.TB
	d   *db.DB
	reg *ops.Registry
	now time.Time
	n   int
	// cards is the public side of the progress cards, registered as the server does.
	cards *studio.Cards
}

func newWorld(t testing.TB) *world {
	t.Helper()
	w := &world{t: t, d: testutil.DB(t), now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	w.reg = registry.Build(w.d, nil, t.TempDir())
	w.cards = studio.NewCards(w.d)
	w.reg.Env().SetService(studio.CardsService, w.cards)
	restore := db.SetClock(func() time.Time { return w.now })
	t.Cleanup(restore)
	return w
}

func (w *world) at(s string) {
	w.t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		w.t.Fatal(err)
	}
	w.now = tm
}

type person struct {
	id string
	p  ops.Principal
}

func (w *world) person(name, zone string) person {
	w.t.Helper()
	id := testutil.User(w.t, w.d, "writer", name+"@test.local")
	if zone != "" {
		if _, err := w.d.Writer().Exec(`UPDATE users SET timezone = ? WHERE id = ?`, zone, id); err != nil {
			w.t.Fatal(err)
		}
	}
	return person{id: id, p: ops.Principal{UserID: id, Role: ops.RoleWriter}}
}

// session gives the person a browser session of their own (a second one is a locked one when a project has a passphrase).
func (w *world) session(p person, id string) person {
	w.t.Helper()
	err := w.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		now := db.Now()
		_, err := tx.Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, '2999-01-01T00:00:00.000Z')`, id, p.id, "hash"+id, now, now)
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	q := p
	q.p.Via, q.p.SessionID = "session", id
	return q
}

func (w *world) try(p person, op string, in any) (any, error) {
	w.t.Helper()
	return w.reg.Call(context.Background(), p.p, op, in)
}

func (w *world) call(p person, op string, in any) any {
	w.t.Helper()
	out, err := w.try(p, op, in)
	if err != nil {
		w.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (w *world) project(owner person, title string) string {
	w.t.Helper()
	return w.call(owner, "project.create", projects.CreateIn{Title: title}).(projects.Item).Item.ID
}

func (w *world) doc(p person, projectID string) string {
	w.t.Helper()
	w.n++
	return w.call(p, "doc.create", docs.CreateIn{ProjectID: projectID, Title: fmt.Sprintf("Chapter %d", w.n)}).(docs.Item).Item.ID
}

// save writes text into a document at the current clock, as a person typing would.
func (w *world) save(p person, docID, text string) {
	w.t.Helper()
	cur := w.call(p, "doc.get", docs.DocIn{ID: docID}).(docs.Item).Item
	w.call(p, "doc.update", docs.UpdateIn{ID: docID, BaseRevision: cur.Revision, Content: &text})
}

// text is what a document holds now.
func (w *world) text(p person, docID string) string {
	w.t.Helper()
	return w.call(p, "doc.get", docs.DocIn{ID: docID}).(docs.Item).Item.Content
}

// lorem makes n distinct words that no other tag makes: tag0 tag1 ...
func lorem(n int, tag string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s%d ", tag, i)
	}
	return b.String()
}
