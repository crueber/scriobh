package imports_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// newEpubEnv is newEnv with the data directory in hand, for tests of what is kept on disk.
func newEpubEnv(t *testing.T) (*env, string) {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	e := &env{t: t, d: d, reg: registry.Build(d, nil, dir)}
	e.w = e.user("writer", ops.RoleWriter)
	return e, dir
}

func TestEpubEditionIsKeptIntactAndMapped(t *testing.T) {
	e, dir := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	// The preview wrote nothing: no edition, no file, no project.
	if n := count(t, e, `SELECT (SELECT count(*) FROM editions) + (SELECT count(*) FROM projects)`); n != 0 {
		t.Fatalf("a preview wrote %d rows", n)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "files")); len(ents) > 0 {
		for _, en := range ents {
			if en.Name() != ".tmp" {
				t.Errorf("a preview wrote files/%s", en.Name())
			}
		}
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "salt.epub", Epub: book, EditionLabel: "  First edition,\u202e 2019 "})
	if ap.EditionID == "" || ap.SourceID == "" {
		t.Fatalf("apply: %+v", ap)
	}
	list := e.must(e.w, "edition.list", editions.ProjectIn{ProjectID: ap.ProjectID}).(editions.ListOut)
	if len(list.Items) != 1 {
		t.Fatalf("editions: %+v", list)
	}
	ed := list.Items[0]
	if ed.Label != "First edition, 2019" || ed.Kind != "epub" || ed.Identifier != "urn:isbn:9780000000002" || !ed.HasFile || ed.SourceID != ap.SourceID ||
		ed.Size != int64(len(book)) || ed.SHA256 != editions.SumOf(book) || ed.Chapters != 6 {
		t.Errorf("edition: %+v", ed)
	}
	for k, want := range map[string]any{"title": "The Salt Road", "publisher": "Fixture House", "date": "2019-05-01", "language": "en-GB", "epub_version": "3.0", "footnotes": float64(2), "source_name": "salt.epub"} {
		if ed.Metadata[k] != want {
			t.Errorf("metadata %s = %v, want %v", k, ed.Metadata[k], want)
		}
	}
	if u, _ := ed.Metadata["cover_url"].(string); !strings.HasPrefix(u, "/files/att_") {
		t.Errorf("the cover is not kept as an attachment: %v", ed.Metadata["cover_url"])
	}
	// The file is the book, byte for byte, for an owner; and not for a reader.
	fo := e.must(e.w, "edition.download", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ed.ID}).(editions.FileOut)
	if string(fo.Body) != string(book) || fo.ContentType != "application/epub+zip" {
		t.Errorf("the downloaded file is not the book (%d bytes of %d)", len(fo.Body), len(book))
	}
	if _, err := os.Stat(filepath.Join(dir, "files", "projects", ap.ProjectID, "editions", ed.SHA256+".epub")); err != nil {
		t.Errorf("the file is not where it is said to be: %v", err)
	}
	// The chapters: where each came from, what it became, and the text as it was read.
	got := e.must(e.w, "edition.get", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ed.ID}).(editions.GetOut)
	var titles []string
	for _, c := range got.Chapters {
		titles = append(titles, c.Kind+":"+c.Title+"@"+c.SourcePath)
		if c.DocumentID == "" {
			t.Errorf("chapter %q became no document", c.Title)
		}
		var k, ti string
		if err := e.d.Read().QueryRow(`SELECT kind, title FROM documents WHERE id = ?`, c.DocumentID).Scan(&k, &ti); err != nil || k != c.Kind || ti != c.Title {
			t.Errorf("chapter %q points at document %q (%s): %v", c.Title, ti, k, err)
		}
		var src string
		if err := e.d.Read().QueryRow(`SELECT source_path FROM document_sources WHERE document_id = ? AND source_id = ?`, c.DocumentID, ap.SourceID).Scan(&src); err != nil || src != c.SourcePath {
			t.Errorf("document_sources for %q: %q, %v", c.Title, src, err)
		}
	}
	eq(t, titles, []string{
		"front_matter:Dedication@OEBPS/text/dedication.xhtml",
		"part:Part One: The Marsh@OEBPS/text/part1.xhtml",
		"chapter:The Road@OEBPS/text/ch01.xhtml",
		"chapter:The Wall@OEBPS/text/ch02.xhtml",
		"chapter:Epilogue@OEBPS/text/epilogue.xhtml",
		"back_matter:Acknowledgments@OEBPS/text/ack.xhtml",
	})
	if got.Chapters[2].ParentOrd != 2 || got.Chapters[1].ParentOrd != 0 {
		t.Errorf("nesting: %+v", got.Chapters)
	}
	base, err := editions.Baseline(t.Context(), e.d.Read(), ed.ID, 3)
	if err != nil || !strings.Contains(base, "They came at *dusk*") || !strings.Contains(base, "scriobh-picture:") || strings.Contains(base, "/files/att_") {
		t.Errorf("baseline: %v\n%s", err, base)
	}
	// The provenance record names the book by its own hash.
	var kind, hash string
	if err := e.d.Read().QueryRow(`SELECT kind, source_hash FROM project_sources WHERE id = ?`, ap.SourceID).Scan(&kind, &hash); err != nil || kind != "epub" || hash != editions.SumOf(book) {
		t.Errorf("project_sources: %s %s %v", kind, hash, err)
	}
	// One file is counted once against the project's storage, with the pictures.
	var b int64
	_ = e.d.Read().QueryRow(`SELECT bytes FROM file_usage WHERE scope = ?`, "owner:"+ap.ProjectID).Scan(&b)
	var pics int64
	_ = e.d.Read().QueryRow(`SELECT COALESCE(sum(bytes),0) FROM attachments WHERE project_id = ?`, ap.ProjectID).Scan(&pics)
	if b != pics+int64(len(book)) {
		t.Errorf("usage %d, want %d pictures + %d file", b, pics, len(book))
	}
	// A reader cannot download it.
	reader := e.user("reader", ops.RoleReader)
	if err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, ap.ProjectID, reader.UserID, ops.ProjectReader, db.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.call(reader, "edition.download", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ed.ID}); err == nil || ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader downloaded the source: %v", err)
	}
	if _, err := e.call(reader, "edition.get", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ed.ID}); err != nil {
		t.Errorf("a reader cannot see the editions: %v", err)
	}
}

func TestEpubImportKeepsNothingWhenItFails(t *testing.T) {
	e, dir := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	imports.FailAfter(func(n int) error {
		if n == 3 {
			return os.ErrInvalid
		}
		return nil
	})
	defer imports.FailAfter(nil)
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "salt.epub", Epub: book}); err == nil {
		t.Fatal("the apply was meant to fail")
	}
	if n := count(t, e, `SELECT (SELECT count(*) FROM editions) + (SELECT count(*) FROM edition_chapters) + (SELECT count(*) FROM projects) + (SELECT count(*) FROM attachments)`); n != 0 {
		t.Errorf("%d rows survived a failed import", n)
	}
	var left []string
	_ = filepath.WalkDir(filepath.Join(dir, "files"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			left = append(left, p)
		}
		return nil
	})
	if len(left) > 0 {
		t.Errorf("files left behind: %v", left)
	}
}

func TestEpubTokenBindsTheBook(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	// Another book, applied with this book's token, is refused.
	other := rewriteTitle(t, book)
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "salt.epub", Epub: other}); err == nil || ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("another book under this token: %v", err)
	}
	// So is the same book with another option that changes what is read.
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "salt.epub", Epub: book, EpubSections: "files"}); err == nil {
		t.Error("a different cut under this token")
	}
}

func count(t testing.TB, e *env, q string) int {
	t.Helper()
	var n int
	if err := e.d.Read().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A comparison of editions reads a second book with no project, and what it reads is what an
// edition of the same file recorded.
func TestReadEpubAgreesWithTheEditionRecord(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "salt.epub", Epub: book})
	ed := e.must(e.w, "edition.list", editions.ProjectIn{ProjectID: ap.ProjectID}).(editions.ListOut).Items[0]
	chs, err := editions.Chapters(t.Context(), e.d.Read(), ed.ID)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := imports.ReadEpub(t.Context(), book, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rb.SHA256 != ed.SHA256 || len(rb.Chapters) != len(chs) {
		t.Fatalf("%s vs %s, %d vs %d chapters", rb.SHA256, ed.SHA256, len(rb.Chapters), len(chs))
	}
	for i, c := range chs {
		r := rb.Chapters[i]
		if r.Ord != c.Ord || r.ParentOrd != c.ParentOrd || r.Kind != c.Kind || r.Title != c.Title || r.SourcePath != c.SourcePath || r.SHA256 != c.SHA256 || r.Words != c.Words {
			t.Errorf("chapter %d differs:\n read  %+v\n kept  %+v", i+1, r, c)
		}
		base, _ := editions.Baseline(t.Context(), e.d.Read(), ed.ID, c.Ord)
		if base != r.Markdown {
			t.Errorf("chapter %d: the text read is not the baseline kept", i+1)
		}
	}
	if _, err := imports.ReadEpub(t.Context(), []byte("not a book"), "", ""); err == nil {
		t.Error("a file that is not a book was read")
	}
	if _, err := imports.ReadEpub(t.Context(), book, "bogus", ""); err == nil {
		t.Error("an unknown cut was accepted")
	}
}

// An export bundle names the editions a project came from, as metadata, and never holds a book; and
// importing that bundle again makes a project with no edition of its own.
func TestExportBundleCarriesEditionMetadataOnly(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "salt.epub", Epub: book})
	out := e.must(e.w, "export.markdown", export.MarkdownIn{ProjectID: ap.ProjectID, Mode: "bundle"}).(export.File)
	zm := unzipAll(t, out.Body)
	var man struct {
		Editions []struct {
			Label, SHA256, Kind string
			Size                int64
			Chapters            int
		}
	}
	if err := json.Unmarshal(zm["scriobh.json"], &man); err != nil {
		t.Fatal(err)
	}
	if len(man.Editions) != 1 || man.Editions[0].SHA256 != editions.SumOf(book) || man.Editions[0].Chapters != 6 || man.Editions[0].Kind != "epub" {
		t.Errorf("editions in the manifest: %+v", man.Editions)
	}
	for name, b := range zm {
		if strings.HasSuffix(name, ".epub") || bytes.HasPrefix(b, []byte("PK")) && name != "scriobh.json" && !strings.HasPrefix(name, "attachments/") {
			t.Errorf("the bundle holds %s, which looks like the book", name)
		}
	}
	// the name the file came with stays on this server: it is in the edition's own record, never in a manifest
	if strings.Contains(string(zm["scriobh.json"]), "source_name") || strings.Contains(string(zm["scriobh.json"]), "salt.epub") {
		t.Errorf("the manifest carries the file's name: %s", zm["scriobh.json"])
	}
	pv2 := e.previewConv(imports.PreviewIn{Name: "bundle.zip", Zip: out.Body})
	ap2 := e.applyConv(pv2, imports.ApplyIn{Name: "bundle.zip", Zip: out.Body})
	if n := count(t, e, `SELECT count(*) FROM editions WHERE project_id = '`+ap2.ProjectID+`'`); n != 0 {
		t.Errorf("a bundle made %d editions", n)
	}
}
