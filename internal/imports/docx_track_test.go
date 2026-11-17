package imports_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// Tracked changes and comments of a Word file become suggestions and comments.

func run(text string) string {
	return `<w:r><w:t xml:space="preserve">` + esc(text) + `</w:t></w:r>`
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

var revID = 100

func ins(author, date, text string) string {
	revID++
	return fmt.Sprintf(`<w:ins w:id="%d" w:author="%s" w:date="%s">%s</w:ins>`, revID, esc(author), date, run(text))
}

func del(author, date, text string) string {
	revID++
	return fmt.Sprintf(`<w:del w:id="%d" w:author="%s" w:date="%s"><w:r><w:delText xml:space="preserve">%s</w:delText></w:r></w:del>`, revID, esc(author), date, esc(text))
}

func para2(parts ...string) string { return `<w:p>` + strings.Join(parts, "") + `</w:p>` }

func (e *env) suggestions(projectID string) []review.SuggestionOut {
	e.t.Helper()
	out := e.must(e.w, "suggestion.list", review.SuggestionListIn{ProjectID: projectID, Status: "all", Limit: 200}).(review.SuggestionListOut)
	return out.Items
}

// The invariant that makes the default honest: the text as imported, with every
// suggestion accepted, is the text the "accept" import gives.
func TestTrackedChangesBecomeSuggestionsAndAcceptingThemGivesTheFinalText(t *testing.T) {
	e := newEnv(t)
	d1, d2 := "2024-03-05T10:21:00Z", "2024-04-09T08:00:00Z"
	doc := body(
		para("Title", "The Book") + para("Heading1", "One") +
			para2(run("The boat came in at "), del("Ann Editor", d1, "dawn"), ins("Ann Editor", d1, "first light"), run(" and the harbour was quiet. Nobody "), ins("Bo Reader", d2, "ever "), run("spoke of the winter.")) +
			para2(run("A second paragraph that stays "), del("Bo Reader", d2, "very "), run("exactly as it was written, with *stars* and [brackets] and a_b.")) +
			para("Heading1", "Two") +
			para2(run("Chapter two has "), ins("Ann Editor", d1, "an added sentence. "), run("Then the rest of the text goes on for a while so it is long enough.")))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	if pv.Suggestions != 3 || pv.Comments != 0 {
		t.Fatalf("preview: %d suggestions, %d comments (%v)", pv.Suggestions, pv.Comments, pv.Warnings)
	}
	w := strings.Join(pv.Warnings, "\n")
	if !strings.Contains(w, "original, as its author left it with every change rejected") || !strings.Contains(w, "2 Word author(s)") {
		t.Errorf("the preview does not say what the text is: %s", w)
	}
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	if ap.Suggestions != 3 {
		t.Fatalf("apply wrote %d suggestions, the preview said %d", ap.Suggestions, pv.Suggestions)
	}
	one := e.docContent(ap.ProjectID, "One")
	// Bo's "ever" is in the same sentence as Ann's replacement: a suggestion has one author, so the later of the two
	// (Bo's) is taken as accepted and is in the text, and the preview says so. Ann's replacement is a suggestion.
	if !strings.Contains(one, "The boat came in at dawn and the harbour was quiet. Nobody ever spoke of the winter.") || strings.Contains(one, "first light") {
		t.Errorf("the body is not the original with Bo's change in it:\n%s", one)
	}
	if !strings.Contains(w, "Bo Reader / Ann Editor") || !strings.Contains(w, "taken as accepted") {
		t.Errorf("the preview does not name the two authors whose changes were too close: %s", w)
	}
	sugs := e.suggestions(ap.ProjectID)
	if len(sugs) != 3 {
		t.Fatalf("%d suggestions listed", len(sugs))
	}
	labels := map[string]int{}
	for _, s := range sugs {
		if s.Author.Kind != "label" || s.Status != "open" || len(s.Changes) < 1 {
			t.Errorf("suggestion %+v", s)
		}
		labels[s.Author.Name]++
		if s.Author.UserID != "" {
			t.Errorf("a Word author became an account: %+v", s.Author)
		}
		if !strings.HasPrefix(s.CreatedAt, "2024-0") {
			t.Errorf("created %q: the time Word recorded is lost", s.CreatedAt)
		}
	}
	// two changes in one sentence by two authors cannot be two suggestions: one author each
	if labels["Ann Editor"] != 2 || labels["Bo Reader"] != 1 {
		t.Errorf("labels %v", labels)
	}
	// accept every one, document by document
	byDoc := map[string][]string{}
	for _, s := range sugs {
		byDoc[s.DocumentID] = append(byDoc[s.DocumentID], s.ID)
	}
	for id, list := range byDoc {
		out := e.must(e.w, "suggestion.accept_all", review.SuggestionAcceptAllIn{ProjectID: ap.ProjectID, DocumentID: id, SuggestionIDs: list}).(review.SuggestionAcceptAllOut)
		if len(out.Skipped) != 0 || len(out.Applied) != len(list) {
			t.Fatalf("accept all: %+v", out)
		}
	}
	// the same file, imported with the changes accepted
	pa := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
	if pa.Suggestions != 0 {
		t.Errorf("accept mode proposes %d suggestions", pa.Suggestions)
	}
	aa := e.applyDocx(d, pa, imports.ApplyIn{DocxChanges: "accept"})
	for _, title := range []string{"One", "Two"} {
		got, want := e.docContent(ap.ProjectID, title), e.docContent(aa.ProjectID, title)
		if got != want {
			t.Errorf("%s: accepting all the suggestions gives\n%q\nthe accept import gives\n%q", title, got, want)
		}
	}
	// reject mode: the original and nothing proposed
	pr := e.previewDocx(d, imports.PreviewIn{DocxChanges: "reject"})
	ar := e.applyDocx(d, pr, imports.ApplyIn{DocxChanges: "reject"})
	if ar.Suggestions != 0 || !strings.Contains(e.docContent(ar.ProjectID, "One"), "Nobody spoke of the winter.") {
		t.Errorf("reject mode: %d suggestions, text %q", ar.Suggestions, e.docContent(ar.ProjectID, "One"))
	}
	// the token covers the changes: another mode is another preview
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Docx: d, Name: "book.docx", DocxChanges: "accept"}); err == nil {
		t.Error("a preview of suggestions applied as accept")
	}
}

func TestSuggestionsFromWordNotifyNobodyAndAreOpen(t *testing.T) {
	e := newEnv(t)
	doc := body(para("Heading1", "One") + para2(run("A line of text that is long enough to be found, "), ins("Ann", "2024-03-05T10:21:00Z", "plus this, "), run("and it ends here.")))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM notifications WHERE project_id = ?`, ap.ProjectID).Scan(&n)
	if n != 0 {
		t.Errorf("%d notifications for an import", n)
	}
	var label string
	var author *string
	e.d.Read().QueryRow(`SELECT author_label, author_id FROM suggestions WHERE project_id = ?`, ap.ProjectID).Scan(&label, &author)
	if label != "Ann" || author != nil {
		t.Errorf("label %q author %v", label, author)
	}
}

func TestStructuralChangesAreTakenAsTheReviewerLeftThemAndCounted(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	insertedPara := `<w:p><w:pPr><w:rPr><w:ins w:id="900" w:author="Ann" w:date="` + d1 + `"/></w:rPr></w:pPr>` + ins("Ann", d1, "A wholly new paragraph.") + `</w:p>`
	deletedPara := `<w:p><w:pPr><w:rPr><w:del w:id="901" w:author="Ann" w:date="` + d1 + `"/></w:rPr></w:pPr>` + del("Ann", d1, "A paragraph the reviewer cut entirely.") + `</w:p>`
	headingChange := `<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr>` + run("A head") + ins("Ann", d1, "ing") + `</w:p>`
	tbl := `<w:tbl><w:tr><w:tc><w:p>` + run("cell ") + ins("Ann", d1, "added") + `</w:p></w:tc><w:tc><w:p>` + run("other") + `</w:p></w:tc></w:tr></w:tbl>`
	notes := `<?xml version="1.0"?><w:footnotes ` + wns + `><w:footnote w:id="1"><w:p>` + run("Note ") + ins("Ann", d1, "text") + `</w:p></w:footnote></w:footnotes>`
	doc := body(para("Heading1", "One") + para2(run("Keep this paragraph as it is, long enough to matter.")) + insertedPara + deletedPara + headingChange + tbl +
		para2(run("With a note"), `<w:r><w:footnoteReference w:id="1"/></w:r>`, run(" and "), ins("Ann", d1, "a plain change"), run(" at the end of the text here.")))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles, "word/footnotes.xml": notes})
	pv := e.previewDocx(d, imports.PreviewIn{})
	rep := pv.Report["docx"].(map[string]any)["tracked"].(map[string]any)
	got := fmt.Sprintf("%v/%v/%v/%v/%v/%v", rep["suggestions"], rep["inserted_paragraphs"], rep["deleted_paragraphs"], rep["in_headings"], rep["in_tables"], rep["in_footnotes"])
	if got != "1/1/1/1/1/1" {
		t.Errorf("counts (suggestions/inserted/deleted/headings/tables/footnotes) %s: %v", got, rep)
	}
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	text := e.docContent(ap.ProjectID, "One")
	for _, want := range []string{"A wholly new paragraph.", "A heading", "cell added", "Note text"} {
		if !strings.Contains(text, want) {
			t.Errorf("the final view of a structural change is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "cut entirely") {
		t.Errorf("a deleted paragraph is still there:\n%s", text)
	}
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "could not be a suggestion and were taken as the reviewer left them") {
		t.Errorf("not said: %v", pv.Warnings)
	}
}

func TestWordCommentsBecomeCommentsByLabelAnchoredWhereTheyWereMade(t *testing.T) {
	e := newEnv(t)
	doc := body(para("Heading1", "One") +
		`<w:p>` + run("The first sentence is long enough to find. ") + `<w:commentRangeStart w:id="0"/>` + run("This phrase has a remark on it") + `<w:commentRangeEnd w:id="0"/><w:r><w:commentReference w:id="0"/></w:r>` + run(" and the text goes on.") + `</w:p>` +
		`<w:p>` + run("A point comment lands here.") + `<w:r><w:commentReference w:id="1"/></w:r></w:p>` +
		`<w:p>` + run("A text with a comment that is never referenced elsewhere.") + `</w:p>`)
	comments := `<?xml version="1.0"?><w:comments ` + wns + `>` +
		`<w:comment w:id="0" w:author="Ann Editor" w:date="2024-03-05T10:21:00Z"><w:p><w:r><w:t>Say this better.</w:t></w:r></w:p><w:p><w:r><w:t>Second paragraph of it.</w:t></w:r></w:p></w:comment>` +
		`<w:comment w:id="1" w:author="Bo" w:date="not a date"><w:p><w:r><w:t>A comment on a point.</w:t></w:r></w:p></w:comment>` +
		`<w:comment w:id="2" w:author="Cy"><w:p><w:r><w:t>Never referenced.</w:t></w:r></w:p></w:comment></w:comments>`
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles, "word/comments.xml": comments})
	pv := e.previewDocx(d, imports.PreviewIn{})
	if pv.Comments != 2 {
		t.Fatalf("preview: %d comments: %v", pv.Comments, pv.Warnings)
	}
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	if ap.Comments != 2 {
		t.Fatalf("apply: %d comments", ap.Comments)
	}
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: ap.ProjectID}).(docs.ListOut)
	var docID string
	for _, m := range lo.Items {
		if m.Title == "One" {
			docID = m.ID
		}
	}
	out := e.must(e.w, "comment.list", review.ListIn{ProjectID: ap.ProjectID, DocumentID: docID, Status: "all"}).(review.ListOut)
	if len(out.Items) != 2 {
		t.Fatalf("%d threads", len(out.Items))
	}
	by := map[string]review.CommentOut{}
	for _, c := range out.Items {
		by[c.Author.Name] = c
	}
	a, b := by["Ann Editor"], by["Bo"]
	if a.Author.Kind != "label" || a.Author.UserID != "" || !strings.Contains(a.Body, "Say this better.") || !strings.Contains(a.Body, "Second paragraph") {
		t.Errorf("Ann's comment: %+v", a)
	}
	if a.Anchor == nil || a.Anchor.Quote != "This phrase has a remark on it" || a.Anchor.State != "active" {
		t.Errorf("anchor: %+v", a.Anchor)
	}
	if !strings.HasPrefix(a.CreatedAt, "2024-03-05") {
		t.Errorf("created %q", a.CreatedAt)
	}
	if b.Anchor != nil || strings.HasPrefix(b.CreatedAt, "not") {
		t.Errorf("Bo's comment: anchor %+v created %q", b.Anchor, b.CreatedAt)
	}
	// they are comments, not content, and they notify nobody
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM notifications WHERE project_id = ?`, ap.ProjectID).Scan(&n)
	if n != 0 {
		t.Errorf("%d notifications", n)
	}
	if strings.Contains(e.docContent(ap.ProjectID, "One"), "Say this better") {
		t.Error("comment text is in the document")
	}
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "3 Word comment(s): 2 imported") {
		t.Errorf("warnings: %v", pv.Warnings)
	}
}

func TestTrackedChangeCapsAreEnforcedAndSaid(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	var b strings.Builder
	b.WriteString(para("Heading1", "One"))
	// 400 paragraphs, each with its own change in a sentence that is its own
	for i := 0; i < 400; i++ {
		b.WriteString(para2(run(fmt.Sprintf("Sentence number %d starts here and ", i)), ins("Ann", d1, fmt.Sprintf("adds word%d ", i)), run(fmt.Sprintf("ends there, item %d.", i))))
	}
	doc := body(b.String())
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	if pv.Suggestions != 300 {
		t.Fatalf("a document is capped at 300: %d (%v)", pv.Suggestions, pv.Warnings)
	}
	rep := pv.Report["docx"].(map[string]any)["tracked"].(map[string]any)
	if rep["over_cap"].(int) != 100 {
		t.Errorf("over the cap: %v", rep["over_cap"])
	}
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	if ap.Suggestions != 300 || len(e.suggestions(ap.ProjectID)) != 200 {
		t.Errorf("wrote %d", ap.Suggestions)
	}
	text := e.docContent(ap.ProjectID, "One")
	// the first 300 are suggestions (not in the text), the last 100 are accepted (in it)
	if strings.Contains(text, "adds word0 ") || !strings.Contains(text, "adds word399 ") {
		t.Errorf("which were accepted: word0 %v word399 %v", strings.Contains(text, "adds word0 "), strings.Contains(text, "adds word399 "))
	}
	// 1,200 changes over four chapters: the import caps at 1,000
	var big strings.Builder
	for c := 0; c < 4; c++ {
		big.WriteString(para("Heading1", fmt.Sprintf("Chapter %d", c)))
		for i := 0; i < 300; i++ {
			big.WriteString(para2(run(fmt.Sprintf("Ch%d sentence %d starts here and ", c, i)), ins("Ann", d1, fmt.Sprintf("adds c%dw%d ", c, i)), run(fmt.Sprintf("ends there, item %d.", i))))
		}
	}
	d2 := miniDocx(t, map[string]string{"word/document.xml": body(big.String()), "word/styles.xml": headingStyles})
	pv2 := e.previewDocx(d2, imports.PreviewIn{})
	if pv2.Suggestions != 1000 {
		t.Errorf("an import is capped at 1,000: %d", pv2.Suggestions)
	}
}

func TestSuggestionsThatWouldBreakTheProjectLimitRefuseTheImport(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	mk := func(n int, tag string) []byte {
		var b strings.Builder
		b.WriteString(para("Heading1", "One"+tag))
		for i := 0; i < n; i++ {
			b.WriteString(para2(run(fmt.Sprintf("%s sentence %d starts here and ", tag, i)), ins("Ann", d1, fmt.Sprintf("adds %s%d ", tag, i)), run(fmt.Sprintf("ends there, item %d.", i))))
		}
		return miniDocx(t, map[string]string{"word/document.xml": body(b.String()), "word/styles.xml": headingStyles})
	}
	first := mk(290, "a")
	pv := e.previewDocx(first, imports.PreviewIn{})
	ap := e.applyDocx(first, pv, imports.ApplyIn{})
	for i := 0; i < 5; i++ { // 6 x 290 = 1,740 open
		d := mk(290, fmt.Sprintf("b%d", i))
		ppv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: ap.ProjectID, Docx: d, Name: "more.docx"}).(imports.PreviewOut)
		if _, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: ap.ProjectID, Token: ppv.Token, Docx: d, Name: "more.docx"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	d := mk(290, "z")
	ppv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: ap.ProjectID, Docx: d, Name: "more.docx"}).(imports.PreviewOut)
	_, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: ap.ProjectID, Token: ppv.Token, Docx: d, Name: "more.docx"})
	if err == nil || ops.AsError(err).Code != ops.CodeConflict || !strings.Contains(err.Error(), "over the limit of 2000") {
		t.Fatalf("over the project's limit: %v", err)
	}
	// nothing of it was written: the import is one transaction
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND title = 'Onez'`, ap.ProjectID).Scan(&n)
	if n != 0 {
		t.Errorf("the refused import left a document")
	}
	// accepting instead is allowed
	pa := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: ap.ProjectID, Docx: d, Name: "more.docx", DocxChanges: "accept"}).(imports.PreviewOut)
	if _, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: ap.ProjectID, Token: pa.Token, Docx: d, Name: "more.docx", DocxChanges: "accept"}); err != nil {
		t.Errorf("accept mode: %v", err)
	}
}

func TestChangesThatCannotBePlacedOrAppliedAreTakenAsAcceptedNotLost(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	// the same sentence twice with the same change: the place cannot be told apart
	same := para2(run("Exactly the same words, "), ins("Ann", d1, "twice "), run("in one paragraph."))
	// an insertion that would make a heading, and one that carries a link the text did not have
	asHeading := para2(run("A paragraph that is plain "), ins("Ann", d1, "\n# not a heading but"), run(" ordinary text with more words to find."))
	link := para2(run("A paragraph with no links "), ins("Ann", d1, "[click](https://evil.example/)"), run(" in it, and more words after."))
	doc := body(para("Heading1", "One") + same + same + asHeading + link)
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	rep := pv.Report["docx"].(map[string]any)["tracked"].(map[string]any)
	t.Logf("tracked: %v", rep)
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	text := e.docContent(ap.ProjectID, "One")
	// every inserted text is either a suggestion or in the body: none is lost
	sugs := e.suggestions(ap.ProjectID)
	inSug := func(s string) bool {
		for _, x := range sugs {
			for _, c := range x.Changes {
				if strings.Contains(c.Text, s) {
					return true
				}
			}
		}
		return false
	}
	for _, want := range []string{"twice", "not a heading but", "click"} {
		if !strings.Contains(text, want) && !inSug(want) {
			t.Errorf("%q is in neither the text nor a suggestion", want)
		}
	}
	if ap.Suggestions != pv.Suggestions {
		t.Errorf("the preview said %d suggestions, apply wrote %d", pv.Suggestions, ap.Suggestions)
	}
	for _, w := range ap.Warnings {
		if strings.Contains(w, "could not be placed in the stored text") {
			t.Errorf("the preview's decision was not the apply's: %s", w)
		}
	}
}

func TestTrackedHostileFiles(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	filler := run("A paragraph of plain words that is long enough to be found again and again. ")
	cases := map[string]string{
		"nested ins in del in ins": para2(filler, `<w:ins w:id="1" w:author="A" w:date="`+d1+`"><w:del w:id="2" w:author="B" w:date="`+d1+`"><w:ins w:id="3" w:author="C"><w:r><w:delText>deep</w:delText></w:r></w:ins></w:del></w:ins>`, filler),
		"overlapping authors":      para2(filler, ins("A", d1, "one "), ins("B", d1, "two "), del("A", d1, "three "), filler),
		"revision in a field":      para2(filler, `<w:r><w:fldChar w:fldCharType="begin"/></w:r><w:ins w:id="5" w:author="A"><w:r><w:instrText>HYPERLINK "http://x"</w:instrText></w:r></w:ins><w:r><w:fldChar w:fldCharType="separate"/></w:r>`+ins("A", d1, "field text")+`<w:r><w:fldChar w:fldCharType="end"/></w:r>`, filler),
		"forged markers":           para2(filler, ins("A\uE000\uE001", d1, "x\uE002\uE003y"), run("tail \uE000999\uE001 and \uE004\uE005"), filler),
		"control characters":       para2(filler, ins("A\n\t \u202Eevil\u200B", d1, "text"), filler),
		"absurd date":              para2(filler, ins("A", "9999-01-01T00:00:00Z", "future"), filler, ins("B", "1066-01-01T00:00:00Z", "past"), filler),
		"empty ins":                para2(filler, `<w:ins w:id="7" w:author="A"/>`, filler),
		"delete everything":        para2(del("A", d1, "A paragraph that is deleted whole, with a mark."), `<w:r><w:t></w:t></w:r>`),
		"move":                     para2(filler, `<w:moveFrom w:id="8" w:author="A"><w:r><w:delText>moved text </w:delText></w:r></w:moveFrom>`, filler, `<w:moveTo w:id="9" w:author="A"><w:r><w:t>moved text </w:t></w:r></w:moveTo>`),
		"formatting change":        para2(`<w:r><w:rPr><w:b/><w:rPrChange w:id="10" w:author="A"><w:rPr/></w:rPrChange></w:rPr><w:t>Bold now, long enough to be a paragraph of text.</w:t></w:r>`),
	}
	for name, p := range cases {
		doc := body(para("Heading1", "One") + p)
		d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
		pv, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: d, Name: "x.docx"})
		if err != nil {
			t.Errorf("%s: preview: %v", name, err)
			continue
		}
		out := pv.(imports.PreviewOut)
		ap, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: out.Token, Docx: d, Name: "x.docx"})
		if err != nil {
			t.Errorf("%s: apply: %v", name, err)
			continue
		}
		a := ap.(imports.ApplyOut)
		if a.Suggestions != out.Suggestions {
			t.Errorf("%s: preview %d suggestions, apply %d", name, out.Suggestions, a.Suggestions)
		}
		text := e.docContent(a.ProjectID, "One")
		if strings.ContainsAny(text, "\uE000\uE001\uE002\uE003\uE004\uE005\uE006\uE007") {
			t.Errorf("%s: a marker reached the text: %q", name, text)
		}
		for _, s := range e.suggestions(a.ProjectID) {
			if strings.ContainsAny(s.Author.Name, "\n\t\u202E\u200B\uE000") || len(s.Author.Name) > 100 {
				t.Errorf("%s: label %q", name, s.Author.Name)
			}
			for _, c := range s.Changes {
				if strings.ContainsAny(c.Text+c.Original, "\uE000\uE001\uE002\uE003\uE004\uE005\uE006\uE007") {
					t.Errorf("%s: a marker reached a suggestion", name)
				}
			}
			if s.CreatedAt > "2200" || s.CreatedAt < "1995" || strings.HasPrefix(s.CreatedAt, "9999") {
				t.Errorf("%s: created %q", name, s.CreatedAt)
			}
		}
	}
}

// A very large number of revisions is bounded in time and memory, and the
// changes over the cap are taken as accepted.
func TestAFloodOfRevisionsIsBounded(t *testing.T) {
	e := newEnv(t)
	var b strings.Builder
	b.WriteString(para("Heading1", "One"))
	b.WriteString(`<w:p>`)
	for i := 0; i < 12_000; i++ {
		b.WriteString(run(fmt.Sprintf("w%d ", i)))
		b.WriteString(fmt.Sprintf(`<w:ins w:id="%d" w:author="A%d" w:date="2024-03-05T10:21:00Z"><w:r><w:t>x</w:t></w:r></w:ins>`, i, i%7))
	}
	b.WriteString(`</w:p>`)
	d := miniDocx(t, map[string]string{"word/document.xml": body(b.String()), "word/styles.xml": headingStyles})
	pv, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: d, Name: "x.docx"})
	if err != nil {
		t.Fatal(err)
	}
	out := pv.(imports.PreviewOut)
	if out.Suggestions > 300 {
		t.Errorf("%d suggestions from one paragraph", out.Suggestions)
	}
}

func TestPreviewAndApplyAgreeOnTheNumbers(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	doc := body(para("Heading1", "One") + para2(run("Some text that is long enough to find, "), ins("Ann", d1, "more, "), run("and it ends.")) +
		para("Heading1", "Two") + para2(run("Other text that is also long enough to find, "), del("Bo", d1, "cut, "), run("and it ends too.")))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	// leave the second chapter out: its suggestion is not counted or written
	skip := pv.Nodes[1].Key
	pv2 := e.must(e.w, "import.preview", imports.PreviewIn{Docx: d, Name: "book.docx", Edits: []imports.Edit{{Key: skip, Skip: true}}}).(imports.PreviewOut)
	if pv.Suggestions != 2 || pv2.Suggestions != 1 {
		t.Errorf("preview %d, with a chapter left out %d", pv.Suggestions, pv2.Suggestions)
	}
	ap := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv2.Token, Docx: d, Name: "book.docx", Edits: []imports.Edit{{Key: skip, Skip: true}}}).(imports.ApplyOut)
	if ap.Suggestions != 1 || len(e.suggestions(ap.ProjectID)) != 1 {
		t.Errorf("apply %d", ap.Suggestions)
	}
}

func italicRun(text string) string {
	return `<w:r><w:rPr><w:i/></w:rPr><w:t xml:space="preserve">` + esc(text) + `</w:t></w:r>`
}

func boldRun(text string) string {
	return `<w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">` + esc(text) + `</w:t></w:r>`
}

func insFmt(author, date, inner string) string {
	revID++
	return fmt.Sprintf(`<w:ins w:id="%d" w:author="%s" w:date="%s">%s</w:ins>`, revID, esc(author), date, inner)
}

func delFmt(author, date, text string, italic bool) string {
	revID++
	rpr := ""
	if italic {
		rpr = `<w:rPr><w:i/></w:rPr>`
	}
	return fmt.Sprintf(`<w:del w:id="%d" w:author="%s" w:date="%s"><w:r>%s<w:delText xml:space="preserve">%s</w:delText></w:r></w:del>`, revID, esc(author), date, rpr, esc(text))
}

// A change inside or at the edge of bold or italic text must still give, when
// accepted, exactly what the "accept" import gives: the markers never cut a
// span in two, and where they would the paragraph is taken as accepted.
func TestChangesInsideAndAtTheEdgeOfFormattedTextAcceptToTheSameText(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	paras := []string{
		para2(run("A long enough paragraph where the word is "), italicRun("quiet"), insFmt("A", d1, italicRun("ly")), run(" said, and then the text goes on for a while.")),
		para2(run("Another long paragraph where "), delFmt("A", d1, "very", true), insFmt("A", d1, italicRun("so")), run(" fine words are in italics here, and more follow it.")),
		para2(run("A third paragraph of plain words, "), insFmt("A", d1, italicRun("with italic added")), run(" and then a plain end of the sentence, long enough.")),
		para2(boldRun("Bold start "), insFmt("A", d1, boldRun("and bold more ")), boldRun("and bold end"), run(" followed by plain words that run on for a good while.")),
		para2(italicRun("Italic words "), insFmt("A", d1, run("then plain inserted ")), run("and plain after the italic, long enough to be found.")),
		para2(run("Plain words then "), delFmt("A", d1, "deleted italic text", true), run(" and the plain words go on, long enough here.")),
	}
	doc := body(para("Heading1", "One") + strings.Join(paras, ""))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	byDoc := map[string][]string{}
	for _, s := range e.suggestions(ap.ProjectID) {
		byDoc[s.DocumentID] = append(byDoc[s.DocumentID], s.ID)
	}
	for id, list := range byDoc {
		out := e.must(e.w, "suggestion.accept_all", review.SuggestionAcceptAllIn{ProjectID: ap.ProjectID, DocumentID: id, SuggestionIDs: list}).(review.SuggestionAcceptAllOut)
		if len(out.Skipped) != 0 {
			t.Fatalf("skipped: %+v", out.Skipped)
		}
	}
	pa := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
	aa := e.applyDocx(d, pa, imports.ApplyIn{DocxChanges: "accept"})
	got, want := e.docContent(ap.ProjectID, "One"), e.docContent(aa.ProjectID, "One")
	if got != want {
		t.Errorf("accepting every suggestion gives\n%q\nthe accept import gives\n%q", got, want)
	}
	t.Logf("%d suggestions of %d changes; %v", pv.Suggestions, 6, pv.Report["docx"].(map[string]any)["tracked"].(map[string]any)["at_formatting_edges"])
}

func TestWhatCannotBeWrittenIsNotCountedAndNothingIsLost(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	big := strings.Repeat("a very long inserted passage ", 2500) // over 50,000 bytes
	same := para2(run("A paragraph where the reviewer deleted a phrase and "), del("A", d1, "retyped the same words"), ins("A", d1, "retyped the same words"), run(" so the text is the same, long enough."))
	huge := para2(run("A paragraph that takes a very large insertion, "), ins("A", d1, big), run(" and goes on with plain words after it, long enough."))
	doc := body(para("Heading1", "One") + same + huge +
		`<w:p>` + run("A paragraph with a comment whose text is empty. ") + `<w:r><w:commentReference w:id="0"/></w:r></w:p>`)
	comments := `<?xml version="1.0"?><w:comments ` + wns + `><w:comment w:id="0" w:author="Ann"><w:p/></w:comment></w:comments>`
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles, "word/comments.xml": comments})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	if ap.Suggestions != pv.Suggestions || ap.Comments != pv.Comments {
		t.Errorf("preview %d suggestions %d comments, apply %d and %d", pv.Suggestions, pv.Comments, ap.Suggestions, ap.Comments)
	}
	for _, w := range ap.Warnings {
		if strings.Contains(w, "could not be placed in the stored text") || strings.Contains(w, "could not be written") {
			t.Errorf("apply lost something the preview did not say: %s", w)
		}
	}
	text := e.docContent(ap.ProjectID, "One")
	inSug := false
	for _, s := range e.suggestions(ap.ProjectID) {
		for _, c := range s.Changes {
			if strings.Contains(c.Text, "very long inserted") {
				inSug = true
			}
		}
	}
	if !strings.Contains(text, "very long inserted passage") && !inSug {
		t.Error("the large insertion is in neither the text nor a suggestion")
	}
}

// One large chapter with 300 changes: each suggestion is checked against the
// chapter, inside the import's transaction.
func TestThreeHundredChangesInOneLargeChapterApplyInTime(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	var b strings.Builder
	b.WriteString(para("Heading1", "One"))
	filler := strings.Repeat("The harbour smelled of salt and tar while the gulls wheeled over the quay. ", 12)
	for i := 0; i < 1100; i++ { // about a megabyte of text
		if i%4 == 0 && i/4 < 300 {
			b.WriteString(para2(run(fmt.Sprintf("Paragraph %d begins here and ", i)), ins("Ann", d1, fmt.Sprintf("adds word%d ", i)), run("then goes on. "+filler)))
		} else {
			b.WriteString(para2(run(fmt.Sprintf("Paragraph %d. %s", i, filler))))
		}
	}
	d := miniDocx(t, map[string]string{"word/document.xml": body(b.String()), "word/styles.xml": headingStyles})
	start := time.Now()
	pv := e.previewDocx(d, imports.PreviewIn{})
	tPreview := time.Since(start)
	start = time.Now()
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	tApply := time.Since(start)
	t.Logf("a %d KB chapter, %d suggestions: preview %v, apply %v", len(e.docContent(ap.ProjectID, "One"))>>10, ap.Suggestions, tPreview, tApply)
	if ap.Suggestions != 275 && ap.Suggestions != 300 {
		t.Logf("suggestions %d", ap.Suggestions)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if tApply > 30*time.Second*timingFactor {
			t.Errorf("apply took %v", tApply)
		}
	})
}

// A property of the whole feature: for random tracked-change documents (a small vocabulary, so that words
// repeat and the stored context of a suggestion cannot always tell its place from another; changes by several
// authors, often beside each other), accepting every suggestion with accept_all, and accepting them one at a
// time in EVERY order for up to six suggestions (a sample of orders for more), both give what the "accept" import
// gives; a change that cannot be proposed is taken as accepted, and the preview counts it. Some documents are
// sparse, so that many suggestions are written and the fallbacks (one that cannot be found again after another is
// accepted) are what is tested rather than the conflict rule.
func TestRandomTrackedChangeDocumentsAcceptToTheAcceptImport(t *testing.T) {
	e := newEnv(t)
	small := strings.Fields("cat dog mat sat ran far cat dog mat the a")
	large := strings.Fields("harbour salt quay gull net wall winter bag letter road shore kiln field sea tide stone boat dawn light slow quiet house window river hill")
	dates := []string{"2024-03-05T10:21:00Z", "2024-03-06T10:21:00Z", "2024-02-01T09:00:00Z"} // later in the file is not always later in time
	rounds := 240
	totalSug, totalAcc, totalRefused, totalConflict, totalOrders := 0, 0, 0, 0, 0
	defer func() {
		t.Logf("%d random documents: %d suggestions written, %d changes taken as accepted (%d by the rule that a suggestion must be found again, %d beside a change of another author), %d orders of acceptance tried", rounds, totalSug, totalAcc, totalRefused, totalConflict, totalOrders)
	}()
	if testing.Short() {
		rounds = 12
	}
	for round := 0; round < rounds; round++ {
		words := small
		switch round % 4 {
		case 1: // a larger vocabulary: most changes can then be proposed, and many are close
			words = large
		}
		refrain := round%4 == 3
		if refrain {
			words = strings.Fields("the sea and the shore")
		}
		sparse := round%4 >= 2 // few changes, far apart, in repeated text: many suggestions, the fallbacks matter
		authors := 3
		if round%3 == 0 {
			authors = 1 // one author: no conflicts between authors, only the finding-again rule
		}
		seed := uint32(round*7919 + 13)
		next := func(n int) int { seed = seed*1664525 + 1013904223; return int(seed>>8) % n }
		var b strings.Builder
		b.WriteString(para("Heading1", "One"))
		for p := 0; p < 1+next(3); p++ {
			var parts []string
			for w := 0; w < 8+next(40)+map[bool]int{true: 40}[refrain]; w++ {
				word := words[next(len(words))]
				if refrain {
					word = words[w%len(words)] // a refrain: the same words, in the same order, over and over
				}
				author := fmt.Sprintf("A%d", next(authors))
				d1 := dates[next(len(dates))]
				kind := next(10)
				if sparse && next(4) != 0 {
					kind = 0
				}
				switch r := kind; {
				case r < 6:
					parts = append(parts, run(word+" "))
				case r == 6:
					parts = append(parts, ins(author, d1, word+" "))
				case r == 7:
					parts = append(parts, del(author, d1, word+" "))
				case r == 8:
					parts = append(parts, del(author, d1, word+" "), ins(author, d1, words[next(len(words))]+" "))
				default:
					parts = append(parts, ins(author, d1, word+" "), del(fmt.Sprintf("A%d", next(authors)), d1, words[next(len(words))]+" "))
				}
			}
			parts = append(parts, run("and the end of it."))
			b.WriteString(para2(parts...))
		}
		d := miniDocx(t, map[string]string{"word/document.xml": body(b.String()), "word/styles.xml": headingStyles})
		pa := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
		aa := e.applyDocx(d, pa, imports.ApplyIn{DocxChanges: "accept", Title: rereviewTitle()})
		want := e.docContent(aa.ProjectID, "One")

		pv := e.previewDocx(d, imports.PreviewIn{})
		tr := pv.Report["docx"].(map[string]any)["tracked"].(map[string]any)
		totalSug += pv.Suggestions
		totalAcc += tr["taken_as_accepted"].(int)
		totalRefused += tr["refused_by_review_checks"].(int)
		totalConflict += tr["author_conflicts"].(int)
		check := func(label string, ap imports.ApplyOut, order []int, sugs []review.SuggestionOut) {
			if len(sugs) > 0 && order == nil {
				var ids []string
				for _, s := range sugs {
					ids = append(ids, s.ID)
				}
				out := e.must(e.w, "suggestion.accept_all", review.SuggestionAcceptAllIn{ProjectID: ap.ProjectID, DocumentID: sugs[0].DocumentID, SuggestionIDs: ids}).(review.SuggestionAcceptAllOut)
				if len(out.Skipped) != 0 {
					t.Fatalf("round %d: accept_all skipped %v\ndocument: %s", round, out.Skipped, b.String())
				}
			}
			for _, i := range order {
				if _, err := e.call(e.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: ap.ProjectID, SuggestionID: sugs[i].ID}); err != nil {
					t.Fatalf("round %d: accepting one at a time (%s, order %v): %v\ndocument: %s", round, label, order, err, b.String())
				}
			}
			if got := e.docContent(ap.ProjectID, "One"); got != want {
				t.Fatalf("round %d (%s, order %v): accepted\n%q\naccept import\n%q\ndocument: %s", round, label, order, got, want, b.String())
			}
		}
		// accept_all
		ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
		if ap.Suggestions != pv.Suggestions {
			t.Fatalf("round %d: preview %d apply %d", round, pv.Suggestions, ap.Suggestions)
		}
		check("accept_all", ap, nil, e.suggestions(ap.ProjectID))
		// one at a time: every order for up to six suggestions, else a few random ones
		var orders [][]int
		if n := pv.Suggestions; n > 0 && n <= 6 {
			orders = permutations(n)
		} else if n > 6 {
			for k := 0; k < 4; k++ {
				o := make([]int, n)
				for i := range o {
					o[i] = i
				}
				for i := n - 1; i > 0; i-- {
					j := next(i + 1)
					o[i], o[j] = o[j], o[i]
				}
				orders = append(orders, o)
			}
		}
		for _, o := range orders {
			ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
			check("one at a time", ap, o, e.suggestions(ap.ProjectID))
			totalOrders++
		}
	}
}

// permutations lists every order of 0..n-1.
func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, p := range permutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(append([]int{}, p[:i]...), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}

// The importer checks every state of the other suggestions only when few can matter to one; with more it samples.
// So here are documents with seven or more suggestions in repeated text, where it samples, tested the hard way: for
// each suggestion, accept some of the others (every subset up to seven suggestions, else a sample of 64) and then
// that one, which must be found. How many of the documents were found, and how many states tried, is logged.
func TestSampledStatesHoldInRefrainDocumentsWithManySuggestions(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	uniq := strings.Fields("harbour salt quay gull net wall winter bag letter road shore kiln field sea tide stone boat dawn light slow quiet house window river hill")
	want, found, tried := 3, 0, 0
	if testing.Short() {
		want = 1
	}
	for seed := uint32(1); seed < 3000 && found < want; seed++ {
		s := seed * 2654435761
		next := func(n int) int { s = s*1664525 + 1013904223; return int(s>>8) % n }
		var parts []string
		for seg := 0; seg < 12; seg++ {
			for k := 0; k < 2+next(2); k++ {
				for _, w := range strings.Fields("the sea and the shore") {
					switch {
					case next(14) == 0:
						parts = append(parts, ins("A", d1, w+" "))
					case next(30) == 0:
						parts = append(parts, del("A", d1, w+" "))
					default:
						parts = append(parts, run(w+" "))
					}
				}
			}
			parts = append(parts, run(uniq[next(len(uniq))]+" "))
		}
		d := miniDocx(t, map[string]string{"word/document.xml": body(para("Heading1", "One") + para2(parts...)), "word/styles.xml": headingStyles})
		pv := e.previewDocx(d, imports.PreviewIn{})
		n := pv.Suggestions
		if n < 7 {
			continue
		}
		found++
		pa := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
		wantText := e.docContent(e.applyDocx(d, pa, imports.ApplyIn{DocxChanges: "accept", Title: rereviewTitle()}).ProjectID, "One")
		var subsets [][]int // of the others, by index into 0..n-1 minus g: bitmasks over all n, g excluded when used
		for g := 0; g < n; g++ {
			masks := []int{}
			if n <= 7 {
				for m := 0; m < 1<<n; m++ {
					if m>>g&1 == 0 {
						masks = append(masks, m)
					}
				}
			} else {
				for k := 0; k < 64; k++ {
					m := 0
					for i := 0; i < n; i++ {
						if i != g && next(2) == 1 {
							m |= 1 << i
						}
					}
					masks = append(masks, m)
				}
			}
			for _, m := range masks {
				subsets = append(subsets, []int{g, m})
			}
		}
		for _, gm := range subsets {
			g, m := gm[0], gm[1]
			ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
			sugs := e.suggestions(ap.ProjectID)
			for i := 0; i < n; i++ {
				if m>>i&1 == 1 {
					if _, err := e.call(e.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: ap.ProjectID, SuggestionID: sugs[i].ID}); err != nil {
						t.Fatalf("seed %d: accepting %d first: %v", seed, i, err)
					}
				}
			}
			if _, err := e.call(e.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: ap.ProjectID, SuggestionID: sugs[g].ID}); err != nil {
				t.Fatalf("seed %d (%d suggestions): after accepting %b, suggestion %d: %v", seed, n, m, g, err)
			}
			tried++
		}
		ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
		for _, s := range e.suggestions(ap.ProjectID) {
			e.must(e.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: ap.ProjectID, SuggestionID: s.ID})
		}
		if got := e.docContent(ap.ProjectID, "One"); got != wantText {
			t.Errorf("seed %d: all accepted\n%q\naccept import\n%q", seed, got, wantText)
		}
	}
	t.Logf("%d refrain documents with 7 or more suggestions, %d states tried", found, tried)
	if found < want {
		t.Errorf("only %d such documents were found", found)
	}
}
