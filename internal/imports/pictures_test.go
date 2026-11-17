package imports_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// The fixtures of the import page's end-to-end test: a small Scrivener project
// with a research picture and a picture in the text (a JPEG that says where it
// was taken), a WordPress export, and a Ghost export with its images folder.
const fx = "../../scripts/fixtures/import-formats"

func zipFiles(t testing.TB, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(b)
	}
	zw.Close()
	return buf.Bytes()
}

func ghostZip(t testing.TB) []byte {
	t.Helper()
	m := map[string][]byte{}
	for _, f := range bundle(t, filepath.Join(fx, "ghost")) {
		m["lighthouse/"+f.Path] = f.Data
	}
	return zipFiles(t, m)
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Read().QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) docText(pid, title string) string {
	e.t.Helper()
	var s string
	if err := e.d.Read().QueryRowContext(context.Background(), `SELECT content FROM documents WHERE project_id = ? AND title = ?`, pid, title).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", title, err)
	}
	return s
}

// blobs lists every stored file under the data folder's files/.
func (e *env) blobs() []string {
	var out []string
	filepath.WalkDir(filepath.Join(e.reg.Env().DataDir, "files"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.Contains(p, ".tmp") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestScrivenerPicturesAreImportedCleanedAndLinked(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, filepath.Join(fx, "Tiny.scriv"))
	pv := e.previewConv(imports.PreviewIn{Name: "Tiny.scriv.zip", Zip: z})
	if pv.Layout != imports.LayoutScrivener || pv.Pictures != 2 || pv.PictureBytes == 0 {
		t.Fatalf("layout %q, %d pictures, %d bytes", pv.Layout, pv.Pictures, pv.PictureBytes)
	}
	out := e.applyConv(pv, imports.ApplyIn{Name: "Tiny.scriv.zip", Zip: z})
	if out.Pictures != 2 {
		t.Fatalf("stored %d pictures", out.Pictures)
	}
	if n := e.count(`SELECT count(*) FROM attachments WHERE project_id = ? AND kind = 'image'`, out.ProjectID); n != 2 {
		t.Fatalf("%d attachment rows", n)
	}
	arrival := e.docText(out.ProjectID, "Arrival")
	if !strings.Contains(arrival, "](/files/att_") || strings.Contains(arrival, "scriobh-picture") || strings.Contains(arrival, "¬") {
		t.Errorf("the picture in the text is not linked to its attachment:\n%s", arrival)
	}
	if n := e.count(`SELECT count(*) FROM attachment_refs r JOIN documents d ON d.id = r.ref_id WHERE r.ref_kind = 'doc' AND d.project_id = ?`, out.ProjectID); n != 1 {
		t.Errorf("the document's reference to its picture was not recorded (%d)", n)
	}
	if n := e.count(`SELECT count(*) FROM attachment_refs r JOIN research_notes n ON n.id = r.ref_id WHERE r.ref_kind = 'research' AND n.project_id = ? AND n.title = 'Harbour map'`, out.ProjectID); n != 1 {
		t.Errorf("the research picture is not referenced by its note (%d)", n)
	}
	// What is stored is the cleaned picture: no place, nothing after the image.
	bs := e.blobs()
	if len(bs) != 2 {
		t.Fatalf("stored files: %v", bs)
	}
	for _, p := range bs {
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte("GPS-SECRET")) || bytes.Contains(b, []byte("Exif")) {
			t.Errorf("%s still carries its EXIF", p)
		}
	}
	if n := e.count(`SELECT count(*) FROM file_usage WHERE scope = 'owner:' || ?`, out.ProjectID); n != 1 {
		t.Errorf("the project's storage use was not counted")
	}
}

func TestPicturesCanBeLeftOut(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, filepath.Join(fx, "Tiny.scriv"))
	pv := e.previewConv(imports.PreviewIn{Name: "Tiny.scriv.zip", Zip: z, Pictures: imports.PicturesSkip})
	if pv.Pictures != 0 {
		t.Fatalf("%d pictures with pictures: skip", pv.Pictures)
	}
	out := e.applyConv(pv, imports.ApplyIn{Name: "Tiny.scriv.zip", Zip: z, Pictures: imports.PicturesSkip})
	if n := e.count(`SELECT count(*) FROM attachments`); n != 0 || len(e.blobs()) != 0 {
		t.Fatalf("%d attachments stored with pictures: skip", n)
	}
	if a := e.docText(out.ProjectID, "Arrival"); !strings.Contains(a, `\[image\]`) {
		t.Errorf("the picture is not marked:\n%s", a)
	}
}

func TestGhostZipPicturesAreOneAttachmentAndAppendReusesIt(t *testing.T) {
	e := newEnv(t)
	z := ghostZip(t)
	pv := e.previewConv(imports.PreviewIn{Name: "lighthouse.zip", Zip: z})
	if pv.Layout != imports.LayoutGhost || pv.Pictures != 1 {
		t.Fatalf("layout %q, %d pictures (the resized copy is the same picture)", pv.Layout, pv.Pictures)
	}
	out := e.applyConv(pv, imports.ApplyIn{Name: "lighthouse.zip", Zip: z})
	lamp, fog := e.docText(out.ProjectID, "The Lamp"), e.docText(out.ProjectID, "Fog")
	if !strings.Contains(lamp, "![The lamp room](/files/att_") || !strings.Contains(fog, "![The lamp again](/files/att_") {
		t.Fatalf("pictures not linked:\n%s\n---\n%s", lamp, fog)
	}
	// Adding the same export to the project again stores nothing new.
	ap := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: out.ProjectID, Name: "lighthouse.zip", Zip: z}).(imports.PreviewOut)
	e.must(e.w, "import.append", imports.AppendIn{ProjectID: out.ProjectID, Token: ap.Token, Name: "lighthouse.zip", Zip: z})
	if n := e.count(`SELECT count(*) FROM attachments WHERE project_id = ?`, out.ProjectID); n != 1 || len(e.blobs()) != 1 {
		t.Errorf("the second import stored the picture again: %d rows, %v", n, e.blobs())
	}
}

// Privacy: an export names its pictures by address; none is ever requested,
// whether it is in the text, an attachment record or the site's address.
func TestExportPicturesAreNeverFetched(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	wxr, err := os.ReadFile(filepath.Join(fx, "harbour-notes.wordpress.xml"))
	if err != nil {
		t.Fatal(err)
	}
	wxr = bytes.ReplaceAll(wxr, []byte("https://harbour.example.invalid"), []byte(srv.URL))
	ghost, _ := os.ReadFile(filepath.Join(fx, "ghost", "lighthouse.ghost.2024-03-01-10-00-00.json"))
	ghost = bytes.ReplaceAll(ghost, []byte("__GHOST_URL__"), []byte(srv.URL))
	e := newEnv(t)
	for _, c := range []struct {
		name string
		in   imports.PreviewIn
	}{
		{"wxr", imports.PreviewIn{Name: "harbour", Files: []imports.File{{Path: "harbour.xml", Data: wxr, Size: int64(len(wxr))}}}},
		{"wxr zip", imports.PreviewIn{Name: "harbour", Zip: zipFiles(t, map[string][]byte{"harbour.xml": wxr})}},
		{"ghost", imports.PreviewIn{Name: "lighthouse", Files: []imports.File{{Path: "l.json", Data: ghost, Size: int64(len(ghost))}}}},
	} {
		pv := e.previewConv(c.in)
		if pv.Pictures != 0 {
			t.Errorf("%s: %d pictures without their bytes", c.name, pv.Pictures)
		}
		out := e.applyConv(pv, imports.ApplyIn{Name: c.in.Name, Files: c.in.Files, Zip: c.in.Zip})
		if out.Pictures != 0 {
			t.Errorf("%s: stored %d pictures", c.name, out.Pictures)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the importer made %d request(s) to an address in an export", n)
	}
	if n := e.count(`SELECT count(*) FROM attachments`); n != 0 {
		t.Fatalf("%d attachments from addresses", n)
	}
}

func tinyPNG(t testing.TB) []byte {
	m := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range m.Pix {
		m.Pix[i] = 200
	}
	m.Set(1, 1, color.RGBA{1, 2, 3, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A hostile zip: pictures that are not what they say, a decompression bomb by
// its header, a damaged file, paths that climb out, and text that tries to make
// its own picture links or point at another project's attachment. The import
// goes through, nothing hostile is stored, and nothing is written outside the
// project's folder.
func TestHostilePictureZip(t *testing.T) {
	e := newEnv(t)
	good := tinyPNG(t)
	bomb := append([]byte{}, good...)
	// width and height in IHDR set to 60000 x 60000: the header says 3.6 gigapixels
	copy(bomb[16:24], []byte{0, 0, 0xea, 0x60, 0, 0, 0xea, 0x60})
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	html := []byte(`<html><script>alert(1)</script></html>`)
	// A real attachment of another project of the same person.
	otherPID := e.must(e.w, "project.create", projects.CreateIn{Title: "Other"}).(projects.Item).Item.ID
	fake := db.NewID("att")
	if err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO attachments(id, project_id, sha256, bytes, mime, ext, kind, filename, alt, width, height, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, fake, otherPID, strings.Repeat("a", 64), 10, "image/png", "png", "image", "o.png", "", 1, 1, e.w.UserID, db.Now(), db.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	post := func(id, title, html string) string {
		return `{"id":"` + id + `","title":"` + title + `","slug":"p` + id + `","status":"published","type":"post","published_at":"2024-01-0` + id + `T10:00:00.000Z","html":` + jsonString(html) + `}`
	}
	posts := []string{
		post("1", "Good", `<p>ok <img src="__GHOST_URL__/content/images/a/good.png" alt="good"></p>`),
		post("2", "Bomb", `<p><img src="__GHOST_URL__/content/images/a/bomb.png" alt="bomb"></p>`),
		post("3", "Svg", `<p><img src="__GHOST_URL__/content/images/a/evil.png" alt="svg"><img src="__GHOST_URL__/content/images/a/page.jpg"></p>`),
		post("4", "Damaged", `<p><img src="__GHOST_URL__/content/images/a/cut.png"></p>`),
		post("5", "Climb", `<p><img src="__GHOST_URL__/content/images/../../../../etc/passwd.png"><img src="__GHOST_URL__/content/images/a/%2e%2e/%2e%2e/x.png"></p>`),
		post("6", "Forged", `<p>![x](scriobh-picture:1) and [y](/files/`+fake+`) and <a href="scriobh-picture:1">z</a> <a href="/files/`+fake+`">w</a> <img src="__GHOST_URL__/content/images/a/good.png" alt="/files/`+fake+`"></p>`),
	}
	js := `{"db":[{"meta":{"exported_on":1,"version":"5.0.0"},"data":{"posts":[` + strings.Join(posts, ",") + `]}}]}`
	z := zipFiles(t, map[string][]byte{
		"g/export.json":               []byte(js),
		"g/content/images/a/good.png": good,
		"g/content/images/a/bomb.png": bomb,
		"g/content/images/a/evil.png": svg,
		"g/content/images/a/page.jpg": html,
		"g/content/images/a/cut.png":  good[:40],
		"../../../../etc/passwd.png":  good,
		"g/content/images/a/../x.png": good,
		"g/content/images/a/link.png": good,
	})
	pv := e.previewConv(imports.PreviewIn{Name: "g", Zip: z})
	if pv.Pictures != 1 {
		t.Errorf("%d pictures kept, want only the good one", pv.Pictures)
	}
	notKept := 0
	for _, w := range pv.Warnings {
		if strings.Contains(w, "picture(s) were not kept") {
			notKept++
		}
	}
	if notKept != 1 {
		t.Errorf("the refused pictures are not reported: %v", pv.Warnings)
	}
	out := e.applyConv(pv, imports.ApplyIn{Name: "g", Zip: z})
	if n := e.count(`SELECT count(*) FROM attachments WHERE project_id = ?`, out.ProjectID); n != 1 {
		t.Fatalf("%d attachments stored", n)
	}
	forged := e.docText(out.ProjectID, "Forged")
	if strings.Contains(forged, "![x](") {
		t.Errorf("foreign text made a picture link:\n%s", forged)
	}
	if n := e.count(`SELECT count(*) FROM attachment_refs WHERE attachment_id = ?`, fake); n != 0 {
		t.Errorf("a reference to an attachment that is not the project's was recorded")
	}
	// Only the good picture's links point at an attachment (Good and Forged show it).
	if n := e.count(`SELECT count(*) FROM attachment_refs r JOIN attachments a ON a.id = r.attachment_id WHERE a.project_id = ?`, out.ProjectID); n != 2 {
		t.Errorf("%d references recorded, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM attachment_refs`); n != 2 {
		t.Errorf("a reference outside the project was recorded")
	}
	root := filepath.Join(e.reg.Env().DataDir, "files", "projects", out.ProjectID)
	for _, p := range e.blobs() {
		if !strings.HasPrefix(p, root) {
			t.Errorf("a file was written outside the project's folder: %s", p)
		}
	}
	for _, title := range []string{"Bomb", "Svg", "Damaged", "Climb"} {
		if s := e.docText(out.ProjectID, title); strings.Contains(s, "/files/") || strings.Contains(s, "scriobh-picture") {
			t.Errorf("%s: a refused picture is linked:\n%s", title, s)
		}
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func TestPicturesOverTheQuotaRefuseTheWholeImport(t *testing.T) {
	e := newEnv(t)
	e.reg.Env().SetService(attachments.ServiceLimits, attachments.Limits{MaxBytes: 25 << 20, OwnerBytes: 10})
	z := ghostZip(t)
	pv := e.previewConv(imports.PreviewIn{Name: "lighthouse.zip", Zip: z})
	_, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "lighthouse.zip", Zip: z})
	if ops.AsError(err).Code != ops.CodeQuota || !strings.Contains(err.Error(), "pictures left out") {
		t.Fatalf("over the quota: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM projects`); n != 0 || len(e.blobs()) != 0 {
		t.Fatalf("something was kept: %d projects, %v", n, e.blobs())
	}
	// Without pictures it goes through.
	pv = e.previewConv(imports.PreviewIn{Name: "lighthouse.zip", Zip: z, Pictures: imports.PicturesSkip})
	e.applyConv(pv, imports.ApplyIn{Name: "lighthouse.zip", Zip: z, Pictures: imports.PicturesSkip})
}

func TestPicturesGoWhenTheImportFailsPartWay(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, filepath.Join(fx, "Tiny.scriv"))
	pv := e.previewConv(imports.PreviewIn{Name: "Tiny.scriv.zip", Zip: z})
	imports.FailAfter(func(n int) error {
		if n == 2 {
			return errors.New("boom")
		}
		return nil
	})
	defer imports.FailAfter(nil)
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "Tiny.scriv.zip", Zip: z}); err == nil {
		t.Fatal("the failure did not fail the import")
	}
	if n := e.count(`SELECT count(*) FROM attachments`); n != 0 || len(e.blobs()) != 0 {
		t.Fatalf("pictures were kept after a failed import: %d rows, %v", n, e.blobs())
	}
	imports.FailAfter(nil)
	// A dry run stores no file either.
	r, err := e.reg.CallDry(context.Background(), e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "Tiny.scriv.zip", Zip: z})
	if err != nil {
		t.Fatal(err)
	}
	_ = r
	if len(e.blobs()) != 0 {
		t.Fatalf("a dry run placed files: %v", e.blobs())
	}
}

func TestSheetsBecomeEntitiesOfTheProjectsCompendium(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, filepath.Join(fx, "Tiny.scriv"))
	pv := e.previewConv(imports.PreviewIn{Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities})
	if pv.Sheets != 2 {
		t.Fatalf("%d sheets in the preview", pv.Sheets)
	}
	out := e.applyConv(pv, imports.ApplyIn{Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities})
	if out.Entities != 2 || out.CompendiumID == "" {
		t.Fatalf("entities %d, compendium %q, warnings %v", out.Entities, out.CompendiumID, out.Warnings)
	}
	if n := e.count(`SELECT count(*) FROM projects WHERE id = ? AND compendium_id = ?`, out.ProjectID, out.CompendiumID); n != 1 {
		t.Fatal("the Compendium is not the project's primary")
	}
	if n := e.count(`SELECT count(*) FROM entities WHERE compendium_id = ? AND ((name = 'Mara' AND kind = 'character') OR (name = 'Saltmouth' AND kind = 'place'))`, out.CompendiumID); n != 2 {
		t.Fatalf("the entities are not there (%d)", n)
	}
	if n := e.count(`SELECT count(*) FROM research_notes WHERE project_id = ? AND title IN ('Mara','Saltmouth')`, out.ProjectID); n != 0 {
		t.Errorf("a sheet made an entity was also written as a note")
	}
	if n := e.count(`SELECT count(*) FROM research_notes WHERE project_id = ? AND title = 'Harbour lore'`, out.ProjectID); n != 1 {
		t.Errorf("an ordinary research note was lost")
	}

	// Adding to a project whose Compendium already has Mara: Mara stays a note.
	ap := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: out.ProjectID, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}).(imports.PreviewOut)
	ao := e.must(e.w, "import.append", imports.AppendIn{ProjectID: out.ProjectID, Token: ap.Token, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}).(imports.ApplyOut)
	if ao.Entities != 0 || e.count(`SELECT count(*) FROM research_notes WHERE project_id = ? AND title = 'Mara'`, out.ProjectID) != 1 {
		t.Errorf("a name the Compendium has: entities %d, warnings %v", ao.Entities, ao.Warnings)
	}

	// A project with no primary Compendium keeps them as notes, and says so.
	bid := e.must(e.w, "project.create", projects.CreateIn{Title: "Bare"}).(projects.Item).Item.ID
	bp := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: bid, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}).(imports.PreviewOut)
	bo := e.must(e.w, "import.append", imports.AppendIn{ProjectID: bid, Token: bp.Token, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}).(imports.ApplyOut)
	if bo.Entities != 0 || !strings.Contains(strings.Join(bo.Warnings, " "), "no primary Compendium") {
		t.Errorf("no primary: entities %d, warnings %v", bo.Entities, bo.Warnings)
	}

	// An editor of the project who may only read its Compendium: notes.
	ed := e.user("editor", ops.RoleWriter)
	now := db.Now()
	if err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, out.ProjectID, ed.UserID, "editor", now); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, out.CompendiumID, ed.UserID, "reader", now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := e.count(`SELECT count(*) FROM entities WHERE compendium_id = ?`, out.CompendiumID)
	ep := e.must(ed, "import.append_preview", imports.AppendPreviewIn{ProjectID: out.ProjectID, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}).(imports.PreviewOut)
	eo := e.must(ed, "import.append", imports.AppendIn{ProjectID: out.ProjectID, Token: ep.Token, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}).(imports.ApplyOut)
	if eo.Entities != 0 || e.count(`SELECT count(*) FROM entities WHERE compendium_id = ?`, out.CompendiumID) != before {
		t.Errorf("a reader of the Compendium wrote to it: %v", eo.Warnings)
	}
}

// A key pinned to one project cannot reach a new project's Compendium through
// an import, nor another project's.
func TestPinnedKeyCannotWriteEntitiesElsewhere(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, filepath.Join(fx, "Tiny.scriv"))
	mine := e.must(e.w, "project.create", projects.CreateIn{Title: "Mine"}).(projects.Item).Item.ID
	key := ops.Principal{UserID: e.w.UserID, Role: e.w.Role, Via: "apikey", Scope: "write", ProjectID: mine, KeyID: "key_test"}
	pv := e.previewConv(imports.PreviewIn{Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities})
	if _, err := e.call(key, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}); err == nil || ops.AsError(err).Code == ops.CodeConflict {
		t.Errorf("a pinned key made a new project (and its Compendium): %v", err)
	}
	other := e.must(e.w, "project.create", projects.CreateIn{Title: "Other"}).(projects.Item).Item.ID
	if _, err := e.call(key, "import.append_preview", imports.AppendPreviewIn{ProjectID: other, Name: "Tiny.scriv.zip", Zip: z, ScrivSheets: imports.SheetsEntities}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a pinned key reached another project: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM compendiums`); n != 0 {
		t.Errorf("%d Compendiums made", n)
	}
}

// A picture shown only by a document that is left out is not stored or counted.
func TestPicturesOfLeftOutDocumentsAreNotStored(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, filepath.Join(fx, "Tiny.scriv"))
	pv := e.previewConv(imports.PreviewIn{Name: "Tiny.scriv.zip", Zip: z})
	var key string
	for _, n := range pv.Nodes {
		if n.Title == "Arrival" {
			key = n.Key
		}
	}
	edits := []imports.Edit{{Key: key, Skip: true}}
	pv = e.previewConv(imports.PreviewIn{Name: "Tiny.scriv.zip", Zip: z, Edits: edits})
	if pv.Pictures != 1 {
		t.Errorf("the preview counts %d pictures with Arrival left out", pv.Pictures)
	}
	out := e.applyConv(pv, imports.ApplyIn{Name: "Tiny.scriv.zip", Zip: z, Edits: edits})
	if out.Pictures != 1 || e.count(`SELECT count(*) FROM attachments WHERE project_id = ?`, out.ProjectID) != 1 || len(e.blobs()) != 1 {
		t.Errorf("stored %d pictures (%v) with Arrival left out", out.Pictures, e.blobs())
	}
}
