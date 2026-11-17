package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// Books shaped like what the common tools make. They are miniatures built from each tool's
// documented and well-known habits (file names, classes, how it puts footnotes and
// anchors), not output of the tools: none of Calibre, Sigil, Vellum, Pandoc, Kindle
// Previewer or a Project Gutenberg download was available to copy from.

// calibre2 is what Calibre makes when it converts a Word or HTML book to EPUB 2: files split
// by size (index_split_NNN.html) with the contents pointing at fragments, emphasis in
// classes of a stylesheet, a continuation file the contents never name, an NCX and a guide.
func calibre2(t testing.TB) []byte {
	opf := `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" unique-identifier="uuid_id" version="2.0">
<metadata xmlns:opf="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:calibre="http://calibre.kovidgoyal.net/2009/metadata">
<dc:language>en</dc:language><dc:title>The Lamplighter</dc:title>
<dc:creator opf:file-as="Marsh, Ina" opf:role="aut">Ina Marsh</dc:creator>
<meta name="calibre:series" content="Harbour Tales"/><meta name="calibre:series_index" content="2.0"/>
<dc:contributor opf:role="bkp">calibre (4.23.0) [https://calibre-ebook.com]</dc:contributor>
<dc:identifier id="uuid_id" opf:scheme="uuid">0c1f2e3d-4b5a-6978-8a9b-0c1d2e3f4a5b</dc:identifier>
<dc:identifier opf:scheme="ISBN">9780000000019</dc:identifier>
<dc:date>2020-06-15T10:00:00+00:00</dc:date><dc:publisher>Harbour Press</dc:publisher>
<meta name="cover" content="cover"/>
</metadata>
<manifest>
<item href="cover.jpeg" id="cover" media-type="image/jpeg"/>
<item href="stylesheet.css" id="css" media-type="text/css"/>
<item href="titlepage.xhtml" id="titlepage" media-type="application/xhtml+xml"/>
<item href="index_split_000.html" id="id1" media-type="application/xhtml+xml"/>
<item href="index_split_001.html" id="id2" media-type="application/xhtml+xml"/>
<item href="index_split_002.html" id="id3" media-type="application/xhtml+xml"/>
<item href="toc.ncx" media-type="application/x-dtbncx+xml" id="ncx"/>
<item href="images/lamp.png" media-type="image/png" id="img1"/>
</manifest>
<spine toc="ncx"><itemref idref="titlepage"/><itemref idref="id1"/><itemref idref="id2"/><itemref idref="id3"/></spine>
<guide><reference href="titlepage.xhtml" type="cover" title="Cover"/></guide>
</package>`
	ncx := `<?xml version='1.0' encoding='utf-8'?>
<!DOCTYPE ncx PUBLIC "-//NISO//DTD ncx 2005-1//EN" "http://www.daisy.org/z3986/2005/ncx-2005-1.dtd">
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1" xml:lang="eng"><head><meta content="0c1f2e3d-4b5a-6978-8a9b-0c1d2e3f4a5b" name="dtb:uid"/><meta content="2" name="dtb:depth"/></head>
<docTitle><text>The Lamplighter</text></docTitle><navMap>
<navPoint id="n1" playOrder="1"><navLabel><text>Chapter One</text></navLabel><content src="index_split_000.html#calibre_toc_1"/></navPoint>
<navPoint id="n2" playOrder="2"><navLabel><text>Chapter Two</text></navLabel><content src="index_split_000.html#calibre_toc_2"/></navPoint>
<navPoint id="n3" playOrder="3"><navLabel><text>Chapter Three</text></navLabel><content src="index_split_002.html#calibre_toc_3"/></navPoint>
</navMap></ncx>`
	return zipEntries(t,
		ze("mimetype", epubMime),
		ze("META-INF/container.xml", containerXML("content.opf")),
		ze("content.opf", opf),
		ze("toc.ncx", ncx),
		ze("stylesheet.css", `.calibre{display:block;font-size:1em;padding-left:0;padding-right:0;margin:0 5pt} .calibre1{display:block;font-size:2em;font-weight:bold;margin:.8em 0} .calibre2{display:block;text-indent:1em;margin:0} .calibre3{font-style:italic} .calibre4{font-weight:bold} .calibre5{display:block;text-align:center;margin:1em 0}`),
		zent{name: "cover.jpeg", data: pngBytes(50, 70, 9), store: true},
		zent{name: "images/lamp.png", data: pngBytes(20, 20, 50), store: true},
		ze("titlepage.xhtml", `<?xml version='1.0' encoding='utf-8'?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd"><html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en"><head><title>Cover</title></head><body><div><svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" version="1.1" width="100%" height="100%" viewBox="0 0 50 70" preserveAspectRatio="none"><image width="50" height="70" xlink:href="cover.jpeg"/></svg></div></body></html>`),
		ze("index_split_000.html", `<?xml version='1.0' encoding='utf-8'?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.1//EN" "http://www.w3.org/TR/xhtml11/DTD/xhtml11.dtd"><html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en"><head><title>Unknown</title><link rel="stylesheet" type="text/css" href="stylesheet.css"/></head><body class="calibre">
<h1 class="calibre1" id="calibre_toc_1">Chapter One</h1>
<p class="calibre2">The lamps along the quay were lit by one man, and he was <span class="calibre3">never</span> thanked. He was <span class="calibre4">proud</span> of that.</p>
<p class="calibre2">Harry said the sea<a id="fnref-1" href="#fn-1"><sup>1</sup></a> was patient.</p>
<p class="calibre5">&#160;</p><p class="calibre5">* * *</p>
<p class="calibre2">Morning came.</p>
<h1 class="calibre1" id="calibre_toc_2">Chapter Two</h1>
<p class="calibre2">Two begins here.<a id="fnref-2" href="#fn-2"><sup>2</sup></a></p>
<div class="calibre"><p class="calibre2"><a id="fn-1" href="#fnref-1">1</a> The sea is not patient.</p><p class="calibre2"><a id="fn-2" href="#fnref-2">2</a> Another note.</p></div>
</body></html>`),
		ze("index_split_001.html", `<?xml version='1.0' encoding='utf-8'?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Unknown</title></head><body class="calibre"><p class="calibre2">Two goes on in a file the contents never name, because Calibre split the chapter by size.</p><p class="calibre2"><img src="images/lamp.png" alt="A lamp"/></p></body></html>`),
		ze("index_split_002.html", `<?xml version='1.0' encoding='utf-8'?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Unknown</title></head><body class="calibre"><h1 class="calibre1" id="calibre_toc_3">Chapter Three</h1><p class="calibre2">Three.</p></body></html>`),
	)
}

func TestEpub2FromCalibre(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := calibre2(t)
	pv := e.previewConv(imports.PreviewIn{Name: "lamplighter.epub", Epub: book})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter One", "chapter:Chapter Two", "chapter:Chapter Three"})
	if pv.Epub.Version != "2.0" || pv.Epub.Strategy != "toc" || pv.Epub.Contents != "NCX" {
		t.Errorf("info: %+v", pv.Epub)
	}
	if pv.Project.Title != "The Lamplighter" || pv.Project.Author != "Ina Marsh" || pv.Epub.Series != "Harbour Tales" || pv.Epub.Publisher != "Harbour Press" {
		t.Errorf("project %+v, info %+v", pv.Project, pv.Epub)
	}
	if pv.Epub.Identifier == "" || pv.Epub.Date == "" || len(pv.Epub.Contributors) == 0 {
		t.Errorf("identifier %q, date %q, contributors %v", pv.Epub.Identifier, pv.Epub.Date, pv.Epub.Contributors)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "lamplighter.epub", Epub: book})
	body := func(title string) string {
		var c string
		if err := e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = ?`, ap.ProjectID, title).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	one := body("Chapter One")
	has(t, one, "*never*", "**proud**", "the sea[^1] was patient", "[^1]: The sea is not patient.", "\n* * *\n", "Morning came.")
	lacks(t, one, "Chapter One", "Two begins", "&#160;")
	two := body("Chapter Two")
	has(t, two, "Two begins here.[^1]", "[^1]: Another note.", "a file the contents never name", "![A lamp](/files/att_")
	lacks(t, two, "The sea is not patient")
	if strings.Count(one, "* * *") != 1 {
		t.Errorf("the empty paragraph and the break made %d breaks", strings.Count(one, "* * *"))
	}
	if body("Chapter Three") != "Three.\n" {
		t.Errorf("three: %q", body("Chapter Three"))
	}
	// The cover is the picture the package names; its page is not a chapter.
	if pv.Epub.Cover == nil || !pv.Epub.Cover.Kept || pv.Epub.Cover.Path != "cover.jpeg" {
		t.Errorf("cover: %+v", pv.Epub.Cover)
	}
}

// gutenberg2 is a Project Gutenberg EPUB: one long text file with the contents naming
// anchors inside headings (<h2><a id="..."></a>Chapter I.</h2>), the licence as a file of its
// own before and after, and footnotes the old way: an anchor, a label "[1]" and a note that
// links back, with no epub:type anywhere.
func gutenberg2(t testing.TB) []byte {
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
<dc:identifier id="id">http://www.gutenberg.org/ebooks/1</dc:identifier><dc:creator opf:file-as="Austen, Jane" opf:role="aut">Jane Austen</dc:creator>
<dc:title>Pride and Pleasure</dc:title><dc:language>en</dc:language><dc:rights>Public domain in the USA.</dc:rights><dc:date opf:event="publication">1813-01-28</dc:date>
<dc:subject>England -- Fiction</dc:subject><meta name="cover" content="item1"/></metadata>
<manifest><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/><item id="item1" href="cover.png" media-type="image/png"/>
<item id="item2" href="wrap0000.html" media-type="application/xhtml+xml"/><item id="item3" href="pg1.html" media-type="application/xhtml+xml"/><item id="item4" href="wrap0001.html" media-type="application/xhtml+xml"/></manifest>
<spine toc="ncx"><itemref idref="item2"/><itemref idref="item3"/><itemref idref="item4"/></spine></package>`
	ncx := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE ncx PUBLIC "-//NISO//DTD ncx 2005-1//EN" "http://www.daisy.org/z3986/2005/ncx-2005-1.dtd">
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1"><head/><docTitle><text>Pride and Pleasure</text></docTitle><navMap>
<navPoint id="np-1" playOrder="1"><navLabel><text>Chapter I.</text></navLabel><content src="pg1.html#link2HCH0001"/></navPoint>
<navPoint id="np-2" playOrder="2"><navLabel><text>Chapter II.</text></navLabel><content src="pg1.html#link2HCH0002"/></navPoint>
<navPoint id="np-3" playOrder="3"><navLabel><text>Chapter III.</text></navLabel><content src="pg1.html#link2HCH0003"/></navPoint>
</navMap></ncx>`
	pg := `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Strict//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-strict.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en"><head><title>Pride and Pleasure</title><style type="text/css">body { margin-left: 20%; } p.i1 { text-indent: 1em }</style></head><body>
<div class="chapter"><h1>PRIDE AND PLEASURE</h1><p class="center">by Jane Austen</p></div>
<div class="chapter"><h2><a id="link2HCH0001"></a>Chapter I.</h2>
<p>It is a truth universally acknowledged,<a id="FNanchor_1" href="#Footnote_1" class="fnanchor">[1]</a> that a single man in possession of a good fortune must be in want of a wife.</p>
<p>However little known the feelings or views of such a man&#8230;</p>
<hr style="width: 65%;" /></div>
<div class="chapter"><h2><a id="link2HCH0002"></a>Chapter II.</h2>
<p>Mr. Bennet was among the earliest of those who waited on Mr. Bingley.<a id="FNanchor_2" href="#Footnote_2" class="fnanchor">[2]</a></p>
<p class="i1">“Why not?” said she.</p></div>
<div class="chapter"><h2><a id="link2HCH0003"></a>Chapter III.</h2><p>Not all that Mrs. Bennet could say.</p></div>
<div class="footnotes"><h3>FOOTNOTES:</h3>
<div class="footnote"><p><a id="Footnote_1" href="#FNanchor_1" class="label">[1]</a> A truth, but a doubtful one.</p></div>
<div class="footnote"><p><a id="Footnote_2" href="#FNanchor_2" class="label">[2]</a> Mr. Bingley is rich.</p></div></div>
</body></html>`
	lic := func(title, text string) string {
		return `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + title + `</title></head><body><div><h2>` + title + `</h2><p>` + text + `</p></div></body></html>`
	}
	return zipEntries(t, ze("mimetype", epubMime), ze("META-INF/container.xml", containerXML("content.opf")), ze("content.opf", opf), ze("toc.ncx", ncx),
		zent{name: "cover.png", data: pngBytes(30, 40, 5), store: true},
		ze("wrap0000.html", lic("The Project Gutenberg eBook of Pride and Pleasure", "This eBook is for the use of anyone anywhere in the United States.")),
		ze("pg1.html", pg),
		ze("wrap0001.html", lic("A Project Gutenberg Licence", "Full terms follow.")))
}

func TestEpub2FromGutenberg(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := gutenberg2(t)
	pv := e.previewConv(imports.PreviewIn{Name: "pg1.epub", Epub: book})
	eq(t, outline(pv.Nodes), []string{
		"front_matter:The Project Gutenberg eBook of Pride and Pleasure",
		"front_matter:PRIDE AND PLEASURE",
		"chapter:Chapter I.", "chapter:Chapter II.", "chapter:Chapter III.",
		"back_matter:A Project Gutenberg Licence",
	})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "pg1.epub", Epub: book})
	body := func(title string) string {
		var c string
		if err := e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = ?`, ap.ProjectID, title).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	one := body("Chapter I.")
	has(t, one, "universally acknowledged,[^1] that a single man", "[^1]: A truth, but a doubtful one.", "feelings or views of such a man…")
	lacks(t, one, "[1]", "FOOTNOTES", "Mr. Bingley is rich", "Chapter I.")
	two := body("Chapter II.")
	has(t, two, "Bingley.[^1]", "[^1]: Mr. Bingley is rich.", "“Why not?” said she.")
	lacks(t, two, "A truth, but a doubtful one")
	if c := body("Chapter III."); strings.Contains(c, "FOOTNOTES") || strings.Contains(c, "Bingley is rich") || !strings.Contains(c, "Not all that Mrs. Bennet") {
		t.Errorf("chapter three (the last of the file, before the notes): %q", c)
	}
	if pv.Epub.Rights != "Public domain in the USA." || pv.Epub.Date != "1813-01-28" || len(pv.Epub.Subjects) != 1 {
		t.Errorf("metadata: %+v", pv.Epub)
	}
}

// pandoc3 is what Pandoc writes for EPUB 3: a section per chapter with its level in the class,
// footnotes as role="doc-noteref" links to an endnotes section with role="doc-backlink", a
// navigation document that is also in the spine (not linear), a title page, an NCX.
func pandoc3(t testing.TB) []byte {
	lorem := strings.Repeat("The marsh was quiet and the water was low. ", 8)
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package version="3.0" xmlns="http://www.idpf.org/2007/opf" unique-identifier="epub-id-1" prefix="ibooks: http://vocabulary.itunes.apple.com/rdf/ibooks/vocabulary-extensions-1.0/">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="epub-id-1">urn:uuid:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee</dc:identifier>
<dc:title id="epub-title-1">Field Notes</dc:title><dc:date id="epub-date">2021-03-04T00:00:00Z</dc:date><dc:language>en-US</dc:language>
<dc:creator id="epub-creator-1">Rae Fieldwork</dc:creator><meta refines="#epub-creator-1" property="role" scheme="marc:relators">aut</meta>
<meta property="dcterms:modified">2021-03-04T00:00:00Z</meta></metadata>
<manifest><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
<item id="style" href="styles/stylesheet1.css" media-type="text/css"/>
<item id="title_page_xhtml" href="text/title_page.xhtml" media-type="application/xhtml+xml"/>
<item id="ch001_xhtml" href="text/ch001.xhtml" media-type="application/xhtml+xml"/><item id="ch002_xhtml" href="text/ch002.xhtml" media-type="application/xhtml+xml"/>
<item id="ch003_xhtml" href="text/ch003.xhtml" media-type="application/xhtml+xml"/><item id="ch004_xhtml" href="text/ch004.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine toc="ncx"><itemref idref="title_page_xhtml" linear="yes"/><itemref idref="nav" linear="no"/><itemref idref="ch001_xhtml"/><itemref idref="ch002_xhtml"/><itemref idref="ch003_xhtml"/><itemref idref="ch004_xhtml"/></spine></package>`
	nav := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="en-US" xml:lang="en-US"><head><meta charset="utf-8" /><title>Field Notes</title></head><body>
<nav epub:type="toc" id="toc"><h1 id="toc-title">Table of Contents</h1><ol class="toc">
<li><a href="text/ch001.xhtml#preface">Preface</a></li>
<li><a href="text/ch002.xhtml#chapter-one">Chapter One</a><ol><li><a href="text/ch002.xhtml#background">Background</a></li><li><a href="text/ch002.xhtml#method">Method</a></li></ol></li>
<li><a href="text/ch003.xhtml#part-two">Part Two</a><ol><li><a href="text/ch003.xhtml#chapter-two">Chapter Two</a></li><li><a href="text/ch004.xhtml#chapter-three">Chapter Three</a></li></ol></li>
</ol></nav></body></html>`
	ncx := `<?xml version="1.0" encoding="UTF-8"?><ncx version="2005-1" xmlns="http://www.daisy.org/z3986/2005/ncx/"><head><meta name="dtb:uid" content="urn:uuid:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"/></head><docTitle><text>Field Notes</text></docTitle><navMap><navPoint id="navPoint-1"><navLabel><text>Preface</text></navLabel><content src="text/ch001.xhtml#preface"/></navPoint></navMap></ncx>`
	ch := func(body string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="en-US" xml:lang="en-US"><head><meta charset="utf-8" /><meta name="generator" content="pandoc" /><title>Field Notes</title><link rel="stylesheet" type="text/css" href="../styles/stylesheet1.css" /></head><body epub:type="bodymatter">` + body + `</body></html>`
	}
	return zipEntries(t, ze("mimetype", epubMime), ze("META-INF/container.xml", containerXML("EPUB/content.opf")), ze("EPUB/content.opf", opf), ze("EPUB/nav.xhtml", nav), ze("EPUB/toc.ncx", ncx),
		ze("EPUB/styles/stylesheet1.css", `code{white-space:pre-wrap} span.smallcaps{font-variant:small-caps} div.sourceCode{margin:1em 0}`),
		ze("EPUB/text/title_page.xhtml", `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Field Notes</title></head><body epub:type="frontmatter"><section epub:type="titlepage" class="titlepage"><h1 class="title">Field Notes</h1><p class="author">Rae Fieldwork</p></section></body></html>`),
		ze("EPUB/text/ch001.xhtml", ch(`<section id="preface" class="level1 unnumbered"><h1 class="unnumbered">Preface</h1><p>These are notes.</p></section>`)),
		ze("EPUB/text/ch002.xhtml", ch(`<section id="chapter-one" class="level1"><h1>Chapter One</h1><p>`+lorem+`</p>
<section id="background" class="level2"><h2>Background</h2><p>The background<a href="#fn1" class="footnote-ref" id="fnref1" role="doc-noteref"><sup>1</sup></a> of the study.</p></section>
<section id="method" class="level2"><h2>Method</h2><p>We walked, and <code>counted</code> things.<a href="#fn2" class="footnote-ref" id="fnref2" role="doc-noteref"><sup>2</sup></a></p><pre><code>step one
step two</code></pre></section></section>
<section class="footnotes" role="doc-endnotes"><hr /><ol><li id="fn1"><p>See the earlier survey.<a href="#fnref1" class="footnote-back" role="doc-backlink">↩︎</a></p></li><li id="fn2"><p>With a second paragraph.</p><p>Here it is.<a href="#fnref2" class="footnote-back" role="doc-backlink">↩︎</a></p></li></ol></section>`)),
		ze("EPUB/text/ch003.xhtml", ch(`<section id="part-two" class="level1"><h1>Part Two</h1></section><section id="chapter-two" class="level2"><h2>Chapter Two</h2><p>Two.</p></section>`)),
		ze("EPUB/text/ch004.xhtml", ch(`<section id="chapter-three" class="level2"><h2>Chapter Three</h2><p>Three.</p></section>`)),
	)
}

func TestEpub3FromPandoc(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := pandoc3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "field.epub", Epub: book})
	eq(t, outline(pv.Nodes), []string{
		"front_matter:Preface",
		"chapter:Chapter One", "  scene:Background", "  scene:Method",
		"part:Part Two", "  chapter:Chapter Two", "  chapter:Chapter Three",
	})
	if pv.Project.Language != "en-US" || pv.Project.Author != "Rae Fieldwork" {
		t.Errorf("project %+v", pv.Project)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "field.epub", Epub: book})
	body := func(title string) string {
		var c string
		if err := e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = ?`, ap.ProjectID, title).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	bg := body("Background")
	has(t, bg, "The background[^1] of the study.", "[^1]: See the earlier survey.")
	lacks(t, bg, "↩", "Chapter One")
	m := body("Method")
	has(t, m, "`counted`", "[^1]: With a second paragraph.", "```\nstep one\nstep two\n```")
	lacks(t, m, "↩", "footnote")
	one := body("Chapter One")
	if strings.Contains(one, "See the earlier survey") || strings.Contains(one, "---") {
		t.Errorf("the notes section was not taken out: %q", one)
	}
}

// vellum3 is shaped like the books Vellum writes: front matter pages each in a file of their own
// (title page, copyright, dedication, epigraph), chapter headings made of spans, an ornament
// image for a scene break, back matter (Acknowledgements, About the Author, Also By) listed in
// the navigation, and no NCX.
func vellum3(t testing.TB) []byte {
	opf := `<?xml version="1.0" encoding="UTF-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="bookid">urn:uuid:5c0b8f7e-1111-4222-8333-444455556666</dc:identifier><dc:title>Harbour Lights</dc:title><dc:creator>Nell Quay</dc:creator><dc:language>en</dc:language><dc:publisher>Quay Books</dc:publisher><meta property="dcterms:modified">2022-02-02T00:00:00Z</meta></metadata>
<manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="orn" href="Images/ornament.png" media-type="image/png"/>
<item id="s1" href="Text/titlepage.xhtml" media-type="application/xhtml+xml"/><item id="s2" href="Text/copyright.xhtml" media-type="application/xhtml+xml"/><item id="s3" href="Text/dedication.xhtml" media-type="application/xhtml+xml"/><item id="s4" href="Text/epigraph.xhtml" media-type="application/xhtml+xml"/>
<item id="s5" href="Text/chapter-1.xhtml" media-type="application/xhtml+xml"/><item id="s6" href="Text/chapter-2.xhtml" media-type="application/xhtml+xml"/>
<item id="s7" href="Text/ack.xhtml" media-type="application/xhtml+xml"/><item id="s8" href="Text/about.xhtml" media-type="application/xhtml+xml"/><item id="s9" href="Text/also.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine><itemref idref="s1"/><itemref idref="s2"/><itemref idref="s3"/><itemref idref="s4"/><itemref idref="s5"/><itemref idref="s6"/><itemref idref="s7"/><itemref idref="s8"/><itemref idref="s9"/></spine></package>`
	nav := page("Contents", "", `<nav epub:type="toc"><ol><li><a href="Text/titlepage.xhtml">Title Page</a></li><li><a href="Text/copyright.xhtml">Copyright</a></li><li><a href="Text/dedication.xhtml">Dedication</a></li><li><a href="Text/epigraph.xhtml">Epigraph</a></li>
<li><a href="Text/chapter-1.xhtml">Chapter 1: Low Tide</a></li><li><a href="Text/chapter-2.xhtml">Chapter 2: The Keeper</a></li><li><a href="Text/ack.xhtml">Acknowledgements</a></li><li><a href="Text/about.xhtml">About the Author</a></li><li><a href="Text/also.xhtml">Also by Nell Quay</a></li></ol></nav>`)
	pg := func(title, typ, body string) string {
		return page(title, "", `<section epub:type="`+typ+`" class="`+typ+`">`+body+`</section>`)
	}
	return zipEntries(t, ze("mimetype", epubMime), ze("META-INF/container.xml", containerXML("OEBPS/content.opf")), ze("OEBPS/content.opf", opf), ze("OEBPS/nav.xhtml", nav),
		zent{name: "OEBPS/Images/ornament.png", data: pngBytes(10, 4, 8), store: true},
		ze("OEBPS/Text/titlepage.xhtml", pg("Title Page", "titlepage", `<h1 class="title">Harbour Lights</h1><p class="author">Nell Quay</p>`)),
		ze("OEBPS/Text/copyright.xhtml", pg("Copyright", "copyright-page", `<p>Copyright © 2022 Nell Quay</p><p>All rights reserved.</p>`)),
		ze("OEBPS/Text/dedication.xhtml", pg("Dedication", "dedication", `<p><em>For the keeper.</em></p>`)),
		ze("OEBPS/Text/epigraph.xhtml", pg("Epigraph", "epigraph", `<blockquote><p>The sea is history.</p><p>—Derek Walcott</p></blockquote>`)),
		ze("OEBPS/Text/chapter-1.xhtml", pg("Chapter 1", "chapter", `<h2 class="chapter-title"><span class="chapter-number">Chapter 1</span><span class="chapter-name">Low Tide</span></h2><p class="first">The tide was out.</p><div class="ornament"><img src="../Images/ornament.png" alt=""/></div><p>It came back.</p>`)),
		ze("OEBPS/Text/chapter-2.xhtml", pg("Chapter 2", "chapter", `<h2 class="chapter-title"><span class="chapter-number">Chapter 2</span><span class="chapter-name">The Keeper</span></h2><p class="first">The keeper lit the lamp.</p>`)),
		ze("OEBPS/Text/ack.xhtml", pg("Acknowledgements", "backmatter", `<h2>Acknowledgements</h2><p>Thanks.</p>`)),
		ze("OEBPS/Text/about.xhtml", pg("About the Author", "backmatter", `<h2>About the Author</h2><p>Nell lives by the sea.</p>`)),
		ze("OEBPS/Text/also.xhtml", pg("Also by Nell Quay", "backmatter", `<h2>Also by Nell Quay</h2><p>Other books.</p>`)),
	)
}

func TestEpub3Vellum(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := vellum3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "hl.epub", Epub: book})
	eq(t, outline(pv.Nodes), []string{
		"front_matter:Copyright", "front_matter:Dedication", "front_matter:Epigraph",
		"chapter:Chapter 1: Low Tide", "chapter:Chapter 2: The Keeper",
		"back_matter:Acknowledgements", "back_matter:About the Author", "back_matter:Also by Nell Quay",
	})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "hl.epub", Epub: book})
	var c string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title LIKE 'Chapter 1%'`, ap.ProjectID).Scan(&c)
	has(t, c, "The tide was out.", "\n* * *\n", "It came back.")
	lacks(t, c, "Low Tide", "/files/att_") // the ornament is the break, not a picture of the text
}
