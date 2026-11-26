package imports

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/pdfread"
)

// The detector behind a PDF and a Pandoc document that have no headings of their own (F-1244): the words alone say where a chapter
// starts, so a label that is bold or in capitals cuts and a plain one is offered; a source that does have headings is cut exactly as it
// always was.

func pdfPara(text string, bold bool) pdfread.Block {
	return pdfread.Block{Kind: pdfread.BlockPara, Runs: []pdfread.Run{{Text: text, Bold: bold}}}
}

func pdfBody(labels []string, bold bool) []pdfread.Block {
	var bs []pdfread.Block
	for _, l := range labels {
		bs = append(bs, pdfPara(l, bold))
		for i := 0; i < 3; i++ {
			bs = append(bs, pdfPara(fmt.Sprintf("The tide came in over the grey stones under %s and went out again, as it does.", strings.ToLower(l)), false))
		}
	}
	return bs
}

func TestPdfWithNoHeadingsIsCutAtBoldAndCapitalLabelsAndOffersTheRest(t *testing.T) {
	cut := func(res *pdfread.Result, opt docxCutOpts) ([]File, *BreakInfo) {
		t.Helper()
		files, _, info, _, err := pdfMarkdown(res, "book", opt, &ProjectInfo{})
		if err != nil {
			t.Fatal(err)
		}
		return files, info
	}
	// bold labels: strong
	files, info := cut(&pdfread.Result{Front: pdfBody([]string{"Chapter 1", "Chapter 2", "Chapter 3"}, true)}, docxCutOpts{})
	if info == nil || info.ChosenBy != "label" || len(files) < 3 {
		t.Fatalf("bold labels: %d files, %+v", len(files), info)
	}
	// plain labels: one document, three offers, nothing cut
	files, info = cut(&pdfread.Result{Front: pdfBody([]string{"Chapter 1", "Chapter 2", "Chapter 3"}, false)}, docxCutOpts{})
	if info == nil || !info.NoneFound || len(info.Suggestions) != 3 || len(files) != 1 {
		t.Fatalf("plain labels: %d files, %+v", len(files), info)
	}
	// the writer chooses them
	files, info = cut(&pdfread.Result{Front: pdfBody([]string{"Chapter 1", "Chapter 2", "Chapter 3"}, false)}, docxCutOpts{split: SplitLabel})
	if len(files) < 3 || info.ChosenBy != "explicit" {
		t.Fatalf("chosen: %d files, %+v", len(files), info)
	}
	// a source with headings is cut as it always was, with no detector's report
	res := &pdfread.Result{Chapters: []pdfread.Chapter{{Title: "One", Blocks: pdfBody([]string{"x"}, false)}, {Title: "Two", Blocks: pdfBody([]string{"y"}, false)}}}
	files, info = cut(res, docxCutOpts{})
	if info != nil || len(files) != 2 || files[0].Path != "01-one.md" {
		t.Fatalf("a source with headings: %d files, info %v, %v", len(files), info, files[0].Path)
	}
	// and the keys of the detector's cut are by place, whatever else changes
	files, _ = cut(&pdfread.Result{Front: pdfBody([]string{"Chapter 1", "Chapter 2"}, true)}, docxCutOpts{})
	for _, f := range files {
		if !reNumbered.MatchString(f.Path) {
			t.Errorf("a key that does not lead with the paragraph it starts at: %s", f.Path)
		}
	}
}

func TestPandocDocumentWithNoHeadingsIsCutLikeAPdf(t *testing.T) {
	var md strings.Builder
	for i, l := range []string{"**CHAPTER ONE**", "**CHAPTER TWO**", "**CHAPTER THREE**"} {
		fmt.Fprintf(&md, "%s\n\nThe tide came in over the grey stones of chapter %d and went out again.\n\nAnd again it came.\n\n", l, i+1)
	}
	in, err := convertPandoc(&pandoc.Imported{Markdown: []byte(md.String()), Format: "rst", Version: "x"}, []byte("src"), "doc", "", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if in.breaks == nil || in.breaks.ChosenBy != "label" || len(in.md) < 3 {
		t.Fatalf("bold capital labels: %d files, %+v", len(in.md), in.breaks)
	}
	plain := strings.NewReplacer("**", "", "CHAPTER ONE", "Chapter 1", "CHAPTER TWO", "Chapter 2", "CHAPTER THREE", "Chapter 3").Replace(md.String())
	in, err = convertPandoc(&pandoc.Imported{Markdown: []byte(plain), Format: "rst", Version: "x"}, []byte("src"), "doc", "", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if in.breaks == nil || !in.breaks.NoneFound || len(in.breaks.Suggestions) != 3 || len(in.md) != 1 {
		t.Fatalf("plain labels: %d files, %+v", len(in.md), in.breaks)
	}
	// headings are the source's own statement: cut as before, no report
	in, err = convertPandoc(&pandoc.Imported{Markdown: []byte("# One\n\ntext\n\n# Two\n\nmore\n"), Format: "rst", Version: "x"}, []byte("src"), "doc", "chapters", convOpts{})
	if err != nil || in.breaks != nil || len(in.md) != 2 {
		t.Fatalf("headings: %v %d %v", err, len(in.md), in.breaks)
	}
}
