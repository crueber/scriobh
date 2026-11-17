package entities_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
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
	owner    ops.Principal // writer; owns the Compendiums and projects the tests create
	editor   ops.Principal // writer
	reader   ops.Principal // instance reader
	stranger ops.Principal // writer, on nothing
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

// fail calls an op that must be refused and returns the error.
func (x *h) fail(p ops.Principal, op string, in any) *ops.Error {
	x.t.Helper()
	_, err := x.reg.Call(x.ctx, p, op, in)
	if err == nil {
		x.t.Fatalf("%s: expected an error", op)
	}
	return ops.AsError(err)
}

func (x *h) expect(p ops.Principal, op string, in any, code ops.Code) *ops.Error {
	x.t.Helper()
	e := x.fail(p, op, in)
	if e.Code != code {
		x.t.Fatalf("%s: got %s (%s), want %s", op, e.Code, e.Message, code)
	}
	return e
}

func (x *h) compendium(p ops.Principal, name string) string {
	x.t.Helper()
	return x.call(p, "compendium.create", compendiums.CreateIn{Name: name}).(compendiums.Item).Item.ID
}

func (x *h) project(p ops.Principal, title string) string {
	x.t.Helper()
	return x.call(p, "project.create", projects.CreateIn{Title: title}).(projects.Item).Item.ID
}

func (x *h) ent(p ops.Principal, cid, kind, name string) entities.Entity {
	x.t.Helper()
	return x.call(p, "entity.create", entities.CreateIn{ID: cid, Kind: kind, Name: name}).(entities.Item).Item
}

func (x *h) get(p ops.Principal, id string) entities.Detail {
	x.t.Helper()
	return x.call(p, "entity.get", entities.GetIn{ID: id}).(entities.DetailItem).Item
}

// member inserts a Compendium membership directly (the ops are tested on their own).
func (x *h) member(cid string, p ops.Principal, role string) {
	x.t.Helper()
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, cid, p.UserID, role, db.Now())
		return err
	})
	if err != nil {
		x.t.Fatal(err)
	}
}

func (x *h) projectMember(pid string, p ops.Principal, role string) {
	x.t.Helper()
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, p.UserID, role, db.Now())
		return err
	})
	if err != nil {
		x.t.Fatal(err)
	}
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

func str(s string) *string { return &s }

type ctxT = context.Context
