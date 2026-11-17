package scenes_test

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
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type h struct {
	t                                         *testing.T
	d                                         *db.DB
	reg                                       *ops.Registry
	ctx                                       context.Context
	owner, editor, reader, stranger           ops.Principal
	cid, pid, maire, elara, harbour, saltRoad string
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
	ent := func(kind, name string, aliases ...string) string {
		return x.call(x.owner, "entity.create", entities.CreateIn{ID: x.cid, Kind: kind, Name: name, Aliases: aliases}).(entities.Item).Item.ID
	}
	x.maire = ent("character", "Máire Ní Bhriain", "Máire")
	x.elara = ent("character", "Elara")
	x.harbour = ent("place", "Port Salt")
	x.saltRoad = ent("place", "Salt Road")
	x.pid = x.call(x.owner, "project.create", projects.CreateIn{Title: "Salt"}).(projects.Item).Item.ID
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

func (x *h) scene(title, body string, mut ...func(*docs.UpdateIn)) docs.Doc {
	x.t.Helper()
	d := x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.pid, Title: title, Kind: "scene", Content: body}).(docs.Item).Item
	if len(mut) > 0 {
		in := docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision}
		mut[0](&in)
		d = x.call(x.owner, "doc.update", in).(docs.Item).Item
	}
	return d
}

func (x *h) exec(q string, args ...any) {
	x.t.Helper()
	if err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		x.t.Fatal(err)
	}
}

func (x *h) projectMember(p ops.Principal, role string) {
	x.exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, x.pid, p.UserID, role, db.Now())
}

func (x *h) compendiumMember(p ops.Principal, role string) {
	x.exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, x.cid, p.UserID, role, db.Now())
}

func (x *h) outline(p ops.Principal, sort string) docs.OutlineOut {
	x.t.Helper()
	return x.call(p, "outline.get", docs.OutlineIn{ProjectID: x.pid, Sort: sort}).(docs.OutlineOut)
}

func (x *h) item(p ops.Principal, id string) docs.OutlineItem {
	x.t.Helper()
	for _, it := range x.outline(p, "").Items {
		if it.ID == id {
			return it
		}
	}
	x.t.Fatalf("%s is not in the outline", id)
	return docs.OutlineItem{}
}

func str(s string) *string { return &s }

func TestSetPovAndLocation(t *testing.T) {
	x := newH(t)
	d := x.scene("One", "Máire at Port Salt.", func(in *docs.UpdateIn) { in.Pov = str("someone else"); in.Location = str("the harbour") })
	out := x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: "Máire"}).(scenes.SetOut)
	if out.PovEntity == nil || out.PovEntity.ID != x.maire || out.Pov != "Máire Ní Bhriain" || out.Revision != d.Revision+1 {
		t.Errorf("set pov: %+v", out)
	}
	out = x.call(x.owner, "scene.set_location", scenes.SetIn{ID: d.ID, Entity: x.harbour, BaseRevision: out.Revision}).(scenes.SetOut)
	if out.LocationEntity == nil || out.LocationEntity.ID != x.harbour || out.Location != "Port Salt" {
		t.Errorf("set location: %+v", out)
	}
	it := x.item(x.owner, d.ID)
	if it.PovEntity == nil || it.PovEntity.Name != "Máire Ní Bhriain" || it.LocationEntity == nil || it.Pov != "Máire Ní Bhriain" {
		t.Errorf("the outline shows %+v", it)
	}
	list := x.call(x.owner, "doc.list", docs.ListIn{ProjectID: x.pid}).(docs.ListOut)
	found := false
	for _, m := range list.Items {
		if m.ID == d.ID {
			found = m.PovEntity != nil && m.LocationEntity != nil
		}
	}
	if !found {
		t.Errorf("doc.list does not carry the entity references")
	}
	// A stale revision is refused.
	x.expect(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.elara, BaseRevision: d.Revision}, ops.CodeConflict)
	// A point of view must be a character.
	x.expect(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.harbour}, ops.CodeInvalid)
	// Clearing drops the link and keeps the text.
	out = x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: ""}).(scenes.SetOut)
	if out.PovEntity != nil || out.Pov != "Máire Ní Bhriain" {
		t.Errorf("clear: %+v", out)
	}
	// Deleting an entity (a merge) repoints; the cleanup trigger of the Compendium clears what is left.
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.elara})
	x.call(x.owner, "entity.merge", entities.MergeIn{ID: x.elara, IntoID: x.maire})
	if it := x.item(x.owner, d.ID); it.PovEntity == nil || it.PovEntity.ID != x.maire {
		t.Errorf("after a merge: %+v", it.PovEntity)
	}
	x.call(x.owner, "entity.trash", entities.StateIn{ID: x.harbour})
	if it := x.item(x.owner, d.ID); it.LocationEntity != nil || it.Location != "Port Salt" {
		t.Errorf("a trashed location should drop out of the references and leave the text: %+v / %q", it.LocationEntity, it.Location)
	}
}

func TestSceneReferencesFollowTheBibleAccess(t *testing.T) {
	x := newH(t)
	d := x.scene("One", "Máire at Port Salt.")
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.maire})
	x.projectMember(x.editor, ops.ProjectEditor)
	x.projectMember(x.reader, ops.ProjectReader)
	// An editor who is not on the Compendium: the id does not resolve, with the same 404 as a name that does not exist.
	real := x.expect(x.editor, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.elara}, ops.CodeNotFound)
	fake := x.expect(x.editor, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: "Nobody"}, ops.CodeNotFound)
	if real.Message != fake.Message {
		t.Errorf("an unreadable entity and a missing one differ: %q vs %q", real.Message, fake.Message)
	}
	// Reading: the text, but no entity.
	if it := x.item(x.reader, d.ID); it.PovEntity != nil || it.Pov != "Máire Ní Bhriain" {
		t.Errorf("a reader got the bible: %+v", it)
	}
	se := x.call(x.reader, "scene.entities", scenes.EntitiesIn{ID: d.ID}).(scenes.EntitiesOut)
	if se.Pov != nil || len(se.Items) != 0 || !se.Hidden || se.PovText != "Máire Ní Bhriain" {
		t.Errorf("scene.entities leaks: %+v", se)
	}
	// With Compendium access the same call shows it.
	x.compendiumMember(x.reader, "reader")
	se = x.call(x.reader, "scene.entities", scenes.EntitiesIn{ID: d.ID}).(scenes.EntitiesOut)
	if se.Pov == nil || se.Hidden || len(se.Items) == 0 {
		t.Errorf("a member sees %+v", se)
	}
}

func TestSceneEntitiesSidebar(t *testing.T) {
	x := newH(t)
	// An override for this book: its own summary.
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: x.pid, EntityID: x.elara, Summary: str("Older in this book.")})
	d := x.scene("One", "Elara and Máire met on the Salt Road. Elara left.", func(in *docs.UpdateIn) { in.Content = nil })
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.elara})
	x.call(x.owner, "scene.set_location", scenes.SetIn{ID: d.ID, Entity: x.harbour})
	if _, err := mentions.Settle(x.ctx, x.d); err != nil {
		t.Fatal(err)
	}
	se := x.call(x.owner, "scene.entities", scenes.EntitiesIn{ID: d.ID}).(scenes.EntitiesOut)
	if se.Indexing || se.Hidden {
		t.Errorf("flags: %+v", se)
	}
	var got []string
	for _, it := range se.Items {
		got = append(got, it.Entity.Name+":"+join(it.Roles))
	}
	// pov first, then location, then the rest in the order they appear.
	want := []string{"Elara:pov,mentioned", "Port Salt:location", "Máire Ní Bhriain:mentioned", "Salt Road:mentioned"}
	if join(got) != join(want) {
		t.Errorf("items %v, want %v", got, want)
	}
	if se.Items[0].Count != 2 || se.Items[0].Entity.Summary != "Older in this book." || !se.Items[0].Entity.Overridden {
		t.Errorf("the book's override is not applied: %+v", se.Items[0])
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

func TestLinkEntitiesMigratesFreeText(t *testing.T) {
	x := newH(t)
	x.call(x.owner, "entity.create", entities.CreateIn{ID: x.cid, Kind: "character", Name: "Salt Road Gang"})
	a := x.scene("A", "a", func(in *docs.UpdateIn) { in.Pov = str("  elara "); in.Location = str("PORT  SALT") })
	b := x.scene("B", "b", func(in *docs.UpdateIn) { in.Pov = str("Máire"); in.Location = str("the harbour") })
	x.scene("C", "c", func(in *docs.UpdateIn) { in.Pov = str("Elara"); in.Location = str("Port Salt") })
	d := x.scene("D", "d", func(in *docs.UpdateIn) { in.Pov = str("Port Salt"); in.Location = str("Salt Road") })
	e := x.scene("E", "e", func(in *docs.UpdateIn) { in.Pov = str("Nobody"); in.Location = str("") })
	f := x.scene("F", "f")
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: f.ID, Entity: x.maire}) // already linked: left alone
	// "Bran" is two characters in two Compendiums: ambiguous.
	cid2 := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Other"}).(compendiums.Item).Item.ID
	x.call(x.owner, "entity.create", entities.CreateIn{ID: x.cid, Kind: "character", Name: "Bran"})
	x.call(x.owner, "entity.create", entities.CreateIn{ID: cid2, Kind: "character", Name: "Bran the Elder", Aliases: []string{"Bran"}})
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: x.pid, CompendiumID: cid2})
	g := x.scene("G", "g", func(in *docs.UpdateIn) { in.Pov = str("bran") })

	dry := x.call(x.owner, "scene.link_entities", scenes.LinkIn{ID: x.pid, DryRun: true}).(scenes.LinkOut)
	if !dry.DryRun || dry.Updated != 0 {
		t.Fatalf("dry run: %+v", dry)
	}
	if it := x.item(x.owner, a.ID); it.PovEntity != nil {
		t.Errorf("a dry run wrote")
	}
	// The global dry-run path agrees with the real call.
	res, err := x.reg.CallDry(x.ctx, x.owner, "scene.link_entities", scenes.LinkIn{ID: x.pid})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Out.(scenes.LinkOut); len(got.Linked) != len(dry.Linked) {
		t.Errorf("CallDry reports %d, dry_run %d", len(got.Linked), len(dry.Linked))
	}
	if it := x.item(x.owner, a.ID); it.PovEntity != nil {
		t.Errorf("CallDry wrote")
	}

	out := x.call(x.owner, "scene.link_entities", scenes.LinkIn{ID: x.pid}).(scenes.LinkOut)
	if out.AlreadyLinked != 1 {
		t.Errorf("already linked %d", out.AlreadyLinked)
	}
	if len(out.Linked) != 6 {
		t.Errorf("linked: %+v", out.Linked)
	}
	why := map[string]string{}
	for _, u := range out.Unmatched {
		why[u.Field+":"+u.Value] = u.Reason
	}
	for k, want := range map[string]string{"location:the harbour": "no_match", "pov:Port Salt": "not_a_character", "pov:Nobody": "no_match", "pov:bran": "ambiguous"} {
		if why[k] != want {
			t.Errorf("%s: %q, want %q (%v)", k, why[k], want, why)
		}
	}
	if out.Updated != 6 {
		t.Errorf("updated %d scenes' fields, want 6 (a's two, b's pov, c's two, d's location)", out.Updated)
	}
	if it := x.item(x.owner, a.ID); it.PovEntity == nil || it.PovEntity.ID != x.elara || it.LocationEntity == nil || it.Pov != "elara" {
		t.Errorf("a: %+v (the text is kept as written)", it)
	}
	if it := x.item(x.owner, b.ID); it.PovEntity == nil || it.LocationEntity != nil || it.Location != "the harbour" {
		t.Errorf("b: %+v", it)
	}
	if it := x.item(x.owner, d.ID); it.PovEntity != nil || it.LocationEntity == nil {
		t.Errorf("d: %+v", it)
	}
	if it := x.item(x.owner, e.ID); it.PovEntity != nil {
		t.Errorf("e: %+v", it)
	}
	if it := x.item(x.owner, g.ID); it.PovEntity != nil {
		t.Errorf("an ambiguous value was guessed: %+v", it.PovEntity)
	}
	// Linking changes no revision: an open editor's next save is not refused.
	if got := x.item(x.owner, a.ID).Revision; got != a.Revision {
		t.Errorf("revision moved from %d to %d", a.Revision, got)
	}
	// Running it again finds nothing new.
	again := x.call(x.owner, "scene.link_entities", scenes.LinkIn{ID: x.pid}).(scenes.LinkOut)
	if again.Updated != 0 {
		t.Errorf("a second run wrote %d", again.Updated)
	}
	// Only the owner runs it.
	x.projectMember(x.editor, ops.ProjectEditor)
	x.expect(x.editor, "scene.link_entities", scenes.LinkIn{ID: x.pid}, ops.CodeForbidden)
}

func TestLinkEntitiesSeesOnlyWhatTheOwnerMayRead(t *testing.T) {
	x := newH(t)
	x.scene("A", "a", func(in *docs.UpdateIn) { in.Pov = str("Elara") })
	// A second person owns a project that uses the Compendium but is not on it.
	p2 := x.call(x.stranger, "project.create", projects.CreateIn{Title: "Mine"}).(projects.Item).Item.ID
	x.exec(`UPDATE projects SET compendium_id = ? WHERE id = ?`, x.cid, p2)
	d := x.call(x.stranger, "doc.create", docs.CreateIn{ProjectID: p2, Title: "S", Kind: "scene"}).(docs.Item).Item
	x.call(x.stranger, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, Pov: str("Elara")})
	out := x.call(x.stranger, "scene.link_entities", scenes.LinkIn{ID: p2}).(scenes.LinkOut)
	if len(out.Linked) != 0 || len(out.Unmatched) != 1 || out.Unmatched[0].Reason != "no_match" {
		t.Errorf("the bible leaked through a repair: %+v", out)
	}
}

func TestOutlineSortsByTimelineAndPov(t *testing.T) {
	x := newH(t)
	mk := func(title, date, pov string) docs.Doc {
		return x.scene(title, "word word word", func(in *docs.UpdateIn) { in.StoryDate = str(date); in.Pov = str(pov) })
	}
	mk("S1", "1998-04-12", "")
	s2 := mk("S2", "1997", "Elara")
	s3 := mk("S3", "", "Máire")
	mk("S4", "1998-04", "Elara")
	mk("S5", "unknown", "")
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: s3.ID, Entity: x.maire})
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: s2.ID, Entity: x.elara})
	ids := func(o docs.OutlineOut) []string {
		var out []string
		for _, it := range o.Items {
			if it.Kind == "scene" {
				out = append(out, it.Title)
			}
		}
		return out
	}
	order := x.outline(x.owner, "order")
	if order.Sort != "order" || len(order.Items) < 6 {
		t.Errorf("order: %+v", order.Sort)
	}
	tl := x.outline(x.owner, "timeline")
	if got := join(ids(tl)); got != "S2,S4,S1,S3,S5" {
		t.Errorf("timeline order %s (undated last, in reading order; 1998-04 before 1998-04-12)", got)
	}
	if tl.Sort != "timeline" || !tl.Items[0].StoryDated {
		t.Errorf("sort flag / story_dated: %+v", tl.Items[0])
	}
	pv := x.outline(x.owner, "pov")
	if got := join(ids(pv)); got != "S2,S4,S3,S1,S5" { // Elara (S2, S4), Máire (S3), then none (S1, S5) in reading order
		t.Errorf("pov order %s", got)
	}
	if len(pv.Groups) != 3 || pv.Groups[0].Label != "Elara" || pv.Groups[0].Documents != 2 || pv.Groups[0].Entity == nil || pv.Groups[1].Label != "Máire Ní Bhriain" || pv.Groups[2].Key != "" || pv.Groups[2].Documents != 3 { // S1, S5 and the chapter the project starts with
		t.Errorf("groups: %+v", pv.Groups)
	}
	for _, it := range pv.Items {
		if it.Breadcrumb == nil {
			t.Errorf("sorted items carry their breadcrumb: %+v", it)
		}
	}
	x.expect(x.owner, "outline.get", docs.OutlineIn{ProjectID: x.pid, Sort: "alphabet"}, ops.CodeInvalid)
	// A reader who is not on the Compendium still gets the text grouping.
	x.projectMember(x.reader, ops.ProjectReader)
	r := x.outline(x.reader, "pov")
	if len(r.Groups) == 0 || r.Groups[0].Entity != nil {
		t.Errorf("a reader sees entities in the groups: %+v", r.Groups)
	}
}
