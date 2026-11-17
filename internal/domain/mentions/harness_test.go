package mentions_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// h is the harness: a migrated database, the full registry and a handful of
// people. Ops are called through the registry, so authorization, validation and
// events are all exercised.
type h struct {
	t        *testing.T
	d        *db.DB
	reg      *ops.Registry
	dir      string
	ctx      context.Context
	owner    ops.Principal // owns the Compendiums and projects the tests create
	editor   ops.Principal
	reader   ops.Principal // a project reader who is not on any Compendium
	stranger ops.Principal
	admin    ops.Principal
}

func newH(t *testing.T) *h {
	d := testutil.DB(t)
	dir := t.TempDir()
	reg := registry.Build(d, events.NewStore(d, nil), dir)
	person := func(role, email string) ops.Principal {
		return ops.Principal{UserID: testutil.User(t, d, role, email), Role: role, Email: email}
	}
	return &h{t: t, d: d, reg: reg, dir: dir, ctx: context.Background(),
		owner: person(ops.RoleWriter, "owner@t.local"), editor: person(ops.RoleWriter, "editor@t.local"), reader: person(ops.RoleReader, "reader@t.local"),
		stranger: person(ops.RoleWriter, "stranger@t.local"), admin: person(ops.RoleAdmin, "admin@t.local")}
}

func (x *h) call(p ops.Principal, op string, in any) any {
	x.t.Helper()
	out, err := x.reg.Call(x.ctx, p, op, in)
	if err != nil {
		x.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (x *h) expect(p ops.Principal, op string, in any, code ops.Code) *ops.Error {
	x.t.Helper()
	_, err := x.reg.Call(x.ctx, p, op, in)
	if err == nil {
		x.t.Fatalf("%s: expected %s", op, code)
	}
	e := ops.AsError(err)
	if e.Code != code {
		x.t.Fatalf("%s: got %s (%s), want %s", op, e.Code, e.Message, code)
	}
	return e
}

func (x *h) compendium(p ops.Principal, name string) string {
	x.t.Helper()
	return x.call(p, "compendium.create", compendiums.CreateIn{Name: name}).(compendiums.Item).Item.ID
}

// project makes a project whose primary Compendium is cid ("" for none).
func (x *h) project(p ops.Principal, title, cid string) string {
	x.t.Helper()
	pid := x.call(p, "project.create", projects.CreateIn{Title: title}).(projects.Item).Item.ID
	if cid != "" {
		x.call(p, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	}
	return pid
}

func (x *h) ent(p ops.Principal, cid, kind, name string, aliases ...string) string {
	x.t.Helper()
	return x.call(p, "entity.create", entities.CreateIn{ID: cid, Kind: kind, Name: name, Aliases: aliases}).(entities.Item).Item.ID
}

func (x *h) doc(p ops.Principal, pid, title, body string) docs.Doc {
	x.t.Helper()
	return x.call(p, "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: "scene", Content: body}).(docs.Item).Item
}

func (x *h) save(p ops.Principal, d docs.Doc, body string) docs.Doc {
	x.t.Helper()
	return x.call(p, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, Content: &body}).(docs.Item).Item
}

// settle waits for the index the way the worker would.
func (x *h) settle() mentions.Stats {
	x.t.Helper()
	st, err := mentions.Settle(x.ctx, x.d)
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

func (x *h) member(cid string, p ops.Principal, role string) {
	x.t.Helper()
	x.exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, cid, p.UserID, role, db.Now())
}

func (x *h) projectMember(pid string, p ops.Principal, role string) {
	x.t.Helper()
	x.exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, p.UserID, role, db.Now())
}

func (x *h) count(query string, args ...any) int {
	x.t.Helper()
	var n int
	if err := x.d.Read().QueryRow(query, args...).Scan(&n); err != nil {
		x.t.Fatal(err)
	}
	return n
}

func (x *h) exec(query string, args ...any) {
	x.t.Helper()
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(query, args...); return err })
	if err != nil {
		x.t.Fatal(err)
	}
}

func (x *h) list(p ops.Principal, docID string, spans bool) mentions.ListOut {
	x.t.Helper()
	return x.call(p, "mention.list", mentions.ListIn{ID: docID, Spans: spans}).(mentions.ListOut)
}

// counts maps entity name to count for a document.
func (x *h) counts(p ops.Principal, docID string) map[string]int {
	x.t.Helper()
	out := map[string]int{}
	for _, m := range x.list(p, docID, false).Items {
		out[m.Entity.Name] = m.Count
	}
	return out
}

func str(s string) *string { return &s }

// bound asserts a duration against a generous limit, scaled under the race detector.
func bound(t *testing.T, name string, got, limit time.Duration) {
	t.Helper()
	t.Logf("%s: %v (limit %v)", name, got.Round(time.Millisecond), limit*timingFactor)
	// The measurement is made by the caller; only the comparison is a timing test, so the behaviour checks around
	// the call keep running when SCRIOBH_TIMING=off.
	t.Run(name, func(t *testing.T) {
		testutil.Timing(t)
		if got > limit*timingFactor {
			t.Errorf("%s took %v, over %v", name, got, limit*timingFactor)
		}
	})
}
