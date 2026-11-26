package imports_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// Plain text (F-344) and the detector shared with the PDF and Pandoc importers (F-1244): the words alone say where a chapter starts, so
// a label in capitals is a chapter and a plain one is an offer, and nothing the writer did not choose cuts.

func textBook(labels []string, plain bool) string {
	var b strings.Builder
	b.WriteString("A Short Book\n\nBy nobody in particular, and written long ago in a house by the sea where the gulls wheel.\n\n")
	for i, l := range labels {
		if i > 0 {
			b.WriteString("* * *\n\n")
		}
		b.WriteString(l + "\n\n")
		for j := 0; j < 3; j++ {
			fmt.Fprintf(&b, "The tide came in over the grey stones of %s and went out again, as it does, and the gulls complained about it for an hour.\n\n", strings.ToLower(l))
		}
	}
	return b.String()
}

func (e *env) previewText(data string, in imports.PreviewIn) imports.PreviewOut {
	e.t.Helper()
	in.Text, in.Name = []byte(data), "book.txt"
	return e.must(e.w, "import.preview", in).(imports.PreviewOut)
}

func textTitles(pv imports.PreviewOut) string {
	var out []string
	for _, n := range pv.Nodes {
		out = append(out, n.Title)
	}
	return strings.Join(out, "|")
}

func TestTextWithCapitalLabelsIsCutAndWithPlainLabelsIsOffered(t *testing.T) {
	e := newEnv(t)
	caps := e.previewText(textBook([]string{"CHAPTER ONE", "CHAPTER TWO", "CHAPTER THREE"}, false), imports.PreviewIn{})
	if caps.Layout != "text" || caps.Documents < 3 || !strings.Contains(textTitles(caps), "CHAPTER TWO") {
		t.Fatalf("labels in capitals: %s (%s)", textTitles(caps), caps.Layout)
	}
	if caps.Breaks == nil || caps.Breaks.ChosenBy != "label" {
		t.Fatalf("how it was cut: %+v", caps.Breaks)
	}
	plain := e.previewText(textBook([]string{"Chapter 1", "Chapter 2", "Chapter 3"}, true), imports.PreviewIn{})
	// plain labels are an offer, never a cut: it is one document, with the three places listed
	if plain.Breaks == nil || !plain.Breaks.NoneFound || len(plain.Breaks.Suggestions) < 3 {
		t.Fatalf("plain labels: %+v", plain.Breaks)
	}
	if strings.Contains(textTitles(plain), "Chapter 2") {
		t.Fatalf("a plain label cut on its own: %s", textTitles(plain))
	}
	// the writer chooses the rule: now they cut
	chosen := e.previewText(textBook([]string{"Chapter 1", "Chapter 2", "Chapter 3"}, true), imports.PreviewIn{DocxSplit: imports.SplitLabel})
	if !strings.Contains(textTitles(chosen), "Chapter 1|Chapter 2|Chapter 3") {
		t.Fatalf("chosen: %s", textTitles(chosen))
	}
	// and a pattern the writer types
	pat := e.previewText(textBook([]string{"Chapter 1", "Chapter 2", "Chapter 3"}, true), imports.PreviewIn{DocxSplit: imports.SplitPattern, DocxSplitPattern: "chapter"})
	if pat.Documents < 3 {
		t.Fatalf("pattern: %s", textTitles(pat))
	}
}

// What was typed is what is shown: Markdown's marks and Scríobh's own syntax in the text are escaped, so a line cannot become a heading, a
// list, a quote, a private note, a link or a wiki link.
func TestTextIsEscapedSoWhatWasTypedIsWhatIsShown(t *testing.T) {
	e := newEnv(t)
	src := "# not a heading\n\n* not a list\n\n1. not a list\n\n> not a quote\n\n%%not private%% and [[not a link]] and [a](http://x) and <b>raw</b> and *stars* and _under_\n\n    four spaces\n\n---\n\nAfter the break.\n"
	f := e.previewText(src, imports.PreviewIn{})
	ap := e.must(e.w, "import.apply", imports.ApplyIn{Token: f.Token, Text: []byte(src), Name: "book.txt"}).(imports.ApplyOut)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: ap.ProjectID}).(docs.ListOut)
	var body string
	for _, m := range list.Items {
		body += e.must(e.w, "doc.get", docs.DocIn{ID: m.ID}).(docs.Item).Item.Content
	}
	for _, bad := range []string{"\n# not", "\n* not", "\n1. not", "\n> not", "[[not a link]]", "%%not private%%", "<b>raw</b>", " [a](http://x)"} {
		if strings.Contains("\n"+body, bad) {
			t.Errorf("%q came through as markup:\n%s", bad, body)
		}
	}
	if !strings.Contains(body, "* * *") || !strings.Contains(body, "four spaces") || !strings.Contains(body, "After the break.") {
		t.Errorf("the scene break and the text are kept:\n%s", body)
	}
	var kind, layout string
	_ = e.d.Read().QueryRow(`SELECT kind, json_extract(metadata_json, '$.layout') FROM project_sources WHERE id = ?`, ap.SourceID).Scan(&kind, &layout)
	if kind != "markdown" || layout != "text" {
		t.Errorf("recorded as %s / %s", kind, layout)
	}
}

func TestTextIsRefusedInWordsWhenItIsNotText(t *testing.T) {
	e := newEnv(t)
	for name, data := range map[string]string{
		"a NUL byte":     "Some text\x00more",
		"UTF-16 (LE)":    "\xff\xfeH\x00i\x00",
		"UTF-16 (BE)":    "\xfe\xff\x00H\x00i",
		"only blank":     "\n\n   \n",
		"over the limit": strings.Repeat("a ", imports.MaxText/2+1),
	} {
		_, err := e.call(e.w, "import.preview", imports.PreviewIn{Text: []byte(data), Name: "x.txt"})
		oe := ops.AsError(err)
		if err == nil || oe.Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// invalid UTF-8 is read, and counted
	pv := e.previewText("A line with a bad byte \xe9 in it.\n\nAnd another.\n", imports.PreviewIn{})
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "1 byte(s) were not valid UTF-8") {
		t.Errorf("warnings: %v", pv.Warnings)
	}
	// a file with no blank line is a paragraph a line, and says so
	pv = e.previewText("One line.\nAnother line.\nA third.\n", imports.PreviewIn{})
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "each line was read as a paragraph") {
		t.Errorf("warnings: %v", pv.Warnings)
	}
}

// A hostile text file costs what an ordinary one does: a hundred thousand label lines are refused at the document cap before anything is
// made of them, and one enormous line is one paragraph.
func TestTextHostileFilesAreBounded(t *testing.T) {
	e := newEnv(t)
	var b strings.Builder
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(&b, "CHAPTER %d\n\nA few words of text under it.\n\n", i+1)
	}
	in := imports.PreviewIn{Text: []byte(b.String()), Name: "x.txt"}
	_, err := e.call(e.w, "import.preview", in)
	oe := ops.AsError(err)
	if err == nil || !strings.Contains(oe.Message, "documents") {
		t.Fatalf("100,000 chapters: %v", err)
	}
	big := strings.Repeat("word ", 400_000) // one 2 MB paragraph
	pv := e.previewText(big+"\n", imports.PreviewIn{})
	if pv.Documents != 1 || pv.Words != 400_000 {
		t.Errorf("one very long line is one paragraph: %d documents, %d words", pv.Documents, pv.Words)
	}
	// the same text with a pattern whose work is too much is refused, as for a Word file
	long := strings.Repeat("x", 300)
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Text: []byte("One.\n\nTwo.\n"), Name: "x.txt", DocxSplit: imports.SplitPattern, DocxSplitPattern: long}); err == nil {
		t.Error("a pattern over 200 characters was accepted")
	}
}

// A staged text file has the review a Word file has: the cut panel's rules, a chapter started by hand, and the edits of the chapters that
// remain kept across a change of rule.
func TestStagedTextKeepsEditsAcrossAChangeOfRuleAndStartsChaptersByHand(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	data := textBook([]string{"Chapter 1", "Chapter 2", "Chapter 3"}, true)
	st := e.mustStage(e.w, imports.StageIn{Kind: "text", Name: "book.txt"}, []byte(data))
	d := e.get(e.w, st.StageID, true)
	if d.Item.Kind != "text" || d.Preview.Documents != 1 || len(d.Preview.Breaks.Suggestions) < 3 {
		t.Fatalf("staged: %s %d %+v", d.Item.Kind, d.Preview.Documents, d.Preview.Breaks)
	}
	// one chapter started by hand, from the offers
	ord := d.Preview.Breaks.Suggestions[1].Ordinal
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &ord})
	if u.Preview.Documents != 2 || u.Note == nil || u.Note.Code != "break" {
		t.Fatalf("a chapter by hand: %d %+v", u.Preview.Documents, u.Note)
	}
	// choose the rule, rename a chapter, then change the rule: what remains keeps its rename
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "label"}})
	if !strings.Contains(titles(u.Preview), "Chapter 2") {
		t.Fatalf("by label: %s", titles(u.Preview))
	}
	two := "The Mill"
	var key string
	for _, n := range u.Preview.Nodes {
		if n.Title == "Chapter 2" {
			key = n.Key
		}
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: key, Title: &two}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "pattern", "docx_split_pattern": "chapter"}})
	if !strings.Contains(titles(u.Preview), "The Mill") {
		t.Fatalf("a rename survives a change of rule: %s (%+v)", titles(u.Preview), u.Note)
	}
	// apply, and the staged file is gone
	ap := e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: u.Item.Revision, Token: u.Preview.Token}).(imports.ApplyOut)
	if ap.Documents < 3 || e.exists(e.stageDir(st.StageID)) {
		t.Fatalf("applied %d documents; stage dir kept: %v", ap.Documents, e.exists(e.stageDir(st.StageID)))
	}
	// a file that is not text is refused when it is staged
	if _, err := e.stage(e.w, imports.StageIn{Kind: "text", Name: "x.txt"}, []byte("PK\x03\x04 a zip")); err == nil {
		t.Error("a zip was staged as text")
	}
}

// A PDF draft staged before the detector held the key of the one lump a book with no headings used to be ("00-front-matter.md"); the cut
// has no such file now, and the draft must still open: the edit is set aside, listed, and nothing fails.
func TestAPdfDraftFromBeforeTheDetectorStillOpens(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	pe := &env{t: t, d: e.d, reg: e.reg, w: e.w}
	_, pdf, _ := pe.compiledBook(t, "print-6x9", 3)
	st := e.mustStage(e.w, imports.StageIn{Kind: "pdf", Name: "book.pdf"}, pdf)
	d := e.get(e.w, st.StageID, true)
	if d.Preview == nil || d.Preview.Documents < 3 {
		t.Fatalf("staged: %+v", d.Preview)
	}
	// the cut with no file named for the lump: one document, by the detector
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"docx_split": "none"}})
	if u.Preview.Documents != 1 {
		t.Fatalf("one document: %d", u.Preview.Documents)
	}
	// what the earlier build would have stored: an edit on the lump, and no mark that the keys are current
	err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE import_drafts SET edits_json = '{"edits":{"00-front-matter.md":{"title":"My lump"}}}' WHERE id = ?`, st.StageID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if has := func() bool {
		var n int
		_ = e.d.Read().QueryRow(`SELECT count(*) FROM import_drafts WHERE id = ? AND edits_json LIKE '%00-front-matter%'`, st.StageID).Scan(&n)
		return n == 1
	}(); !has {
		t.Fatal("the old edit was not written")
	}
	g, gerr := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: st.StageID, Preview: true})
	if gerr != nil {
		t.Fatalf("a PDF draft with an edit on a key the cut no longer has does not open: %v", gerr)
	}
	out := g.(imports.DraftOut)
	if out.Preview == nil || out.Preview.Documents != 1 {
		t.Fatalf("preview: %+v", out.Preview)
	}
}

// A NUL anywhere in a text file is not text: it is refused when it is staged, by the whole file and not only its first bytes, and no draft
// is kept for it.
func TestATextFileWithANulFarInIsRefusedWhenStagedAndKeepsNothing(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	body := strings.Repeat("A line of ordinary text that goes on for a while.\n\n", 400) + "and then a NUL \x00 far in\n"
	_, err := e.stage(e.w, imports.StageIn{Kind: "text", Name: "x.txt"}, []byte(body))
	oe := ops.AsError(err)
	if err == nil || oe.Code != ops.CodeInvalid || oe.Data["reason"] != "not_text" {
		t.Fatalf("a NUL at byte %d: %v", strings.IndexByte(body, 0), err)
	}
	list := e.must(e.w, "import.draft.list", struct{}{}).(imports.DraftListOut)
	if len(list.Items) != 0 {
		t.Errorf("a draft was kept: %+v", list.Items)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dataDir, "imports", "stage")); len(ents) != 0 {
		t.Errorf("a staged file was kept: %d", len(ents))
	}
}

// What the writer typed is kept: a lone #, ~~~ or ornament stays as escaped text and is counted, and only a line that Markdown itself reads
// as a thematic break (and is the same thing in a text file) becomes the scene break.
func TestTextKeepsALoneHashAndOrnamentsAndMakesOnlyThematicBreaksScenes(t *testing.T) {
	e := newEnv(t)
	src := "First.\n\n#\n\nSecond.\n\n~~~\n\nThird.\n\n⁂\n\nFourth.\n\n***\n\nFifth.\n\n- - -\n\nSixth.\n\n___\n\nSeventh.\n"
	pv := e.previewText(src, imports.PreviewIn{})
	ap := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Text: []byte(src), Name: "book.txt"}).(imports.ApplyOut)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: ap.ProjectID}).(docs.ListOut)
	var body string
	for _, m := range list.Items {
		body += e.must(e.w, "doc.get", docs.DocIn{ID: m.ID}).(docs.Item).Item.Content
	}
	for _, kept := range []string{"\\#", "\\~\\~\\~", "⁂"} {
		if !strings.Contains(body, kept) {
			t.Errorf("%q was typed and is gone:\n%s", kept, body)
		}
	}
	if n := strings.Count(body, "* * *"); n != 3 {
		t.Errorf("%d scene breaks, want 3 (***, - - -, ___):\n%s", n, body)
	}
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "3 line(s) of a lone # or an ornament were kept as text") {
		t.Errorf("not said: %v", pv.Warnings)
	}
}

// What a PDF or Pandoc read, held for a few minutes, is let go of when its draft ends (discarded here), and by its timer if nothing asks again.
func TestWhatWasReadOfABookIsLetGoWhenItsDraftEndsAndByTheTimer(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	pe := &env{t: t, d: e.d, reg: e.reg, w: e.w}
	_, pdf, _ := pe.compiledBook(t, "print-6x9", 3)
	st := e.mustStage(e.w, imports.StageIn{Kind: "pdf", Name: "book.pdf"}, pdf)
	e.get(e.w, st.StageID, true)
	if !imports.RawCached(st.SHA256) {
		t.Fatal("the PDF's text is not held, so this proves nothing")
	}
	e.must(e.w, "import.draft.discard", imports.DraftIDIn{ID: st.StageID})
	if imports.RawCached(st.SHA256) {
		t.Error("the book's text is still in memory after its draft was discarded")
	}
	// applied
	st = e.mustStage(e.w, imports.StageIn{Kind: "pdf", Name: "book.pdf"}, pdf)
	d := e.get(e.w, st.StageID, true)
	e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: d.Item.Revision, Token: d.Preview.Token})
	if imports.RawCached(st.SHA256) {
		t.Error("the book's text is still in memory after its draft was applied")
	}
	// the timer
	imports.RawPut("pdf|orphan", 1)
	if !imports.RawCached("orphan") {
		t.Fatal("nothing was put")
	}
	imports.RawSweepAt(time.Now().Add(imports.RawTTL() + time.Second))
	if imports.RawCached("orphan") {
		t.Error("the timer's sweep kept an entry past its time")
	}
}
