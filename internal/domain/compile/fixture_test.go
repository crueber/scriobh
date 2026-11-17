package compile_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
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
	reader   ops.Principal
	outsider ops.Principal
	pid      string
}

func newFx(t *testing.T, title string) *fx {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	f := &fx{t: t, d: d, dir: dir, ctx: context.Background(), reg: registry.Build(d, events.NewStore(d, nil), dir)}
	f.owner = ops.Principal{UserID: testutil.User(t, d, "writer", "o@t.local"), Role: ops.RoleWriter}
	f.reader = ops.Principal{UserID: testutil.User(t, d, "reader", "r@t.local"), Role: ops.RoleReader}
	f.outsider = ops.Principal{UserID: testutil.User(t, d, "writer", "x@t.local"), Role: ops.RoleWriter}
	out, err := f.reg.Call(f.ctx, f.owner, "project.create", projects.CreateIn{Title: title, Subtitle: "A sub title", AuthorName: "Máire Ní Bhriain", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	f.pid = out.(projects.Item).Item.ID
	if _, err := d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, f.pid, f.reader.UserID, "reader", db.Now()); err != nil {
		t.Fatal(err)
	}
	// The seeded first page is not part of these trees.
	lo, err := f.reg.Call(f.ctx, f.owner, "doc.list", docs.ListIn{ProjectID: f.pid})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range lo.(docs.ListOut).Items {
		if _, err := f.reg.Call(f.ctx, f.owner, "doc.trash", docs.DocIn{ID: m.ID}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fx) doc(title, kind, parent, content string) docs.Doc {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, f.owner, "doc.create", docs.CreateIn{ProjectID: f.pid, Title: title, Kind: kind, ParentID: parent, Content: content})
	if err != nil {
		f.t.Fatalf("create %q: %v", title, err)
	}
	return out.(docs.Item).Item
}

func (f *fx) exclude(d docs.Doc) {
	f.t.Helper()
	no := false
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, IncludeInCompile: &no}); err != nil {
		f.t.Fatal(err)
	}
}

const altsComment = "<!-- scriobh:alts {\"v\":1,\"alts\":[{\"at\":3,\"text\":\"other wording\"}]} -->"

// novel builds a manuscript with everything a compile must decide about.
func (f *fx) novel() map[string]docs.Doc {
	d := map[string]docs.Doc{}
	d["front"] = f.doc("Dedication", "front_matter", "", "For the ones who walked, dedmark.")
	d["p1"] = f.doc("Part One", "part", "", "Epigraph of part one, epimark.")
	d["c1"] = f.doc("The Arrival", "chapter", d["p1"].ID, "Chapter one opens here, ch1mark. Some <scriobh-ghost>ghosted words, ghostmark,</scriobh-ghost> stay or go. A %%private aside, privmark%% remark.")
	d["s1"] = f.doc("Harbour", "scene", d["c1"].ID, "Scene one on the quay, s1mark.\n\n"+altsComment+"\n")
	d["s2"] = f.doc("", "scene", d["c1"].ID, "An untitled scene, s2mark.")
	d["n1"] = f.doc("Research", "note", d["c1"].ID, "A private research note, n1mark.")
	d["c2"] = f.doc("", "chapter", d["p1"].ID, "Chapter two has no title, ch2mark.\n\n* * *\n")
	d["p2"] = f.doc("Part Two", "part", "", "")
	d["c3"] = f.doc("The End", "chapter", d["p2"].ID, "Last words with a footnote.[^1] ch3mark.\n\n[^1]: The note text, notemark.\n")
	d["n2"] = f.doc("Plot Notes", "note", "", "Top level note, n2mark.")
	d["skip"] = f.doc("Cut Chapter", "chapter", "", "Excluded text, skipmark.")
	d["skipkid"] = f.doc("Cut Scene", "scene", d["skip"].ID, "Inside the excluded chapter, kidmark.")
	d["gone"] = f.doc("Trashed", "chapter", "", "Trashed text, gonemark.")
	d["back"] = f.doc("Afterword", "back_matter", "", "Thanks, backmark.")
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.trash", docs.DocIn{ID: d["gone"].ID}); err != nil {
		f.t.Fatal(err)
	}
	f.exclude(d["skip"])
	return d
}

func (f *fx) setContact() {
	f.t.Helper()
	n, s, a, e, p := "Máire Ní Bhriain", "Ní Bhriain", "12 Quay Street\nGalway", "maire@example.com", "+353 1 555 0100"
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.contact.set", compile.ContactSetIn{ProjectID: f.pid, Name: &n, Surname: &s, Address: &a, Email: &e, Phone: &p}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) compile(preset string, over publish.Options) compile.Out {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: preset, Options: over})
	if err != nil {
		f.t.Fatalf("export.compile %s: %v", preset, err)
	}
	return out.(compile.Out)
}

func (f *fx) preview(preset string, over publish.Options) compile.PreviewOut {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, f.owner, "compile.preview", compile.PreviewIn{ProjectID: f.pid, Preset: preset, Options: over})
	if err != nil {
		f.t.Fatalf("compile.preview %s: %v", preset, err)
	}
	return out.(compile.PreviewOut)
}

func unzip(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}
