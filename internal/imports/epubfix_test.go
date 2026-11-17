package imports_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

// Books for the EPUB tests are built here, from the specifications and from what the
// tools that make books are known to write. No real file of any tool was available:
// each fixture is a faithful miniature of one tool's habits, and says which.

type zent struct {
	name  string
	data  []byte
	store bool
}

func ze(name, data string) zent { return zent{name: name, data: []byte(data)} }

// zipEntries writes a zip with the mimetype entry first and stored, as the format asks.
func zipEntries(t testing.TB, ents ...zent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}
		if e.store || e.name == "mimetype" {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const epubMime = "application/epub+zip"

func containerXML(opf string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="` + opf + `" media-type="application/oebps-package+xml"/></rootfiles></container>`
}

const xhtmlHead = `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="en"><head><title>%s</title><link rel="stylesheet" type="text/css" href="../style.css"/></head><body%s>`

func page(title, bodyAttr, body string) string {
	return fmt.Sprintf(xhtmlHead, title, bodyAttr) + body + "</body></html>"
}

func pngBytes(w, h int, seed uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{seed + uint8(x*7), uint8(y * 5), 90, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// salt3 is a small EPUB 3 book written the way Sigil writes one: a navigation document,
// an NCX for old readers, one file per chapter, epub:type on sections, footnotes as
// asides in a notes file, a cover image with a cover page.
func salt3(t testing.TB) []byte {
	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="en">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
<dc:identifier id="bookid">urn:isbn:9780000000002</dc:identifier>
<dc:title id="t1">The Salt Road</dc:title>
<meta refines="#t1" property="title-type">main</meta>
<dc:title id="t2">A Novel of the Marsh</dc:title>
<meta refines="#t2" property="title-type">subtitle</meta>
<dc:creator id="c1">Ada Fixture</dc:creator><meta refines="#c1" property="role" scheme="marc:relators">aut</meta>
<dc:creator id="c2">Bo Translator</dc:creator><meta refines="#c2" property="role" scheme="marc:relators">trl</meta>
<dc:language>en-GB</dc:language><dc:publisher>Fixture House</dc:publisher><dc:date>2019-05-01</dc:date>
<meta property="dcterms:modified">2020-01-02T03:04:05Z</meta>
<meta name="cover" content="cover-img"/>
</metadata>
<manifest>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
<item id="css" href="style.css" media-type="text/css"/>
<item id="cover-img" href="images/cover.png" media-type="image/png" properties="cover-image"/>
<item id="pic1" href="images/map.png" media-type="image/png"/>
<item id="cover" href="text/cover.xhtml" media-type="application/xhtml+xml"/>
<item id="title" href="text/title.xhtml" media-type="application/xhtml+xml"/>
<item id="ded" href="text/dedication.xhtml" media-type="application/xhtml+xml"/>
<item id="p1" href="text/part1.xhtml" media-type="application/xhtml+xml"/>
<item id="c1" href="text/ch01.xhtml" media-type="application/xhtml+xml"/>
<item id="c2" href="text/ch02.xhtml" media-type="application/xhtml+xml"/>
<item id="ep" href="text/epilogue.xhtml" media-type="application/xhtml+xml"/>
<item id="notes" href="text/notes.xhtml" media-type="application/xhtml+xml"/>
<item id="ack" href="text/ack.xhtml" media-type="application/xhtml+xml"/>
</manifest>
<spine toc="ncx"><itemref idref="cover" linear="no"/><itemref idref="title"/><itemref idref="ded"/><itemref idref="p1"/><itemref idref="c1"/><itemref idref="c2"/><itemref idref="ep"/><itemref idref="notes"/><itemref idref="ack"/></spine>
</package>`
	nav := page("Contents", "", `<nav epub:type="toc" id="toc"><h1>Contents</h1><ol>
<li><a href="text/dedication.xhtml">Dedication</a></li>
<li><a href="text/part1.xhtml">Part One: The Marsh</a><ol>
<li><a href="text/ch01.xhtml">The Road</a></li>
<li><a href="text/ch02.xhtml">The Wall</a></li></ol></li>
<li><a href="text/epilogue.xhtml">Epilogue</a></li>
<li><a href="text/ack.xhtml">Acknowledgments</a></li>
</ol></nav><nav epub:type="landmarks" hidden="hidden"><ol><li><a epub:type="cover" href="text/cover.xhtml">Cover</a></li><li><a epub:type="bodymatter" href="text/part1.xhtml">Start</a></li></ol></nav>`)
	ncx := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE ncx PUBLIC "-//NISO//DTD ncx 2005-1//EN" "http://www.daisy.org/z3986/2005/ncx-2005-1.dtd">
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><head><meta name="dtb:uid" content="urn:isbn:9780000000002"/></head><docTitle><text>The Salt Road</text></docTitle><navMap>
<navPoint id="n1" playOrder="1"><navLabel><text>Dedication</text></navLabel><content src="text/dedication.xhtml"/></navPoint>
</navMap></ncx>`
	return zipEntries(t,
		ze("mimetype", epubMime),
		ze("META-INF/container.xml", containerXML("OEBPS/content.opf")),
		ze("OEBPS/content.opf", opf),
		ze("OEBPS/nav.xhtml", nav),
		ze("OEBPS/toc.ncx", ncx),
		ze("OEBPS/style.css", `.italic{font-style:italic} span.smallcaps{font-variant:small-caps} .b{font-weight:bold} .del{text-decoration:line-through}`),
		zent{name: "OEBPS/images/cover.png", data: pngBytes(60, 80, 1), store: true},
		zent{name: "OEBPS/images/map.png", data: pngBytes(40, 30, 40), store: true},
		ze("OEBPS/text/cover.xhtml", page("Cover", ` epub:type="cover"`, `<section epub:type="cover"><img src="../images/cover.png" alt="Cover"/></section>`)),
		ze("OEBPS/text/title.xhtml", page("The Salt Road", ` epub:type="frontmatter"`, `<section epub:type="titlepage"><h1>The Salt Road</h1><p>Ada Fixture</p></section>`)),
		ze("OEBPS/text/dedication.xhtml", page("Dedication", ` epub:type="frontmatter"`, `<section epub:type="dedication"><h2>Dedication</h2><p><em>For the marsh.</em></p></section>`)),
		ze("OEBPS/text/part1.xhtml", page("Part One", ` epub:type="bodymatter"`, `<section epub:type="part"><h1>Part One: The Marsh</h1><p class="epigraph">The water keeps what it is given.</p></section>`)),
		ze("OEBPS/text/ch01.xhtml", page("The Road", "", `<section epub:type="chapter"><h2>Chapter 1<br/>The Road</h2>
<p>They came at <span class="italic">dusk</span>, the three of them.<a epub:type="noteref" href="notes.xhtml#fn1">1</a></p>
<p>The bus left them at the crossroads &amp; went on.</p>
<hr/>
<p>She waited. “Nothing,” she said.<sup><a epub:type="noteref" href="notes.xhtml#fn2">2</a></sup></p>
<p><img src="../images/map.png" alt="A map of the marsh"/></p>
<h3>The Causeway</h3>
<p>Another line.</p></section>`)),
		ze("OEBPS/text/ch02.xhtml", page("The Wall", "", `<section epub:type="chapter"><h2>Chapter 2<br/>The Wall</h2><p>Rain.</p><p class="scenebreak">* * *</p><p>More rain.</p></section>`)),
		ze("OEBPS/text/epilogue.xhtml", page("Epilogue", "", `<section epub:type="epilogue"><h2>Epilogue</h2><p>Years later.</p></section>`)),
		ze("OEBPS/text/notes.xhtml", page("Notes", "", `<section epub:type="endnotes"><h2>Notes</h2><ol>
<aside epub:type="footnote" id="fn1"><p>The first note, <em>with</em> emphasis. <a href="ch01.xhtml#r1">↩</a></p></aside>
<aside epub:type="footnote" id="fn2"><p>The second note.</p></aside></ol></section>`)),
		ze("OEBPS/text/ack.xhtml", page("Acknowledgments", ` epub:type="backmatter"`, `<section epub:type="acknowledgments"><h2>Acknowledgments</h2><p>Thanks to all.</p></section>`)),
	)
}

func has(t testing.TB, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func lacks(t testing.TB, s string, nots ...string) {
	t.Helper()
	for _, w := range nots {
		if strings.Contains(s, w) {
			t.Errorf("unexpected %q in:\n%s", w, s)
		}
	}
}

// rewriteTitle makes the book into another one: the same chapters, a different text in the first.
func rewriteTitle(t testing.TB, book []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	if err != nil {
		t.Fatal(err)
	}
	var ents []zent
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		d := b.Bytes()
		if strings.HasSuffix(f.Name, "ch01.xhtml") {
			d = bytes.ReplaceAll(d, []byte("They came at"), []byte("They left at"))
		}
		ents = append(ents, zent{name: f.Name, data: d})
	}
	return zipEntries(t, ents...)
}

type mbChapter struct {
	file, title, body string // file under OEBPS/; the body is what goes in <body>
	bodyAttr          string
	skipNav           bool
}

// miniBook is a plain EPUB 3: the chapters in the order given, in the navigation (unless skipNav) and the spine.
func miniBook(t testing.TB, title string, chs []mbChapter, extra ...zent) []byte {
	t.Helper()
	var man, spine, nav strings.Builder
	for i, c := range chs {
		fmt.Fprintf(&man, `<item id="i%d" href="%s" media-type="application/xhtml+xml"/>`+"\n", i, c.file)
		fmt.Fprintf(&spine, `<itemref idref="i%d"/>`+"\n", i)
		if !c.skipNav {
			fmt.Fprintf(&nav, `<li><a href="%s">%s</a></li>`+"\n", c.file, c.title)
		}
	}
	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="id">urn:uuid:11111111-2222-3333-4444-555555555555</dc:identifier><dc:title>` + title + `</dc:title><dc:language>en</dc:language><dc:creator>An Author</dc:creator></metadata>
<manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>` + man.String() + `</manifest><spine>` + spine.String() + `</spine></package>`
	ents := []zent{
		ze("mimetype", epubMime),
		ze("META-INF/container.xml", containerXML("OEBPS/content.opf")),
		ze("OEBPS/content.opf", opf),
		ze("OEBPS/nav.xhtml", page("Contents", "", `<nav epub:type="toc"><ol>`+nav.String()+`</ol></nav>`)),
	}
	for _, c := range chs {
		ents = append(ents, ze("OEBPS/"+c.file, page(c.title, c.bodyAttr, c.body)))
	}
	return zipEntries(t, append(ents, extra...)...)
}

// replaceInZip returns the book with old replaced by new in one file; it fails if the file or the text is not there.
func replaceInZip(t testing.TB, book []byte, name, old, new string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	if err != nil {
		t.Fatal(err)
	}
	var ents []zent
	done := false
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		d := b.Bytes()
		if f.Name == name {
			if !bytes.Contains(d, []byte(old)) {
				t.Fatalf("%s has no %q", name, old)
			}
			d = bytes.Replace(d, []byte(old), []byte(new), 1)
			done = true
		}
		ents = append(ents, zent{name: f.Name, data: d, store: f.Method == zip.Store})
	}
	if !done {
		t.Fatalf("no %s in the book", name)
	}
	return zipEntries(t, ents...)
}
