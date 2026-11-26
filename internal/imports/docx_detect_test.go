package imports_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// The detector's own rules, one at a time, and the limits of the cut.

func (e *env) cut(paras []cp, in imports.PreviewIn) imports.PreviewOut {
	e.t.Helper()
	return e.corpusPreview(corpusSample{paras: paras}, in)
}

func keysOf(pv imports.PreviewOut) []string {
	var out []string
	for _, n := range pv.Nodes {
		out = append(out, n.Key)
	}
	return out
}

// A chapter's key is named for the paragraph it starts at, so adding a break does not renumber the others.
func TestDocxKeysAreNamedForTheParagraphAndSurviveAnAddedBreak(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(one(cPh1("One")), corpusBody(2), one(cPh1("Two")), corpusBody(3), one(cPh1("Three")), corpusBody(2))
	before := e.cut(paras, imports.PreviewIn{})
	for _, n := range before.Nodes {
		if !strings.HasPrefix(n.Key, fmt.Sprintf("%07d-", n.Ordinal)) {
			t.Fatalf("key %q is not named for ordinal %d", n.Key, n.Ordinal)
		}
	}
	// start a chapter in the middle of the second: the first and third keep their keys
	second := before.Nodes[1].Ordinal
	after := e.cut(paras, imports.PreviewIn{DocxBreaks: []int{second + 2}})
	if len(after.Nodes) != 4 {
		t.Fatalf("a break makes a chapter: %v", outline(after.Nodes))
	}
	keep := map[string]bool{}
	for _, k := range keysOf(after) {
		keep[k] = true
	}
	for _, k := range keysOf(before) {
		if !keep[k] {
			t.Errorf("key %s did not survive the added break: %v", k, keysOf(after))
		}
	}
	// and the same file always gives the same keys
	again := e.cut(paras, imports.PreviewIn{})
	eq(t, keysOf(again), keysOf(before))
}

// The folder importer reads seven-digit numbers: the largest paragraph ordinal sorts in its place.
func TestFolderNumbersCanHaveSevenDigits(t *testing.T) {
	pv, err := imports.Preview(files(map[string]string{"0000009-a.md": "# A\n\nText.\n", "1000000-b.md": "# B\n\nText.\n", "0999999-c.md": "# C\n\nText.\n"}), imports.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, outline(pv.Nodes), []string{"chapter:A", "chapter:C", "chapter:B"})
}

// A page break in a style's chain, a size, a space above and capitals are read as signals; a stale lastRenderedPageBreak is not.
func TestDocxSignalsAreRead(t *testing.T) {
	e := newEnv(t)
	// a stale layout cache is no break: a paragraph holding only lastRenderedPageBreak does not make a suggestion
	stale := cp{runs: []string{`<w:r><w:lastRenderedPageBreak/><w:t>Text after a page Word once drew.</w:t></w:r>`}}
	pv := e.cut(corpusCat(corpusBody(2), one(stale), corpusBody(2)), imports.PreviewIn{})
	if pv.Breaks.SuggestionsTotal != 0 {
		t.Errorf("lastRenderedPageBreak was read as a break: %+v", pv.Breaks)
	}
	// larger type: a bigger "Chapter N" line is a title with no other mark
	big := cp{text: "Chapter 1", size: 40}
	big2 := cp{text: "Chapter 2", size: 40}
	pv = e.cut(corpusCat(one(big), corpusBody(3), one(big2), corpusBody(3)), imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter 1", "chapter:Chapter 2"})
	if !strings.Contains(strings.Join(pv.Nodes[0].Why, ","), "larger") {
		t.Errorf("why: %v", pv.Nodes[0].Why)
	}
	// a plain line with no mark and no neighbours is no chapter
	pv = e.cut(corpusCat(corpusBody(3), one(cP("Chapter 9")), corpusBody(3)), imports.PreviewIn{})
	if pv.Documents != 1 || pv.Breaks.ByRule["label"] != 1 {
		t.Errorf("a lone plain label is only a suggestion: %d %+v", pv.Documents, pv.Breaks)
	}
	// caps alone make a label line a title
	pv = e.cut(corpusCat(one(cP("CHAPTER 1")), corpusBody(3), one(cP("CHAPTER 2")), corpusBody(3)), imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:CHAPTER 1", "chapter:CHAPTER 2"})
}

// Other languages' chapter words, number words and the separator that begins a title.
func TestDocxLabelGrammar(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		line, title string
	}{
		{"Chapter Three: The Road", "The Road"},
		{"Chapter 12 - The Mill", "The Mill"},
		{"Chapitre IV", "Chapitre IV"},
		{"Kapitel 3. Der Weg", "Der Weg"},
		{"Capítulo 2", "Capítulo 2"},
		{"Chapter twenty-one", "Chapter twenty-one"},
	} {
		pv := e.cut(corpusCat(one(cPbc(c.line)), corpusBody(2), one(cPbc("Chapter 99")), corpusBody(2)), imports.PreviewIn{})
		if len(pv.Nodes) != 2 || pv.Nodes[0].Title != c.title {
			t.Errorf("%q: %v (want first title %q)", c.line, outline(pv.Nodes), c.title)
		}
	}
	// what is not a label
	for _, line := range []string{"Chapter 3 of the report was missing", "Chapter mix", "Chapters of my life", "Chapter", "Chapter 3 the road"} {
		pv := e.cut(corpusCat(one(cPbc(line)), corpusBody(2)), imports.PreviewIn{})
		if pv.Breaks.Chapters != 0 {
			t.Errorf("%q was read as a label: %v", line, outline(pv.Nodes))
		}
	}
}

// Parts and chapters at one level (Vellum's way), and parts over chapters.
func TestDocxPartsAtTheSameLevel(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(one(cPh1("Part One")), one(cPh1("Chapter 1")), corpusBody(2), one(cPh1("Chapter 2")), corpusBody(2), one(cPh1("Part Two")), one(cPh1("Chapter 3")), corpusBody(2))
	pv := e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"part:Part One", "  chapter:Chapter 1", "  chapter:Chapter 2", "part:Part Two", "  chapter:Chapter 3"})
	// the override: chapters makes every heading of that level a chapter, parts included
	pv = e.cut(paras, imports.PreviewIn{DocxHeadings: "chapters"})
	if len(pv.Nodes) != 5 || pv.Nodes[0].Kind != "chapter" {
		t.Errorf("chapters override: %v", outline(pv.Nodes))
	}
}

// A preface in the middle of the book stays a chapter, and trailing back matter is back matter, whatever its heading level.
func TestDocxFrontAndBackMatterByPlace(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(one(cPh1("Foreword")), corpusBody(1), one(cPh1("Chapter 1")), corpusBody(2), one(cPh1("Preface to the second edition")), corpusBody(1), one(cPh1("Chapter 2")), corpusBody(2),
		one(cPh1("Appendix A")), corpusBody(1), one(cPh1("Glossary")), corpusBody(1), one(cPh1("Also by A. Writer")), corpusBody(1))
	pv := e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"front_matter:Foreword", "chapter:Chapter 1", "chapter:Preface to the second edition", "chapter:Chapter 2", "back_matter:Appendix A", "back_matter:Glossary", "back_matter:Also by A. Writer"})
	// an appendix before the last chapter is a chapter
	paras = corpusCat(one(cPh1("Chapter 1")), corpusBody(2), one(cPh1("Appendix")), corpusBody(1), one(cPh1("Chapter 2")), corpusBody(2))
	pv = e.cut(paras, imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter 1", "chapter:Appendix", "chapter:Chapter 2"})
}

// A contents page left out is a page of the front of the book; lines like it in a chapter stay text.
func TestDocxContentsOnlyAtTheFront(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(one(cPh1("Chapter 1")), corpusBody(1), one(cP("Contents"), cP("Part A ........ 3"), cP("Part B ........ 9"), cP("Part C ........ 12")), corpusBody(1), one(cPh1("Chapter 2")), corpusBody(1))
	pv := e.cut(paras, imports.PreviewIn{})
	if len(pv.LeftOut) != 0 || pv.Documents != 2 {
		t.Fatalf("a mid-book list is not a contents page: %v %v", pv.LeftOut, outline(pv.Nodes))
	}
	// and its lines never became chapters
	if pv.Breaks.Chapters != 2 {
		t.Errorf("chapters: %+v", pv.Breaks)
	}
}

// A tracked change in a title: the title is the final view, the change is counted as taken as accepted, and the file still imports.
func TestDocxTrackedChangeInATitle(t *testing.T) {
	e := newEnv(t)
	title := cp{runs: []string{
		`<w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">Chapter </w:t></w:r>`,
		`<w:del w:id="1" w:author="Ann" w:date="2024-01-01T00:00:00Z"><w:r><w:rPr><w:b/></w:rPr><w:delText>One</w:delText></w:r></w:del>`,
		`<w:ins w:id="2" w:author="Ann" w:date="2024-01-01T00:00:00Z"><w:r><w:rPr><w:b/></w:rPr><w:t>1</w:t></w:r></w:ins>`}, center: true}
	pv := e.cut(corpusCat(one(title), corpusBody(2), one(cPbc("Chapter 2")), corpusBody(2)), imports.PreviewIn{})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter 1", "chapter:Chapter 2"})
	rep := fmt.Sprint(pv.Report["docx"])
	if !strings.Contains(rep, "in_headings:1") {
		t.Errorf("the change in the title is counted: %s", rep)
	}
}

// A page break after every paragraph: thousands of suggestions, a bounded list, the page not hanging, and a warning when the rule is chosen.
func TestDocxPageBreakAfterEveryParagraph(t *testing.T) {
	e := newEnv(t)
	var paras []cp
	for i := 0; i < 5000; i++ {
		paras = append(paras, cP(fmt.Sprintf("Paragraph number %d runs on for a little while.", i)), cPbreak())
	}
	pv := e.cut(paras, imports.PreviewIn{})
	if pv.Documents != 1 || !pv.Breaks.NoneFound || pv.Breaks.SuggestionsTotal != 4999 || len(pv.Breaks.Suggestions) != 400 {
		t.Fatalf("suggestions: %d documents, %d total, %d listed", pv.Documents, pv.Breaks.SuggestionsTotal, len(pv.Breaks.Suggestions))
	}
	// the explicit rule would make more documents than an import takes: refused in the writer's words
	_, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: corpusDoc(paras), Name: "b.docx", DocxSplit: imports.SplitPageBreaks})
	if err == nil || !strings.Contains(err.Error(), "an import takes at most 2,000") {
		t.Fatalf("5000 chapters: %v", err)
	}
	// a few hundred is allowed, and said to look like pages
	pv = e.cut(paras[:600], imports.PreviewIn{DocxSplit: imports.SplitPageBreaks})
	if pv.Documents != 300 {
		t.Fatalf("page_breaks: %d documents", pv.Documents)
	}
	said := false
	for _, w := range pv.Warnings {
		if strings.Contains(w, "pages, not chapters") {
			said = true
		}
	}
	if !said {
		t.Errorf("no warning that these are pages: %v", pv.Warnings)
	}
}

// 150,000 words in each of three forms, and the size cut as a suggestion until it is chosen.
func TestDocxLongBooks(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		kind     string
		chapters int
		docs     int
	}{{"h1", 60, 60}, {"plain", 60, 60}, {"none", 60, 1}} {
		pv := e.cut(corpusBig(c.kind, c.chapters, 100), imports.PreviewIn{})
		if pv.Documents != c.docs {
			t.Fatalf("%s: %d documents, want %d", c.kind, pv.Documents, c.docs)
		}
		if pv.Words < 140_000 {
			t.Fatalf("%s: %d words", c.kind, pv.Words)
		}
		if c.kind == "none" {
			if !pv.Breaks.NoneFound || pv.Breaks.Pieces < 25 {
				t.Fatalf("a book of no marks says so and offers pieces: %+v", pv.Breaks)
			}
			// the size rule is the writer's choice: pieces of about 5,000 words, titled Section N
			pv = e.cut(corpusBig("none", 60, 100), imports.PreviewIn{DocxSplit: imports.SplitSize})
			if pv.Documents < 25 || pv.Nodes[0].Title != "Section 1" || pv.Nodes[1].Title != "Section 2" {
				t.Fatalf("size: %d documents, %s", pv.Documents, outline(pv.Nodes[:2]))
			}
			for _, n := range pv.Nodes {
				if n.Words > 7000 {
					t.Fatalf("a piece of %d words", n.Words)
				}
			}
		}
	}
	// 500,000 words, 1,000 chapters
	pv := e.cut(corpusBig("h1", 200, 100), imports.PreviewIn{})
	if pv.Documents != 200 {
		t.Fatalf("500k: %d", pv.Documents)
	}
}

// An unsplit document over the 2 MiB file limit cannot be imported as it is: the file is listed as skipped for that reason, nothing is
// imported, and the size cut is there to choose.
func TestDocxTooLongForOneDocumentIsNotImported(t *testing.T) {
	e := newEnv(t)
	pv := e.cut(corpusBig("none", 200, 100), imports.PreviewIn{})
	if pv.Documents != 0 || pv.SkippedTotal != 1 || !strings.Contains(pv.Skipped[0].Reason, "2 MiB") {
		t.Fatalf("an unsplit 500,000-word book: %d documents, skipped %v", pv.Documents, pv.Skipped)
	}
	if pv.Breaks == nil || pv.Breaks.Pieces < 90 {
		t.Fatalf("the pieces are offered: %+v", pv.Breaks)
	}
	pv = e.cut(corpusBig("none", 200, 100), imports.PreviewIn{DocxSplit: imports.SplitSize})
	if pv.Documents < 90 {
		t.Fatalf("the size cut makes it importable: %d documents", pv.Documents)
	}
}

// Every explicit rule runs on every sample without failing.
func TestDocxEveryRuleOnEverySample(t *testing.T) {
	e := newEnv(t)
	for _, s := range corpusSamples() {
		for _, r := range []string{"heading1", "heading2", "label", "page_breaks", "section_breaks", "numerals", "size", "none"} {
			pv := e.corpusPreview(s, imports.PreviewIn{DocxSplit: r})
			if pv.Documents == 0 {
				t.Errorf("%s with %s: nothing", s.name, r)
			}
		}
	}
}

// What the options refuse.
func TestDocxCutOptionsAreChecked(t *testing.T) {
	e := newEnv(t)
	s := corpusByName(t, "01")
	for name, in := range map[string]imports.PreviewIn{
		"split":     {DocxSplit: "sideways"},
		"style":     {DocxSplit: "style"},
		"words":     {DocxSplitWords: 12},
		"too many":  {DocxBreaks: make([]int, 5001)},
		"zero":      {DocxBreaks: []int{0}},
		"headings":  {DocxHeadings: "levels"},
		"long name": {DocxSplit: "style", DocxSplitStyle: strings.Repeat("x", 201)},
	} {
		in.Docx, in.Name = corpusDoc(s.paras), "b.docx"
		if _, err := e.call(e.w, "import.preview", in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// An untitled chapter is "Chapter N" by its place, and its title is not its key: a break added above it renumbers the title, and the
// key (the paragraph it starts at, and a slug) must stay, or every edit of it goes stale.
func TestDocxKeysOfUntitledChaptersDoNotFollowTheirPlace(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(corpusBody(3), one(cPbreak()), corpusBody(3), one(cPbreak()), corpusBody(3), one(cPbreak()), corpusBody(3))
	in := imports.PreviewIn{DocxSplit: "page_breaks"}
	before := e.cut(paras, in)
	if len(before.Nodes) != 4 {
		t.Fatalf("four chapters: %v", outline(before.Nodes))
	}
	in.DocxBreaks = []int{before.Nodes[0].Ordinal + 2} // a break inside the first: every chapter after it is numbered one more
	after := e.cut(paras, in)
	if len(after.Nodes) != 5 {
		t.Fatalf("five chapters: %v", outline(after.Nodes))
	}
	keep := map[string]bool{}
	for _, k := range keysOf(after) {
		keep[k] = true
	}
	for _, k := range keysOf(before) {
		if !keep[k] {
			t.Errorf("key %s did not survive a break above it: %v", k, keysOf(after))
		}
	}
	// the titles do follow the place: that is what an untitled chapter is called
	if after.Nodes[4].Title != "Chapter 5" {
		t.Errorf("titles: %v", outline(after.Nodes))
	}
}
