package mirror_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/imports"
)

func miniEpub(t *testing.T) []byte {
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
	put("c.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="i"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="i">urn:isbn:1</dc:identifier><dc:title>The Salt Road</dc:title><dc:language>en</dc:language></metadata>
<manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="a" href="a.xhtml" media-type="application/xhtml+xml"/><item id="b" href="b.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="a"/><itemref idref="b"/></spine></package>`, false)
	put("nav.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><ol><li><a href="a.xhtml">The Road</a></li><li><a href="b.xhtml">The Wall</a></li></ol></nav></body></html>`, false)
	put("a.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>The Road</h1><p>They came at dusk.</p></body></html>`, false)
	put("b.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>The Wall</h1><p>Rain.</p></body></html>`, false)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// What the mirror (and so a Git push) carries of an imported book is metadata: the label, the hash, the
// size and the date, in project.json. The book itself is never copied, in any file, under any name.
func TestEditionIsInTheMirrorAsMetadataOnly(t *testing.T) {
	e := newEnv(t)
	e.start()
	book := miniEpub(t)
	pv := e.call(e.owner, "import.preview", imports.PreviewIn{Name: "salt.epub", Epub: book}).(imports.PreviewOut)
	ap := e.call(e.owner, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "salt.epub", Epub: book, EditionLabel: "First edition"}).(imports.ApplyOut)
	e.settle(ap.ProjectID)
	root := e.root(ap.ProjectID)
	var m struct {
		Editions []struct {
			ID, Kind, Label, Identifier, SHA256 string
			Size                                int64
			ImportedAt                          string `json:"imported_at"`
			Chapters                            int
		}
	}
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "project.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Editions) != 1 || m.Editions[0].Label != "First edition" || m.Editions[0].SHA256 != editions.SumOf(book) || m.Editions[0].Size != int64(len(book)) ||
		m.Editions[0].Identifier != "urn:isbn:1" || m.Editions[0].Chapters != 2 || m.Editions[0].ImportedAt == "" || m.Editions[0].Kind != "epub" {
		t.Errorf("editions in project.json: %+v", m.Editions)
	}
	for name, content := range files(t, root) {
		if strings.HasSuffix(strings.ToLower(name), ".epub") || strings.Contains(name, "editions") || strings.Contains(content, "application/epub+zip") || strings.HasPrefix(content, "PK") {
			t.Errorf("the mirror carries the book itself: %s", name)
		}
	}
	// A project that was never imported from a book has no editions key at all.
	other := e.project("Plain")
	e.settle(other)
	if strings.Contains(read(t, filepath.Join(e.root(other), "project.json")), `"editions"`) {
		t.Error("a project with no edition lists editions")
	}
}
