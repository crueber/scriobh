package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

// `scriobh import book.docx` and `scriobh import ./Vault --layout obsidian`.
func TestImportCommandWordAndObsidian(t *testing.T) {
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

	// A small Word document: a heading and two paragraphs, in the form every writer of DOCX makes.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	put := func(name, body string) {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	put("[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`)
	put("word/styles.xml", `<w:styles `+ns+`><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`)
	put("word/document.xml", `<w:document `+ns+`><w:body><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>The Arrival</w:t></w:r></w:p><w:p><w:r><w:t>They came at dusk.</w:t></w:r></w:p><w:p><w:r><w:t>#</w:t></w:r></w:p><w:p><w:r><w:t>She waited.</w:t></w:r></w:p></w:body></w:document>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	docx := filepath.Join(t.TempDir(), "the-salt-road.docx")
	if err := os.WriteFile(docx, zb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"import", docx}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"Word document", "The Arrival", "Nothing was written"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("preview lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", docx, "--apply"}, &out, &errb); code != 0 {
		t.Fatalf("apply: exit %d: %s", code, errb.String())
	}
	if n := count(t, dir, `SELECT count(*) FROM project_sources WHERE kind = 'docx'`); n != 1 {
		t.Errorf("docx provenance rows: %d", n)
	}
	if n := count(t, dir, `SELECT count(*) FROM documents WHERE content LIKE '%* * *%'`); n != 1 {
		t.Errorf("the scene break was not read")
	}

	// An Obsidian vault, chosen with --layout.
	vault := filepath.Join(t.TempDir(), "Vault")
	writeTree(t, vault, map[string]string{
		"One.md":             "See [[Two#Heading]] and ![[pic.png]].\n\n%%private%%\n",
		"Two.md":             "Second.\n",
		".obsidian/app.json": "{}",
		"Board.canvas":       "{}",
	})
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", vault, "--layout", "obsidian"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"Obsidian vault", "canvas", "image"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("vault preview lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", vault, "--layout", "hugo"}, &out, &errb); code != 2 {
		t.Errorf("an unknown layout: exit %d", code)
	}
}

// `scriobh import book.txt`: a plain text file is read, its plain "Chapter" lines are offered and not cut, and a second import of the same
// file into the project it made is flagged.
func TestImportCommandPlainTextAndTheRepeatWarning(t *testing.T) {
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
	txt := filepath.Join(t.TempDir(), "the-salt-road.txt")
	body := "Chapter 1\n\nThey came at dusk and the gulls wheeled over the grey water.\n\nChapter 2\n\nShe waited by the window until the light had gone.\n"
	if err := os.WriteFile(txt, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"import", txt}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"plain text file", "No chapter breaks were found", "--docx-break", "Nothing was written"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("preview lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", txt, "--docx-split", "label", "--apply"}, &out, &errb); code != 0 {
		t.Fatalf("apply: exit %d: %s%s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "2 documents") {
		t.Errorf("cut at the labels the writer chose:\n%s", out.String())
	}
	m := regexp.MustCompile(`\((prj_[0-9A-Za-z]+)\)`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no project id in %s", out.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", txt, "--into", m[1]}, &out, &errb); code != 0 {
		t.Fatalf("into: exit %d: %s%s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "Already imported") || !strings.Contains(out.String(), "import undo") {
		t.Errorf("the repeat is not flagged:\n%s", out.String())
	}
}
