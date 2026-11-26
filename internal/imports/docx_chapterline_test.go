package imports_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// U7: a chapter's title can hold the text before its label ("I hope Mr. Bingley will like it. CHAPTER II."). The label is the
// chapter's title ("Chapter II", from the numeral, never a position number); "Use the chapter line as the title" renames the
// chapters that still hold it, once, with one Undo, and the words before the label stay in the chapter's text. A Word table is
// counted in the review row, and the image mark never leaves asterisks next to a letter.

func lineTitles(pv *imports.PreviewOut) string {
	var out []string
	for _, n := range pv.Nodes {
		out = append(out, n.LineTitle)
	}
	return strings.Join(out, "|")
}

// The chapter line stays the title until the writer asks; the label is what it would be renamed to, and the bulk action counts
// the chapters that still hold it.
func TestChapterLineIsOfferedAndRenamedOnceKeepingTheCaption(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "21-")
	const captions = "I hope Mr. Bingley will like it. CHAPTER II.|Mr. Bingley is a pleasant young man. CHAPTER III.|Mrs. Bennet has her own opinions. CHAPTER IV."
	if got := titles(d.Preview); got != captions {
		t.Fatalf("the chapter line is the title until the writer asks: %s", got)
	}
	if got := lineTitles(d.Preview); got != "Chapter II|Chapter III|Chapter IV" {
		t.Fatalf("the label is the title it would take: %s", got)
	}
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, ChapterLineAll: true})
	if got := titles(u.Preview); got != "Chapter II|Chapter III|Chapter IV" || u.Note == nil || u.Note.Code != "line_title" || u.Note.Count != 3 {
		t.Fatalf("bulk rename: %s %+v", got, u.Note)
	}
	// the caption is kept as each chapter's first paragraph, once; the chapters and their keys do not move
	keptAsFirstLine(t, d.Preview, u.Preview, []string{"I hope Mr. Bingley will like it.", "Mr. Bingley is a pleasant young man.", "Mrs. Bennet has her own opinions."})
	if got := lineTitles(u.Preview); got != "||" {
		t.Fatalf("a renamed chapter no longer offers its line: %s", got)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, ChapterLineAll: true}); reason(err) != "no_line" {
		t.Fatalf("nothing is left to rename: %v", err)
	}
	// one Undo puts every chapter line back, and takes the captions out of the text again
	undone := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true})
	if got := titles(undone.Preview); got != captions {
		t.Fatalf("undo: %s", got)
	}
	sameShape(t, d.Preview, undone.Preview)
	// renamed again, a change of rule and back keeps the renames (the same chapters come back)
	again := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: undone.Item.Revision, ChapterLineAll: true})
	away := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: again.Item.Revision, Options: map[string]string{"docx_split": "heading1"}})
	back := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: away.Item.Revision, Options: map[string]string{"docx_split": "auto"}})
	if got := titles(back.Preview); got != "Chapter II|Chapter III|Chapter IV" {
		t.Fatalf("a rule changed and back keeps the renames: %s", got)
	}
	keptAsFirstLine(t, d.Preview, back.Preview, []string{"I hope Mr. Bingley will like it.", "Mr. Bingley is a pleasant young man.", "Mrs. Bennet has her own opinions."})
}

// The bulk rename, applied: each caption is in the imported chapter once, as its first paragraph, and undo takes it out again.
func TestChapterLineBulkKeepsTheCaptionInTheImportedText(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "21-")
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, ChapterLineAll: true})
	ap, err := applyStaged(e, st.StageID)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	text := e.docContent(ap.ProjectID, "Chapter II")
	if n := strings.Count(text, "I hope Mr. Bingley will like it."); n != 1 {
		t.Fatalf("the caption is in the chapter once, not %d times:\n%s", n, text)
	}
	if !strings.HasPrefix(strings.TrimLeft(text, "\n"), "I hope Mr. Bingley will like it.") {
		t.Fatalf("the caption is the chapter's first paragraph:\n%s", text)
	}
}

// A chapter the writer has named keeps its name; the bulk action takes the rest.
func TestChapterLineBulkKeepsTheWritersOwnTitles(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "21-")
	opening := "Opening"
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &opening}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, ChapterLineAll: true})
	if got := titles(u.Preview); got != "Opening|Chapter III|Chapter IV" || u.Note.Count != 2 {
		t.Fatalf("the named chapter is kept and the rest are renamed: %s %+v", got, u.Note)
	}
	// the writer's own rename adds no words to the chapter; the others start with their captions
	keptAsFirstLine(t, d.Preview, u.Preview, []string{"", "Mr. Bingley is a pleasant young man.", "Mrs. Bennet has her own opinions."})
}

// A pattern cut that finds a caption line titles the chapter from its label (never its place among the chapters), and keeps the
// words before the label in the text.
func TestPatternCutTitlesAChapterFromItsLabelNotItsPlace(t *testing.T) {
	e := newEnv(t)
	paras := corpusCat(one(cPs("Sample Novel", "Title")),
		one(cPbc("I hope Mr. Bingley will like it. CHAPTER II.")), corpusBody(2),
		one(cPbc("Mr. Bingley is a pleasant young man. CHAPTER III.")), corpusBody(2),
		one(cPbc("Mrs. Bennet has her own opinions. CHAPTER IV.")), corpusBody(2))
	pv := e.cut(paras, imports.PreviewIn{DocxSplit: imports.SplitPattern, DocxSplitPattern: `CHAPTER\s+[IVX]+`, DocxSplitMatch: imports.MatchRegex})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter II", "chapter:Chapter III", "chapter:Chapter IV"})
	// the line stays in the text as it is; the file keeps the chapter's name (its key does not move)
	if fl := pv.Nodes[0].FirstLine; fl != "I hope Mr. Bingley will like it. CHAPTER II." {
		t.Fatalf("the line stays in the text: first line %q", fl)
	}
	if pv.Nodes[0].NoTitle || pv.Nodes[0].LineTitle != "" {
		t.Fatalf("a labelled chapter is not 'Chapter N' by place, and its title is not offered as a line: %+v", pv.Nodes[0])
	}
	// the keys are those the cut made before the label was the title (checked on main): an edit staged then still finds its chapter
	if got := strings.Join(keysOf(pv), "|"); got != "0000002-chapter.md|0000005-chapter.md|0000008-chapter.md" {
		t.Fatalf("the keys moved: %s", got)
	}
}

// sameShape says two previews hold the same chapters, keys, words and first lines: a rename changes the titles and nothing else.
func sameShape(t *testing.T, before, after *imports.PreviewOut) {
	t.Helper()
	if len(before.Nodes) != len(after.Nodes) || before.Words != after.Words {
		t.Fatalf("the shape changed: %d/%d documents, %d/%d words", len(before.Nodes), len(after.Nodes), before.Words, after.Words)
	}
	for i := range before.Nodes {
		b, a := before.Nodes[i], after.Nodes[i]
		if b.Key != a.Key || b.Words != a.Words || b.FirstLine != a.FirstLine {
			t.Fatalf("chapter %d changed beyond its title: %+v -> %+v", i, b, a)
		}
	}
}

// The image mark is written so that Markdown renders no stray asterisks: an image against a word is plain text.
func TestImageMarkerNeverLeavesStrayAsterisks(t *testing.T) {
	e := newEnv(t)
	doc := body(para("Heading1", "Alpha") + `<w:p><w:r><w:drawing/></w:r><w:r><w:t>APPY</w:t></w:r></w:p>` + para("", "Some words here."))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	text := e.docContent(ap.ProjectID, "Alpha")
	if !strings.Contains(text, `\[image\]APPY`) {
		t.Fatalf("the image mark is not beside the word:\n%s", text)
	}
	if regexp.MustCompile(`[\pL\pN]\*|\*[\pL\pN]`).MatchString(text) {
		t.Fatalf("stray asterisks next to letters in the imported Markdown:\n%s", text)
	}
}

// A Word table in a review row: the table and its cells are said, and the first row is the preview line. The word count is unchanged.
func TestTableRowSaysItIsATableAndShowsItsFirstRow(t *testing.T) {
	e := newEnv(t)
	row := func(a, b string) string {
		return `<w:tr><w:tc><w:p><w:r><w:t>` + a + `</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>` + b + `</w:t></w:r></w:p></w:tc></w:tr>`
	}
	tbl := `<w:tbl>` + row("Plate", "Frontispiece") + row("Plate II", "The Mill") + row("Plate III", "Snow") + `</w:tbl>`
	doc := body(para("Heading1", "List of Illustrations") + tbl + para("Heading1", "One") + para("", "Words of the first chapter."))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	if len(pv.Nodes) < 2 {
		t.Fatalf("two chapters: %+v", pv.Nodes)
	}
	n := pv.Nodes[0]
	if n.Tables != 1 || n.Cells != 6 {
		t.Fatalf("one table of six cells: tables %d, cells %d", n.Tables, n.Cells)
	}
	if n.FirstLine != "Plate · Frontispiece" {
		t.Fatalf("the first row is the preview line: %q", n.FirstLine)
	}
	if pv.Nodes[1].Tables != 0 || pv.Nodes[1].Cells != 0 {
		t.Fatalf("a chapter with no table says nothing about one")
	}
}

// keptAsFirstLine says each chapter keeps its key, and starts with the caption want names for it, or with the first line it had when
// want is "" (a chapter whose title the writer changed by hand has no caption in its text).
func keptAsFirstLine(t *testing.T, before, after *imports.PreviewOut, want []string) {
	t.Helper()
	if len(before.Nodes) != len(after.Nodes) || len(want) != len(after.Nodes) {
		t.Fatalf("the number of chapters changed: %d/%d", len(before.Nodes), len(after.Nodes))
	}
	for i := range before.Nodes {
		b, a := before.Nodes[i], after.Nodes[i]
		if b.Key != a.Key {
			t.Fatalf("chapter %d moved its key: %s -> %s", i, b.Key, a.Key)
		}
		exp := b.FirstLine
		if want[i] != "" {
			exp = want[i]
		}
		if a.FirstLine != exp {
			t.Fatalf("chapter %d starts %q, want %q", i, a.FirstLine, exp)
		}
	}
}
