package review_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// h is the harness: a migrated database, the full registry, and a project with
// an owner, an editor, a reviewer, a second reviewer, a reader and a stranger.
type h struct {
	t         *testing.T
	d         *db.DB
	reg       *ops.Registry
	ctx       context.Context
	owner     ops.Principal
	editor    ops.Principal
	reviewer  ops.Principal
	reviewer2 ops.Principal
	reader    ops.Principal
	stranger  ops.Principal
	admin     ops.Principal
	project   string
}

func newH(t *testing.T) *h {
	d := testutil.DB(t)
	reg := registry.Build(d, events.NewStore(d, nil), t.TempDir())
	person := func(role, name string) ops.Principal {
		id := testutil.User(t, d, role, name+"@t.local")
		if _, err := d.Writer().Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, id); err != nil {
			t.Fatal(err)
		}
		return ops.Principal{UserID: id, Role: role, Email: name + "@t.local", Name: name}
	}
	x := &h{t: t, d: d, reg: reg, ctx: context.Background(),
		owner: person(ops.RoleWriter, "Olive"), editor: person(ops.RoleWriter, "Edith"), reviewer: person(ops.RoleReader, "Rita"),
		reviewer2: person(ops.RoleReader, "Ravi"), reader: person(ops.RoleReader, "Reed"), stranger: person(ops.RoleWriter, "Stan"), admin: person(ops.RoleAdmin, "Ada")}
	x.project = x.call(x.owner, "project.create", projects.CreateIn{Title: "Review Book"}).(projects.Item).Item.ID
	x.member(x.editor, ops.ProjectEditor)
	x.member(x.reviewer, ops.ProjectReviewer)
	x.member(x.reviewer2, ops.ProjectReviewer)
	x.member(x.reader, ops.ProjectReader)
	return x
}

func (x *h) member(p ops.Principal, role string) {
	x.t.Helper()
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, x.project, p.UserID, role, db.Now())
		return err
	})
	if err != nil {
		x.t.Fatal(err)
	}
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

// doc makes a chapter with the text and returns its id.
func (x *h) doc(title, content string) string {
	x.t.Helper()
	return x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: x.project, Title: title, Kind: "chapter", Content: content}).(docs.Item).Item.ID
}

func (x *h) get(id string) docs.Doc {
	x.t.Helper()
	return x.call(x.owner, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
}

// set replaces a document's text as the owner would save it.
func (x *h) set(id, content string) docs.Doc {
	x.t.Helper()
	d := x.get(id)
	return x.call(x.owner, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Content: &content}).(docs.Item).Item
}

// comment writes a comment anchored to quote in doc as p.
func (x *h) comment(p ops.Principal, doc, quote, body string) review.CommentOut {
	x.t.Helper()
	return x.call(p, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: quote, Body: body}).(review.ThreadItem).Item
}

func (x *h) thread(p ops.Principal, id string) review.CommentOut {
	x.t.Helper()
	return x.call(p, "comment.get", review.GetIn{ProjectID: x.project, CommentID: id}).(review.ThreadItem).Item
}

func (x *h) count(query string, args ...any) int {
	x.t.Helper()
	var n int
	if err := x.d.Read().QueryRow(query, args...).Scan(&n); err != nil {
		x.t.Fatal(err)
	}
	return n
}

func words(n int) string { return strings.TrimSpace(strings.Repeat("word ", n)) }
