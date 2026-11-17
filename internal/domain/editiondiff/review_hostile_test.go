package editiondiff_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/editiondiff"
)

func hostileBook(t *testing.T, bodies ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, data string, store bool) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if store {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	var man, spine, nav strings.Builder
	for i := range bodies {
		fmt.Fprintf(&man, `<item id="i%d" href="c%d.xhtml" media-type="application/xhtml+xml"/>`, i, i)
		fmt.Fprintf(&spine, `<itemref idref="i%d"/>`, i)
		fmt.Fprintf(&nav, `<li><a href="c%d.xhtml">Chapter number %d</a></li>`, i, i)
	}
	add("mimetype", "application/epub+zip", true)
	add("META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, false)
	add("OEBPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">urn:uuid:1</dc:identifier><dc:title>Hostile</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`+man.String()+`</manifest><spine>`+spine.String()+`</spine></package>`, false)
	add("OEBPS/nav.xhtml", `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>c</title></head><body><nav epub:type="toc"><ol>`+nav.String()+`</ol></nav></body></html>`, false)
	for i, b := range bodies {
		add(fmt.Sprintf("OEBPS/c%d.xhtml", i), `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>c</title></head><body><h1>Chapter number `+fmt.Sprint(i)+`</h1>`+b+`</body></html>`, false)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Text of a foreign book that looks like Scriobh syntax must be as inert in a taken chapter as it was when imported.
func TestReviewTakeKeepsHostileBookTextInert(t *testing.T) {
	f := newFx(t)
	hostile := `<p>Before [[Alice]] and %%a private note%% and &lt;scriobh-ghost&gt;ghost&lt;/scriobh-ghost&gt; after.</p>` +
		`<p>---</p><p>title: x</p><p>---</p>` +
		`<p>[click](javascript:alert(1)) ![p](http://evil.example/x.png) http://evil.example/</p><p>Tail paragraph with enough ordinary words to match.</p>`
	data := hostileBook(t, hostile, `<p>Second chapter has its own ordinary words for matching purposes.</p>`)
	ap := f.importBook(data, "Hostile")
	pid, eid := ap.ProjectID, f.edition(ap.ProjectID)
	d := f.docByTitle(pid, "Chapter number 0")
	orig := f.doc(d.ID)
	t.Logf("imported content: %q", orig.Content)
	r := f.diff(pid, eid, "")
	st := stepFor(t, r, "Chapter number 0")
	if st.Status != editiondiff.StatusUnchanged {
		t.Errorf("imported and left alone, the chapter is %q (baseline differs from the document)", st.Status)
	}
	f.setBody(d.ID, "Rewritten.\n")
	doc := f.doc(d.ID)
	st = stepFor(t, f.diff(pid, eid, ""), "Chapter number 0")
	if st.Offer == nil || !st.Offer.Take {
		t.Fatalf("not takeable (the fixture is wrong): %+v", st)
	}
	_, err := f.call(f.w, "edition.take", editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision})
	if err != nil {
		t.Fatalf("take refused: %v", err)
	}
	if got := f.doc(d.ID).Content; got != orig.Content {
		t.Errorf("taken text differs from the imported text:\n%q\n%q", got, orig.Content)
	}
}
