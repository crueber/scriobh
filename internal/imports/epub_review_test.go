package imports_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// A navigation document of 8 MiB that is nothing but landmarks, and a guide of as many references: the landmarks
// are capped when they are read and matched to files by a map, so the book reads in linear time.
func TestEpubWithAHundredThousandLandmarks(t *testing.T) {
	e := newEnv(t)
	var lm strings.Builder
	for i := 0; lm.Len() < 8<<20-(200<<10); i++ {
		fmt.Fprintf(&lm, `<li><a epub:type="bodymatter" href="a.xhtml#l%d">x</a></li>`, i)
	}
	nav := `<nav epub:type="toc"><ol><li><a href="a.xhtml">A</a></li><li><a href="b.xhtml">B</a></li></ol></nav><nav epub:type="landmarks"><ol>` + lm.String() + `</ol></nav>`
	book := miniBook(t, "Landmarks", []mbChapter{
		{file: "a.xhtml", title: "A", body: `<h1>Alpha</h1><p>One.</p>`},
		{file: "b.xhtml", title: "B", body: `<h1>Beta</h1><p>Two.</p>`},
	})
	book = replaceXHTML(t, book, "OEBPS/nav.xhtml", page("Contents", "", nav))
	var guide strings.Builder
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(&guide, `<reference type="text" title="t" href="a.xhtml#g%d"/>`, i)
	}
	book = replaceInZip(t, book, "OEBPS/content.opf", `</package>`, `<guide>`+guide.String()+`</guide></package>`)
	t0 := time.Now()
	pv, err := previewErr(e, book)
	took := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("a navigation document of %.1f MiB and a guide of 100,000 references: %v", float64(len(nav))/(1<<20), took.Round(time.Millisecond))
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 3*time.Second*timingFactor {
			t.Errorf("took %v; the budget is 3 s (scaled under the race detector)", took)
		}
	})
	eq(t, outline(pv.Nodes), []string{"chapter:A", "chapter:B"})
	// A caller that has gone stops the read.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := imports.ReadEpub(ctx, book, "", ""); err == nil {
		t.Error("a cancelled read went on")
	}
}

// What the preview accepts the apply must accept: a chapter over a document's size is left out at the preview, with
// the reason, and the rest applies.
func TestEpubChapterOverADocumentIsLeftOutAtThePreview(t *testing.T) {
	e, _ := newEpubEnv(t)
	big := strings.Repeat("<p>"+strings.Repeat("word ", 40)+"</p>\n", 12000) // about 2.5 MiB of Markdown
	book := miniBook(t, "Heavy", []mbChapter{
		{file: "a.xhtml", title: "Light", body: `<h1>Light</h1><p>Fine.</p>`},
		{file: "b.xhtml", title: "Heavy", body: `<h1>Heavy</h1>` + big},
		{file: "c.xhtml", title: "Last", body: `<h1>Last</h1><p>Fine too.</p>`},
	})
	pv := e.previewConv(imports.PreviewIn{Name: "h.epub", Epub: book})
	eq(t, outline(pv.Nodes), []string{"chapter:Light", "chapter:Last"})
	var said bool
	for _, s := range pv.Skipped {
		if strings.Contains(s.Reason, "larger than the 2 MiB") {
			said = true
		}
	}
	if !said {
		t.Errorf("the chapter left out is not said to be: %+v", pv.Skipped)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "h.epub", Epub: book})
	if ap.Documents != 2 || ap.EditionID == "" {
		t.Errorf("apply: %+v", ap)
	}
	ed := e.must(e.w, "edition.get", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ap.EditionID}).(editions.GetOut)
	if len(ed.Chapters) != 2 {
		t.Errorf("the edition records %d chapters, the book's two that are imported", len(ed.Chapters))
	}
}

// A chapter that is almost a document's size and is all pictures grows when the pictures are named by their hashes,
// and still fits what the edition keeps.
func TestEpubBaselineOfAChapterOfPicturesFits(t *testing.T) {
	e, _ := newEpubEnv(t)
	imgs := strings.Repeat(`<p><img src="p.png" alt=""/></p>`, 78_000)
	book := miniBook(t, "Plates", []mbChapter{{file: "a.xhtml", title: "Plates", body: `<h1>Plates</h1>` + imgs}}, zent{name: "OEBPS/p.png", data: pngBytes(4, 4, 9), store: true})
	pv := e.previewConv(imports.PreviewIn{Name: "p.epub", Epub: book})
	if pv.Documents != 1 {
		t.Fatalf("documents %d, skipped %+v", pv.Documents, pv.Skipped)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "p.epub", Epub: book})
	if ap.EditionID == "" {
		t.Fatal("no edition")
	}
	b, err := editions.Baseline(t.Context(), e.d.Read(), ap.EditionID, 1)
	if err != nil || len(b) < 2<<20 {
		t.Errorf("baseline: %d bytes, %v", len(b), err)
	}
}

// More chapters than an edition records: the preview says how many are read, and the apply goes through.
func TestEpubMoreChaptersThanAnEditionHolds(t *testing.T) {
	e, _ := newEpubEnv(t)
	var sb strings.Builder
	for i := 0; i < 5200; i++ {
		fmt.Fprintf(&sb, "<h2>H%d</h2><p>t</p>", i)
	}
	book := miniBook(t, "Dense", []mbChapter{{file: "a.xhtml", title: "A", skipNav: true, body: sb.String()}})
	pv := e.previewConv(imports.PreviewIn{Name: "d.epub", Epub: book})
	if pv.Epub.Strategy != "headings" {
		t.Fatalf("strategy %q", pv.Epub.Strategy)
	}
	has(t, strings.Join(pv.Warnings, "\n"), "first 5000 are read")
	ap := e.applyConv(pv, imports.ApplyIn{Name: "d.epub", Epub: book})
	ed := e.must(e.w, "edition.get", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ap.EditionID}).(editions.GetOut)
	if len(ed.Chapters) != imports.MaxEpubChapters {
		t.Errorf("the edition records %d chapters", len(ed.Chapters))
	}
}

// An entry that is a link in the zip is not followed, and the book still reads.
func TestEpubSymbolicLinkEntries(t *testing.T) {
	e := newEnv(t)
	good := miniBook(t, "Links", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>Alpha</h1><p>One.</p>`}})
	good = replaceInZip(t, good, "OEBPS/content.opf", `</manifest>`, `<item id="lnk" href="b.xhtml" media-type="application/xhtml+xml"/></manifest>`)
	good = replaceInZip(t, good, "OEBPS/content.opf", `</spine>`, `<itemref idref="lnk"/></spine>`)
	zr, _ := zip.NewReader(bytes.NewReader(good), int64(len(good)))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		h := &zip.FileHeader{Name: f.Name, Method: f.Method}
		w, _ := zw.CreateHeader(h)
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		w.Write(b.Bytes())
	}
	h := &zip.FileHeader{Name: "OEBPS/b.xhtml", Method: zip.Store}
	h.SetMode(os.ModeSymlink | 0o777)
	w, _ := zw.CreateHeader(h)
	w.Write([]byte("/etc/passwd"))
	zw.Close()
	pv, err := previewErr(e, buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	eq(t, outline(pv.Nodes), []string{"chapter:Alpha"})
	if pv.Report["zip_entries_ignored"] == nil {
		t.Errorf("the link is not counted: %v", pv.Report)
	}
}

// What the book says about itself is bounded wherever it is kept.
func TestEpubMetadataIsBounded(t *testing.T) {
	e, _ := newEpubEnv(t)
	var people strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&people, `<dc:creator>Author %d %s</dc:creator><dc:contributor>Helper %d</dc:contributor>`, i, strings.Repeat("x", 400), i)
	}
	book := miniBook(t, "Meta", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p>x</p>`}})
	book = replaceInZip(t, book, "OEBPS/content.opf", `<package xmlns="http://www.idpf.org/2007/opf" version="3.0"`, `<package xmlns="http://www.idpf.org/2007/opf" version="`+strings.Repeat("9", 100_000)+`"`)
	book = replaceInZip(t, book, "OEBPS/content.opf", `<dc:title>Meta</dc:title>`, `<dc:title>Meta</dc:title>`+people.String()+`<dc:description>`+strings.Repeat("d", 100_000)+`</dc:description><dc:publisher>`+strings.Repeat("p", 100_000)+`</dc:publisher>`)
	pv := e.previewConv(imports.PreviewIn{Name: "m.epub", Epub: book})
	if len(pv.Epub.Version) > 20 || len(pv.Epub.Authors) > 50 || len(pv.Epub.Contributors) > 50 || len(pv.Epub.Publisher) > 200 || len(pv.Epub.Description) > 4000 {
		t.Errorf("unbounded: version %d, authors %d, contributors %d, publisher %d, description %d", len(pv.Epub.Version), len(pv.Epub.Authors), len(pv.Epub.Contributors), len(pv.Epub.Publisher), len(pv.Epub.Description))
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "m.epub", Epub: book})
	var meta string
	if err := e.d.Read().QueryRow(`SELECT metadata_json FROM editions WHERE id = ?`, ap.EditionID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if len(meta) > 40<<10 {
		t.Errorf("the edition's metadata is %d bytes", len(meta))
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(meta), &m)
	if v, _ := m["epub_version"].(string); len(v) > 20 {
		t.Errorf("epub_version %d", len(v))
	}
}

// Who imported an edition is the owner's to know, as who is on the project is.
func TestEditionImportedByIsForOwnersAndAdministrators(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "salt.epub", Epub: book})
	roles := map[string]ops.Principal{
		ops.ProjectOwner: e.w, ops.ProjectEditor: e.user("editor", ops.RoleWriter), ops.ProjectReviewer: e.user("reviewer", ops.RoleReader), ops.ProjectReader: e.user("reader", ops.RoleReader),
	}
	for role, p := range roles {
		if role == ops.ProjectOwner {
			continue
		}
		role, p := role, p
		if err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, ap.ProjectID, p.UserID, role, db.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	admin := e.user("admin", ops.RoleAdmin)
	roles["admin"] = admin
	for role, p := range roles {
		want := role == ops.ProjectOwner || role == "admin"
		l := e.must(p, "edition.list", editions.ProjectIn{ProjectID: ap.ProjectID}).(editions.ListOut)
		g := e.must(p, "edition.get", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ap.EditionID}).(editions.GetOut)
		if got := l.Items[0].ImportedBy != ""; got != want {
			t.Errorf("%s: edition.list shows the importer: %v, want %v", role, got, want)
		}
		if got := g.Item.ImportedBy != ""; got != want {
			t.Errorf("%s: edition.get shows the importer: %v, want %v", role, got, want)
		}
	}
}

func TestReadEpubRefusesWhatImportRefuses(t *testing.T) {
	big := make([]byte, editions.MaxFileBytes+1)
	if _, err := imports.ReadEpub(t.Context(), big, "", ""); err == nil || ops.AsError(err).Code != ops.CodeTooLarge || !strings.Contains(err.Error(), "32 MB") {
		t.Errorf("a book over the size that is kept: %v", err)
	}
	if _, err := imports.ReadEpub(nil, []byte("not a book"), "", ""); err == nil { //nolint:staticcheck // a nil context is tolerated
		t.Error("a file that is not a book was read")
	}
}
