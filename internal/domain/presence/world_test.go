package presence_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/presence"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// person is one test identity with a real session row (the service asks the database whether a session still stands).
type person struct {
	name string
	p    ops.Principal
	role string // the role on the project; "" for a non-member
}

type world struct {
	t   testing.TB
	d   *db.DB
	reg *ops.Registry
	svc *presence.Service

	owner, editor, reviewer, reader, admin, stranger *person
	pid                                              string
	docA, docB, docTrashed, docForeign               string
}

// Titles that must reach only the people who may be told them. A canary anywhere else is a leak.
const (
	titleA       = "Canary Chapter Alpha"
	titleB       = "Canary Chapter Beta"
	titleTrashed = "Canary Chapter Trashed"
	titleForeign = "Canary Chapter Foreign"
)

func (w *world) session(userID string) string {
	id := db.NewID("ses")
	now := db.Now()
	if _, err := w.d.Writer().Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES (?,?,?,?,?,?)`,
		id, userID, "hash-"+id, now, now, db.Format(time.Now().Add(24*time.Hour))); err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) newPerson(name, instanceRole, projectRole string) *person {
	uid := testutil.User(w.t, w.d, instanceRole, name+"@t.local")
	if _, err := w.d.Writer().Exec(`UPDATE users SET display_name = ? WHERE id = ?`, strings.ToUpper(name[:1])+name[1:], uid); err != nil {
		w.t.Fatal(err)
	}
	pe := &person{name: name, role: projectRole, p: ops.Principal{UserID: uid, Role: instanceRole, Via: "session", SessionID: w.session(uid)}}
	return pe
}

func (w *world) share(p *person, on string) {
	if _, err := w.d.Writer().Exec(`INSERT INTO user_prefs(user_id, key, value) VALUES (?,?,?) ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, p.p.UserID, presence.PrefKey, on); err != nil {
		w.t.Fatal(err)
	}
}

func newWorld(t testing.TB) *world {
	d := testutil.DB(t)
	reg := registry.Build(d, nil, t.TempDir())
	svc, _ := ops.Service[*presence.Service](reg.Env(), presence.ServiceName)
	w := &world{t: t, d: d, reg: reg, svc: svc}
	w.owner = w.newPerson("olive", ops.RoleWriter, ops.ProjectOwner)
	w.editor = w.newPerson("edgar", ops.RoleWriter, ops.ProjectEditor)
	w.reviewer = w.newPerson("rhea", ops.RoleReader, ops.ProjectReviewer)
	w.reader = w.newPerson("rory", ops.RoleReader, ops.ProjectReader)
	w.admin = w.newPerson("adam", ops.RoleAdmin, "")
	w.stranger = w.newPerson("sam", ops.RoleWriter, "")
	cli := func(p *person) ops.Principal { q := p.p; q.Via = "cli"; q.SessionID = ""; return q }
	out, err := reg.Call(context.Background(), cli(w.owner), "project.create", projects.CreateIn{Title: "Presence Book"})
	if err != nil {
		t.Fatal(err)
	}
	w.pid = out.(projects.Item).Item.ID
	for _, m := range []*person{w.editor, w.reviewer, w.reader} {
		if err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, w.pid, m.p.UserID, m.role, db.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(pid, title string) string {
		o, err := reg.Call(context.Background(), cli(w.owner), "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: "chapter"})
		if err != nil {
			t.Fatal(err)
		}
		return o.(docs.Item).Item.ID
	}
	w.docA, w.docB, w.docTrashed = mk(w.pid, titleA), mk(w.pid, titleB), mk(w.pid, titleTrashed)
	if _, err := reg.Call(context.Background(), cli(w.owner), "doc.trash", docs.DocIn{ID: w.docTrashed}); err != nil {
		t.Fatal(err)
	}
	other, err := reg.Call(context.Background(), cli(w.owner), "project.create", projects.CreateIn{Title: "Another Book"})
	if err != nil {
		t.Fatal(err)
	}
	w.docForeign = mk(other.(projects.Item).Item.ID, titleForeign)
	return w
}

func (w *world) beat(p *person, doc string) (presence.View, error) {
	out, err := w.reg.Call(context.Background(), p.p, "presence.beat", presence.BeatIn{ID: w.pid, DocumentID: doc})
	if err != nil {
		return presence.View{}, err
	}
	return out.(presence.View), nil
}

func (w *world) list(p *person) (presence.View, error) {
	out, err := w.reg.Call(context.Background(), p.p, "presence.list", presence.ListIn{ID: w.pid})
	if err != nil {
		return presence.View{}, err
	}
	return out.(presence.View), nil
}

func (w *world) must(v presence.View, err error) presence.View {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

func raw(v presence.View) string { b, _ := json.Marshal(v); return string(b) }

func names(v presence.View) []string {
	var out []string
	for _, it := range v.Items {
		out = append(out, it.Name)
	}
	return out
}

func docOf(v presence.View, name string) *presence.Doc {
	for _, it := range v.Items {
		if it.Name == name {
			return it.Document
		}
	}
	return nil
}
