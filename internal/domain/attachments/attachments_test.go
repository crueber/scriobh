package attachments_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type env struct {
	t     *testing.T
	d     *db.DB
	reg   *ops.Registry
	dir   string
	owner ops.Principal
	pid   string
	cid   string
}

func newEnv(t *testing.T, tune func(*registry.Options)) *env {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	o := registry.Options{Attachments: attachments.DefaultLimits}
	if tune != nil {
		tune(&o)
	}
	e := &env{t: t, d: d, dir: dir, reg: registry.BuildWith(d, events.NewStore(d, nil), dir, o)}
	e.owner = ops.Principal{UserID: testutil.User(t, d, ops.RoleWriter, "o@t.local"), Role: ops.RoleWriter}
	e.pid = e.call("project.create", projects.CreateIn{Title: "Pictures"}).(projects.Item).Item.ID
	e.cid = e.call("compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	return e
}

func (e *env) call(op string, in any) any {
	e.t.Helper()
	out, err := e.reg.Call(context.Background(), e.owner, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (e *env) fails(op string, in any, code ops.Code) *ops.Error {
	e.t.Helper()
	_, err := e.reg.Call(context.Background(), e.owner, op, in)
	if err == nil || ops.AsError(err).Code != code {
		e.t.Fatalf("%s: %v, want %s", op, err, code)
	}
	return ops.AsError(err)
}

func pngFile(seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8; i++ {
		img.Set(i, i, color.RGBA{uint8(seed), 3, 4, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (e *env) upload(op, owner string, seed int) attachments.Item {
	e.t.Helper()
	b := pngFile(seed)
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(b), Length: int64(len(b))})
	out, err := e.reg.Call(ctx, e.owner, op, attachments.UploadIn{ID: owner, Filename: "p.png"})
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out.(attachments.Item)
}

func (e *env) usage(scope string) (bytes_, files int64) {
	_ = e.d.Read().QueryRow(`SELECT bytes, files FROM file_usage WHERE scope = ?`, scope).Scan(&bytes_, &files)
	return
}

// The counters the quotas read are the sums of what is stored, through uploads,
// repeats and deletes.
func TestUsageCountersAreTheSumsOfWhatIsStored(t *testing.T) {
	e := newEnv(t, nil)
	a := e.upload("project.attachment.upload", e.pid, 1)
	b := e.upload("project.attachment.upload", e.pid, 2)
	if again := e.upload("project.attachment.upload", e.pid, 1); !again.Duplicate || again.Item.ID != a.Item.ID {
		t.Fatalf("a repeat is the same attachment: %+v", again)
	}
	c := e.upload("compendium.attachment.upload", e.cid, 1) // the same bytes in another owner: its own file
	if c.Duplicate || c.Item.ID == a.Item.ID {
		t.Fatalf("another owner: %+v", c)
	}
	sum := a.Item.Bytes + b.Item.Bytes + c.Item.Bytes
	check := func(when string, wantInstance int64, wantFiles int64) {
		t.Helper()
		var real, files int64
		_ = e.d.Read().QueryRow(`SELECT COALESCE(sum(bytes),0), count(*) FROM attachments`).Scan(&real, &files)
		got, gf := e.usage("instance")
		if got != real || got != wantInstance || gf != files || gf != wantFiles {
			t.Errorf("%s: counter %d bytes %d files, stored %d bytes %d files, want %d/%d", when, got, gf, real, files, wantInstance, wantFiles)
		}
		po, _ := e.usage("owner:" + e.pid)
		var pr int64
		_ = e.d.Read().QueryRow(`SELECT COALESCE(sum(bytes),0) FROM attachments WHERE project_id = ?`, e.pid).Scan(&pr)
		if po != pr {
			t.Errorf("%s: project counter %d, stored %d", when, po, pr)
		}
		uo, _ := e.usage("user:" + e.owner.UserID)
		if uo != real {
			t.Errorf("%s: user counter %d, stored %d", when, uo, real)
		}
	}
	check("after uploads", sum, 3)
	e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: b.Item.ID})
	check("after a delete", sum-b.Item.Bytes, 2)
	// A purge's cascade is counted too.
	e.call("project.trash", projects.StateIn{ID: e.pid})
	e.call("project.purge", projects.PurgeIn{ID: e.pid, ConfirmTitle: "Pictures"})
	check("after a purge", c.Item.Bytes, 1)
}

// A delete is refused while anything links to the attachment, however the link
// got there: a recorded reference, text written without one (a bulk import, a
// body saved before the attachment existed), or an entity's body.
func TestDeleteSeesLinksThatWereNeverRecorded(t *testing.T) {
	e := newEnv(t, nil)
	a := e.upload("project.attachment.upload", e.pid, 3)
	doc := e.call("doc.create", docs.CreateIn{ProjectID: e.pid, Title: "Chapter", Content: "![x](" + a.Item.URL + ")"}).(docs.Item).Item
	var recorded int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM attachment_refs WHERE attachment_id = ?`, a.Item.ID).Scan(&recorded)
	if recorded != 1 {
		t.Fatalf("the save recorded %d references", recorded)
	}
	if err := e.fails("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: a.Item.ID}, ops.CodeConflict); err.Data["refs"] != 1 {
		t.Errorf("refs: %v", err.Data)
	}
	// Lose the record, as an import or a restore of old data would: the text still links.
	if _, err := e.d.Writer().Exec(`DELETE FROM attachment_refs`); err != nil {
		t.Fatal(err)
	}
	e.fails("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: a.Item.ID}, ops.CodeConflict)
	uses := e.call("project.attachment.refs", attachments.AttIn{ID: e.pid, AttID: a.Item.ID}).(attachments.RefsOut)
	if len(uses.Items) != 1 || uses.Items[0].ID != doc.ID || uses.Items[0].Kind != "doc" {
		t.Errorf("uses: %+v", uses.Items)
	}
	// Text written straight to the table, as a bulk writer does, for a second attachment.
	b := e.upload("project.attachment.upload", e.pid, 4)
	if _, err := e.d.Writer().Exec(`UPDATE documents SET content = content || ? WHERE id = ?`, "\n![y]("+b.Item.URL+")", doc.ID); err != nil {
		t.Fatal(err)
	}
	e.fails("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: b.Item.ID}, ops.CodeConflict)
	// An unlinked one still goes, and so does a linked one when forced.
	c := e.upload("project.attachment.upload", e.pid, 5)
	e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: c.Item.ID})
	e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: b.Item.ID, Force: true})

	// A Compendium's entity body counts.
	ca := e.upload("compendium.attachment.upload", e.cid, 6)
	ent := e.call("entity.create", entities.CreateIn{ID: e.cid, Kind: "character", Name: "Ada"}).(entities.Item).Item
	body := "Portrait: ![p](" + ca.Item.URL + ")"
	e.call("entity.update", entities.UpdateIn{ID: ent.ID, Body: &body})
	err := e.fails("compendium.attachment.delete", attachments.DeleteIn{ID: e.cid, AttID: ca.Item.ID}, ops.CodeConflict)
	if err.Data["refs"] != 1 {
		t.Errorf("entity link: %v", err.Data)
	}
	if uses := e.call("compendium.attachment.refs", attachments.AttIn{ID: e.cid, AttID: ca.Item.ID}).(attachments.RefsOut); len(uses.Items) != 1 || uses.Items[0].Kind != "entity" {
		t.Errorf("entity uses: %+v", uses.Items)
	}
	// An id that only looks like another's does not match it (the underscore is a LIKE wildcard).
	other := e.upload("project.attachment.upload", e.pid, 7)
	if _, err := e.d.Writer().Exec(`UPDATE documents SET content = ? WHERE id = ?`, "/files/att_"+other.Item.ID[4:5]+"XXXXXXXXXXXXXXXXXXXXXXXXX", doc.ID); err != nil {
		t.Fatal(err)
	}
	e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: other.Item.ID})
}

// A stored file reached through a link is not opened.
func TestOpenRegularRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	files := filepath.Join(dir, "files", "projects", "prj_x", "attachments")
	if err := os.MkdirAll(files, 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(files, "real.png")
	_ = os.WriteFile(real, []byte("bytes"), 0o600)
	outside := filepath.Join(t.TempDir(), "outside")
	_ = os.WriteFile(outside, []byte("secret"), 0o600)
	link := filepath.Join(files, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if f, _, err := attachments.OpenRegular(dir, real); err != nil {
		t.Errorf("a regular file: %v", err)
	} else {
		f.Close()
	}
	if f, _, err := attachments.OpenRegular(dir, link); err == nil {
		f.Close()
		t.Error("a symlink was opened")
	}
	if _, _, err := attachments.OpenRegular(dir, outside); err == nil {
		t.Error("a path outside data/files was opened")
	}
	if _, _, err := attachments.OpenRegular(dir, filepath.Join(files, "..", "..", "..", "..", "..", "outside")); err == nil {
		t.Error("a path that climbs out was opened")
	}
}
