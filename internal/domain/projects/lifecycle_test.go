package projects_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type fx struct {
	t        *testing.T
	d        *db.DB
	reg      *ops.Registry
	dir      string
	ctx      context.Context
	owner    ops.Principal
	editor   ops.Principal
	reader   ops.Principal
	outsider ops.Principal
	admin    ops.Principal
	pid      string
}

// newFx builds a registry with a real event store, so events and audit rows
// are written and can be asserted on.
func newFx(t *testing.T) *fx {
	t.Helper()
	// Every clock read is a millisecond later than the last, so ordering by time
	// is deterministic however fast the test runs.
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	t.Cleanup(db.SetClock(func() time.Time { now = now.Add(time.Millisecond); return now }))
	d := testutil.DB(t)
	dir := t.TempDir()
	f := &fx{t: t, d: d, dir: dir, ctx: context.Background(), reg: registry.Build(d, events.NewStore(d, nil), dir)}
	mk := func(role, email string) ops.Principal {
		return ops.Principal{UserID: testutil.User(t, d, role, email), Role: role}
	}
	f.owner = mk(ops.RoleWriter, "owner@t.local")
	f.editor = mk(ops.RoleWriter, "editor@t.local")
	f.reader = mk(ops.RoleReader, "reader@t.local")
	f.outsider = mk(ops.RoleWriter, "outsider@t.local")
	f.admin = mk(ops.RoleAdmin, "admin@t.local")
	f.pid = f.create(f.owner, "Alpha")
	f.member(f.pid, f.editor, ops.ProjectEditor)
	f.member(f.pid, f.reader, ops.ProjectReader)
	return f
}

func (f *fx) create(p ops.Principal, title string) string {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, p, "project.create", projects.CreateIn{Title: title})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(projects.Item).Item.ID
}

func (f *fx) member(pid string, p ops.Principal, role string) {
	f.t.Helper()
	err := f.d.WithTx(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, p.UserID, role, db.Now())
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) item(p ops.Principal, op string, in any) projects.Project {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, p, op, in)
	if err != nil {
		f.t.Fatalf("%s: %v", op, err)
	}
	return out.(projects.Item).Item
}

func (f *fx) list(p ops.Principal, in projects.ListIn) []projects.Project {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, p, "project.list", in)
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(projects.ListOut).Items
}

func (f *fx) titles(p ops.Principal, state string) []string {
	var out []string
	for _, pr := range f.list(p, projects.ListIn{State: state}) {
		out = append(out, pr.Title)
	}
	return out
}

func code(err error) ops.Code { return ops.AsError(err).Code }

func field(err error) string { return ops.AsError(err).Field }

func (f *fx) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.d.Read().QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func s(v string) *string { return &v }
func n(v int) *int       { return &v }

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUpdateHappyPathKeepsSlug(t *testing.T) {
	f := newFx(t)
	before := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid})
	p := f.item(f.owner, "project.update", projects.UpdateIn{ID: f.pid,
		Title: s("  A New Name  "), Subtitle: s("sub"), AuthorName: s("A. Writer"), Genre: s("Fantasy"), Language: s("ga"),
		Status: s("drafting"), Description: s("  A long\nstory.  "), WordGoal: n(80000), Deadline: s("2027-03-01"), CoverColor: s("#7a4b2a")})
	if p.Title != "A New Name" || p.Subtitle != "sub" || p.AuthorName != "A. Writer" || p.Genre != "Fantasy" || p.Language != "ga" ||
		p.Status != "drafting" || p.Description != "A long\nstory." || p.WordGoal != 80000 || p.Deadline != "2027-03-01" || p.CoverColor != "#7A4B2A" {
		t.Errorf("updated %+v", p)
	}
	if p.Slug != before.Slug {
		t.Errorf("the slug changed from %q to %q; URLs must stay stable", before.Slug, p.Slug)
	}
	if p.UpdatedAt == before.UpdatedAt {
		t.Error("updated_at was not bumped")
	}
	// An empty deadline clears it.
	p = f.item(f.owner, "project.update", projects.UpdateIn{ID: f.pid, Deadline: s("")})
	if p.Deadline != "" {
		t.Errorf("deadline = %q", p.Deadline)
	}
	var raw sql.NullString
	if err := f.d.Read().QueryRow(`SELECT deadline FROM projects WHERE id = ?`, f.pid).Scan(&raw); err != nil || raw.Valid {
		t.Errorf("a cleared deadline must be NULL, got %v %v", raw, err)
	}
}

func TestUpdateNoChangeIsQuiet(t *testing.T) {
	f := newFx(t)
	before := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid})
	events := f.count(`SELECT count(*) FROM events WHERE type = 'project.updated'`)
	p := f.item(f.owner, "project.update", projects.UpdateIn{ID: f.pid})
	p2 := f.item(f.owner, "project.update", projects.UpdateIn{ID: f.pid, Title: s("Alpha")})
	if p.UpdatedAt != before.UpdatedAt || p2.UpdatedAt != before.UpdatedAt {
		t.Error("a no-op update must not bump updated_at")
	}
	if got := f.count(`SELECT count(*) FROM events WHERE type = 'project.updated'`); got != events {
		t.Errorf("a no-op update emitted %d events", got-events)
	}
}

func TestUpdateValidation(t *testing.T) {
	f := newFx(t)
	long := func(n int) *string { return s(strings.Repeat("a", n)) }
	cases := []struct {
		name  string
		in    projects.UpdateIn
		field string
	}{
		{"empty title", projects.UpdateIn{Title: s("   ")}, "title"},
		{"long title", projects.UpdateIn{Title: long(201)}, "title"},
		{"newline in title", projects.UpdateIn{Title: s("a\nb")}, "title"},
		{"long subtitle", projects.UpdateIn{Subtitle: long(201)}, "subtitle"},
		{"long author", projects.UpdateIn{AuthorName: long(201)}, "author_name"},
		{"long genre", projects.UpdateIn{Genre: long(101)}, "genre"},
		{"empty language", projects.UpdateIn{Language: s("")}, "language"},
		{"markup language", projects.UpdateIn{Language: s("en<b>")}, "language"},
		{"bad status", projects.UpdateIn{Status: s("done")}, "status"},
		{"long description", projects.UpdateIn{Description: long(5001)}, "description"},
		{"negative goal", projects.UpdateIn{WordGoal: n(-1)}, "word_goal"},
		{"huge goal", projects.UpdateIn{WordGoal: n(10_000_001)}, "word_goal"},
		{"deadline words", projects.UpdateIn{Deadline: s("next week")}, "deadline"},
		{"deadline not a day", projects.UpdateIn{Deadline: s("2027-02-30")}, "deadline"},
		{"deadline wrong shape", projects.UpdateIn{Deadline: s("2027-3-1")}, "deadline"},
		{"off palette", projects.UpdateIn{CoverColor: s("#123456")}, "cover_color"},
	}
	for _, tc := range cases {
		tc.in.ID = f.pid
		_, err := f.reg.Call(f.ctx, f.owner, "project.update", tc.in)
		if code(err) != ops.CodeInvalid || field(err) != tc.field {
			t.Errorf("%s: got %v, want invalid on %s", tc.name, err, tc.field)
		}
	}
	// Edges that are fine.
	for _, in := range []projects.UpdateIn{
		{Title: long(200)}, {WordGoal: n(0)}, {WordGoal: n(10_000_000)}, {Description: long(5000)}, {Deadline: s("2028-02-29")},
	} {
		in.ID = f.pid
		if _, err := f.reg.Call(f.ctx, f.owner, "project.update", in); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	// A rejected update changes nothing, not even the valid fields beside it.
	before := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid})
	if _, err := f.reg.Call(f.ctx, f.owner, "project.update", projects.UpdateIn{ID: f.pid, Subtitle: s("kept?"), Status: s("nope")}); err == nil {
		t.Fatal("want an error")
	}
	if after := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid}); after.Subtitle != before.Subtitle {
		t.Error("a rejected update was partially applied")
	}
}

func TestOwnerOnlyOps(t *testing.T) {
	f := newFx(t)
	owner := []struct {
		op string
		in any
	}{
		{"project.update", projects.UpdateIn{ID: f.pid, Title: s("x")}},
		{"project.trash", projects.StateIn{ID: f.pid}},
		{"project.restore", projects.StateIn{ID: f.pid}},
		{"project.archive", projects.StateIn{ID: f.pid}},
		{"project.unarchive", projects.StateIn{ID: f.pid}},
		{"project.purge", projects.PurgeIn{ID: f.pid, ConfirmTitle: "Alpha"}},
	}
	for _, tc := range owner {
		for name, p := range map[string]ops.Principal{"editor": f.editor, "reader": f.reader} {
			if _, err := f.reg.Call(f.ctx, p, tc.op, tc.in); code(err) != ops.CodeForbidden {
				t.Errorf("%s as %s: %v, want forbidden", tc.op, name, err)
			}
		}
		if _, err := f.reg.Call(f.ctx, f.outsider, tc.op, tc.in); code(err) != ops.CodeNotFound {
			t.Errorf("%s as outsider: %v, want not_found", tc.op, err)
		}
	}
	// Nothing above got through.
	if p := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid}); p.Title != "Alpha" || p.Trashed || p.Archived {
		t.Errorf("a refused op changed the project: %+v", p)
	}
	// Pin and open are open to any member, and to nobody else.
	for _, op := range []string{"project.pin", "project.open"} {
		var in any = projects.PinIn{ID: f.pid, Pinned: true}
		if op == "project.open" {
			in = projects.OpenIn{ID: f.pid}
		}
		for name, p := range map[string]ops.Principal{"owner": f.owner, "editor": f.editor, "reader": f.reader} {
			if _, err := f.reg.Call(f.ctx, p, op, in); err != nil {
				t.Errorf("%s as %s: %v", op, name, err)
			}
		}
		if _, err := f.reg.Call(f.ctx, f.outsider, op, in); code(err) != ops.CodeNotFound {
			t.Errorf("%s as outsider: %v, want not_found", op, err)
		}
	}
	// An instance admin who is not a member acts as owner, including on the
	// per-person ops, which must not 404 for them.
	if _, err := f.reg.Call(f.ctx, f.admin, "project.pin", projects.PinIn{ID: f.pid, Pinned: true}); err != nil {
		t.Errorf("admin pin: %v", err)
	}
	if p := f.item(f.admin, "project.archive", projects.StateIn{ID: f.pid}); !p.Archived || p.Role != ops.ProjectOwner {
		t.Errorf("admin archive: %+v", p)
	}
}

func TestTrashRestoreArchiveAreIdempotent(t *testing.T) {
	f := newFx(t)
	other := f.create(f.owner, "Beta")
	count := func(typ string) int {
		return f.count(`SELECT count(*) FROM events WHERE type = ? AND project_id = ?`, typ, f.pid)
	}
	audits := func(a string) int {
		return f.count(`SELECT count(*) FROM audit_log WHERE action = ? AND target_id = ?`, a, f.pid)
	}
	before := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid})

	steps := []struct {
		op, event string
		check     func(projects.Project) bool
	}{
		{"project.trash", "project.trashed", func(p projects.Project) bool { return p.Trashed && p.TrashedAt != "" }},
		{"project.restore", "project.restored", func(p projects.Project) bool { return !p.Trashed && p.TrashedAt == "" }},
		{"project.archive", "project.archived", func(p projects.Project) bool { return p.Archived && p.ArchivedAt != "" }},
		{"project.unarchive", "project.unarchived", func(p projects.Project) bool { return !p.Archived && p.ArchivedAt == "" }},
	}
	for _, st := range steps {
		for i := 0; i < 2; i++ {
			p := f.item(f.owner, st.op, projects.StateIn{ID: f.pid})
			if !st.check(p) {
				t.Errorf("%s (call %d): %+v", st.op, i+1, p)
			}
		}
		// The first call of each step changed something; the second did not.
		const want = 1
		if got := count(st.event); got != want {
			t.Errorf("%s emitted %d %s events, want %d", st.op, got, st.event, want)
		}
		if got := audits(st.op); got != want {
			t.Errorf("%s wrote %d audit rows, want %d", st.op, got, want)
		}
	}
	// Restore/unarchive with nothing to undo emit nothing.
	f.item(f.owner, "project.restore", projects.StateIn{ID: f.pid})
	f.item(f.owner, "project.unarchive", projects.StateIn{ID: f.pid})
	if count("project.restored") != 1 || count("project.unarchived") != 1 {
		t.Error("a no-op restore or unarchive emitted an event")
	}
	if after := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid}); after.UpdatedAt != before.UpdatedAt {
		t.Error("housekeeping must not change the last-edited time")
	}
	// The other project was never touched.
	if p := f.item(f.owner, "project.get", projects.GetIn{ID: other}); p.Trashed || p.Archived {
		t.Errorf("state leaked to another project: %+v", p)
	}
}

func TestListStatesAndTrashedStillReadable(t *testing.T) {
	f := newFx(t)
	arch := f.create(f.owner, "Archived one")
	trash := f.create(f.owner, "Trashed one")
	both := f.create(f.owner, "Both")
	f.item(f.owner, "project.archive", projects.StateIn{ID: arch})
	f.item(f.owner, "project.trash", projects.StateIn{ID: trash})
	f.item(f.owner, "project.archive", projects.StateIn{ID: both})
	f.item(f.owner, "project.trash", projects.StateIn{ID: both})

	sorted := func(xs []string) []string {
		out := append([]string(nil), xs...)
		for i := range out {
			for j := i + 1; j < len(out); j++ {
				if out[j] < out[i] {
					out[i], out[j] = out[j], out[i]
				}
			}
		}
		return out
	}
	for state, want := range map[string][]string{
		"":         {"Alpha"},
		"active":   {"Alpha"},
		"archived": {"Archived one"},
		"trashed":  {"Both", "Trashed one"},
		"all":      {"Alpha", "Archived one", "Both", "Trashed one"},
	} {
		if got := sorted(f.titles(f.owner, state)); !same(got, want) {
			t.Errorf("state %q: %v, want %v", state, got, want)
		}
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "project.list", projects.ListIn{State: "deleted"}); code(err) != ops.CodeInvalid || field(err) != "state" {
		t.Errorf("unknown state: %v", err)
	}
	// A trashed project is still readable by its owner, so the trash view works.
	if p := f.item(f.owner, "project.get", projects.GetIn{ID: trash}); !p.Trashed {
		t.Errorf("get on a trashed project: %+v", p)
	}
	// Only owners see the trash: an editor on the trashed project does not list it.
	f.member(trash, f.editor, ops.ProjectEditor)
	if got := f.titles(f.editor, "trashed"); len(got) != 0 {
		t.Errorf("an editor sees the owner's trash: %v", got)
	}
	for _, p := range f.list(f.editor, projects.ListIn{State: "all"}) {
		if p.Trashed {
			t.Errorf("an editor sees the owner's trashed project %q under state=all", p.Title)
		}
	}
	// The search filter applies within the state.
	if got := f.list(f.owner, projects.ListIn{State: "all", Q: "both"}); len(got) != 1 || got[0].Title != "Both" {
		t.Errorf("q within all: %+v", got)
	}
	if got := f.list(f.owner, projects.ListIn{Q: "both"}); len(got) != 0 {
		t.Errorf("q within active should not find a trashed project: %+v", got)
	}
}

func TestListOrderPinnedThenRecentThenEdited(t *testing.T) {
	f := newFx(t)
	// Created newest-edited last: Alpha, A, B, C, D.
	a, b, c, d := f.create(f.owner, "A"), f.create(f.owner, "B"), f.create(f.owner, "C"), f.create(f.owner, "D")
	if got := f.titles(f.owner, ""); !same(got, []string{"D", "C", "B", "A", "Alpha"}) {
		t.Fatalf("by edit time: %v", got)
	}
	f.item(f.owner, "project.open", projects.OpenIn{ID: b})
	f.item(f.owner, "project.open", projects.OpenIn{ID: a})
	if got := f.titles(f.owner, ""); !same(got, []string{"A", "B", "D", "C", "Alpha"}) {
		t.Fatalf("recently opened first: %v", got)
	}
	f.item(f.owner, "project.pin", projects.PinIn{ID: c, Pinned: true})
	f.item(f.owner, "project.pin", projects.PinIn{ID: d, Pinned: true})
	// Pinned first; among the pinned, recents then edit time (neither opened: D newer).
	if got := f.titles(f.owner, ""); !same(got, []string{"D", "C", "A", "B", "Alpha"}) {
		t.Fatalf("pinned first: %v", got)
	}
	f.item(f.owner, "project.pin", projects.PinIn{ID: d, Pinned: false})
	f.item(f.owner, "project.pin", projects.PinIn{ID: d, Pinned: false}) // idempotent
	if got := f.titles(f.owner, ""); !same(got, []string{"C", "A", "B", "D", "Alpha"}) {
		t.Fatalf("after unpin: %v", got)
	}
	p := f.list(f.owner, projects.ListIn{})[0]
	if !p.Pinned || p.Title != "C" {
		t.Errorf("pinned flag: %+v", p)
	}
}

func TestPerPersonStateIsIsolated(t *testing.T) {
	f := newFx(t)
	before := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid})
	f.item(f.owner, "project.pin", projects.PinIn{ID: f.pid, Pinned: true})
	f.item(f.editor, "project.open", projects.OpenIn{ID: f.pid})

	o := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid})
	e := f.item(f.editor, "project.get", projects.GetIn{ID: f.pid})
	r := f.item(f.reader, "project.get", projects.GetIn{ID: f.pid})
	if !o.Pinned || o.LastOpenedAt != "" {
		t.Errorf("owner: %+v", o)
	}
	if e.Pinned || e.LastOpenedAt == "" {
		t.Errorf("editor: %+v", e)
	}
	if r.Pinned || r.LastOpenedAt != "" {
		t.Errorf("reader: %+v", r)
	}
	// Pinning and opening are not edits and leave no project events.
	if o.UpdatedAt != before.UpdatedAt {
		t.Error("pin/open changed updated_at")
	}
	if n := f.count(`SELECT count(*) FROM events WHERE project_id = ? AND type NOT IN ('project.created')`, f.pid); n != 0 {
		t.Errorf("pin/open emitted %d events", n)
	}
	// Roles are the caller's own.
	if o.Role != "owner" || e.Role != "editor" || r.Role != "reader" {
		t.Errorf("roles %s %s %s", o.Role, e.Role, r.Role)
	}
}

func TestOpenRecordsDocumentAndRejectsForeignOnes(t *testing.T) {
	f := newFx(t)
	other := f.create(f.owner, "Beta")
	mk := func(pid, title string) string {
		out, err := f.reg.Call(f.ctx, f.owner, "doc.create", docs.CreateIn{ProjectID: pid, Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return out.(docs.Item).Item.ID
	}
	d1, d2, foreign := mk(f.pid, "One"), mk(f.pid, "Two"), mk(other, "Elsewhere")

	p := f.item(f.owner, "project.open", projects.OpenIn{ID: f.pid, DocumentID: d1})
	if p.LastDocumentID != d1 || p.LastOpenedAt == "" {
		t.Errorf("open: %+v", p)
	}
	// Opening without a document keeps the last one.
	if p = f.item(f.owner, "project.open", projects.OpenIn{ID: f.pid}); p.LastDocumentID != d1 {
		t.Errorf("last document lost: %+v", p)
	}
	if p = f.item(f.owner, "project.open", projects.OpenIn{ID: f.pid, DocumentID: d2}); p.LastDocumentID != d2 {
		t.Errorf("last document not updated: %+v", p)
	}
	// Pinning afterwards must not clobber the recents columns, and vice versa.
	if p = f.item(f.owner, "project.pin", projects.PinIn{ID: f.pid, Pinned: true}); p.LastDocumentID != d2 || p.LastOpenedAt == "" || !p.Pinned {
		t.Errorf("pin clobbered open state: %+v", p)
	}
	if p = f.item(f.owner, "project.open", projects.OpenIn{ID: f.pid}); !p.Pinned {
		t.Errorf("open clobbered the pin: %+v", p)
	}
	// A document of another project, or one that does not exist, is invalid.
	for _, id := range []string{foreign, "doc_missing"} {
		if _, err := f.reg.Call(f.ctx, f.owner, "project.open", projects.OpenIn{ID: f.pid, DocumentID: id}); code(err) != ops.CodeInvalid || field(err) != "document_id" {
			t.Errorf("document %s: %v", id, err)
		}
	}
	// A trashed document is no longer a place to resume: it is rejected, and an
	// already-stored one is not offered.
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.trash", docs.DocIn{ID: d2}); err != nil {
		t.Fatal(err)
	}
	if got := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid}); got.LastDocumentID != "" {
		t.Errorf("a trashed document is offered for resume: %q", got.LastDocumentID)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "project.open", projects.OpenIn{ID: f.pid, DocumentID: d2}); code(err) != ops.CodeInvalid {
		t.Errorf("opening a trashed document: %v", err)
	}
}

func TestListCountsWordsAndDocuments(t *testing.T) {
	f := newFx(t)
	empty := f.create(f.owner, "Empty")
	mk := func(content string) string {
		out, err := f.reg.Call(f.ctx, f.owner, "doc.create", docs.CreateIn{ProjectID: f.pid, Title: "x", Kind: "scene", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return out.(docs.Item).Item.ID
	}
	base := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid})
	mk("one two three")
	gone := mk("four five six seven")
	if p := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid}); p.WordCount != base.WordCount+7 || p.DocumentCount != base.DocumentCount+2 {
		t.Errorf("counts: %+v vs base %+v", p, base)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.trash", docs.DocIn{ID: gone}); err != nil {
		t.Fatal(err)
	}
	var fromList projects.Project
	for _, p := range f.list(f.owner, projects.ListIn{}) {
		if p.ID == f.pid {
			fromList = p
		}
		if p.ID == empty && p.WordCount != 0 {
			t.Errorf("empty project: %+v", p)
		}
	}
	if fromList.WordCount != base.WordCount+3 || fromList.DocumentCount != base.DocumentCount+1 {
		t.Errorf("trashed documents must not count: %+v vs base %+v", fromList, base)
	}
	if g := f.item(f.owner, "project.get", projects.GetIn{ID: f.pid}); g.WordCount != fromList.WordCount || g.DocumentCount != fromList.DocumentCount {
		t.Error("list and get disagree")
	}
}

func TestListCapReportsTruncation(t *testing.T) {
	f := newFx(t)
	err := f.d.WithTx(f.ctx, func(tx *sql.Tx) error {
		for i := 0; i < projects.ListCap+3; i++ {
			id := db.NewID("prj")
			now := db.Now()
			if _, err := tx.Exec(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
				id, f.owner.UserID, "Bulk", id, now, now); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, id, f.owner.UserID, "owner", now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.reg.Call(f.ctx, f.owner, "project.list", projects.ListIn{})
	if err != nil {
		t.Fatal(err)
	}
	lo := out.(projects.ListOut)
	if len(lo.Items) != projects.ListCap || !lo.Truncated {
		t.Errorf("got %d items, truncated=%v", len(lo.Items), lo.Truncated)
	}
	out, _ = f.reg.Call(f.ctx, f.reader, "project.list", projects.ListIn{})
	if lo := out.(projects.ListOut); lo.Truncated || len(lo.Items) != 1 {
		t.Errorf("a small list is not truncated: %+v", lo)
	}
}

func TestPurgeSafetyRules(t *testing.T) {
	f := newFx(t)
	purge := func(p ops.Principal, id, confirm string) error {
		_, err := f.reg.Call(f.ctx, p, "project.purge", projects.PurgeIn{ID: id, ConfirmTitle: confirm})
		return err
	}
	exists := func() bool { return f.count(`SELECT count(*) FROM projects WHERE id = ?`, f.pid) == 1 }

	// Not trashed: conflict, even with the right title.
	if err := purge(f.owner, f.pid, "Alpha"); code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "trash") {
		t.Errorf("purging a live project: %v", err)
	}
	f.item(f.owner, "project.trash", projects.StateIn{ID: f.pid})
	// Trashed but the title is wrong (or empty, or differs only by case or space).
	for _, bad := range []string{"", "alpha", "Alpha ", "Beta"} {
		if err := purge(f.owner, f.pid, bad); code(err) != ops.CodeInvalid || field(err) != "confirm_title" {
			t.Errorf("confirm %q: %v", bad, err)
		}
	}
	if !exists() {
		t.Fatal("a refused purge deleted the project")
	}
	// Not an owner, not purged: a trashed project is invisible to its other members.
	if err := purge(f.editor, f.pid, "Alpha"); code(err) != ops.CodeNotFound { // a trashed project is the owner's alone
		t.Errorf("editor purge: %v", err)
	}
	if err := purge(f.outsider, f.pid, "Alpha"); code(err) != ops.CodeNotFound {
		t.Errorf("outsider purge: %v", err)
	}
	// Restoring puts it back out of reach of purge.
	f.item(f.owner, "project.restore", projects.StateIn{ID: f.pid})
	if err := purge(f.owner, f.pid, "Alpha"); code(err) != ops.CodeConflict {
		t.Errorf("purge after restore: %v", err)
	}
	if !exists() {
		t.Fatal("the project vanished")
	}
	// The title is whatever it is now, not what it was.
	f.item(f.owner, "project.update", projects.UpdateIn{ID: f.pid, Title: s("Renamed")})
	f.item(f.owner, "project.trash", projects.StateIn{ID: f.pid})
	if err := purge(f.owner, f.pid, "Alpha"); code(err) != ops.CodeInvalid {
		t.Errorf("old title: %v", err)
	}
	if err := purge(f.owner, f.pid, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if exists() {
		t.Error("purge did not delete the project")
	}
	// Gone is gone, for everyone, and purge is not repeatable.
	if err := purge(f.owner, f.pid, "Renamed"); code(err) != ops.CodeNotFound {
		t.Errorf("purge twice: %v", err)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "project.get", projects.GetIn{ID: f.pid}); code(err) != ops.CodeNotFound {
		t.Errorf("get after purge: %v", err)
	}
}

func TestPurgeCascadeAuditAndFiles(t *testing.T) {
	f := newFx(t)
	const word = "zanzibarquokka"
	keep := f.create(f.owner, "Keeper")
	mk := func(pid, content string) string {
		out, err := f.reg.Call(f.ctx, f.owner, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Chapter", Kind: "scene", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return out.(docs.Item).Item.ID
	}
	doc := mk(f.pid, "The "+word+" crossed the harbour.")
	mk(keep, "A different story entirely.")
	f.item(f.owner, "project.pin", projects.PinIn{ID: f.pid, Pinned: true})
	f.item(f.editor, "project.open", projects.OpenIn{ID: f.pid, DocumentID: doc})
	if _, err := f.d.Writer().Exec(`INSERT INTO exports(id, project_id, user_id, format, filename, created_at) VALUES ('exp_1', ?, ?, 'md', 'a.md', ?)`,
		f.pid, f.owner.UserID, db.Now()); err != nil {
		t.Fatal(err)
	}
	conflicts := filepath.Join(f.dir, "conflicts", f.pid)
	if err := os.MkdirAll(conflicts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conflicts, "x.md"), []byte(word), 0o600); err != nil {
		t.Fatal(err)
	}
	keepConflicts := filepath.Join(f.dir, "conflicts", keep)
	if err := os.MkdirAll(keepConflicts, 0o700); err != nil {
		t.Fatal(err)
	}

	if f.count(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH ?`, word) != 1 {
		t.Fatal("the fixture word is not indexed")
	}
	f.item(f.owner, "project.trash", projects.StateIn{ID: f.pid})
	if _, err := f.reg.Call(f.ctx, f.owner, "project.purge", projects.PurgeIn{ID: f.pid, ConfirmTitle: "Alpha"}); err != nil {
		t.Fatal(err)
	}

	for table, q := range map[string]string{
		"projects":           `SELECT count(*) FROM projects WHERE id = ?1`,
		"project_members":    `SELECT count(*) FROM project_members WHERE project_id = ?1`,
		"documents":          `SELECT count(*) FROM documents WHERE project_id = ?1`,
		"doc_text":           `SELECT count(*) FROM doc_text WHERE project_id = ?1`,
		"user_project_state": `SELECT count(*) FROM user_project_state WHERE project_id = ?1`,
		"exports":            `SELECT count(*) FROM exports WHERE project_id = ?1`,
	} {
		if n := f.count(q, f.pid); n != 0 {
			t.Errorf("%s: %d rows survive the purge", table, n)
		}
	}
	if n := f.count(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH ?`, word); n != 0 {
		t.Errorf("the purged text is still searchable (%d FTS rows)", n)
	}
	if n := f.count(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'different'`); n != 1 {
		t.Errorf("another project's text was lost: %d", n)
	}
	if n := f.count(`SELECT count(*) FROM projects WHERE id = ?`, keep); n != 1 {
		t.Error("another project was deleted")
	}
	if _, err := os.Stat(conflicts); !os.IsNotExist(err) {
		t.Errorf("conflict copies of the purged project remain: %v", err)
	}
	if _, err := os.Stat(keepConflicts); err != nil {
		t.Errorf("another project's conflict copies were removed: %v", err)
	}
	// Users are untouched.
	if n := f.count(`SELECT count(*) FROM users WHERE id IN (?, ?)`, f.owner.UserID, f.editor.UserID); n != 2 {
		t.Errorf("users lost: %d", n)
	}

	// The audit row has the id and a length, never the title; no event carries content.
	var detail, actor string
	if err := f.d.Read().QueryRow(`SELECT detail_json, actor_id FROM audit_log WHERE action = 'project.purge' AND target_id = ?`, f.pid).Scan(&detail, &actor); err != nil {
		t.Fatal(err)
	}
	if detail != `{"title_length":5}` || actor != f.owner.UserID {
		t.Errorf("audit detail %s actor %s", detail, actor)
	}
	var dump string
	if err := f.d.Read().QueryRow(`SELECT group_concat(detail_json || target_id || action, ' ') FROM audit_log`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, "Alpha") || strings.Contains(dump, word) {
		t.Error("the audit log contains the project title or text")
	}
	var payload string
	if err := f.d.Read().QueryRow(`SELECT payload_json FROM events WHERE type = 'project.purged' AND project_id = ?`, f.pid).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != "{}" {
		t.Errorf("purge event payload %q", payload)
	}
}

func TestEventsAndAuditForUpdateAndStateChanges(t *testing.T) {
	f := newFx(t)
	f.item(f.owner, "project.update", projects.UpdateIn{ID: f.pid, Title: s("Secret Title"), Status: s("drafting"), WordGoal: n(5)})
	var payload, actor string
	if err := f.d.Read().QueryRow(`SELECT payload_json, actor_id FROM events WHERE type = 'project.updated' AND project_id = ?`, f.pid).Scan(&payload, &actor); err != nil {
		t.Fatal(err)
	}
	if actor != f.owner.UserID || !strings.Contains(payload, `"title"`) || !strings.Contains(payload, `"status"`) || !strings.Contains(payload, `"word_goal"`) {
		t.Errorf("payload %s actor %s", payload, actor)
	}
	if strings.Contains(payload, "Secret") || strings.Contains(payload, "drafting") {
		t.Errorf("an event carries a value: %s", payload)
	}
	// Only the fields that changed are named.
	f.item(f.owner, "project.update", projects.UpdateIn{ID: f.pid, Title: s("Secret Title"), Genre: s("Crime")})
	var last string
	if err := f.d.Read().QueryRow(`SELECT payload_json FROM events WHERE type = 'project.updated' ORDER BY seq DESC LIMIT 1`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != `{"fields":["genre"]}` {
		t.Errorf("last payload %s", last)
	}
}

func TestSharedProjectKeepsOwnerRoleAndDocsStayPrivate(t *testing.T) {
	f := newFx(t)
	// A reader's list shows the project with their role; the outsider sees nothing.
	got := f.list(f.reader, projects.ListIn{})
	if len(got) != 1 || got[0].Role != "reader" || got[0].ID != f.pid {
		t.Errorf("reader list: %+v", got)
	}
	if got := f.list(f.outsider, projects.ListIn{State: "all"}); len(got) != 0 {
		t.Errorf("outsider list: %+v", got)
	}
}
