package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

func prevEpub(e *env, book []byte, mod func(*imports.PreviewIn)) imports.PreviewOut {
	in := imports.PreviewIn{Name: "book.epub", Epub: book}
	if mod != nil {
		mod(&in)
	}
	return e.previewConv(in)
}

func TestEpubWithoutAContentsIsCutByFileOrHeading(t *testing.T) {
	e := newEnv(t)
	// Several files and no navigation: a section for each.
	files := miniBook(t, "Plain", []mbChapter{
		{file: "a.xhtml", title: "A", body: `<h1>Alpha</h1><p>One.</p>`, skipNav: true},
		{file: "b.xhtml", title: "B", body: `<h1>Beta</h1><p>Two.</p>`, skipNav: true},
		{file: "c.xhtml", title: "C", body: `<p>No heading here.</p>`, skipNav: true},
	})
	pv := prevEpub(e, files, nil)
	eq(t, outline(pv.Nodes), []string{"chapter:Alpha", "chapter:Beta", "chapter:C"})
	if pv.Epub.Strategy != "files" {
		t.Errorf("strategy %q", pv.Epub.Strategy)
	}
	// One file with a heading for each chapter: cut at the headings.
	one := miniBook(t, "One File", []mbChapter{
		{file: "all.xhtml", title: "All", skipNav: true, body: `<p>Before the first chapter.</p><h2>First</h2><p>1.</p><h3>Sub</h3><p>sub</p><h2>Second</h2><p>2.</p><h2>Third</h2><p>3.</p>`},
	})
	pv = prevEpub(e, one, nil)
	if pv.Epub.Strategy != "headings" {
		t.Errorf("strategy %q", pv.Epub.Strategy)
	}
	eq(t, outline(pv.Nodes), []string{"front_matter:All", "chapter:First", "chapter:Second", "chapter:Third"})
	// The writer can choose the cut.
	pv = prevEpub(e, one, func(in *imports.PreviewIn) { in.EpubSections = "files" })
	if pv.Epub.Strategy != "files" || len(pv.Nodes) != 1 {
		t.Errorf("files: %+v", outline(pv.Nodes))
	}
	// A book of one file with no heading at all is one document.
	flat := miniBook(t, "Flat", []mbChapter{{file: "x.xhtml", title: "X", body: `<p>Just text.</p>`, skipNav: true}})
	pv = prevEpub(e, flat, nil)
	if len(pv.Nodes) != 1 || pv.Nodes[0].Words != 2 {
		t.Errorf("flat: %+v", pv.Nodes)
	}
}

func TestEpubContentsThatPointNowhere(t *testing.T) {
	e := newEnv(t)
	// Entries for a file that is not there, a fragment that is not there, and one out of order.
	nav := func(entries string) []byte {
		book := miniBook(t, "Broken", []mbChapter{
			{file: "a.xhtml", title: "A", body: `<h1 id="a">Alpha</h1><p>One.</p>`, skipNav: true},
			{file: "b.xhtml", title: "B", body: `<h1 id="b">Beta</h1><p>Two.</p>`, skipNav: true},
			{file: "c.xhtml", title: "C", body: `<h1 id="c">Gamma</h1><p>Three.</p>`, skipNav: true},
		})
		return replaceInZip(t, book, "OEBPS/nav.xhtml", `<ol></ol>`, `<ol>`+entries+`</ol>`)
	}
	book := nav(`<li><a href="a.xhtml#a">Alpha</a></li><li><a href="gone.xhtml">Ghost</a></li><li><a href="c.xhtml#c">Gamma</a></li><li><a href="b.xhtml#b">Beta, out of order</a></li><li><a href="b.xhtml#nope">Beta again</a></li>`)
	pv := prevEpub(e, book, nil)
	got := outline(pv.Nodes)
	if len(got) < 2 || got[0] != "chapter:Alpha" {
		t.Errorf("outline: %v", got)
	}
	has(t, strings.Join(pv.Warnings, "\n"), "left out")
	// Every file of the book is accounted for: its text is in a document or the preview says why not.
	for _, f := range pv.Epub.Files {
		if f.Key == "" && f.Skipped == "" {
			t.Errorf("%s is neither imported nor skipped", f.Path)
		}
	}
}

func TestEpubReferencesThatLoopOrAreMissing(t *testing.T) {
	e := newEnv(t)
	// A spine item that names nothing, a manifest item whose file is missing, a link of a chapter to itself.
	book := miniBook(t, "Loops", []mbChapter{
		{file: "a.xhtml", title: "A", body: `<h1 id="a">Alpha</h1><p>See <a href="a.xhtml#a">this</a> and <a href="b.xhtml#b">that</a> and <a href="../../../etc/passwd">out</a> and <a href="javascript:alert(1)">js</a>.</p>`},
		{file: "b.xhtml", title: "B", body: `<h1 id="b">Beta</h1><p>Back to <a href="a.xhtml#a">alpha</a>.</p>`},
	})
	book = replaceInZip(t, book, "OEBPS/content.opf", `<spine>`, `<spine><itemref idref="ghost"/>`)
	book = replaceInZip(t, book, "OEBPS/content.opf", `</manifest>`, `<item id="missing" href="nothere.xhtml" media-type="application/xhtml+xml"/><item id="evil" href="../../../etc/passwd" media-type="application/xhtml+xml"/></manifest>`)
	book = replaceInZip(t, book, "OEBPS/content.opf", `</spine>`, `<itemref idref="missing"/><itemref idref="evil"/></spine>`)
	pv := prevEpub(e, book, nil)
	eq(t, outline(pv.Nodes), []string{"chapter:A", "chapter:B"})
	has(t, strings.Join(pv.Warnings, "\n"), "not in the file")
	ap := e.applyConv(pv, imports.ApplyIn{Name: "book.epub", Epub: book})
	var c string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = 'A'`, ap.ProjectID).Scan(&c)
	lacks(t, c, "passwd", "javascript", "](")
	has(t, c, "See this and that and out and js.")
}

func TestEpubTitlePageAndNonLinearItems(t *testing.T) {
	e := newEnv(t)
	book := miniBook(t, "Pages", []mbChapter{
		{file: "t.xhtml", title: "Title", bodyAttr: ` epub:type="frontmatter"`, body: `<section epub:type="titlepage"><h1>Pages</h1><p>An Author</p></section>`},
		{file: "a.xhtml", title: "Alpha", body: `<h1>Alpha</h1><p>One.</p>`},
	})
	pv := prevEpub(e, book, nil)
	eq(t, outline(pv.Nodes), []string{"chapter:Alpha"})
	pv = prevEpub(e, book, func(in *imports.PreviewIn) { in.EpubPages = "keep" })
	if len(pv.Nodes) != 2 || pv.Nodes[0].Kind != "front_matter" {
		t.Errorf("kept title page: %v", outline(pv.Nodes))
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "x", Epub: book, EpubPages: "bogus"}); err == nil {
		t.Error("an unknown epub_pages was accepted")
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "x", Epub: book, EpubSections: "bogus"}); err == nil {
		t.Error("an unknown epub_sections was accepted")
	}
}

func TestEpubSceneBreaksAndEmphasis(t *testing.T) {
	e := newEnv(t)
	css := `.it{font-style:italic} .bd{font-weight:700} p.sc{text-align:center}`
	body := `<h1>One</h1>
<p>A <i>real italic</i>, a <span class="it">class italic</span>, <b>bold</b>, <span class="bd">class bold</span>, <span style="font-style: italic">styled</span>, <del>gone</del>.</p>
<p>&#8258;</p>
<p>After an asterism.</p>
<p class="scenebreak"></p>
<p>After an empty ornament paragraph.</p>
<div class="ornament"><img src="orn.png" alt=""/></div>
<p>After an ornament image.</p>
<hr class="short"/>
<p>After a rule.</p>
<p>* * *</p><p>* * *</p>
<p>After two.</p>
<p>A very short line of ordinary text, which is not a break: #</p>`
	book := miniBook(t, "Breaks", []mbChapter{{file: "a.xhtml", title: "One", body: body}}, ze("OEBPS/s.css", css))
	book = replaceInZip(t, book, "OEBPS/content.opf", `</manifest>`, `<item id="css" href="s.css" media-type="text/css"/></manifest>`)
	pv := e.previewConv(imports.PreviewIn{Name: "b.epub", Epub: book})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "b.epub", Epub: book})
	var c string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = 'One'`, ap.ProjectID).Scan(&c)
	has(t, c, "*real italic*", "*class italic*", "**bold**", "**class bold**", "*styled*", "~~gone~~")
	if n := strings.Count(c, "* * *"); n != 5 {
		t.Errorf("%d scene breaks, want 5 (asterism, empty ornament, ornament image, rule, one for the pair):\n%s", n, c)
	}
	has(t, c, "which is not a break: #")
}

// The map from files to documents stays right when a section in the middle is left out.
func TestEpubFileMapSurvivesASkippedSection(t *testing.T) {
	e := newEnv(t)
	book := miniBook(t, "Gap", []mbChapter{
		{file: "a.xhtml", title: "A", body: `<h1>Alpha</h1><p>One.</p>`, skipNav: true},
		{file: "b.xhtml", title: "B", body: `<p></p>`, skipNav: true},
		{file: "c.xhtml", title: "C", body: `<h1>Gamma</h1><p>Three.</p>`, skipNav: true},
	})
	pv := prevEpub(e, book, nil)
	title := map[string]string{}
	for _, n := range pv.Nodes {
		title[n.Key] = n.Title
	}
	got := map[string]string{}
	for _, f := range pv.Epub.Files {
		got[f.Path] = title[f.Key] + "|" + f.Skipped
	}
	if got["OEBPS/a.xhtml"] != "Alpha|" || got["OEBPS/c.xhtml"] != "Gamma|" || !strings.HasPrefix(got["OEBPS/b.xhtml"], "|") || got["OEBPS/b.xhtml"] == "|" {
		t.Errorf("file map: %v", got)
	}
}

// A navigation document whose head has a self-closed <title/> reads as one that has not.
func TestEpubNavigationWithSelfClosedHeadElements(t *testing.T) {
	e := newEnv(t)
	book := miniBook(t, "Nav", []mbChapter{
		{file: "a.xhtml", title: "A", body: `<h1>Alpha</h1><p>One.</p>`},
		{file: "b.xhtml", title: "B", body: `<h1>Beta</h1><p>Two.</p>`},
	})
	book = replaceInZip(t, book, "OEBPS/nav.xhtml", `<title>Contents</title>`, `<title/><script src="x.js"/><style/>`)
	pv := prevEpub(e, book, nil)
	if pv.Epub.Strategy != "toc" {
		t.Errorf("strategy %q: the contents were swallowed", pv.Epub.Strategy)
	}
	eq(t, outline(pv.Nodes), []string{"chapter:A", "chapter:B"})
}
