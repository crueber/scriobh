package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
	"github.com/crueber/scriobh/internal/testutil"
)

const fixtures = "../../internal/imports/testdata/"

func convData(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SCRIOBH_DATA_DIR", dir)
	t.Setenv("SCRIOBH_LISTEN", "127.0.0.1:0")
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	testutil.User(t, d, "writer", "w@t.local")
	d.Close()
	return dir
}

// Each foreign source, from the command line: preview, then apply, with what
// the report promises.
func TestImportCommandScrivenerWordPressGhost(t *testing.T) {
	dir := convData(t)
	var out, errb bytes.Buffer
	step := func(args ...string) string {
		out.Reset()
		errb.Reset()
		if code := run(append([]string{"import"}, args...), &out, &errb); code != 0 {
			t.Fatalf("%v: exit %d: %s%s", args, code, errb.String(), out.String())
		}
		return out.String()
	}

	// A Scrivener project folder, recognised by its .scrivx.
	proj := fixtures + "Salt Road.scriv"
	got := step(proj)
	for _, want := range []string{"Scrivener project", "Part One", "The Road", "Dusk", "7 research notes and folders", "2 look like character or place sheets", "Trash", "Nothing was written"} {
		if !strings.Contains(got, want) {
			t.Errorf("scrivener preview lacks %q:\n%s", want, got)
		}
	}
	if n := count(t, dir, `SELECT count(*) FROM projects`); n != 0 {
		t.Fatalf("a preview wrote a project")
	}
	got = step(proj, "--apply")
	if !strings.Contains(got, "Created project") || !strings.Contains(got, "Research: 7 notes") {
		t.Errorf("apply:\n%s", got)
	}
	if n := count(t, dir, `SELECT count(*) FROM research_notes`); n != 7 {
		t.Errorf("research notes: %d", n)
	}
	if n := count(t, dir, `SELECT count(*) FROM documents WHERE include_in_compile = 0`); n != 1 {
		t.Errorf("do not compile: %d", n)
	}
	if n := count(t, dir, `SELECT count(*) FROM project_sources WHERE kind = 'markdown' AND metadata_json LIKE '%"layout":"scrivener"%'`); n != 1 {
		t.Errorf("provenance: %d", n)
	}

	// The same project as a zip, without research.
	zp := filepath.Join(t.TempDir(), "Salt Road.scriv.zip")
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	filepath.WalkDir(proj, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Dir(proj), p)
		w, _ := zw.Create(filepath.ToSlash(rel))
		b, _ := os.ReadFile(p)
		w.Write(b)
		return nil
	})
	zw.Close()
	os.WriteFile(zp, zb.Bytes(), 0o644)
	got = step(zp, "--scrivener-research", "skip")
	if !strings.Contains(got, "Scrivener project") || strings.Contains(got, "research notes and folders") {
		t.Errorf("zip preview:\n%s", got)
	}

	// A WordPress export.
	got = step(fixtures+"wordpress-1.2.xml", "--wp-status", "all", "--wp-types", "both")
	for _, want := range []string{"WordPress export", "blog", "A Draft", "Pages", "About", "comment(s) were not imported"} {
		if !strings.Contains(got, want) {
			t.Errorf("wordpress preview lacks %q:\n%s", want, got)
		}
	}
	step(fixtures+"wordpress-1.2.xml", "--apply")
	if n := count(t, dir, `SELECT count(*) FROM project_sources WHERE metadata_json LIKE '%"layout":"wordpress"%'`); n != 1 {
		t.Errorf("wordpress provenance: %d", n)
	}

	// A Ghost export.
	got = step(fixtures + "ghost-5.json")
	if !strings.Contains(got, "Ghost export") || !strings.Contains(got, "Lexical Post") {
		t.Errorf("ghost preview:\n%s", got)
	}
	step(fixtures+"ghost-4.json", "--apply", "--owner", "w@t.local")
	if n := count(t, dir, `SELECT count(*) FROM project_sources WHERE metadata_json LIKE '%"layout":"ghost"%'`); n != 1 {
		t.Errorf("ghost provenance: %d", n)
	}

	// Refusals: an unknown layout, an unknown option, a file of no known kind, a broken export.
	// (a .txt used to be the file of no known kind; it is plain text now, and has its own test)
	txt := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(txt, []byte("hello"), 0o644)
	for _, c := range []struct {
		args []string
		code int
	}{
		{[]string{fixtures + "ghost-5.json", "--layout", "evernote"}, 2},
		{[]string{fixtures + "ghost-5.json", "--ghost-status", "none"}, 1},
		{[]string{txt}, 2},
	} {
		out.Reset()
		errb.Reset()
		if code := run(append([]string{"import"}, c.args...), &out, &errb); code != c.code {
			t.Errorf("%v: exit %d (%s%s)", c.args, code, errb.String(), out.String())
		}
	}
	bad := filepath.Join(t.TempDir(), "broken.json")
	os.WriteFile(bad, []byte(`{"db":[{"data":{"posts":`), 0o644)
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", bad, "--layout", "ghost"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "not readable JSON") {
		t.Errorf("a broken export: %d %s", code, errb.String())
	}
}

// A PDF and a Word file with tracked changes, from the command line: the
// preview says what was read, the apply writes the suggestions, a scanned PDF
// and a bad option are refused.
func TestImportCommandPdfAndTrackedChanges(t *testing.T) {
	dir := convData(t)
	var out, errb bytes.Buffer
	run1 := func(args ...string) (int, string) {
		out.Reset()
		errb.Reset()
		code := run(append([]string{"import"}, args...), &out, &errb)
		return code, out.String() + errb.String()
	}
	tmp := t.TempDir()

	// a PDF
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	var c strings.Builder
	c.WriteString("BT /F1 20 Tf 72 700 Td (Chapter One) Tj ET\n")
	for l := 0; l < 6; l++ {
		fmt.Fprintf(&c, "BT /F1 11 Tf 72 %d Td (Line %d of ordinary text in a chapter.) Tj ET\n", 660-14*l, l)
	}
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), c.String(), "")
	pdfPath := filepath.Join(tmp, "book.pdf")
	os.WriteFile(pdfPath, d.Write(d.Pages([]int{pg}, ""), pdftest.Opt{}), 0o644)
	code, got := run1(pdfPath)
	if code != 0 || !strings.Contains(got, "PDF with a text layer") || !strings.Contains(got, "Chapter One") || !strings.Contains(got, "Nothing was written") {
		t.Fatalf("pdf preview: %d\n%s", code, got)
	}
	if code, got = run1(pdfPath, "--apply"); code != 0 || !strings.Contains(got, "Created project") {
		t.Fatalf("pdf apply: %d\n%s", code, got)
	}
	if n := count(t, dir, `SELECT count(*) FROM project_sources WHERE kind = 'pdf'`); n != 1 {
		t.Errorf("pdf provenance: %d", n)
	}
	// a scanned one is refused with the plain message
	sd := pdftest.New()
	img := sd.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode", []byte{0xff, 0xd8}, "")
	var pgs []int
	for i := 0; i < 3; i++ {
		pgs = append(pgs, sd.Page(612, 792, fmt.Sprintf("/XObject << /Im1 %d 0 R >>", img), "q 612 0 0 792 0 0 cm /Im1 Do Q", ""))
	}
	scan := filepath.Join(tmp, "scan.pdf")
	os.WriteFile(scan, sd.Write(sd.Pages(pgs, ""), pdftest.Opt{}), 0o644)
	if code, got = run1(scan); code == 0 || !strings.Contains(got, "no text layer") {
		t.Errorf("a scan: %d %s", code, got)
	}

	// a Word file with a tracked change
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	put := func(name, body string) {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	ns := `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	put("[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`)
	put("word/styles.xml", `<?xml version="1.0"?><w:styles `+ns+`><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`)
	put("word/document.xml", `<?xml version="1.0"?><w:document `+ns+`><w:body><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>One</w:t></w:r></w:p><w:p><w:r><w:t xml:space="preserve">A sentence that is long enough to be found, </w:t></w:r><w:ins w:id="1" w:author="Ann" w:date="2024-03-05T10:21:00Z"><w:r><w:t xml:space="preserve">with an addition, </w:t></w:r></w:ins><w:r><w:t>and an end.</w:t></w:r></w:p></w:body></w:document>`)
	zw.Close()
	docx := filepath.Join(tmp, "book.docx")
	os.WriteFile(docx, zb.Bytes(), 0o644)
	if code, got = run1(docx, "--apply"); code != 0 || !strings.Contains(got, "pending suggestions") {
		t.Fatalf("docx apply: %d\n%s", code, got)
	}
	if n := count(t, dir, `SELECT count(*) FROM suggestions WHERE author_label = 'Ann' AND author_id IS NULL`); n != 1 {
		t.Errorf("suggestions: %d", n)
	}
	if code, got = run1(docx, "--docx-changes", "accept", "--apply"); code != 0 {
		t.Fatalf("accept: %d\n%s", code, got)
	}
	if n := count(t, dir, `SELECT count(*) FROM suggestions`); n != 1 {
		t.Errorf("accept mode wrote suggestions: %d in all", n)
	}
	if code, _ = run1(docx, "--docx-changes", "sideways"); code == 0 {
		t.Error("an unknown --docx-changes was accepted")
	}
}
