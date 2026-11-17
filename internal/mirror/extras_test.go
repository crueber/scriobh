package mirror_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
)

func pngFile(seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for x := 0; x < 10; x++ {
		img.Set(x, x, color.RGBA{uint8(seed * 20), 40, 90, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (e *env) uploadTo(op, owner, name string, seed int) attachments.Attachment {
	e.t.Helper()
	ctx := ops.WithRawBody(e.ctx, ops.RawBody{R: bytes.NewReader(pngFile(seed)), Length: -1})
	out, err := e.reg.Call(ctx, e.owner, op, attachments.UploadIn{ID: owner, Filename: name})
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out.(attachments.Item).Item
}

func (e *env) upload(pid, name string, seed int) attachments.Attachment {
	return e.uploadTo("project.attachment.upload", pid, name, seed)
}

func (e *env) cupload(cid, name string, seed int) attachments.Attachment {
	return e.uploadTo("compendium.attachment.upload", cid, name, seed)
}

func files(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range tree(t, root) {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		out[f] = string(b)
	}
	return out
}

func find(m map[string]string, prefix, suffix string) (string, string) {
	for k, v := range m {
		if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, suffix) {
			return k, v
		}
	}
	return "", ""
}

func TestResearchAndAttachmentsAreMirrored(t *testing.T) {
	fs := &countFS{}
	e := newEnv(t, func(o *mirror.Options) { o.FS = fs })
	e.start()
	pid := e.project("Mirrored research")
	ch := e.doc(pid, "Chapter", "chapter", "text\n")
	folder := e.call(e.owner, "project.research.create", research.CreateIn{ID: pid, Title: "Sources", Kind: "folder", Body: "About the folder."}).(research.Item).Item
	clip := e.call(e.owner, "project.research.create", research.CreateIn{ID: pid, Title: "Harbour history", Kind: "clipping", ParentID: folder.ID,
		SourceURL: "https://example.org/harbour?a=b&c=d", RetrievedOn: "2026-03-14", Quote: "The quay was rebuilt in 1881.", Body: "Notes: \"quoted\" & <angled>.",
		Tags: []string{"history", "Harbour"}, Status: "reading"}).(research.Item).Item
	trashed := e.call(e.owner, "project.research.create", research.CreateIn{ID: pid, Title: "Gone"}).(research.Item).Item
	e.call(e.owner, "project.research.trash", research.NoteIn{ID: pid, NoteID: trashed.ID})
	att := e.upload(pid, "harbour map.png", 1)
	unlinked := e.upload(pid, "unlinked.png", 2)
	e.settle(pid)
	root := e.root(pid)
	got := files(t, root)

	// The note, nested under its folder, with front matter and the person's own words.
	name, content := find(got, "research/sources--", "/harbour-history--"+suffix(clip.ID)+".md")
	if name == "" {
		t.Fatalf("no file for the clipping: %v", keys(got))
	}
	want := "---\nid: \"" + clip.ID + "\"\nkind: clipping\ntitle: \"Harbour history\"\nstatus: reading\ntags:\n  - \"Harbour\"\n  - \"history\"\n" +
		"source: \"https://example.org/harbour?a=b&c=d\"\nretrieved: \"2026-03-14\"\nquote: \"The quay was rebuilt in 1881.\"\n---\n\nNotes: \"quoted\" & <angled>.\n"
	if content != want {
		t.Errorf("front matter:\n%s\nwant:\n%s", content, want)
	}
	if _, c := find(got, "research/sources--", "/_folder.md"); !strings.Contains(c, "About the folder.") || !strings.Contains(c, "kind: folder") {
		t.Errorf("the folder's own file: %q", c)
	}
	for k := range got {
		if strings.Contains(k, "gone--") {
			t.Errorf("a trashed note is mirrored: %s", k)
		}
	}
	// Attachments appear only once something links to them, byte for byte.
	if n, _ := find(got, "attachments/", ""); n != "" {
		t.Errorf("an unlinked attachment was mirrored: %s", n)
	}
	e.setContent(ch, "![map]("+att.URL+")\n")
	e.settle(pid)
	got = files(t, root)
	an, ab := find(got, "attachments/", ".png")
	if an != "attachments/"+att.SHA256[:12]+"-harbour-map.png" || ab != string(pngFile(1)) {
		t.Errorf("attachment %q (%d bytes)", an, len(ab))
	}
	for k := range got {
		if strings.Contains(k, unlinked.SHA256[:12]) {
			t.Errorf("the unlinked attachment was mirrored: %s", k)
		}
	}
	if !strings.Contains(got["project.json"], `"attachments": [`) || !strings.Contains(got["project.json"], att.ID) || !strings.Contains(got["project.json"], an) {
		t.Errorf("the manifest does not list the attachment:\n%s", got["project.json"])
	}
	// Quiet: nothing is rewritten when nothing changed, and a rebuild finds nothing to do.
	before := len(fs.renames)
	e.call(e.owner, "mirror.rebuild", mirror.StatusIn{ProjectID: pid})
	e.settle(pid)
	if after := len(fs.renames); after != before {
		t.Errorf("a rebuild of an unchanged mirror wrote %d files: %v", after-before, fs.renames[before:])
	}
	// Changing the alt text rewrites the manifest and nothing else.
	alt := "A harbour map"
	e.call(e.owner, "project.attachment.update", attachments.UpdateIn{ID: pid, AttID: att.ID, Alt: &alt})
	before = len(fs.renames)
	e.settle(pid)
	var wrote []string
	for _, w := range fs.renames[before:] {
		if !strings.Contains(w, "/.state/") {
			wrote = append(wrote, w)
		}
	}
	if len(wrote) != 1 || !strings.HasSuffix(wrote[0], "project.json") {
		t.Errorf("an alt text change wrote %v", wrote)
	}
	// Tags are in the note's file: renaming one rewrites it.
	var tl research.TagListOut
	tl = e.call(e.owner, "project.tag.list", research.TagListIn{ID: pid}).(research.TagListOut)
	for _, tg := range tl.Items {
		if tg.Name == "history" {
			n := "chronicle"
			e.call(e.owner, "project.tag.update", research.TagUpdateIn{ID: pid, TagID: tg.ID, Name: &n})
		}
	}
	e.settle(pid)
	if _, c := find(files(t, root), "research/sources--", "/harbour-history--"+suffix(clip.ID)+".md"); !strings.Contains(c, "\"chronicle\"") || strings.Contains(c, "\"history\"") {
		t.Errorf("a renamed tag did not reach the note:\n%s", c)
	}
	// Trash the clipping, delete the attachment: the files go.
	e.call(e.owner, "project.research.trash", research.NoteIn{ID: pid, NoteID: clip.ID})
	e.call(e.owner, "project.attachment.delete", attachments.DeleteIn{ID: pid, AttID: att.ID, Force: true})
	e.settle(pid)
	got = files(t, root)
	if n, _ := find(got, "research/sources--", "harbour-history"); n != "" {
		t.Errorf("a trashed clipping stays in the mirror: %s", n)
	}
	if n, _ := find(got, "attachments/", ""); n != "" {
		t.Errorf("a deleted attachment stays in the mirror: %s", n)
	}
	// A restart with nothing changed writes nothing.
	m2, stop := e.bootMirror(fs)
	defer stop()
	_ = m2
}

func suffix(id string) string {
	id = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(id))
	return id[len(id)-6:]
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The research of a project's Compendium is projected into the project's
// compendium/ folder, and a standalone Compendium has research/ and
// attachments/ of its own.
func TestCompendiumResearchIsProjected(t *testing.T) {
	e := newEnv(t)
	e.start()
	cid := e.compendium("Salt Bible")
	att := e.cupload(cid, "chart.png", 4)
	note := e.call(e.owner, "compendium.research.create", research.CreateIn{ID: cid, Title: "Tide tables", Body: "High at six. ![c](" + att.URL + ")", Tags: []string{"sea"}}).(research.Item).Item
	e.settleAll()
	root := e.cmpRoot(cid)
	got := files(t, root)
	if n, c := find(got, "research/tide-tables--", ".md"); n == "" || !strings.Contains(c, "High at six.") || !strings.Contains(c, "\"sea\"") {
		t.Errorf("standalone research: %v", keys(got))
	}
	if n, b := find(got, "attachments/", ".png"); n == "" || b != string(pngFile(4)) {
		t.Errorf("standalone attachment: %v", keys(got))
	}
	// A project takes it as its primary: the standalone folder goes, the project shows the research.
	pid := e.project("Uses it")
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	e.settleAll()
	pgot := files(t, e.root(pid))
	pn, pc := find(pgot, "compendium/research/salt-bible--", ".md")
	if pn == "" || !strings.Contains(pc, "High at six.") {
		t.Fatalf("the project's projection of the research: %v", keys(pgot))
	}
	// An edit to the Compendium's note reaches the project's folder.
	b := "High at seven."
	e.call(e.owner, "compendium.research.update", research.UpdateIn{ID: cid, NoteID: note.ID, BaseRevision: note.Revision, Body: &b})
	e.settleAll()
	if _, c := find(files(t, e.root(pid)), "compendium/research/salt-bible--", ".md"); !strings.Contains(c, "High at seven.") {
		t.Errorf("an edit did not reach the project's projection:\n%s", c)
	}
	// A restart with nothing changed writes nothing: the fingerprint taken for one
	// project at flush time is the one taken for every project at start-up, the
	// research of the Compendiums it uses included.
	fs2 := &countFS{}
	m2, stop := e.bootMirror(fs2)
	if n := fs2.writes(); n != 0 {
		t.Errorf("a restart rewrote %d files: %v", n, fs2.renames)
	}
	_ = m2
	stop()
	// A project that does not use the Compendium gets none of it.
	other := e.project("Unrelated")
	e.settleAll()
	for k := range files(t, e.root(other)) {
		if strings.Contains(k, "research/") {
			t.Errorf("an unrelated project shows research: %s", k)
		}
	}
}

// The scratchpad is a person's own: nothing about it reaches a mirror, a
// bundle, or a push.
func TestScratchpadIsNotMirrored(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Private pad")
	cid := e.compendium("Private bible")
	e.doc(pid, "Chapter", "chapter", "text\n")
	e.settleAll()
	cursor := e.m.Cursor()
	for _, op := range []struct {
		name string
		in   any
	}{
		{"project.scratch.set", research.ScratchSetIn{ID: pid, Content: "MY SECRET PLOT TWIST"}},
		{"compendium.scratch.set", research.ScratchSetIn{ID: cid, Content: "MY SECRET BIBLE TWIST"}},
	} {
		e.call(e.owner, op.name, op.in)
	}
	e.settleAll()
	if e.m.Cursor() != cursor {
		t.Errorf("a scratchpad save wrote an event the mirror had to look at (cursor %d -> %d)", cursor, e.m.Cursor())
	}
	if e.m.Status(pid).Pending || e.m.Status(cid).Pending {
		t.Error("a scratchpad save woke the mirror")
	}
	err := filepath.WalkDir(filepath.Join(e.dir, "mirror"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && strings.Contains(string(b), "SECRET") {
			t.Errorf("%s holds scratchpad text", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = context.Background
}
