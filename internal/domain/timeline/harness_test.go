package timeline_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// h is a small world: one Compendium (people and places), one book that uses it.
type h struct {
	t                               *testing.T
	d                               *db.DB
	reg                             *ops.Registry
	ctx                             context.Context
	owner, editor, reader, stranger ops.Principal
	cid, pid                        string
	maire, elara, tomas             string
	dublin, cork                    string
}

func newH(t *testing.T) *h {
	d := testutil.DB(t)
	reg := registry.Build(d, events.NewStore(d, nil), t.TempDir())
	person := func(role, email string) ops.Principal {
		return ops.Principal{UserID: testutil.User(t, d, role, email), Role: role, Email: email}
	}
	x := &h{t: t, d: d, reg: reg, ctx: context.Background(),
		owner: person(ops.RoleWriter, "owner@t.local"), editor: person(ops.RoleWriter, "editor@t.local"),
		reader: person(ops.RoleReader, "reader@t.local"), stranger: person(ops.RoleWriter, "stranger@t.local")}
	x.cid = x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	for _, k := range []string{"born", "died"} {
		x.call(x.owner, "entity.field.set", entities.FieldSetIn{ID: x.cid, Kind: "character", Key: k, Type: "date"})
	}
	ent := func(kind, name string, fields map[string]any) string {
		return x.call(x.owner, "entity.create", entities.CreateIn{ID: x.cid, Kind: kind, Name: name, Fields: fields}).(entities.Item).Item.ID
	}
	x.maire = ent("character", "Máire", map[string]any{"born": "1998-04-15"})
	x.elara = ent("character", "Elara", nil)
	x.tomas = ent("character", "Tomas", nil)
	x.dublin = ent("place", "Dublin", nil)
	x.cork = ent("place", "Cork", nil)
	x.pid = x.call(x.owner, "project.create", projects.CreateIn{Title: "Book"}).(projects.Item).Item.ID
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: x.pid, CompendiumID: x.cid})
	return x
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

func (x *h) exec(q string, args ...any) {
	x.t.Helper()
	if err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		x.t.Fatal(err)
	}
}

func (x *h) count(q string, args ...any) int {
	x.t.Helper()
	var n int
	if err := x.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		x.t.Fatal(err)
	}
	return n
}

// ev creates an event in the Compendium.
func (x *h) ev(title, date, end string, loc string, people ...string) timeline.Event {
	x.t.Helper()
	in := timeline.CreateIn{ID: x.cid, Title: title, StoryDate: date, StoryDateEnd: end, Location: loc}
	for _, p := range people {
		in.Participants = append(in.Participants, timeline.Participant{Entity: p})
	}
	return x.call(x.owner, "timeline.event.create", in).(timeline.EventItem).Item
}

func (x *h) scene(title, date string) docs.Doc {
	x.t.Helper()
	d := x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: title, Kind: "scene", Content: "Words."}).(docs.Item).Item
	if date != "" {
		d = x.call(x.owner, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, StoryDate: &date}).(docs.Item).Item
	}
	return d
}

func (x *h) view(p ops.Principal) timeline.ViewOut {
	x.t.Helper()
	return x.call(p, "timeline.project.view", timeline.ViewIn{ID: x.pid}).(timeline.ViewOut)
}

func (x *h) check(p ops.Principal) timeline.CheckOut {
	x.t.Helper()
	return x.call(p, "timeline.check", timeline.CheckIn{ID: x.pid}).(timeline.CheckOut)
}

func (x *h) settle() {
	x.t.Helper()
	if _, err := mentions.Settle(x.ctx, x.d); err != nil {
		x.t.Fatal(err)
	}
}

func (x *h) projectMember(p ops.Principal, role string) {
	x.exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, x.pid, p.UserID, role, db.Now())
}

func (x *h) compendiumMember(p ops.Principal, role string) {
	x.exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, x.cid, p.UserID, role, db.Now())
}

func str(s string) *string { return &s }

func titles(es []timeline.Event) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Title)
	}
	return out
}

func keys(v timeline.ViewOut, in []string) []string {
	titleOf := map[string]string{}
	for _, it := range v.Items {
		titleOf[it.Key] = it.Title
	}
	out := []string{}
	for _, k := range in {
		out = append(out, titleOf[k])
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %v, want %v", what, got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s: %v, want %v", what, got, want)
			return
		}
	}
}
