package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

func tinyEpub(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	put := func(name, body string, store bool) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if store {
			h.Method = zip.Store
		}
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(body))
	}
	put("mimetype", "application/epub+zip", true)
	put("META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="c.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, false)
	put("c.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="i"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="i">urn:isbn:1</dc:identifier><dc:title>The Salt Road</dc:title><dc:language>en</dc:language><dc:creator>Ada Fixture</dc:creator><dc:date>2019-01-01</dc:date></metadata>
<manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="a" href="a.xhtml" media-type="application/xhtml+xml"/><item id="b" href="b.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="a"/><itemref idref="b"/></spine></package>`, false)
	put("nav.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><ol><li><a href="a.xhtml">The Road</a></li><li><a href="b.xhtml">The Wall</a></li></ol></nav></body></html>`, false)
	put("a.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>The Road</h1><p>They came at dusk.</p></body></html>`, false)
	put("b.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>The Wall</h1><p>Rain.</p><hr/><p>More rain.</p></body></html>`, false)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// `scriobh import book.epub`: previewed, then created with its edition.
func TestImportCommandEpub(t *testing.T) {
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
	book := filepath.Join(t.TempDir(), "salt-road.epub")
	if err := os.WriteFile(book, tinyEpub(t), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"import", book}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"EPUB", "The Road", "The Wall", "cut by toc", "Edition label: The Salt Road (2019)", "Nothing was written"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("preview lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", book, "--apply", "--edition-label", "First edition"}, &out, &errb); code != 0 {
		t.Fatalf("apply: exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "kept, as it was received, as edition edn_") {
		t.Errorf("apply: %s", out.String())
	}
	if n := count(t, dir, `SELECT count(*) FROM editions WHERE label = 'First edition' AND has_file = 1`); n != 1 {
		t.Errorf("editions: %d", n)
	}
	if n := count(t, dir, `SELECT count(*) FROM project_sources WHERE kind = 'epub'`); n != 1 {
		t.Errorf("epub provenance rows: %d", n)
	}
	// An option that is not one is refused before anything is read.
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", book, "--epub-sections", "nonsense"}, &out, &errb); code == 0 {
		t.Error("an unknown --epub-sections was accepted")
	}
}
