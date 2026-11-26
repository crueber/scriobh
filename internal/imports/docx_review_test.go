package imports_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// Cases an independent review found, each as a test of its own.

// A list level is a number the document chooses: it must not size anything. A paragraph or a numbering definition with a level of
// millions, or past an int, is read as level 8 at most, in a few bytes.
func TestDocxHugeListLevelIsClamped(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"1000000", "20000000", "2147483648", "99999999999999999999"} {
		numbering := fmt.Sprintf(`<?xml version="1.0"?><w:numbering %s><w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="Chapter %%1"/></w:lvl><w:lvl w:ilvl="%s"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="Chapter %%1"/></w:lvl></w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num></w:numbering>`, wns, n)
		h := fmt.Sprintf(`<w:p><w:pPr><w:pStyle w:val="Heading1"/><w:numPr><w:ilvl w:val="%s"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>Heading</w:t></w:r></w:p>`, n)
		doc := body(h + para("", "Some text here that is a paragraph.") + para("Heading1", "Second") + para("", "More."))
		d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles, "word/numbering.xml": numbering})
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		pv := e.previewDocx(d, imports.PreviewIn{})
		runtime.ReadMemStats(&m1)
		if pv.Documents != 2 {
			t.Errorf("ilvl %s: %d documents", n, pv.Documents)
		}
		if grew := (m1.TotalAlloc - m0.TotalAlloc) >> 20; grew > 64 {
			t.Errorf("ilvl %s: %d MB allocated for a tiny file", n, grew)
		}
	}
}

func wordsOf(pv imports.PreviewOut) []int {
	var out []int
	for _, n := range pv.Nodes {
		out = append(out, n.Words)
	}
	return out
}

func noEmpty(t *testing.T, pv imports.PreviewOut, what string) {
	t.Helper()
	for _, n := range pv.Nodes {
		if n.Words == 0 {
			t.Errorf("%s: %q is an empty chapter in %v", what, n.Title, outline(pv.Nodes))
		}
	}
}

// D6: a page break never cuts. Pages of prose before the first chapter are one Front Matter, and their breaks are suggestions.
func TestDocxFrontMatterIsNotCutAtPageBreaks(t *testing.T) {
	e := newEnv(t)
	var paras []cp
	for i := 0; i < 12; i++ {
		paras = append(paras, cP(fmt.Sprintf("Page %d opens with a line of prose that goes on for a good while and is plainly not a title.", i)), cP("More prose follows the first paragraph of this page."), cPbreak())
	}
	paras = append(paras, cPh1("Acknowledgments"), cP("Thanks to everyone."))
	pv := e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"front_matter:Front Matter", "back_matter:Acknowledgments"})
	if pv.Breaks.SuggestionsTotal < 11 {
		t.Errorf("the page breaks are listed: %d", pv.Breaks.SuggestionsTotal)
	}
	// a copyright page and a dedication after a break are named pieces, and only those cut
	paras = corpusCat(one(cP("Copyright 2026 A. Writer. All rights reserved.")), corpusBody(2), one(cPbreak(), cPc("For my mother")), one(cPbreak()), corpusBody(3), one(cPbreak()), corpusBody(3), one(cPh1("Chapter 1")), corpusBody(2), one(cPh1("Chapter 2")), corpusBody(2))
	pv = e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"front_matter:Copyright", "front_matter:Dedication", "front_matter:Front Matter", "chapter:Chapter 1", "chapter:Chapter 2"})
	// the same pages, long: no region that long is split by what it says
	var long []cp
	long = append(long, cP("Copyright 2026 A. Writer. All rights reserved."), cPbreak())
	for i := 0; i < 40; i++ {
		long = append(long, cP(strings.TrimSpace(strings.Repeat("word ", 100))+"."), cPbreak())
	}
	long = append(long, cPh1("Chapter 1"), cP("Text."), cPh1("Chapter 2"), cP("Text."))
	pv = e.cut(long, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"front_matter:Front Matter", "chapter:Chapter 1", "chapter:Chapter 2"})
}

// A chapter is never empty for want of a line: a number in one style over a title in another, a heading over a second line.
func TestDocxAdjacentBoundariesAreOneChapter(t *testing.T) {
	e := newEnv(t)
	var paras []cp
	for i, t := range []string{"The Road", "The Mill", "Snow"} {
		paras = append(paras, cPs(fmt.Sprintf("Chapter %d", i+1), "ChapterNumber"), cPs(t, "ChapterTitle"))
		paras = append(paras, corpusBody(2)...)
	}
	pv := e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:The Road", "chapter:The Mill", "chapter:Snow"})
	noEmpty(t, pv, "number over title")
	// a heading with a subtitle in a "Chapter Subtitle" style: the subtitle is text, not a boundary
	paras = nil
	for i := 1; i <= 3; i++ {
		paras = append(paras, cPh1(fmt.Sprintf("Chapter %d", i)), cPs("A subtitle for it", "ChapterSubtitle"))
		paras = append(paras, corpusBody(2)...)
	}
	pv = e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter 1", "chapter:Chapter 2", "chapter:Chapter 3"})
	noEmpty(t, pv, "heading and subtitle")
	// a table's title is not a chapter's
	paras = corpusCat(one(cPh1("Chapter One")), corpusBody(1), one(cPs("Table 1 sales", "TableTitle")), corpusBody(1), one(cPs("Table 2 costs", "TableTitle")), corpusBody(1))
	pv = e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter One"})
	// two headings with a title between them in words: "Chapter One" over "The Mill" is "The Mill"
	pv = e.cut(corpusCat(one(cPh1("Chapter One"), cPh1("The Mill")), corpusBody(2), one(cPh1("Chapter Two"), cPh1("Snow")), corpusBody(2)), imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:The Mill", "chapter:Snow"})
	noEmpty(t, pv, "two headings")
	// two bare numbers are two chapters, even if one is empty: that is what the writer wrote
	pv = e.cut(corpusCat(one(cPh1("Chapter 1"), cPh1("Chapter 2")), corpusBody(2)), imports.PreviewIn{})
	if len(pv.Nodes) != 2 {
		t.Errorf("two bare labels: %v", outline(pv.Nodes))
	}
}

// docx_headings parts: the top headings are parts whatever they say, and the next level the chapters; the default does not guess it.
func TestDocxPartsOverrideMakesPartsOfTheTopHeadings(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(one(cPh1("The Beginning")), one(cPh2("Ch A")), corpusBody(2), one(cPh2("Ch B")), corpusBody(2), one(cPh1("The Middle")), one(cPh2("Ch C")), corpusBody(2), one(cPh2("Ch D")), corpusBody(2))
	pv := e.cut(paras, imports.PreviewIn{DocxHeadings: "parts"})
	eq(t, outline(pv.Nodes), []string{"part:The Beginning", "  chapter:Ch A", "  chapter:Ch B", "part:The Middle", "  chapter:Ch C", "  chapter:Ch D"})
	pv = e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:The Beginning", "chapter:The Middle"})
	if pv.Breaks.Levels != 2 {
		t.Errorf("the review offers the override when there are two levels: %d", pv.Breaks.Levels)
	}
	pv = e.cut(paras, imports.PreviewIn{DocxHeadings: "chapters"})
	eq(t, outline(pv.Nodes), []string{"chapter:The Beginning", "chapter:The Middle"})
}

// A footnote or a comment on a paragraph that gives its title is not lost: the line stays as the first line of the chapter.
func TestDocxFootnoteAndCommentOnATitleAreKept(t *testing.T) {
	e := newEnv(t)
	notes := `<?xml version="1.0"?><w:footnotes ` + wns + `><w:footnote w:id="1"><w:p><w:r><w:t>FOOTNOTE-ON-THE-TITLE</w:t></w:r></w:p></w:footnote></w:footnotes>`
	comments := `<?xml version="1.0"?><w:comments ` + wns + `><w:comment w:id="0" w:author="Ann"><w:p><w:r><w:t>COMMENT-ON-THE-TITLE</w:t></w:r></w:p></w:comment></w:comments>`
	pb := `<w:r><w:br w:type="page"/></w:r>`
	run := func(x string) string { return `<w:r><w:t xml:space="preserve">` + x + `</w:t></w:r>` }
	doc := body(
		`<w:p>` + run("The first chapter text is long enough to be a real paragraph of prose here.") + `</w:p>` +
			`<w:p>` + pb + `<w:commentRangeStart w:id="0"/>` + run("The Mill") + `<w:commentRangeEnd w:id="0"/><w:r><w:commentReference w:id="0"/></w:r><w:r><w:footnoteReference w:id="1"/></w:r></w:p>` +
			`<w:p>` + run("Second chapter text, another long enough paragraph of ordinary prose.") + `</w:p>`)
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles, "word/footnotes.xml": notes, "word/comments.xml": comments})
	pv := e.previewDocx(d, imports.PreviewIn{DocxSplit: "page_breaks"})
	if pv.Documents != 2 || pv.Nodes[1].Title != "The Mill" {
		t.Fatalf("%v", outline(pv.Nodes))
	}
	if !strings.Contains(strings.Join(pv.Warnings, "|"), "footnote or a comment") {
		t.Errorf("the writer is told: %v", pv.Warnings)
	}
	ap := e.applyDocx(d, pv, imports.ApplyIn{DocxSplit: "page_breaks"})
	text := e.docContent(ap.ProjectID, "The Mill")
	if !strings.Contains(text, "FOOTNOTE-ON-THE-TITLE") || !strings.Contains(text, "The Mill") {
		t.Errorf("the footnote is in the chapter: %q", text)
	}
	if pv.Comments != 1 {
		t.Errorf("the comment is imported: %d", pv.Comments)
	}
}

// Dialogue that trails off is not a contents page; a contents page needs a page for each entry, and every line, the first included,
// has to be short.
func TestDocxContentsNeedsPagesNotEllipses(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(one(cPh1("One")), one(cP("The door opened."), cP("\"Well...\""), cP("\"I don't know...\""), cP("\"Maybe...\""), cP("She stepped inside.")), one(cPh1("Two")), corpusBody(1))
	pv := e.cut(paras, imports.PreviewIn{})
	if len(pv.LeftOut) != 0 || pv.Documents != 2 || pv.Nodes[0].Words < 11 {
		t.Errorf("dialogue was read as a contents page: %v words %v left out %v", outline(pv.Nodes), wordsOf(pv), pv.LeftOut)
	}
	// the same with no chapters at all
	pv = e.cut([]cp{cP("A…"), cP("B…"), cP("C…"), cP("Real text follows here in a long sentence of prose.")}, imports.PreviewIn{})
	if len(pv.LeftOut) != 0 || pv.Documents != 1 {
		t.Errorf("trailing ellipses: %v", pv.LeftOut)
	}
	// three long lines that happen to end in dots
	long := strings.TrimSpace(strings.Repeat("word ", 40)) + " ........ 5"
	pv = e.cut([]cp{cP(long), cP(long), cP(long), cP("The end.")}, imports.PreviewIn{})
	if len(pv.LeftOut) != 0 {
		t.Errorf("long lines are not a contents page: %v", pv.LeftOut)
	}
	// the real thing still is one
	pv = e.cut(corpusCat(one(cP("Contents"), cP("One ........ 3"), cP("Two .... 9")), one(cPh1("One")), corpusBody(1), one(cPh1("Two")), corpusBody(1)), imports.PreviewIn{})
	if len(pv.LeftOut) != 1 {
		t.Errorf("a contents page: %v", pv.LeftOut)
	}
}

// The front, the contents, the parts and their intros count toward the documents an import takes; over them the writer is told so.
func TestDocxDocumentCapCountsEveryPiece(t *testing.T) {
	e := newEnv(t)
	mk := func(chapters int) []cp {
		paras := []cp{cP("Copyright 2026 A. Writer. All rights reserved."), cPbreak(), cPc("For my mother"), cPbreak(), cP("Contents"), cP("One ........ 3"), cP("Two ........ 9")}
		for i := 0; i < chapters; i++ {
			paras = append(paras, cPh1(fmt.Sprintf("Ch %d", i)), cP("Some text here."))
		}
		return paras
	}
	_, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: corpusDoc(mk(1998)), Name: "b.docx"})
	if err == nil || !strings.Contains(err.Error(), "an import takes at most 2,000") {
		t.Fatalf("1,998 chapters, two front documents and a contents page: %v", err)
	}
	pv := e.cut(mk(1997), imports.PreviewIn{})
	if pv.Documents != 1999 || len(pv.LeftOut) != 1 {
		t.Fatalf("1,997 and three: %d documents, %d left out", pv.Documents, len(pv.LeftOut))
	}
}

// A style named for chapters that Word numbers ("Chapter %1") is a chapter title, not a list; the number is not in the title.
func TestDocxNumberedChapterStyleIsATitle(t *testing.T) {
	e := newEnv(t)
	mk := func(t string) cp { return cp{text: t, style: "ChapterTitle", num: true} }
	pv := e.cut(corpusCat(one(mk("The Road")), corpusBody(2), one(mk("The Mill")), corpusBody(2), one(mk("Snow")), corpusBody(2)), imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:The Road", "chapter:The Mill", "chapter:Snow"})
	if pv.Nodes[0].WordNumber != "Chapter 1" || pv.Nodes[2].WordNumber != "Chapter 3" {
		t.Errorf("Word's number is shown: %+v", pv.Nodes[0])
	}
	// a real numbered list in a plain style is still a list
	paras := corpusCat(one(cPh1("One")), one(cp{text: "first item", num: true}, cp{text: "second item", num: true}), corpusBody(1), one(cPh1("Two")), corpusBody(1))
	pv = e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:One", "chapter:Two"})
}

// Break ordinals are found by index: many of them, late in a long file, and the cap before the work.
func TestDocxManyBreaksAreFoundByIndex(t *testing.T) {
	e := newEnv(t)
	var paras []cp
	for i := 0; i < 30000; i++ {
		paras = append(paras, cP(fmt.Sprintf("P%d x", i)))
	}
	doc := corpusDoc(paras)
	var br []int
	for i := 0; i < 1990; i++ {
		br = append(br, 29000-i*3)
	}
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Docx: doc, Name: "b.docx", DocxBreaks: br}).(imports.PreviewOut)
	if pv.Documents != 1991 {
		t.Errorf("1,990 breaks: %d documents", pv.Documents)
	}
	br = append(br, 100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111)
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: doc, Name: "b.docx", DocxBreaks: br}); err == nil || !strings.Contains(err.Error(), "at most 2,000") {
		t.Errorf("past the cap: %v", err)
	}
}

// Capitals become title case; a Roman numeral stays only as a chapter's number, so real words that spell one are words.
func TestDocxTitleCaseKeepsNumeralsOnlyAsNumbers(t *testing.T) {
	e := newEnv(t)
	for in, want := range map[string]string{"THE MIX": "The Mix", "MILL": "Mill", "A LIVID MIX": "A Livid Mix", "CHAPTER XIV: THE MIX": "Chapter XIV: The Mix", "XIV": ""} {
		pv := e.cut(corpusCat(one(cPh1(in)), corpusBody(2), one(cPh1("Plain")), corpusBody(2)), imports.PreviewIn{})
		if got := pv.Nodes[0].SuggestedTitle; got != want {
			t.Errorf("%q: suggested %q, want %q", in, got, want)
		}
	}
}

// A possible chapter start shows its line even when the paragraph is long prose.
func TestDocxSuggestionShowsTheFirstLineOfALongParagraph(t *testing.T) {
	e := newEnv(t)
	long := "Dawn came grey over the harbour and " + strings.Repeat("the gulls wheeled over the stones all the long morning ", 12)
	pv := e.cut([]cp{cP(long), cPbreak(), cP(long), cPbreak(), cP(long)}, imports.PreviewIn{})
	if len(pv.Breaks.Suggestions) != 2 || !strings.HasPrefix(pv.Breaks.Suggestions[0].Line, "Dawn came grey over the harbour") || len(pv.Breaks.Suggestions[0].Line) > 90 {
		t.Errorf("suggestions: %+v", pv.Breaks.Suggestions)
	}
}
